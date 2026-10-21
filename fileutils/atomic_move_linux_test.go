package fileutils

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dawidpiotrowski/filebrowser-ng/v2/files"
	"github.com/spf13/afero"
)

func TestRealCrossVolumeMoveCommitsBeforeRemovingSource(t *testing.T) {
	root, err := os.MkdirTemp("/dev/shm", "filebrowser-ng-move-test-")
	if err != nil {
		t.Skip("writable second filesystem unavailable:", err)
	}
	defer os.RemoveAll(root)
	srcRoot := t.TempDir()
	src := filepath.Join(srcRoot, "source")
	dst := filepath.Join(root, "destination")
	if err := os.WriteFile(src, []byte("complete-file"), 0o640); err != nil {
		t.Fatal(err)
	}
	afs := files.NewFs(afero.NewOsFs(), "/", true)
	if err := MoveFileWithPolicy(afs, src, dst, 0o600, 0o700, false); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(dst); err != nil || string(data) != "complete-file" {
		t.Fatal(string(data), err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("source retained after successful move", err)
	}
	if err := os.WriteFile(src, []byte("keep-source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := MoveFileWithPolicy(afs, src, dst, 0o600, 0o700, false); !errors.Is(err, os.ErrExist) {
		t.Fatal("conflict ignored", err)
	}
	if data, _ := os.ReadFile(src); string(data) != "keep-source" {
		t.Fatal("source lost", string(data))
	}
	if data, _ := os.ReadFile(dst); string(data) != "complete-file" {
		t.Fatal("destination changed", string(data))
	}
}
