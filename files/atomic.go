package files

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/spf13/afero"
)

const stagingPrefix = ".filebrowser-ng-staging-"

var (
	ErrFileChanged  = errors.New("file changed while the operation was pending")
	ErrPrecondition = errors.New("file precondition failed")
	ErrWriteBusy    = errors.New("too many active staged writes")
	mutationMu      sync.Mutex
	activeWrites    = make(chan struct{}, 256)
)

// IsReservedPath applies regardless of dotfile preferences, including aliases
// and filesystem/share roots. Case-folding also protects Windows/macOS volumes.
func IsReservedPath(name string) bool {
	for _, part := range strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == '\\' }) {
		if strings.HasPrefix(strings.ToLower(part), stagingPrefix) {
			return true
		}
	}
	return false
}

type WriteOptions struct {
	Mode    fs.FileMode
	Create  bool
	Replace bool
	// Check runs against the original target and again at commit under the
	// process mutation lock. It must not call a mutating filesystem method.
	Check func(os.FileInfo) error
}

// AtomicFile is private, unpublished content. Commit flushes/closes it before
// publishing. Abort is idempotent and must be called on every abandoned write.
type AtomicFile interface {
	afero.File
	Commit() (os.FileInfo, error)
	Abort() error
	Validate() error
}

func BeginAtomicWrite(afs afero.Fs, name string, opts WriteOptions) (AtomicFile, error) {
	if fsys, ok := afs.(interface {
		BeginAtomicWrite(string, WriteOptions) (AtomicFile, error)
	}); ok {
		return fsys.BeginAtomicWrite(name, opts)
	}
	return nil, errors.ErrUnsupported
}

func RenameNoReplace(afs afero.Fs, src, dst string) error {
	if fsys, ok := afs.(interface{ RenameNoReplace(string, string) error }); ok {
		return fsys.RenameNoReplace(src, dst)
	}
	return errors.ErrUnsupported
}

func RemoveIfUnchanged(afs afero.Fs, name string, expected os.FileInfo) error {
	if native, ok := afs.(interface {
		RemoveIfUnchanged(string, os.FileInfo) error
	}); ok {
		return native.RemoveIfUnchanged(name, expected)
	}
	return errors.ErrUnsupported
}

func (s *ScopedFs) RemoveIfUnchanged(name string, expected os.FileInfo) error {
	mutationMu.Lock()
	defer mutationMu.Unlock()
	root, dir, leaf, err := s.atomicParent(name, false)
	if err != nil {
		return err
	}
	defer root.Close()
	defer dir.Close()
	current, err := root.Stat(leaf)
	if err != nil {
		return err
	}
	if !sameVersion(current, expected) {
		return ErrFileChanged
	}
	if err = root.Remove(leaf); err != nil {
		return err
	}
	return syncDirectory(dir)
}

func (s *ScopedFs) atomicParent(name string, follow bool) (*os.Root, *os.File, string, error) {
	if !s.osBacked {
		return nil, nil, "", errors.ErrUnsupported
	}
	root, rel, err := s.confined(name, follow)
	if err != nil {
		return nil, nil, "", err
	}
	defer root.Close()
	leaf := filepath.Base(rel)
	if leaf == "." || leaf == ".." {
		return nil, nil, "", os.ErrPermission
	}
	parent, err := root.OpenRoot(filepath.Dir(rel))
	if err != nil {
		return nil, nil, "", err
	}
	dir, err := parent.Open(".")
	if err != nil {
		parent.Close()
		return nil, nil, "", err
	}
	return parent, dir, leaf, nil
}

func (s *ScopedFs) RenameNoReplace(src, dst string) error { return s.rename(src, dst, false) }

func (s *ScopedFs) rename(src, dst string, replace bool) error {
	mutationMu.Lock()
	defer mutationMu.Unlock()
	fromRoot, fromDir, from, err := s.atomicParent(src, false)
	if err != nil {
		return err
	}
	defer fromRoot.Close()
	defer fromDir.Close()
	toRoot, toDir, to, err := s.atomicParent(dst, false)
	if err != nil {
		return err
	}
	defer toRoot.Close()
	defer toDir.Close()
	if err := renameAt(fromDir, from, toDir, to, replace); err != nil {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: err}
	}
	return errors.Join(syncDirectory(toDir), syncDirectory(fromDir))
}

type stagedFile struct {
	afero.File
	mu              sync.Mutex
	fs              *ScopedFs
	name            string
	opts            WriteOptions
	original        os.FileInfo
	parentInfo      os.FileInfo
	parent          *os.Root
	parentDir       *os.File
	stage           *os.Root
	stageDir        *os.File
	stageInfo       os.FileInfo
	payloadInfo     os.FileInfo
	stageName, leaf string
	closed, done    bool
}

