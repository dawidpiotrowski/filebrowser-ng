package fbhttp

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/dawidpiotrowski/filebrowser-ng/v2/auth"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/settings"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/users"
)

func TestBootstrapJSONRemainsAnObjectAndEscapesBranding(t *testing.T) {
	st := scopedUserStorage(t, t.TempDir(), users.Permissions{}, []byte("key"))
	if err := st.Auth.Save(&auth.JSONAuth{}); err != nil {
		t.Fatal(err)
	}
	set, err := st.Settings.Get()
	if err != nil {
		t.Fatal(err)
	}
	set.AuthMethod = auth.MethodJSONAuth
	set.Branding.Name = "</script><script>alert('x')</script>"
	fs := fstest.MapFS{"index.html": {Data: []byte(`<script>window.FileBrowser = [{[ .Json ]}];</script>`)}}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	status, err := handleWithStaticData(w, r, &data{store: st, settings: set, server: &settings.Server{}}, fs, "index.html", "text/html")
	if status != 0 || err != nil {
		t.Fatalf("render: %d %v", status, err)
	}
	body := w.Body.String()
	if strings.Count(body, "<script>") != 1 || strings.Count(body, "</script>") != 1 {
		t.Fatal("branding escaped script context", body)
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(body, "<script>window.FileBrowser = "), ";</script>")
	var data map[string]any
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		t.Fatalf("bootstrap must be an object: %s: %v", raw, err)
	}
	if data["Name"] != set.Branding.Name {
		t.Fatal("branding data changed")
	}
}

func TestAuthenticationRateBudgetIsBoundedAndRefills(t *testing.T) {
	// Use a dedicated peer; forwarded headers never select a rate-limit bucket.
	now := time.Now()
	for i := 0; i < 20; i++ {
		if !allowAuthentication("198.51.100.83:1234", now) {
			t.Fatal("burst denied")
		}
	}
	if allowAuthentication("198.51.100.83:5678", now) {
		t.Fatal("source-port rotation bypassed budget")
	}
	if !allowAuthentication("198.51.100.83:1234", now.Add(2*time.Second)) {
		t.Fatal("budget did not refill")
	}
}

func TestBrandingOverridesRejectTraversalAndEscapingSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "img"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "img", "logo.svg"), []byte("logo"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"img/../../secret", `img\..\..\secret`} {
		if ok, err := serveBrandingFile(httptest.NewRecorder(), httptest.NewRequest("GET", "/static/img/logo.svg", nil), root, name); ok || err == nil {
			t.Fatal("traversal accepted", name)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "img", "link.svg")); err == nil {
		if ok, err := serveBrandingFile(httptest.NewRecorder(), httptest.NewRequest("GET", "/static/img/link.svg", nil), root, "img/link.svg"); ok || err == nil {
			t.Fatal("escaping branding symlink accepted")
		}
	}
	w := httptest.NewRecorder()
	if ok, err := serveBrandingFile(w, httptest.NewRequest("GET", "/static/img/logo.svg", nil), root, "img/logo.svg"); !ok || err != nil || w.Body.String() != "logo" {
		t.Fatal("valid branding failed", err)
	}
}
