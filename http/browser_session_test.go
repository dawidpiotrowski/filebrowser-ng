package fbhttp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	fbAuth "github.com/dawidpiotrowski/filebrowser-ng/v2/auth"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/diskcache"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/settings"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/storage"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/users"
)

type testBrowserState struct {
	Authenticated bool      `json:"authenticated"`
	User          *userInfo `json:"user"`
	CSRF          string    `json:"csrfToken"`
	LoginCSRF     string    `json:"loginCSRF"`
	Version       string    `json:"sessionVersion"`
	Expires       int64     `json:"expires"`
	Absolute      int64     `json:"absoluteExpires"`
}

var browserFixtureSequence atomic.Uint32

type browserFixture struct {
	client *http.Client
	server *httptest.Server
	store  *storage.Storage
	base   string
}

func newBrowserFixture(t *testing.T, base string) *browserFixture {
	t.Helper()
	st := scopedUserStorage(t, t.TempDir(), users.Permissions{Create: true, Modify: true, Download: true}, []byte("browser-session-test-key"))
	set, err := st.Settings.Get()
	if err != nil {
		t.Fatal(err)
	}
	set.AuthMethod = fbAuth.MethodJSONAuth
	if err = st.Settings.Save(set); err != nil {
		t.Fatal(err)
	}
	if err = st.Auth.Save(&fbAuth.JSONAuth{}); err != nil {
		t.Fatal(err)
	}
	u, err := st.Users.Get("", false, uint(1))
	if err != nil {
		t.Fatal(err)
	}
	u.Password, err = users.ValidateAndHashPwd("Browser-Test-Password!", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.Users.Update(u, "Password"); err != nil {
		t.Fatal(err)
	}
	cache := newMemoryUploadCache()
	t.Cleanup(cache.Close)
	handler, err := NewHandler(nil, diskcache.NewNoOp(), cache, st, &settings.Server{Root: t.TempDir(), BaseURL: base}, fstest.MapFS{"public/index.html": {Data: []byte("app")}})
	if err != nil {
		t.Fatal(err)
	}
	// Each simulated browser has its own TCP peer budget. Fast Linux runs
	// must not exhaust one process-global login bucket across unrelated tests.
	fixtureID := 10 + browserFixtureSequence.Add(1)
	peer := fmt.Sprintf("127.%d.%d.%d:1234", (fixtureID>>16)&255, (fixtureID>>8)&255, fixtureID&255)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = peer
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	return &browserFixture{client: &http.Client{Jar: jar}, server: srv, store: st, base: base}
}

func (f *browserFixture) request(t *testing.T, method, path, body string, headers map[string]string) (int, http.Header, []byte) {
	t.Helper()
	r, err := http.NewRequest(method, f.server.URL+f.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if method != "GET" {
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	res, err := f.client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, res.Header, b
}
func (f *browserFixture) bootstrap(t *testing.T) testBrowserState {
	t.Helper()
	code, h, b := f.request(t, "GET", "/api/session", "", nil)
	if code != 200 || h.Get("Cache-Control") != "no-store" {
		t.Fatalf("bootstrap: %d %s", code, b)
	}
	var state testBrowserState
	if err := json.Unmarshal(b, &state); err != nil {
		t.Fatalf("bootstrap returned a bearer or invalid JSON: %s %v", b, err)
	}
	if state.LoginCSRF == "" {
		t.Fatal("bootstrap lacks login CSRF proof")
	}
	return state
}
func (f *browserFixture) login(t *testing.T) testBrowserState {
	t.Helper()
	anon := f.bootstrap(t)
	code, h, b := f.request(t, "POST", "/api/login", `{"username":"u","password":"Browser-Test-Password!"}`, map[string]string{"X-FB-CSRF": anon.LoginCSRF})
	if code != 200 || !strings.HasPrefix(h.Get("Content-Type"), "application/json") {
		t.Fatalf("browser login leaks a bearer or failed: %d %s", code, b)
	}
	var state testBrowserState
	if err := json.Unmarshal(b, &state); err != nil {
		t.Fatal(err)
	}
	if !state.Authenticated || state.User == nil || state.Version == "" || state.CSRF == "" {
		t.Fatalf("incomplete authenticated state: %s", b)
	}
	if strings.Contains(string(b), "password") {
		t.Fatal("credential material returned in browser JSON")
	}
	return state
}

func TestBrowserLoginUsesCookieAndRejectsCSRF(t *testing.T) {
	for _, base := range []string{"", "/files-app"} {
		t.Run(base, func(t *testing.T) {
			f := newBrowserFixture(t, base)
			state := f.login(t)
			if code, _, _ := f.request(t, "GET", "/api/resources/", "", nil); code != 200 {
				t.Fatal("cookie cannot read protected route", code)
			}
			valid := map[string]string{"X-FB-CSRF": state.CSRF, "X-FB-Session": state.Version}
			for _, headers := range []map[string]string{nil, {"X-FB-CSRF": "wrong", "X-FB-Session": state.Version}, {"X-FB-CSRF": state.CSRF, "X-FB-Session": "wrong"}, {"X-FB-CSRF": state.CSRF, "X-FB-Session": state.Version, "Origin": "https://evil.invalid"}, {"X-FB-CSRF": state.CSRF, "X-FB-Session": state.Version, "Sec-Fetch-Site": "same-site"}} {
				if code, _, _ := f.request(t, "POST", "/api/resources/new.txt", "bytes", headers); code != 403 {
					t.Fatal("cookie mutation bypassed CSRF", code)
				}
			}
			if code, _, b := f.request(t, "POST", "/api/resources/new.txt", "complete", valid); code != 200 {
				t.Fatalf("valid write rejected: %d %s", code, b)
			}
		})
	}
}

func TestConcurrentBrowserRenewalAndLogoutDoNotResurrectSessions(t *testing.T) {
	f := newBrowserFixture(t, "")
	state := f.login(t)
	headers := map[string]string{"X-FB-CSRF": state.CSRF, "X-FB-Session": state.Version}
	start := make(chan struct{})
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			code, h, _ := f.request(t, "POST", "/api/renew", "", headers)
			for _, c := range (&http.Response{Header: h}).Cookies() {
				if strings.Contains(c.Name, "session") {
					t.Error("renewal replaces the browser credential")
				}
			}
			codes <- code
		}()
	}
	close(start)
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != 200 {
			t.Fatal("parallel tabs invalidate each other", code)
		}
	}
	if next := f.bootstrap(t); next.Version != state.Version || next.CSRF != state.CSRF {
		t.Fatal("renewal invalidated another tab's state")
	}
	if code, _, _ := f.request(t, "POST", "/api/logout", "", headers); code != 204 {
		t.Fatal("logout failed", code)
	}
	if code, _, _ := f.request(t, "POST", "/api/renew", "", headers); code != 401 {
		t.Fatal("revoked cookie renewed", code)
	}
	if code, _, _ := f.request(t, "GET", "/api/resources/", "", nil); code != 401 {
		t.Fatal("another tab remains authorized", code)
	}
}