func (s *ScopedFs) BeginAtomicWrite(name string, opts WriteOptions) (AtomicFile, error) {
	if !s.osBacked {
		return nil, errors.ErrUnsupported
	}
	select {
	case activeWrites <- struct{}{}:
	default:
		return nil, ErrWriteBusy
	}
	ok := false
	defer func() {
		if !ok {
			<-activeWrites
		}
	}()
	mutationMu.Lock()
	defer mutationMu.Unlock()
	parent, dir, leaf, err := s.atomicParent(name, opts.Replace)
	if err != nil {
		return nil, err
	}
	cleanupParent := true
	defer func() {
		if cleanupParent {
			dir.Close()
			parent.Close()
		}
	}()
	// Stat the actual target through this pinned parent, not a resolved string.
	original, err := parent.Stat(leaf)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		if !original.Mode().IsRegular() {
			return nil, os.ErrPermission
		}
		if !opts.Replace {
			return nil, os.ErrExist
		}
		opts.Mode = original.Mode().Perm()
	} else {
		original = nil
		if !opts.Create {
			return nil, os.ErrNotExist
		}
	}
	if opts.Check != nil {
		if err := opts.Check(original); err != nil {
			return nil, err
		}
	}
	parentInfo, err := dir.Stat()
	if err != nil {
		return nil, err
	}
	random := make([]byte, 16)
	if _, err = rand.Read(random); err != nil {
		return nil, err
	}
	stageName := stagingPrefix + hex.EncodeToString(random)
	if err = parent.Mkdir(stageName, 0o700); err != nil {
		return nil, err
	}
	stage, err := parent.OpenRoot(stageName)
	if err != nil {
		return nil, err
	}
	stageDir, err := stage.Open(".")
	if err != nil {
		stage.Close()
		return nil, err
	}
	stageInfo, err := stageDir.Stat()
	if err != nil {
		stageDir.Close()
		stage.Close()
		return nil, err
	}
	file, err := stage.OpenFile("data", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		stageDir.Close()
		stage.Close()
		_ = parent.Remove(stageName)
		return nil, err
	}
	tx := &stagedFile{File: file, fs: s, name: name, opts: opts, original: original, parentInfo: parentInfo,
		parent: parent, parentDir: dir, stage: stage, stageDir: stageDir, stageInfo: stageInfo, stageName: stageName, leaf: leaf}
	tx.payloadInfo, err = file.Stat()
	if err != nil {
		file.Close()
		_ = stage.Remove("data")
		stageDir.Close()
		stage.Close()
		_ = parent.Remove(stageName)
		return nil, err
	}
	if original != nil {
		// Preserve the existing OS write-access check: replacing an inode must
		// not bypass a read-only file just because its directory is writable.
		old, openErr := parent.OpenFile(leaf, os.O_RDWR|nonBlockingOpenFlag, 0)
		if openErr == nil {
			actual, statErr := old.Stat()
			if statErr != nil {
				openErr = statErr
			} else if !sameVersion(original, actual) {
				openErr = ErrFileChanged
			} else {
				openErr = preserveMetadata(old, file, actual)
			}
			openErr = errors.Join(openErr, old.Close())
		}
		if openErr != nil {
			file.Close()
			_ = stage.Remove("data")
			stageDir.Close()
			stage.Close()
			_ = parent.Remove(stageName)
			return nil, openErr
		}
	}
	ok = true
	cleanupParent = false
	return tx, nil
}

func sameVersion(a, b os.FileInfo) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime()) && a.Mode() == b.Mode()
}

func (f *stagedFile) validate() error {
	if f.done {
		return os.ErrClosed
	}
	// A parent renamed/replaced while bytes streamed is a conflict, even if
	// its pinned handle remains valid. Never publish into a moved namespace.
	root, dir, leaf, err := f.fs.atomicParent(f.name, f.opts.Replace)
	if err != nil {
		return errors.Join(ErrFileChanged, err)
	}
	defer root.Close()
	defer dir.Close()
	info, err := dir.Stat()
	if err != nil {
		return err
	}
	if leaf != f.leaf || !os.SameFile(info, f.parentInfo) {
		return ErrFileChanged
	}
	current, err := f.parent.Stat(f.leaf)
	if errors.Is(err, os.ErrNotExist) {
		current = nil
	} else if err != nil {
		return err
	}
	if f.opts.Check != nil {
		if err := f.opts.Check(current); err != nil {
			return err
		}
	}
	if !sameVersion(current, f.original) {
		return ErrFileChanged
	}
	return nil
}

func (f *stagedFile) Validate() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	mutationMu.Lock()
	defer mutationMu.Unlock()
	return f.validate()
}

func (f *stagedFile) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	return f.File.Close()
}

func (f *stagedFile) Commit() (os.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.done || f.closed {
		return nil, os.ErrClosed
	}
	if file, ok := f.File.(interface{ Chmod(os.FileMode) error }); ok {
		if err := file.Chmod(f.opts.Mode.Perm()); err != nil {
			return nil, err
		}
	} else {
		return nil, errors.ErrUnsupported
	}
	if err := f.File.Sync(); err != nil {
		return nil, err
	}
	info, err := f.File.Stat()
	if err != nil {
		return nil, err
	}
	f.closed = true
	if err = f.File.Close(); err != nil {
		return nil, err
	}
	mutationMu.Lock()
	defer mutationMu.Unlock()
	if err = f.validate(); err != nil {
		return nil, err
	}
	payload, err := f.stage.Lstat("data")
	if err != nil {
		return nil, err
	}
	if !os.SameFile(payload, f.payloadInfo) {
		return nil, ErrFileChanged
	}
	if err = renameAt(f.stageDir, "data", f.parentDir, f.leaf, f.original != nil); err != nil {
		return nil, err
	}
	// Publication has happened. A subsequent directory-sync error cannot roll
	// it back; report it accurately and never retry/remove the published file.
	err = syncDirectory(f.parentDir)
	f.finish()
	if err != nil {
		return info, fmt.Errorf("file published but directory sync failed: %w", err)
	}
	return info, nil
}

func (f *stagedFile) finish() {
	if current, err := f.stage.Lstat("data"); err == nil && os.SameFile(current, f.payloadInfo) {
		_ = f.stage.Remove("data")
	}
	if current, err := f.parent.Lstat(f.stageName); err == nil && os.SameFile(current, f.stageInfo) {
		_ = f.parent.Remove(f.stageName)
	}
	f.stageDir.Close()
	f.stage.Close()
	f.parentDir.Close()
	f.parent.Close()
	f.done = true
	<-activeWrites
}

func (f *stagedFile) Abort() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.done {
		return nil
	}
	var err error
	if !f.closed {
		f.closed = true
		err = f.File.Close()
	}
	f.finish()
	return err
}
