package search

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
)

type searchChecker func(string) bool

func (c searchChecker) Check(path string) bool { return c(path) }

type failedStatFs struct{ afero.Fs }

func (f failedStatFs) Stat(name string) (os.FileInfo, error) {
	if filepath.Base(name) == "vanished" {
		return nil, os.ErrNotExist
	}
	return f.Fs.Stat(name)
}

func TestSearchDoesNotPassMissingMetadataToCallback(t *testing.T) {
	fs := afero.NewMemMapFs()
	for _, name := range []string{"/kept", "/vanished"} {
		if err := afero.WriteFile(fs, name, []byte("content"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var found []string
	err := Search(context.Background(), failedStatFs{fs}, "/", "", searchChecker(func(string) bool { return true }), func(path string, f os.FileInfo) error {
		if f == nil {
			t.Fatal("search passed nil metadata after a failed stat; HTTP callback would panic")
		}
		found = append(found, path)
		return nil
	})
	if err != nil || len(found) != 1 || found[0] != "kept" {
		t.Fatalf("search result: %v %v", found, err)
	}
}

func TestSearchReturnsRootError(t *testing.T) {
	err := Search(context.Background(), afero.NewMemMapFs(), "/missing", "", searchChecker(func(string) bool { return true }), func(string, os.FileInfo) error {
		t.Fatal("callback called for missing root")
		return nil
	})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing search root silently succeeded: %v", err)
	}
}