func TestBrowserLoginAndSignupRequireCSRF(t *testing.T) {
	f := newBrowserFixture(t, "")
	for _, path := range []string{"/api/login", "/api/signup"} {
		if code, _, _ := f.request(t, "POST", path, `{"username":"u","password":"Browser-Test-Password!"}`, nil); code != 403 {
			t.Fatal("unprotected login/signup", path, code)
		}
	}
	s := f.bootstrap(t)
	if code, _, _ := f.request(t, "POST", "/api/login", `{"username":"u","password":"Browser-Test-Password!"}`, map[string]string{"X-FB-CSRF": s.LoginCSRF, "Origin": "https://evil.test"}); code != 403 {
		t.Fatal("cross-origin login accepted", code)
	}
}

func TestBrowserAndMachineCredentialsAreSeparate(t *testing.T) {
	f := newBrowserFixture(t, "")
	// A machine must explicitly opt in, with no browser cookie or metadata.
	code, h, b := f.request(t, "POST", "/api/login", `{"username":"u","password":"Browser-Test-Password!"}`, map[string]string{"X-FB-Client": "api"})
	if code != 200 || strings.Count(string(b), ".") != 2 || len((&http.Response{Header: h}).Cookies()) != 0 {
		t.Fatalf("machine login failed: %d", code)
	}
	token := string(b)
	if code, _, _ := f.request(t, "POST", "/api/resources/machine.txt", "machine", map[string]string{"X-Auth": token}); code != 200 {
		t.Fatal("explicit API auth lost", code)
	}
	for _, headers := range []map[string]string{{"X-FB-Client": "api", "Origin": f.server.URL}, {"X-FB-Client": "api", "Sec-Fetch-Site": "same-origin"}} {
		if code, _, _ := f.request(t, "POST", "/api/login", `{"username":"u","password":"Browser-Test-Password!"}`, headers); code != 403 {
			t.Fatal("browser exchanged credentials for a bearer", code)
		}
	}
	state := f.login(t)
	if code, _, _ := f.request(t, "GET", "/api/resources/", "", map[string]string{"X-Auth": token}); code != 400 {
		t.Fatal("mixed credentials accepted", code)
	}
	if code, _, _ := f.request(t, "POST", "/api/login", `{"username":"u","password":"Browser-Test-Password!"}`, map[string]string{"X-FB-Client": "api"}); code != 403 {
		t.Fatal("cookie exchanged for bearer", code)
	}
	if code, _, _ := f.request(t, "POST", "/api/logout", "", map[string]string{"X-FB-CSRF": state.CSRF, "X-FB-Session": state.Version}); code != 204 {
		t.Fatal("logout", code)
	}
	// Bootstrap intentionally ignores the old JWT cookie and bearer header.
	jar, _ := cookiejar.New(nil)
	f.client.Jar = jar
	if code, _, _ := f.request(t, "GET", "/api/resources/", "", map[string]string{"Cookie": "auth=" + token}); code != 401 {
		t.Fatal("legacy JWT cookie accepted", code)
	}
	if code, _, _ := f.request(t, "GET", "/api/resources/", "", map[string]string{"X-Auth": token}); code != 200 {
		t.Fatal("browser logout revoked independent API session", code)
	}
	code, _, b = f.request(t, "POST", "/api/renew", "", map[string]string{"X-Auth": token})
	if code != 200 || strings.Count(string(b), ".") != 2 {
		t.Fatal("machine renewal", code)
	}
	if code, _, _ := f.request(t, "GET", "/api/resources/", "", map[string]string{"X-Auth": token}); code != 401 {
		t.Fatal("API renewal replay accepted", code)
	}
}

