package fileutils

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	fberrors "github.com/dawidpiotrowski/filebrowser-ng/v2/errors"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/files"
	"github.com/spf13/afero"
)

func TestDirectoryCopyRejectsAnAliasedDescendantBeforeMutation(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "tree"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "tree"), filepath.Join(root, "alias")); err != nil {
		t.Skip(err)
	}
	afs := files.NewScopedFs(afero.NewOsFs(), root)
	if err := Copy(afs, "/tree", "/alias/child", 0o600, 0o700); !errors.Is(err, fberrors.ErrSourceIsParent) {
		t.Fatal("aliased descendant accepted", err)
	}
	if _, err := os.Stat(filepath.Join(root, "tree", "child")); !os.IsNotExist(err) {
		t.Fatal("descendant created", err)
	}
}

func TestSameVolumeDirectoryMoveKeepsItsIdentity(t *testing.T) {
	root := t.TempDir()
	afs := files.NewScopedFs(afero.NewOsFs(), root)
	if err := afs.Mkdir("/tree", 0o700); err != nil {
		t.Fatal(err)
	}
	before, _ := afs.Stat("/tree")
	if err := MoveFileWithPolicy(afs, "/tree", "/moved", 0o600, 0o700, false); err != nil {
		t.Fatal(err)
	}
	after, _ := afs.Stat("/moved")
	if !os.SameFile(before, after) {
		t.Fatal("directory copied instead of renamed")
	}
}
