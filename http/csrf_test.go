package fbhttp

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dawidpiotrowski/filebrowser-ng/v2/settings"
)

func TestBrowserCookieScopeAndForwardedTLS(t *testing.T) {
	for _, tc := range []struct {
		name, remote, forwarded, base string
		tls, trusted, secure          bool
	}{
		{name: "LAN", remote: "192.0.2.1:50", base: "/browser"},
		{name: "untrusted forwarded HTTPS", remote: "192.0.2.1:50", forwarded: "https", base: "/browser"},
		{name: "trusted HTTPS", remote: "192.0.2.1:50", forwarded: "https", base: "/browser", trusted: true, secure: true},
		{name: "direct TLS", remote: "192.0.2.1:50", tls: true, secure: true},
		{name: "trusted plain HTTP", remote: "192.0.2.1:50", forwarded: "http", trusted: true},
		{name: "ambiguous forwarding", remote: "192.0.2.1:50", forwarded: "https,http", trusted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &data{server: &settings.Server{BaseURL: tc.base}, settings: &settings.Settings{Key: []byte("key")}}
			if tc.trusted {
				d.server.TrustedProxies = []string{"192.0.2.0/24"}
			}
			r := httptest.NewRequest("GET", "http://files.example.test/api/session", nil)
			r.RemoteAddr = tc.remote
			r.Header.Set("X-Forwarded-Proto", tc.forwarded)
			if tc.tls {
				r.TLS = &tls.ConnectionState{}
			}
			w := httptest.NewRecorder()
			proof := loginProof(w, r, d)
			cookies := w.Result().Cookies()
			if len(cookies) != 1 {
				t.Fatal("no challenge cookie")
			}
			c := cookies[0]
			if !c.HttpOnly || c.Secure != tc.secure || c.SameSite != http.SameSiteStrictMode || c.Domain != "" {
				t.Fatal("unsafe cookie attributes")
			}
			if strings.HasPrefix(c.Name, "__Host-") != tc.secure {
				t.Fatal("missing secure cookie prefix")
			}
			wantPath := tc.base + "/"
			if tc.secure {
				wantPath = "/"
			}
			if c.Path != wantPath {
				t.Fatal("cookie path", c.Path)
			}
			r.AddCookie(c)
			r.Header.Set(csrfHeader, proof)
			if !validLoginCSRF(r, d) {
				t.Fatal("own login proof rejected")
			}
			d.server.BaseURL = "/other"
			if validLoginCSRF(r, d) {
				t.Fatal("proof crossed BaseURL namespace")
			}
		})
	}
}

func TestCSRFOriginValidation(t *testing.T) {
	d := &data{server: &settings.Server{}, settings: &settings.Settings{Key: []byte("key")}}
	for _, tc := range []struct {
		name    string
		headers map[string]string
		ok      bool
	}{
		{name: "missing metadata", ok: true},
		{name: "own origin", headers: map[string]string{"Origin": "https://files.example.test"}, ok: true},
		{name: "default port", headers: map[string]string{"Origin": "https://files.example.test:443"}, ok: true},
		{name: "own referer", headers: map[string]string{"Referer": "https://files.example.test/files/a?x=y"}, ok: true},
		{name: "sibling", headers: map[string]string{"Origin": "https://evil.example.test"}},
		{name: "null", headers: map[string]string{"Origin": "null"}},
		{name: "origin includes path", headers: map[string]string{"Origin": "https://files.example.test/"}},
		{name: "cross site", headers: map[string]string{"Sec-Fetch-Site": "cross-site"}},
		{name: "same site sibling", headers: map[string]string{"Sec-Fetch-Site": "same-site"}},
		{name: "foreign referer", headers: map[string]string{"Referer": "https://evil.test/files/"}},
		{name: "origin wins", headers: map[string]string{"Origin": "https://evil.test", "Referer": "https://files.example.test/"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "https://files.example.test/api/login", nil)
			for k, v := range tc.headers {
				r.Header.Set(k, v)
			}
			if sameOriginRequest(r, d) != tc.ok {
				t.Fatal("origin result")
			}
		})
	}
	r := httptest.NewRequest("POST", "https://files.example.test/api/login", nil)
	r.Header.Add("Origin", "https://files.example.test")
	r.Header.Add("Origin", "https://evil.test")
	if sameOriginRequest(r, d) {
		t.Fatal("duplicate Origin accepted")
	}
	r = httptest.NewRequest("GET", "http://files.example.test/api/session", nil)
	w := httptest.NewRecorder()
	proof := loginProof(w, r, d)
	c := w.Result().Cookies()[0]
	r.AddCookie(c)
	r.AddCookie(c)
	r.Header.Set(csrfHeader, proof)
	if validLoginCSRF(r, d) {
		t.Fatal("ambiguous cookies accepted")
	}
}
