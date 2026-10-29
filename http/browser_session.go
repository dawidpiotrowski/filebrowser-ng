package fbhttp

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	fbAuth "github.com/dawidpiotrowski/filebrowser-ng/v2/auth"
	fberrors "github.com/dawidpiotrowski/filebrowser-ng/v2/errors"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/sessions"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/users"
)

const browserAbsoluteLifetime = 24 * time.Hour
const apiAudience = "filebrowser-ng/api"

type browserState struct {
	Authenticated bool      `json:"authenticated"`
	User          *userInfo `json:"user"`
	CSRF          string    `json:"csrfToken"`
	LoginCSRF     string    `json:"loginCSRF"`
	Version       string    `json:"sessionVersion"`
	Expires       int64     `json:"expires"`
	Absolute      int64     `json:"absoluteExpires"`
}

func infoForUser(user *users.User) userInfo {
	return userInfo{ID: user.ID, Locale: user.Locale, ViewMode: user.ViewMode, SingleClick: user.SingleClick, RedirectAfterCopyMove: user.RedirectAfterCopyMove,
		Perm: user.Perm, LockPassword: user.LockPassword, Commands: user.Commands, HideDotfiles: user.HideDotfiles, DateFormat: user.DateFormat, Username: user.Username, AceEditorTheme: user.AceEditorTheme}
}

func browserID(secret string) string {
	sum := sha256.Sum256([]byte("filebrowser-ng/browser\x00" + secret))
	return hex.EncodeToString(sum[:])
}

func browserIdle(d *data) time.Duration {
	return min(d.server.GetTokenExpirationTime(DefaultTokenExpirationTime), browserAbsoluteLifetime)
}

func browserCookie(r *http.Request, d *data) (*http.Cookie, bool) {
	name, _ := cookieScope(r, d, "session")
	return uniqueCookie(r, name)
}

func authenticateBrowser(r *http.Request, d *data) (int, error) {
	cookie, ok := browserCookie(r, d)
	if !ok {
		return http.StatusUnauthorized, nil
	}
	secret, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil || len(secret) != 32 || d.store.Sessions == nil {
		return http.StatusUnauthorized, nil
	}
	session, err := d.store.Sessions.Get(browserID(cookie.Value))
	if errors.Is(err, fberrors.ErrNotExist) {
		return http.StatusUnauthorized, nil
	}
	if err != nil {
		return http.StatusInternalServerError, err
	}
	if session.Kind != sessions.Browser || session.PublicID == "" || session.AbsoluteExpires <= time.Now().Unix() {
		return http.StatusUnauthorized, nil
	}
	user, err := d.store.Users.Get(d.server.Root, d.server.FollowExternalSymlinks, session.UserID)
	if errors.Is(err, fberrors.ErrNotExist) {
		return http.StatusUnauthorized, nil
	}
	if err != nil {
		return http.StatusInternalServerError, err
	}
	if session.Credential != sessions.Credential(user) {
		return http.StatusUnauthorized, nil
	}
	if d.settings.AuthMethod == fbAuth.MethodProxyAuth && !proxyAsserts(r, d, user.ID) {
		return http.StatusUnauthorized, nil
	}
	d.user = user
	d.session = session
	d.browser = true
	return 0, nil
}

// Browser refresh never changes the cookie. Delayed renewal responses cannot
// replace a newer login, and losing a response does not lose the credential.
func refreshBrowser(d *data) error {
	idle := browserIdle(d)
	if idle <= 0 {
		return errors.New("invalid browser session timeout")
	}
	next, err := d.store.Sessions.Refresh(d.session.ID, d.session.Credential, time.Now().Add(idle).Unix())
	if err == nil {
		d.session = next
	}
	return err
}

func writeBrowserState(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Add("Vary", "Cookie")
	state := browserState{LoginCSRF: loginProof(w, r, d)}
	if d.session != nil && d.browser {
		user := infoForUser(d.user)
		state.Authenticated = true
		state.User = &user
		state.CSRF = csrfProof(d, "session", d.session.ID)
		state.Version = d.session.PublicID
		state.Expires = d.session.Expires
		state.Absolute = d.session.AbsoluteExpires
	}
	// Expire the old JWT cookie, never a current browser-session cookie. An
	// unauthenticated delayed response must not clear a more recent login.
	for _, path := range []string{"/", strings.TrimRight(d.server.BaseURL, "/") + "/"} {
		http.SetCookie(w, &http.Cookie{Name: "auth", Value: "", Path: path, MaxAge: -1, Expires: time.Unix(1, 0), HttpOnly: true, Secure: secureBrowserRequest(r, d), SameSite: http.SameSiteStrictMode})
	}
	return renderJSON(w, r, state)
}

var browserSessionHandler = func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	if r.Header.Get("X-Auth") != "" {
		return http.StatusBadRequest, nil
	}
	if !sameOriginRequest(r, d) {
		return http.StatusForbidden, nil
	}
	status, err := authenticateBrowser(r, d)
	if status != 0 && status != http.StatusUnauthorized {
		return status, err
	}
	return writeBrowserState(w, r, d)
}

func issueBrowser(w http.ResponseWriter, r *http.Request, d *data, user *users.User) (int, error) {
	idle := browserIdle(d)
	if d.store.Sessions == nil || idle <= 0 || len(d.settings.Key) == 0 {
		return http.StatusInternalServerError, errors.New("invalid browser session configuration")
	}
	secret := make([]byte, 32)
	_, _ = rand.Read(secret)
	value := base64.RawURLEncoding.EncodeToString(secret)
	now := time.Now()
	absolute := now.Add(browserAbsoluteLifetime)
	session := &sessions.Session{ID: browserID(value), Kind: sessions.Browser, PublicID: rand.Text(), UserID: user.ID, Credential: sessions.Credential(user), Created: now.Unix(), Refreshed: now.Unix(), Expires: min(now.Add(idle).Unix(), absolute.Unix()), AbsoluteExpires: absolute.Unix()}
	// The challenge family lets the server revoke an earlier concurrent login
	// even if that login's Set-Cookie response has not reached the browser yet.
	challengeName, _ := cookieScope(r, d, "login")
	challenge, ok := uniqueCookie(r, challengeName)
	if !ok || !validChallenge(challenge.Value) {
		return http.StatusForbidden, nil
	}
	session.FamilyID = csrfProof(d, "family", challenge.Value)
	oldID := ""
	if status, err := authenticateBrowser(r, d); status == 0 {
		oldID = d.session.ID
	} else if status != http.StatusUnauthorized {
		return status, err
	}
	if err := d.store.Sessions.CreateBrowser(oldID, session); err != nil {
		return http.StatusInternalServerError, err
	}
	name, path := cookieScope(r, d, "session")
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: path, HttpOnly: true, Secure: secureBrowserRequest(r, d), SameSite: http.SameSiteStrictMode, Expires: absolute})
	d.user = user
	d.session = session
	d.browser = true
	return writeBrowserState(w, r, d)
}

// API login must be explicit, use no cookies, and not be a browser request. A
// logged-in page cannot exchange its HttpOnly session for a readable bearer.
func apiEntry(r *http.Request) bool {
	return r.Header.Get("X-FB-Client") == "api" && r.Header.Get("Cookie") == "" && r.Header.Get("Origin") == "" && r.Header.Get("Referer") == "" && r.Header.Get("Sec-Fetch-Site") == "" && r.Header.Get("Sec-Fetch-Mode") == ""
}
