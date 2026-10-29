package fbhttp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	fbAuth "github.com/dawidpiotrowski/filebrowser-ng/v2/auth"
)

const csrfHeader = "X-FB-CSRF"
const sessionHeader = "X-FB-Session"
const loginChallengeLifetime = 30 * time.Minute

func trustedProxy(r *http.Request, d *data) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	peer = peer.Unmap()
	for _, network := range d.server.TrustedProxies {
		if prefix, err := netip.ParsePrefix(network); err == nil && prefix.Contains(peer) {
			return true
		}
	}
	if d.settings.AuthMethod == fbAuth.MethodProxyAuth {
		a, err := d.store.Auth.Get(fbAuth.MethodProxyAuth)
		if err == nil {
			if proxy, ok := a.(*fbAuth.ProxyAuth); ok {
				return proxy.Trusted(r)
			}
		}
	}
	return false
}

func secureBrowserRequest(r *http.Request, d *data) bool {
	return r.TLS != nil || (trustedProxy(r, d) && r.Header.Get("X-Forwarded-Proto") == "https")
}

func cookieScope(r *http.Request, d *data, kind string) (string, string) {
	sum := sha256.Sum256([]byte(d.server.BaseURL))
	name := "fb_" + kind + "_" + hex.EncodeToString(sum[:8])
	path := strings.TrimRight(d.server.BaseURL, "/") + "/"
	if secureBrowserRequest(r, d) {
		return "__Host-" + name, "/"
	}
	return name, path
}

func uniqueCookie(r *http.Request, name string) (*http.Cookie, bool) {
	var found *http.Cookie
	for _, cookie := range r.Cookies() {
		if cookie.Name == name {
			if found != nil {
				return nil, false
			}
			found = cookie
		}
	}
	return found, found != nil
}

func csrfProof(d *data, kind, value string) string {
	mac := hmac.New(sha256.New, d.settings.Key)
	_, _ = mac.Write([]byte("filebrowser-ng/csrf/" + kind + "\x00" + d.server.BaseURL + "\x00" + value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func sameProof(got, want string) bool {
	return len(got) == len(want) && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func normalizedOrigin(raw string, referer bool) string {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.Fragment != "" {
		return ""
	}
	if !referer && (u.Path != "" || u.RawQuery != "") {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host = net.JoinHostPort(u.Hostname(), port)
		host = strings.ToLower(host)
	}
	return u.Scheme + "://" + host
}

// A synchronizer proof is required independently of Origin/Fetch Metadata.
// Missing browser metadata is supported for cookie clients with a valid proof;
// metadata that identifies another origin is never ignored.
func sameOriginRequest(r *http.Request, d *data) bool {
	site := r.Header.Get("Sec-Fetch-Site")
	if site != "" && site != "same-origin" && site != "none" {
		return false
	}
	scheme := "http"
	if secureBrowserRequest(r, d) {
		scheme = "https"
	}
	expected := normalizedOrigin(scheme+"://"+r.Host, false)
	if expected == "" {
		return false
	}
	if values := r.Header.Values("Origin"); len(values) > 0 {
		return len(values) == 1 && normalizedOrigin(values[0], false) == expected
	}
	if values := r.Header.Values("Referer"); len(values) > 0 {
		return len(values) == 1 && normalizedOrigin(values[0], true) == expected
	}
	return true
}

func validChallenge(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 2 {
		return false
	}
	nonce, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(nonce) != 32 {
		return false
	}
	expires, err := strconv.ParseInt(parts[1], 10, 64)
	now := time.Now().Unix()
	return err == nil && expires > now && expires <= now+int64(loginChallengeLifetime/time.Second)+1
}

func loginProof(w http.ResponseWriter, r *http.Request, d *data) string {
	name, path := cookieScope(r, d, "login")
	cookie, ok := uniqueCookie(r, name)
	if !ok || !validChallenge(cookie.Value) {
		nonce := make([]byte, 32)
		_, _ = rand.Read(nonce)
		expires := time.Now().Add(loginChallengeLifetime)
		cookie = &http.Cookie{Name: name, Value: base64.RawURLEncoding.EncodeToString(nonce) + "." + strconv.FormatInt(expires.Unix(), 10), Path: path, Expires: expires, HttpOnly: true, Secure: secureBrowserRequest(r, d), SameSite: http.SameSiteStrictMode}
		http.SetCookie(w, cookie)
	}
	return csrfProof(d, "login", cookie.Value)
}

func validLoginCSRF(r *http.Request, d *data) bool {
	name, _ := cookieScope(r, d, "login")
	cookie, ok := uniqueCookie(r, name)
	return ok && validChallenge(cookie.Value) && sameOriginRequest(r, d) && sameProof(r.Header.Get(csrfHeader), csrfProof(d, "login", cookie.Value))
}

func validSessionCSRF(r *http.Request, d *data) bool {
	return sameOriginRequest(r, d) && sameProof(r.Header.Get(csrfHeader), csrfProof(d, "session", d.session.ID)) && sameProof(r.Header.Get(sessionHeader), d.session.PublicID)
}

func safeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}
