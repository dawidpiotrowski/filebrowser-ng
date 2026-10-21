package files

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/afero"
)

// ScopedFs is an afero.Fs that confines every operation to a base directory and
// refuses to follow a symbolic link whose on-disk target resolves outside that
// base. It wraps an *afero.BasePathFs — which already provides the lexical
// confinement — and adds a per-operation scope check on every call that would
// dereference a symlink at the OS layer (open, stat, lstat, chmod, …).
type ScopedFs struct {
	base     *afero.BasePathFs
	osBacked bool
}

var (
	_ afero.Fs      = (*ScopedFs)(nil)
	_ afero.Lstater = (*ScopedFs)(nil)
)

// maxSymlinkHops bounds how many dangling symlinks within() will follow before
// giving up, so a pathological chain cannot loop forever. It mirrors the kernel
// MAXSYMLINKS limit; the operation is rejected once the bound is exceeded.
const maxSymlinkHops = 255

func NewScopedFs(source afero.Fs, path string) *ScopedFs {
	osBacked := source.Name() == "OsFs"
	if s, ok := source.(*ScopedFs); ok {
		osBacked = s.osBacked
		source = s.base
	} else if info, err := source.Stat(path); err == nil {
		osBacked = info.Sys() != nil
	}
	return &ScopedFs{base: afero.NewBasePathFs(source, path).(*afero.BasePathFs), osBacked: osBacked}
}

// NewFs builds a user filesystem rooted at path. When followExternal is true it
// returns a bare BasePathFs, so symlinks whose target resolves outside the scope
// are followed; otherwise it returns a ScopedFs that refuses to follow them.
func NewFs(source afero.Fs, path string, followExternal bool) afero.Fs {
	if followExternal {
		return afero.NewBasePathFs(source, path)
	}
	return NewScopedFs(source, path)
}

// BasePath returns the underlying *afero.BasePathFs of a user filesystem built
// by NewFs, whether it is a *ScopedFs or a bare *afero.BasePathFs, or nil if it
// is neither.
func BasePath(fs afero.Fs) *afero.BasePathFs {
	switch f := fs.(type) {
	case *ScopedFs:
		return f.BasePathFs()
	case *afero.BasePathFs:
		return f
	}
	return nil
}

// BasePathFs returns the underlying *afero.BasePathFs.
func (s *ScopedFs) BasePathFs() *afero.BasePathFs { return s.base }

// RealPath resolves a scoped path to the real on-disk path by delegating to
// the underlying BasePathFs. This is needed by callers that need the actual
// filesystem path (e.g. disk.UsageWithContext).
func (s *ScopedFs) RealPath(name string) (string, error) {
	return s.base.RealPath(name)
}

// guard returns an error if name's on-disk target resolves outside the scope.
func (s *ScopedFs) guard(name string) error {
	if !s.osBacked {
		_, err := s.base.RealPath(name)
		return err
	}
	ok, err := s.within(name)
	if err != nil {
		return err
	}
	if !ok {
		return os.ErrPermission
	}
	return nil
}

// within reports whether the on-disk target of p — after resolving any symbolic
// links — stays within the scoped root. It exists to stop a symlink that lives
// lexically inside the scope but points outside it from being followed for
// reads, writes, or shares.
//
// Paths that do not exist yet (e.g. a brand-new file being created) are
// validated against their nearest existing ancestor, so legitimate new files
// are always allowed. A dangling symlink — a link whose target does not exist
// yet — is the exception: it is followed to where it points and validated
// there, so a write cannot dereference the link to create a file outside the
// scope.
func (s *ScopedFs) within(p string) (bool, error) {
	root, err := filepath.EvalSymlinks(afero.FullBaseFsPath(s.base, "/"))
	if err != nil {
		return false, err
	}

	target := afero.FullBaseFsPath(s.base, p)
	resolved, err := filepath.EvalSymlinks(target)
	// When target does not resolve, work out where the operation would actually
	// land. A non-existent regular path resolves to the file that would be
	// created inside its containing directory, so walk up to the nearest
	// existing ancestor and validate that. But when target itself is a dangling
	// symlink, follow it one level instead: validating its lexical parent would
	// wrongly accept a link pointing outside the scope, letting a write follow
	// the link and create the file out of bounds.
	for hops := 0; errors.Is(err, fs.ErrNotExist); {
		if fi, lerr := os.Lstat(target); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
			hops++
			if hops > maxSymlinkHops {
				return false, os.ErrPermission
			}
			dest, rerr := os.Readlink(target)
			if rerr != nil {
				return false, rerr
			}
			if !filepath.IsAbs(dest) {
				// Resolve the link relative to the directory that really contains
				// it, not its lexical parent: a symlinked ancestor could otherwise
				// shift the computed target back into scope while the real write
				// lands outside it. The parent is guaranteed to resolve here
				// because os.Lstat above already traversed it.
				base, berr := filepath.EvalSymlinks(filepath.Dir(target))
				if berr != nil {
					return false, berr
				}
				dest = filepath.Join(base, dest)
			}
			target = filepath.Clean(dest)
		} else {
			parent := filepath.Dir(target)
			if parent == target {
				break
			}
			target = parent
		}
		resolved, err = filepath.EvalSymlinks(target)
	}
	if err != nil {
		return false, err
	}

	// Compare against root with a trailing separator so a sibling like
	// "/srvother" is not treated as being inside "/srv". When root is itself the
	// filesystem boundary (e.g. "/"), it already ends in a separator, so avoid
	// producing "//" — which no path would match — and accept any path under it.
	prefix := root
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}

	return resolved == root || strings.HasPrefix(resolved, prefix), nil
}

