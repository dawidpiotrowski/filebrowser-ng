package fbhttp

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dawidpiotrowski/filebrowser-ng/v2/settings"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/users"
	"github.com/spf13/afero"
)

func tusOwnershipRequest(starter http.Handler, method, token, offset, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/upload.txt", strings.NewReader(body))
	r.Header.Set("X-Auth", token)
	r.Header.Set("Upload-Length", "5")
	r.Header.Set("Upload-Offset", offset)
	r.Header.Set("Content-Type", "application/offset+octet-stream")
	w := httptest.NewRecorder()
	starter.ServeHTTP(w, r)
	return w
}

func TestTusUploadsAreBoundToTheirCreator(t *testing.T) {
	for _, method := range []string{"HEAD", "PATCH", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			root := t.TempDir()
			key := []byte("tus-owner-key")
			perm := users.Permissions{Create: true}
			st := scopedUserStorage(t, root, perm, key)
			otherPerm := users.Permissions{Create: true, Delete: method == "DELETE"}
			other := &users.User{Username: "other", Password: "pw", Perm: otherPerm}
			if err := st.Users.Save(other); err != nil {
				t.Fatal(err)
			}
			ownerToken := signToken(t, st, perm, key)
			otherToken := signShareTestToken(t, st, other.ID, other.Username, otherPerm, key)
			cache := newMemoryUploadCache()
			t.Cleanup(cache.Close)
			server := &settings.Server{}
			if w := tusOwnershipRequest(handle(tusPostHandler(cache), "", st, server), "POST", ownerToken, "0", ""); w.Code != http.StatusCreated {
				t.Fatalf("create: %d %s", w.Code, w.Body)
			}
			fn := tusHeadHandler(cache)
			if method == "PATCH" {
				fn = tusPatchHandler(cache)
			}
			if method == "DELETE" {
				fn = tusDeleteHandler(cache)
			}
			w := tusOwnershipRequest(handle(fn, "", st, server), method, otherToken, "0", "evil!")
			if w.Code != http.StatusForbidden {
				t.Errorf("another user accessed the active upload: %d %s", w.Code, w.Body)
			}
			if _, err := os.Stat(filepath.Join(root, "upload.txt")); !os.IsNotExist(err) {
				t.Fatalf("unfinished upload became public: %v", err)
			}
			w = tusOwnershipRequest(handle(tusPatchHandler(cache), "", st, server), "PATCH", ownerToken, "0", "hello")
			if w.Code != http.StatusNoContent {
				t.Fatalf("creator cannot continue: %d %s", w.Code, w.Body)
			}
		})
	}
}

func TestTusCannotModifyAReplacementAtTheOriginalPath(t *testing.T) {
	root := t.TempDir()
	key := []byte("tus-file-identity")
	perm := users.Permissions{Create: true, Delete: true}
	st := scopedUserStorage(t, root, perm, key)
	token := signToken(t, st, perm, key)
	cache := newMemoryUploadCache()
	t.Cleanup(cache.Close)
	server := &settings.Server{}
	if w := tusOwnershipRequest(handle(tusPostHandler(cache), "", st, server), "POST", token, "0", ""); w.Code != http.StatusCreated {
		t.Fatal(w.Code)
	}
	name := filepath.Join(root, "upload.txt")
	if err := os.WriteFile(name, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := tusOwnershipRequest(handle(tusPatchHandler(cache), "", st, server), "PATCH", token, "0", "hello")
	if w.Code != http.StatusConflict {
		t.Errorf("stale upload modified replacement: %d %s", w.Code, w.Body)
	}
	if content, err := os.ReadFile(name); err != nil || string(content) != "keep" {
		t.Fatalf("replacement was changed: %q %v", content, err)
	}
}

func TestTusCreatorCanResumeAfterSessionRotation(t *testing.T) {
	key := []byte("tus-session-rotation")
	perm := users.Permissions{Create: true}
	st := scopedUserStorage(t, t.TempDir(), perm, key)
	token := signToken(t, st, perm, key)
	cache := newMemoryUploadCache()
	t.Cleanup(cache.Close)
	server := &settings.Server{}
	if w := tusOwnershipRequest(handle(tusPostHandler(cache), "", st, server), "POST", token, "0", ""); w.Code != http.StatusCreated {
		t.Fatal(w.Code)
	}
	w := securityRequest(t, st, server, renewHandler(time.Hour), "POST", "/renew", token, nil)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
	w = tusOwnershipRequest(handle(tusPatchHandler(cache), "", st, server), "PATCH", w.Body.String(), "0", "hello")
	if w.Code != http.StatusNoContent {
		t.Fatalf("rotated session cannot resume own upload: %d %s", w.Code, w.Body)
	}
}

func TestTusUnsupportedFilesystemCannotTruncateExistingData(t *testing.T) {
	mem := afero.NewBasePathFs(afero.NewMemMapFs(), string(filepath.Separator))
	if err := afero.WriteFile(mem, "/upload.txt", []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	key := []byte("tus-unsupported-identity")
	perm := users.Permissions{Create: true, Modify: true}
	st := scopedUserStorage(t, t.TempDir(), perm, key)
	st.Users = &customFSUser{Store: st.Users, fs: mem}
	cache := newMemoryUploadCache()
	t.Cleanup(cache.Close)
	r := httptest.NewRequest("POST", "/upload.txt?override=true", nil)
	r.Header.Set("X-Auth", signToken(t, st, perm, key))
	r.Header.Set("Upload-Length", "5")
	w := httptest.NewRecorder()
	handle(tusPostHandler(cache), "", st, &settings.Server{}).ServeHTTP(w, r)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("unsupported filesystem: %d %s", w.Code, w.Body)
	}
	if content, err := afero.ReadFile(mem, "/upload.txt"); err != nil || string(content) != "keep" {
		t.Fatalf("unsupported upload truncated file: %q %v", content, err)
	}
}