func TestBrowserCookieOpaqueAndPasswordChangeRevokesIt(t *testing.T) {
	f := newBrowserFixture(t, "")
	state := f.login(t)
	u, _ := url.Parse(f.server.URL)
	var secret string
	for _, c := range f.client.Jar.Cookies(u) {
		if strings.Contains(c.Name, "session") {
			secret = c.Value
		}
	}
	if secret == "" || strings.Contains(secret, ".") {
		t.Fatal("not an opaque credential")
	}
	stored, err := f.store.Sessions.Get(browserID(secret))
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID == secret || stored.PublicID != state.Version || strings.Contains(state.CSRF, secret) {
		t.Fatal("credential exposed outside cookie")
	}
	user, err := f.store.Users.Get("", false, uint(1))
	if err != nil {
		t.Fatal(err)
	}
	user.Password = "changed credential hash"
	if err = f.store.Users.Update(user, "Password"); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := f.request(t, "GET", "/api/resources/", "", nil); code != 401 {
		t.Fatal("password change did not revoke cookie", code)
	}
	if s := f.bootstrap(t); s.Authenticated || s.CSRF != "" || s.User != nil {
		t.Fatal("bootstrap exposed revoked account")
	}
}

func TestBrowserFamilyRevokesLostLoginAndDelayedLogoutCannotClearNewLogin(t *testing.T) {
	f := newBrowserFixture(t, "")
	first := f.login(t)
	u, _ := url.Parse(f.server.URL)
	var old *http.Cookie
	for _, c := range f.client.Jar.Cookies(u) {
		if strings.Contains(c.Name, "session") {
			old = c
		}
	}
	// Simulate losing the response/cookie while keeping the login challenge.
	f.client.Jar.SetCookies(u, []*http.Cookie{{Name: old.Name, Path: "/", MaxAge: -1}})
	second := f.login(t)
	if second.Version == first.Version {
		t.Fatal("login reused a session")
	}
	if _, err := f.store.Sessions.Get(browserID(old.Value)); err == nil {
		t.Fatal("lost concurrent login remains reusable")
	}
	headers := map[string]string{"X-FB-CSRF": second.CSRF, "X-FB-Session": second.Version}
	code, h, _ := f.request(t, "POST", "/api/logout", "", headers)
	if code != 204 {
		t.Fatal(code)
	}
	for _, c := range (&http.Response{Header: h}).Cookies() {
		if strings.Contains(c.Name, "session") {
			t.Fatal("delayed logout could overwrite a new login")
		}
	}
	third := f.login(t)
	if code, _, _ := f.request(t, "POST", "/api/renew", "", headers); code != 403 {
		t.Fatal("stale tab wrote under new account/session", code)
	}
	if f.bootstrap(t).Version != third.Version {
		t.Fatal("stale response revoked new login")
	}
}

