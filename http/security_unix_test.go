//go:build unix

package fbhttp

import (
	"archive/zip"
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/dawidpiotrowski/filebrowser-ng/v2/files"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/settings"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/users"
)

func TestArchivesSkipNamedPipesAndSymlinkCycles(t *testing.T) {
	scope := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(scope, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scope, "file.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".", filepath.Join(scope, "cycle")); err != nil {
		t.Fatal(err)
	}
	key := []byte("key")
	perm := users.Permissions{Download: true}
	st := scopedUserStorage(t, scope, perm, key)
	token := signToken(t, st, perm, key)
	w := securityRequest(t, st, &settings.Server{}, rawHandler, "GET", "/?algo=zip", token, nil)
	if w.Code != 200 {
		t.Fatalf("archive: %d %s", w.Code, w.Body)
	}
	zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if f.Name == "pipe" || f.Name == "cycle/" {
			t.Fatal("special or cyclic entry included", f.Name)
		}
	}
	file := &files.FileInfo{Mode: os.ModeNamedPipe}
	status, err := rawFileHandler(httptest.NewRecorder(), httptest.NewRequest("GET", "/pipe", nil), file)
	if status != 400 || err != nil {
		t.Fatalf("public raw pipe: %d %v", status, err)
	}
}
