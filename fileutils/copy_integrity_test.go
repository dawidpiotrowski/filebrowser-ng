package fileutils

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/dawidpiotrowski/filebrowser-ng/v2/files"
	"github.com/spf13/afero"
)

func TestCopyRejectsAliasesOfItsSourceWithoutTruncating(t *testing.T) {
	for _, kind := range []string{"same-path", "hardlink", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source")
			if err := os.WriteFile(source, []byte("valuable-source-data"), 0o600); err != nil {
				t.Fatal(err)
			}
			dest := "/source"
			if kind != "same-path" {
				dest = "/alias"
				link := os.Link
				if kind == "symlink" {
					link = os.Symlink
				}
				if err := link(source, filepath.Join(root, "alias")); err != nil {
					t.Skip(err)
				}
			}
			err := CopyFile(files.NewScopedFs(afero.NewOsFs(), root), "/source", dest, 0o600, 0o700)
			if err == nil {
				t.Error("copy of source onto itself unexpectedly succeeded")
			}
			if content, err := os.ReadFile(source); err != nil || string(content) != "valuable-source-data" {
				t.Fatalf("source was truncated through its alias: %q %v", content, err)
			}
		})
	}
}

type copyFlushFailureFs struct {
	afero.Fs
	syncErr, closeErr error
}

type copyFlushFailureFile struct {
	files.AtomicFile
	syncErr, closeErr error
}

func (f copyFlushFailureFs) Rename(_, _ string) error { return syscall.EXDEV }

func (f copyFlushFailureFs) BeginAtomicWrite(name string, opts files.WriteOptions) (files.AtomicFile, error) {
	stage, err := files.BeginAtomicWrite(f.Fs, name, opts)
	if err != nil {
		return nil, err
	}
	return &copyFlushFailureFile{AtomicFile: stage, syncErr: f.syncErr, closeErr: f.closeErr}, nil
}

func (f *copyFlushFailureFile) Commit() (os.FileInfo, error) {
	return nil, errors.Join(f.syncErr, f.closeErr)
}

func TestCrossDeviceMoveKeepsSourceWhenCopyCannotFlush(t *testing.T) {
	failure := errors.New("storage flush failed")
	for _, closeFailure := range []bool{false, true} {
		name := "sync"
		if closeFailure {
			name = "close"
		}
		t.Run(name, func(t *testing.T) {
			mem := files.NewScopedFs(afero.NewOsFs(), t.TempDir())
			if err := afero.WriteFile(mem, "/source", []byte("keep-source"), 0o600); err != nil {
				t.Fatal(err)
			}
			fs := copyFlushFailureFs{Fs: mem, syncErr: failure}
			if closeFailure {
				fs.syncErr = nil
				fs.closeErr = failure
			}
			if err := MoveFile(fs, "/source", "/destination", 0o600, 0o700); !errors.Is(err, failure) {
				t.Errorf("flush failure was ignored: %v", err)
			}
			if content, err := afero.ReadFile(mem, "/source"); err != nil || string(content) != "keep-source" {
				t.Fatalf("source was removed before successful copy completion: %q %v", content, err)
			}
		})
	}
}