func TestBrowserActivityRefreshesIdleButBootstrapDoesNot(t *testing.T) {
	f := newBrowserFixture(t, "")
	f.login(t)
	u, _ := url.Parse(f.server.URL)
	var id string
	for _, c := range f.client.Jar.Cookies(u) {
		if strings.Contains(c.Name, "session") {
			id = browserID(c.Value)
		}
	}
	session, err := f.store.Sessions.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	session.Expires = time.Now().Add(10 * time.Minute).Unix()
	if err = f.store.Sessions.Create(session); err != nil {
		t.Fatal(err)
	}
	if s := f.bootstrap(t); s.Expires != session.Expires {
		t.Fatal("background tab kept idle session alive")
	}
	if code, _, _ := f.request(t, "GET", "/api/resources/", "", nil); code != 200 {
		t.Fatal(code)
	}
	next, err := f.store.Sessions.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if next.Expires <= session.Expires || next.AbsoluteExpires != session.AbsoluteExpires {
		t.Fatal("activity did not extend idle or exceeded absolute expiry")
	}
}

func TestBrowserTUSUsesCookieCSRFAndSurvivesRenewal(t *testing.T) {
	f := newBrowserFixture(t, "")
	s := f.login(t)
	headers := map[string]string{"X-FB-CSRF": s.CSRF, "X-FB-Session": s.Version, "Upload-Length": "10"}
	if code, _, _ := f.request(t, "POST", "/api/tus/tus.txt", "", nil); code != 403 {
		t.Fatal("TUS create without CSRF", code)
	}
	code, h, b := f.request(t, "POST", "/api/tus/tus.txt", "", headers)
	if code != 201 {
		t.Fatalf("create %d %s", code, b)
	}
	location := h.Get("Location")
	headers["Content-Type"] = "application/offset+octet-stream"
	headers["Upload-Offset"] = "0"
	if code, _, b = f.request(t, "PATCH", location, "hello", headers); code != 204 {
		t.Fatalf("chunk1 %d %s", code, b)
	}
	if code, _, _ := f.request(t, "POST", "/api/renew", "", headers); code != 200 {
		t.Fatal("renew", code)
	}
	if code, _, _ := f.request(t, "HEAD", location, "", nil); code != 200 {
		t.Fatal("TUS HEAD", code)
	}
	headers["Upload-Offset"] = "5"
	if code, _, _ := f.request(t, "PATCH", location, "world", nil); code != 403 {
		t.Fatal("TUS PATCH bypassed CSRF", code)
	}
	if code, _, b = f.request(t, "PATCH", location, "world", headers); code != 204 {
		t.Fatalf("chunk2 %d %s", code, b)
	}
	if code, _, b = f.request(t, "GET", "/api/raw/tus.txt", "", nil); code != 200 || string(b) != "helloworld" {
		t.Fatalf("cookie download %d %s", code, b)
	}
}

func TestBrowserProxyAuthRequiresIdentityEveryRequestAndNoAuthBootstraps(t *testing.T) {
	for _, method := range []settings.AuthMethod{fbAuth.MethodProxyAuth, fbAuth.MethodNoAuth} {
		t.Run(string(method), func(t *testing.T) {
			f := newBrowserFixture(t, "")
			set, _ := f.store.Settings.Get()
			set.AuthMethod = method
			if err := f.store.Settings.Save(set); err != nil {
				t.Fatal(err)
			}
			var auther fbAuth.Auther
			if method == fbAuth.MethodProxyAuth {
				auther = &fbAuth.ProxyAuth{Header: "X-Identity", TrustedProxies: []string{"127.0.0.0/8"}}
			} else {
				auther = &fbAuth.NoAuth{}
			}
			if err := f.store.Auth.Save(auther); err != nil {
				t.Fatal(err)
			}
			anon := f.bootstrap(t)
			headers := map[string]string{"X-FB-CSRF": anon.LoginCSRF}
			if method == fbAuth.MethodProxyAuth {
				headers["X-Identity"] = "u"
			}
			code, _, b := f.request(t, "POST", "/api/login", "{}", headers)
			if code != 200 {
				t.Fatalf("login %d %s", code, b)
			}
			if code, _, _ := f.request(t, "GET", "/api/resources/", "", headers); code != 200 {
				t.Fatal("authorized proxy/noauth", code)
			}
			if method == fbAuth.MethodProxyAuth {
				for _, identity := range []string{"", "someone-else"} {
					if code, _, _ := f.request(t, "GET", "/api/resources/", "", map[string]string{"X-Identity": identity}); code != 401 {
						t.Fatal("missing/changed proxy identity accepted", code)
					}
				}
			}
		})
	}
}

