package fbhttp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asdine/storm/v3"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/diskcache"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/files"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/settings"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/share"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/storage"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/storage/bolt"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/users"
	"github.com/spf13/afero"
	"golang.org/x/crypto/bcrypt"
)

func securityRequest(t *testing.T, st *storage.Storage, server *settings.Server, fn handleFunc, method, target, token string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, body)
	if token != "" {
		req.Header.Set("X-Auth", token)
	}
	rec := httptest.NewRecorder()
	handle(fn, "", st, server).ServeHTTP(rec, req)
	return rec
}

func TestUploadFailureCannotDeleteDirectory(t *testing.T) {
	scope := t.TempDir()
	if err := os.Mkdir(filepath.Join(scope, "tree"), 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(scope, "tree", "private.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	key := []byte("key")
	perm := users.Permissions{Create: true, Modify: true}
	st := denyRuleStorage(t, scope, "/tree/private.txt", perm, key)
	token := signToken(t, st, perm, key)
	rec := securityRequest(t, st, &settings.Server{}, resourcePostHandler(diskcache.NewNoOp()), "POST", "/tree?override=true", token, strings.NewReader("replacement"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("directory overwrite = %d", rec.Code)
	}
	if content, err := os.ReadFile(marker); err != nil || string(content) != "keep" {
		t.Fatalf("directory content lost: %q %v", content, err)
	}
}

func TestSymlinkAliasesEnforceRulesOnReadWriteAndShare(t *testing.T) {
	scope := t.TempDir()
	if err := os.Mkdir(filepath.Join(scope, "private"), 0o700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(scope, "private", "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("private", filepath.Join(scope, "alias")); err != nil {
		t.Skip(err)
	}
	key := []byte("key")
	perm := users.Permissions{Create: true, Modify: true, Download: true, Share: true, Delete: true, Rename: true}
	st := denyRuleStorage(t, scope, "/private", perm, key)
	token := signToken(t, st, perm, key)
	cache := newMemoryUploadCache()
	t.Cleanup(cache.Close)
	for _, tc := range []struct {
		method, path string
		fn           handleFunc
	}{
		{"GET", "/alias/secret.txt", rawHandler},
		{"GET", "/alias/secret.txt", resourceGetHandler},
		{"PUT", "/alias/secret.txt", resourcePutHandler},
		{"POST", "/alias/new.txt", resourcePostHandler(diskcache.NewNoOp())},
		{"POST", "/alias/secret.txt", sharePostHandler},
		{"POST", "/alias/new.txt", tusPostHandler(cache)},
		{"DELETE", "/alias/secret.txt", resourceDeleteHandler(diskcache.NewNoOp())},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			rec := securityRequest(t, st, &settings.Server{}, tc.fn, tc.method, tc.path, token, strings.NewReader("changed"))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("alias = %d: %s", rec.Code, rec.Body)
			}
		})
	}
	if content, _ := os.ReadFile(secret); string(content) != "secret" {
		t.Fatal("secret changed")
	}
}

func TestSubtitleConversionBoundsActualInput(t *testing.T) {
	fs := afero.NewMemMapFs()
	for _, ext := range []string{"srt", "ssa", "ass"} {
		name := "/large." + ext
		if err := afero.WriteFile(fs, name, []byte(strings.Repeat("x", maxSubtitleBytes+1)), 0o600); err != nil {
			t.Fatal(err)
		}
		// Deliberately stale metadata: the bounded reader must enforce the cap.
		file := &files.FileInfo{Fs: fs, Path: name, Name: filepath.Base(name), Size: 1}
		status, _ := subtitleFileHandler(httptest.NewRecorder(), httptest.NewRequest("GET", name, nil), file)
		if status != http.StatusRequestEntityTooLarge {
			t.Fatalf("%s status %d", ext, status)
		}
	}
}

type pausedUpload struct {
	started, release chan struct{}
	sent             bool
}

func (p *pausedUpload) Read(b []byte) (int, error) {
	if p.sent {
		return 0, io.EOF
	}
	p.sent = true
	close(p.started)
	<-p.release
	return copy(b, "hello"), nil
}

func TestConcurrentTusPatchCannotExceedDeclaredLength(t *testing.T) {
	scope := t.TempDir()
	key := []byte("key")
	perm := users.Permissions{Create: true, Modify: true}
	st := scopedUserStorage(t, scope, perm, key)
	token := signToken(t, st, perm, key)
	cache := newMemoryUploadCache()
	t.Cleanup(cache.Close)
	post := httptest.NewRequest("POST", "/file.txt", nil)
	post.Header.Set("Upload-Length", "5")
	post.Header.Set("X-Auth", token)
	rec := httptest.NewRecorder()
	handle(tusPostHandler(cache), "", st, &settings.Server{}).ServeHTTP(rec, post)
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	patch := func(body io.Reader) *httptest.ResponseRecorder {
		r := httptest.NewRequest("PATCH", "/file.txt", body)
		r.Header.Set("Upload-Offset", "0")
		r.Header.Set("Content-Type", "application/offset+octet-stream")
		r.Header.Set("X-Auth", token)
		w := httptest.NewRecorder()
		handle(tusPatchHandler(cache), "", st, &settings.Server{}).ServeHTTP(w, r)
		return w
	}
	p := &pausedUpload{started: make(chan struct{}), release: make(chan struct{})}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- patch(p) }()
	select {
	case <-p.started:
	case <-time.After(3 * time.Second):
		close(p.release)
		t.Fatal("first writer did not start")
	}
	second := patch(strings.NewReader("world"))
	close(p.release)
	if second.Code != http.StatusConflict {
		t.Errorf("racing writer: %d", second.Code)
	}
	select {
	case first := <-done:
		if first.Code != 204 {
			t.Fatalf("first writer: %d %s", first.Code, first.Body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("writer did not finish")
	}
	content, err := os.ReadFile(filepath.Join(scope, "file.txt"))
	if err != nil || string(content) != "hello" {
		t.Fatalf("content %q %v", content, err)
	}
}

func TestInvalidTusLengthDoesNotTruncateExistingFile(t *testing.T) {
	scope := t.TempDir()
	key := []byte("key")
	perm := users.Permissions{Create: true, Modify: true}
	if err := os.WriteFile(filepath.Join(scope, "keep.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := scopedUserStorage(t, scope, perm, key)
	token := signToken(t, st, perm, key)
	cache := newMemoryUploadCache()
	t.Cleanup(cache.Close)
	for _, length := range []string{"-1", "invalid", ""} {
		r := httptest.NewRequest("POST", "/keep.txt?override=true", nil)
		r.Header.Set("Upload-Length", length)
		r.Header.Set("X-Auth", token)
		w := httptest.NewRecorder()
		handle(tusPostHandler(cache), "", st, &settings.Server{}).ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("length %q: %d", length, w.Code)
		}
		if b, _ := os.ReadFile(filepath.Join(scope, "keep.txt")); string(b) != "keep" {
			t.Fatal("invalid request truncated file")
		}
	}
}

func TestSessionsRotateRevokeAndBindToPassword(t *testing.T) {
	st := scopedUserStorage(t, t.TempDir(), users.Permissions{}, []byte("key"))
	server := &settings.Server{}
	protected := withUser(func(_ http.ResponseWriter, _ *http.Request, _ *data) (int, error) { return http.StatusNoContent, nil })
	token := signToken(t, st, users.Permissions{}, []byte("key"))
	req := func(fn handleFunc, method, token string) *httptest.ResponseRecorder {
		return securityRequest(t, st, server, fn, method, "/", token, nil)
	}
	if w := req(protected, "GET", token); w.Code != 204 {
		t.Fatal(w.Code)
	}
	w := req(renewHandler(time.Hour), "POST", token)
	if w.Code != 200 {
		t.Fatalf("renew: %d %s", w.Code, w.Body)
	}
	next := w.Body.String()
	if w := req(protected, "GET", token); w.Code != 401 {
		t.Fatal("rotated token replayed")
	}
	if w := req(renewHandler(time.Hour), "POST", token); w.Code != 401 {
		t.Fatal("renewal replayed")
	}
	if w := req(logoutHandler, "POST", next); w.Code != 204 {
		t.Fatal("logout", w.Code)
	}
	if w := req(protected, "GET", next); w.Code != 401 {
		t.Fatal("logout token replayed")
	}
	token = signToken(t, st, users.Permissions{}, []byte("key"))
	u, err := st.Users.Get("", false, uint(1))
	if err != nil {
		t.Fatal(err)
	}
	u.Password = "new password hash"
	if err := st.Users.Update(u, "Password"); err != nil {
		t.Fatal(err)
	}
	if w := req(protected, "GET", token); w.Code != 401 {
		t.Fatal("old password session replayed")
	}
}

func TestPasswordShareUsesScopedExpiringCookie(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	link := &share.Link{Hash: "one", PasswordHash: string(hash), Token: "legacy"}
	d := &data{settings: &settings.Settings{Key: []byte("key")}, server: &settings.Server{BaseURL: "/browser"}}
	r := httptest.NewRequest("GET", "https://example.test/browser/api/public/share/one?token=legacy", nil)
	w := httptest.NewRecorder()
	status, _ := authenticateShareRequest(w, r, d, link)
	if status != 401 {
		t.Fatal("static token accepted")
	}
	r.Header.Set("X-SHARE-PASSWORD", "password")
	w = httptest.NewRecorder()
	status, err = authenticateShareRequest(w, r, d, link)
	if status != 0 || err != nil {
		t.Fatalf("password: %d %v", status, err)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing grant")
	}
	cookie := cookies[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.Path != "/browser/api/public/" || time.Until(cookie.Expires) > shareGrantLifetime {
		t.Fatalf("unsafe cookie: %#v", cookie)
	}
	r = httptest.NewRequest("GET", "https://example.test/browser/api/public/dl/one", nil)
	r.AddCookie(cookie)
	if status, err := authenticateShareRequest(httptest.NewRecorder(), r, d, link); status != 0 || err != nil {
		t.Fatal("grant rejected", status, err)
	}
	other := *link
	other.Hash = "two"
	if status, _ := authenticateShareRequest(httptest.NewRecorder(), r, d, &other); status != 401 {
		t.Fatal("grant accepted for other share")
	}
	link.Token = "rotated"
	if status, _ := authenticateShareRequest(httptest.NewRecorder(), r, d, link); status != 401 {
		t.Fatal("revoked grant accepted")
	}
}

func TestFilesystemMutationsRevokeSharesAcrossOverlappingScopes(t *testing.T) {
	for _, action := range []string{"delete", "rename"} {
		t.Run(action, func(t *testing.T) {
			root := t.TempDir()
			for _, dir := range []string{"team", "other"} {
				if err := os.Mkdir(filepath.Join(root, dir), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, dir, "file.txt"), []byte("data"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			db, err := storm.Open(filepath.Join(t.TempDir(), "db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			st, err := bolt.NewStorage(db)
			if err != nil {
				t.Fatal(err)
			}
			key := []byte("key")
			if err := st.Settings.Save(&settings.Settings{Key: key}); err != nil {
				t.Fatal(err)
			}
			perm := users.Permissions{Delete: true, Rename: true}
			for _, u := range []*users.User{{Username: "admin", Password: "pw", Scope: "/", Perm: perm}, {Username: "owner", Password: "pw", Scope: "/team"}, {Username: "unrelated", Password: "pw", Scope: "/other"}} {
				if err := st.Users.Save(u); err != nil {
					t.Fatal(err)
				}
			}
			for _, l := range []*share.Link{{Hash: "owner", UserID: 2, Path: "/file.txt"}, {Hash: "unrelated", UserID: 3, Path: "/file.txt"}} {
				if err := st.Share.Save(l); err != nil {
					t.Fatal(err)
				}
			}
			token := signToken(t, st, perm, key)
			server := &settings.Server{Root: root}
			fn := resourceDeleteHandler(diskcache.NewNoOp())
			method, target := "DELETE", "/team/file.txt"
			if action == "rename" {
				fn = resourcePatchHandler(diskcache.NewNoOp())
				method, target = "PATCH", "/team/file.txt?action=rename&destination=%2Fteam%2Fmoved.txt"
			}
			w := securityRequest(t, st, server, fn, method, target, token, nil)
			if w.Code >= 400 {
				t.Fatalf("mutation: %d %s", w.Code, w.Body)
			}
			if _, err := st.Share.GetByHash("owner"); err == nil {
				t.Fatal("stale cross-scope share survived")
			}
			if _, err := st.Share.GetByHash("unrelated"); err != nil {
				t.Fatal("unrelated same-named share removed", err)
			}
		})
	}
}