// confined opens a traversal-resistant root for the operation and converts
// absolute in-scope symlinks to a root-relative target. os.Root enforces the
// boundary during the syscall even if another process swaps a checked path.
func (s *ScopedFs) confined(name string, followFinal bool) (*os.Root, string, error) {
	if err := s.guard(name); err != nil {
		return nil, "", err
	}
	rootName, err := filepath.EvalSymlinks(afero.FullBaseFsPath(s.base, "/"))
	if err != nil {
		return nil, "", err
	}
	target := afero.FullBaseFsPath(s.base, name)
	if target == afero.FullBaseFsPath(s.base, "/") {
		target = rootName
	} else if followFinal {
		target, err = ResolveMissingPath(target)
	} else {
		parent, perr := ResolveMissingPath(filepath.Dir(target))
		err = perr
		target = filepath.Join(parent, filepath.Base(target))
	}
	if err != nil {
		return nil, "", err
	}
	info, err := os.Stat(rootName)
	if err != nil {
		return nil, "", err
	}
	if !info.IsDir() {
		if target != rootName {
			return nil, "", os.ErrPermission
		}
		rootName = filepath.Dir(rootName)
	}
	rel, err := filepath.Rel(rootName, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return nil, "", os.ErrPermission
	}
	root, err := os.OpenRoot(rootName)
	return root, rel, err
}

func (s *ScopedFs) Create(name string) (afero.File, error) {
	return s.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
}

func (s *ScopedFs) Mkdir(name string, perm os.FileMode) error {
	if !s.osBacked {
		return s.base.Mkdir(name, perm)
	}
	root, rel, err := s.confined(name, true)
	if err != nil {
		return err
	}
	defer root.Close()
	return root.Mkdir(rel, perm.Perm())
}

func (s *ScopedFs) MkdirAll(name string, perm os.FileMode) error {
	if !s.osBacked {
		return s.base.MkdirAll(name, perm)
	}
	root, rel, err := s.confined(name, true)
	if err != nil {
		return err
	}
	defer root.Close()
	return root.MkdirAll(rel, perm.Perm())
}

func (s *ScopedFs) Open(name string) (afero.File, error) {
	return s.OpenFile(name, os.O_RDONLY, 0)
}

func (s *ScopedFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if !s.osBacked {
		return s.base.OpenFile(name, flag, perm)
	}
	root, rel, err := s.confined(name, true)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.OpenFile(rel, flag|nonBlockingOpenFlag, perm.Perm())
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || (!info.Mode().IsRegular() && !info.IsDir()) {
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		return nil, os.ErrPermission
	}
	return f, nil
}

func (s *ScopedFs) Remove(name string) error {
	if !s.osBacked {
		return s.base.Remove(name)
	}
	root, rel, err := s.confined(name, false)
	if err != nil {
		return err
	}
	defer root.Close()
	return root.Remove(rel)
}

func (s *ScopedFs) RemoveAll(name string) error {
	if !s.osBacked {
		return s.base.RemoveAll(name)
	}
	root, rel, err := s.confined(name, false)
	if err != nil {
		return err
	}
	defer root.Close()
	return root.RemoveAll(rel)
}

func (s *ScopedFs) Rename(oldname, newname string) error {
	if !s.osBacked {
		return s.base.Rename(oldname, newname)
	}
	root, oldrel, err := s.confined(oldname, false)
	if err != nil {
		return err
	}
	defer root.Close()
	other, newrel, err := s.confined(newname, false)
	if err != nil {
		return err
	}
	defer other.Close()
	return root.Rename(oldrel, newrel)
}

func (s *ScopedFs) Stat(name string) (os.FileInfo, error) {
	if !s.osBacked {
		return s.base.Stat(name)
	}
	root, rel, err := s.confined(name, true)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Stat(rel)
}

func (s *ScopedFs) Name() string { return "ScopedFs" }

func (s *ScopedFs) Chmod(name string, mode os.FileMode) error {
	if !s.osBacked {
		return s.base.Chmod(name, mode)
	}
	f, err := s.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.(*os.File).Chmod(mode)
}

func (s *ScopedFs) Chown(name string, uid, gid int) error {
	if !s.osBacked {
		return s.base.Chown(name, uid, gid)
	}
	f, err := s.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.(*os.File).Chown(uid, gid)
}

func (s *ScopedFs) Chtimes(name string, atime, mtime time.Time) error {
	if !s.osBacked {
		return s.base.Chtimes(name, atime, mtime)
	}
	root, rel, err := s.confined(name, true)
	if err != nil {
		return err
	}
	defer root.Close()
	return root.Chtimes(rel, atime, mtime)
}

func (s *ScopedFs) LstatIfPossible(name string) (os.FileInfo, bool, error) {
	if !s.osBacked {
		return s.base.LstatIfPossible(name)
	}
	// The root itself can be a file for a single-file public share.
	if name == "" || name == "/" || name == "." {
		if err := s.guard(name); err != nil {
			return nil, false, err
		}
		return s.base.LstatIfPossible(name)
	}
	root, rel, err := s.confined(name, false)
	if err != nil {
		return nil, false, err
	}
	defer root.Close()
	info, err := root.Lstat(rel)
	return info, true, err
}