func TestHTTPSBrowserSessionUsesHostCookie(t *testing.T) {
	f := newBrowserFixture(t, "/browser")
	srv := httptest.NewTLSServer(f.server.Config.Handler)
	t.Cleanup(srv.Close)
	f.server = srv
	f.client = srv.Client()
	jar, _ := cookiejar.New(nil)
	f.client.Jar = jar
	anon := f.bootstrap(t)
	code, h, b := f.request(t, "POST", "/api/login", `{"username":"u","password":"Browser-Test-Password!"}`, map[string]string{"X-FB-CSRF": anon.LoginCSRF, "Origin": srv.URL})
	if code != 200 {
		t.Fatalf("TLS login %d %s", code, b)
	}
	found := false
	for _, c := range (&http.Response{Header: h}).Cookies() {
		if strings.Contains(c.Name, "session") {
			found = true
			if !strings.HasPrefix(c.Name, "__Host-") || !c.Secure || !c.HttpOnly || c.Path != "/" || c.Domain != "" || c.SameSite != http.SameSiteStrictMode {
				t.Fatal("unsafe TLS session cookie")
			}
		}
	}
	if !found {
		t.Fatal("no TLS cookie")
	}
	if code, _, _ := f.request(t, "GET", "/api/resources/", "", nil); code != 200 {
		t.Fatal("TLS cookie unusable", code)
	}
}

func TestBrowserAccountSwitchRevokesPreviousAccount(t *testing.T) {
	f := newBrowserFixture(t, "")
	first := f.login(t)
	u, _ := url.Parse(f.server.URL)
	var old *http.Cookie
	for _, c := range f.client.Jar.Cookies(u) {
		if strings.Contains(c.Name, "session") {
			old = c
		}
	}
	previous, err := f.store.Users.Get("", false, uint(1))
	if err != nil {
		t.Fatal(err)
	}
	next := &users.User{Username: "second", Password: previous.Password, Scope: previous.Scope, Perm: previous.Perm}
	if err = f.store.Users.Save(next); err != nil {
		t.Fatal(err)
	}
	challenge := f.bootstrap(t)
	code, _, b := f.request(t, "POST", "/api/login", `{"username":"second","password":"Browser-Test-Password!"}`, map[string]string{"X-FB-CSRF": challenge.LoginCSRF})
	if code != 200 {
		t.Fatalf("switch %d %s", code, b)
	}
	state := f.bootstrap(t)
	if !state.Authenticated || state.User.ID != next.ID || state.Version == first.Version {
		t.Fatal("account switch did not replace session")
	}
	if _, err = f.store.Sessions.Get(browserID(old.Value)); err == nil {
		t.Fatal("previous account remains authorized")
	}
	if code, _, _ := f.request(t, "POST", "/api/resources/stale.txt", "old content", map[string]string{"X-FB-CSRF": first.CSRF, "X-FB-Session": first.Version}); code != 403 {
		t.Fatal("old account wrote through new cookie", code)
	}
	if code, _, _ := f.request(t, "POST", "/api/resources/current.txt", "new content", map[string]string{"X-FB-CSRF": state.CSRF, "X-FB-Session": state.Version}); code != 200 {
		t.Fatal("new account cannot write", code)
	}
}

func TestInvalidTrustedProxyCIDRRejectsStartup(t *testing.T) {
	if _, err := NewHandler(nil, nil, nil, nil, &settings.Server{TrustedProxies: []string{"trust-everyone"}}, nil); err == nil {
		t.Fatal("invalid proxy CIDR accepted")
	}
}
