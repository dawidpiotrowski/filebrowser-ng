package fbhttp

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/dawidpiotrowski/filebrowser-ng/v2/auth"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/diskcache"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/settings"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/storage"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/users"
)

func TestSecurityHeadersCoverApplicationFallbackAndAPI(t *testing.T) {
	st := scopedUserStorage(t, t.TempDir(), users.Permissions{}, []byte("key"))
	if err := st.Auth.Save(&auth.JSONAuth{}); err != nil {
		t.Fatal(err)
	}
	set, err := st.Settings.Get()
	if err != nil {
		t.Fatal(err)
	}
	set.AuthMethod = auth.MethodJSONAuth
	if err := st.Settings.Save(set); err != nil {
		t.Fatal(err)
	}
	assets := fstest.MapFS{
		"public/index.html": {Data: []byte(`<html><script>window.FileBrowser = [{[ .Json ]}];</script></html>`)},
		"test.css":          {Data: []byte(`body { color: black; }`)},
	}
	h, err := NewHandler(nil, diskcache.NewNoOp(), nil, st, &settings.Server{Root: t.TempDir()}, assets)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/", "/login", "/files/", "/settings/profile", "/static/test.css", "/health", "/api/resources/", "/api/unknown"} {
		t.Run(target, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", target, nil))
			if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'self'") || !strings.Contains(csp, "base-uri 'self'") {
				t.Errorf("missing framing/base policy on status %d: %q", w.Code, csp)
			}
			for name, want := range map[string]string{
				"X-Content-Type-Options": "nosniff",
				"X-Frame-Options":        "SAMEORIGIN",
				"Referrer-Policy":        "no-referrer",
			} {
				if got := w.Header().Get(name); got != want {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}
			if target == "/api/resources/" && !strings.Contains(w.Header().Get("Content-Security-Policy"), "default-src 'self'") {
				t.Error("API/content policy was weakened")
			}
		})
	}
}

type failingSettingsBackend struct{ settings.StorageBackend }

func (failingSettingsBackend) Get() (*settings.Settings, error) {
	return nil, errors.New("unavailable database")
}

func TestSettingsReadFailureReturns500(t *testing.T) {
	st := &storage.Storage{Settings: settings.NewStorage(failingSettingsBackend{})}
	called := false
	h := handle(func(http.ResponseWriter, *http.Request, *data) (int, error) {
		called = true
		return 0, nil
	}, "", st, &settings.Server{})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusInternalServerError || called {
		t.Fatalf("settings failure: status=%d handler-called=%v", w.Code, called)
	}
}
