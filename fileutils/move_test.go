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

type failedRenameFs struct {
	afero.Fs
	renameErr error
	readErr   error
}

func (f failedRenameFs) Rename(_, _ string) error { return f.renameErr }

func (f failedRenameFs) RemoveIfUnchanged(name string, info os.FileInfo) error {
	return files.RemoveIfUnchanged(f.Fs, name, info)
}

func (f failedRenameFs) BeginAtomicWrite(name string, opts files.WriteOptions) (files.AtomicFile, error) {
	return files.BeginAtomicWrite(f.Fs, name, opts)
}

func (f failedRenameFs) Open(name string) (afero.File, error) {
	if name == "/source" && f.readErr != nil {
		return nil, f.readErr
	}
	return f.Fs.Open(name)
}

func TestFailedMovePreservesExistingDestination(t *testing.T) {
	for _, renameErr := range []error{os.ErrPermission, syscall.EXDEV} {
		t.Run(renameErr.Error(), func(t *testing.T) {
			mem := afero.NewBasePathFs(afero.NewMemMapFs(), string(filepath.Separator))
			for name, content := range map[string]string{"/source": "source", "/destination": "valuable-existing-file"} {
				if err := afero.WriteFile(mem, name, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			fs := failedRenameFs{Fs: mem, renameErr: renameErr, readErr: os.ErrPermission}
			if err := MoveFile(fs, "/source", "/destination", 0o600, 0o700); err == nil {
				t.Fatal("move unexpectedly succeeded")
			}
			content, err := afero.ReadFile(mem, "/destination")
			if err != nil || string(content) != "valuable-existing-file" {
				t.Fatalf("failed move destroyed existing destination: %q %v", content, err)
			}
		})
	}
}

func TestMoveDoesNotFallbackOnPermissionFailure(t *testing.T) {
	mem := afero.NewBasePathFs(afero.NewMemMapFs(), string(filepath.Separator))
	if err := afero.WriteFile(mem, "/source", []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := MoveFile(failedRenameFs{Fs: mem, renameErr: os.ErrPermission}, "/source", "/destination", 0o600, 0o700)
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("rename error was bypassed by copy/delete: %v", err)
	}
	if exists, _ := afero.Exists(mem, "/destination"); exists {
		t.Fatal("failed rename created destination")
	}
}

func TestCrossDeviceMoveStillWorks(t *testing.T) {
	// Normalize virtual paths through the root adapter, as production user
	// filesystems do. MemMapFs.Chmod otherwise treats / and \\ differently on
	// Windows even when OpenFile accepted the forward-slash spelling.
	mem := files.NewScopedFs(afero.NewOsFs(), t.TempDir())
	if err := afero.WriteFile(mem, "/source", []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := MoveFile(failedRenameFs{Fs: mem, renameErr: &os.LinkError{Op: "rename", Err: syscall.EXDEV}}, "/source", "/destination", 0o600, 0o700); err != nil {
		t.Fatal(err)
	}
	if content, err := afero.ReadFile(mem, "/destination"); err != nil || string(content) != "source" {
		t.Fatalf("cross-device destination: %q %v", content, err)
	}
	if exists, _ := afero.Exists(mem, "/source"); exists {
		t.Fatal("successful move retained source")
	}
}
