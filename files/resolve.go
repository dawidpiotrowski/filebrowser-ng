package files

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/afero"
)

// ResolveMissingPath resolves links even when the final file does not yet
// exist. This is necessary when authorizing a create through a symlink alias.
func ResolveMissingPath(name string) (string, error) {
	return resolveMissingPath(filepath.Clean(name), 0)
}

func resolveMissingPath(name string, depth int) (string, error) {
	if depth > maxSymlinkHops {
		return "", os.ErrPermission
	}
	resolved, err := filepath.EvalSymlinks(name)
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		return resolved, err
	}
	if info, lerr := os.Lstat(name); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
		target, rerr := os.Readlink(name)
		if rerr != nil {
			return "", rerr
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(name), target)
		}
		return resolveMissingPath(target, depth+1)
	}
	parent := filepath.Dir(name)
	if parent == name {
		return "", err
	}
	resolved, err = resolveMissingPath(parent, depth+1)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(name)), nil
}

// ResolvedVirtualPath maps a symlink target back to the filesystem's virtual
// namespace, preserving missing suffixes for create/write authorization.
func ResolvedVirtualPath(fsys afero.Fs, name string) (string, error) {
	if scoped, ok := fsys.(*ScopedFs); ok && !scoped.osBacked {
		return name, nil
	}
	base := BasePath(fsys)
	if base == nil {
		return name, nil
	}
	root, err := filepath.EvalSymlinks(afero.FullBaseFsPath(base, "/"))
	if err != nil {
		return "", err
	}
	target, err := ResolveMissingPath(afero.FullBaseFsPath(base, name))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}
