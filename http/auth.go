package fbhttp

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/golang-jwt/jwt/v5/request"

	fbAuth "github.com/dawidpiotrowski/filebrowser-ng/v2/auth"
	fberrors "github.com/dawidpiotrowski/filebrowser-ng/v2/errors"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/sessions"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/users"
)

const (
	DefaultTokenExpirationTime = time.Hour * 2

	maxAuthBodySize = 1 << 20 // 1 MiB
)

type userInfo struct {
	ID                    uint              `json:"id"`
	Locale                string            `json:"locale"`
	ViewMode              users.ViewMode    `json:"viewMode"`
	SingleClick           bool              `json:"singleClick"`
	RedirectAfterCopyMove bool              `json:"redirectAfterCopyMove"`
	Perm                  users.Permissions `json:"perm"`
	Commands              []string          `json:"commands"`
	LockPassword          bool              `json:"lockPassword"`
	HideDotfiles          bool              `json:"hideDotfiles"`
	DateFormat            bool              `json:"dateFormat"`
	Username              string            `json:"username"`
	AceEditorTheme        string            `json:"aceEditorTheme"`
}

type authToken struct {
	User userInfo `json:"user"`
	jwt.RegisteredClaims
}

type extractor []string

func (e extractor) ExtractToken(r *http.Request) (string, error) {
	token, _ := request.HeaderExtractor{"X-Auth"}.ExtractToken(r)

	// Checks if the token isn't empty and if it contains two dots.
	// The former prevents incompatibility with URLs that previously
	// used basic auth.
	if token != "" && strings.Count(token, ".") == 2 {
		return token, nil
	}

	return "", request.ErrNoTokenInRequest
}

// proxyAsserts reports whether the proxy-auth header on r identifies the user
// the token was issued for. The username is resolved through the user store, so
// that it is matched exactly as a regular proxy login would match it.
func proxyAsserts(r *http.Request, d *data, id uint) bool {
	auther, err := d.store.Auth.Get(fbAuth.MethodProxyAuth)
	if err != nil {
		return false
	}

	proxy, ok := auther.(*fbAuth.ProxyAuth)
	if !ok || proxy.Header == "" || !proxy.Trusted(r) {
		return false
	}

	username := r.Header.Get(proxy.Header)
	if username == "" {
		return false
	}

	user, err := d.store.Users.Get(d.server.Root, d.server.FollowExternalSymlinks, username)
	if err != nil {
		return false
	}

	return user.ID == id
}

func withUser(fn handleFunc) handleFunc {
	return func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
		name, _ := cookieScope(r, d, "session")
		for _, cookie := range r.Cookies() {
			if cookie.Name == name && r.Header.Get("X-Auth") != "" {
				return http.StatusBadRequest, nil
			}
		}
		if r.Header.Get("X-Auth") == "" {
			status, err := authenticateBrowser(r, d)
			if status != 0 {
				return status, err
			}
			if !safeMethod(r.Method) && !validSessionCSRF(r, d) {
				return http.StatusForbidden, nil
			}
			// Reads caused by real application/media activity extend idle expiry.
			// Bootstrap does not, so background tabs cannot keep a session alive.
			idle := browserIdle(d)
			if idle <= 0 {
				return http.StatusInternalServerError, errors.New("invalid browser session timeout")
			}
			if time.Until(time.Unix(d.session.Expires, 0)) < idle/2 && d.session.Expires < d.session.AbsoluteExpires {
				if err = refreshBrowser(d); err != nil {
					if errors.Is(err, fberrors.ErrNotExist) {
						return http.StatusUnauthorized, nil
					}
					return http.StatusInternalServerError, err
				}
			}
			w.Header().Set("X-Session-Expires", time.Unix(d.session.Expires, 0).UTC().Format(time.RFC3339))
			if d.store.Users.LastUpdate(d.user.ID) > d.session.Refreshed {
				w.Header().Set("X-Renew-Token", "true")
			}
			canonicalizeRequestPath(r)
			return fn(w, r, d)
		}
		keyFunc := func(_ *jwt.Token) (interface{}, error) {
			return d.settings.Key, nil
		}

		var tk authToken
		p := jwt.NewParser(jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithExpirationRequired(), jwt.WithIssuer("filebrowser-ng"), jwt.WithAudience(apiAudience))
		token, err := request.ParseFromRequest(r, &extractor{}, keyFunc, request.WithClaims(&tk), request.WithParser(p))
		if err != nil || token == nil || !token.Valid || tk.ID == "" || d.store.Sessions == nil {
			return http.StatusUnauthorized, nil
		}

		expiresSoon := tk.ExpiresAt != nil && time.Until(tk.ExpiresAt.Time) < time.Hour
		updated := tk.IssuedAt != nil && tk.IssuedAt.Unix() < d.store.Users.LastUpdate(tk.User.ID)

		if expiresSoon || updated {
			w.Header().Add("X-Renew-Token", "true")
		}

		d.user, err = d.store.Users.Get(d.server.Root, d.server.FollowExternalSymlinks, tk.User.ID)
		if errors.Is(err, fberrors.ErrNotExist) {
			return http.StatusUnauthorized, nil
		}
		if err != nil {
			return http.StatusInternalServerError, err
		}

		d.session, err = d.store.Sessions.Get(tk.ID)
		if err != nil || d.session.Kind != sessions.API || d.session.UserID != d.user.ID || d.session.Credential != sessions.Credential(d.user) {
			return http.StatusUnauthorized, nil
		}
		if d.settings.AuthMethod == fbAuth.MethodProxyAuth && !proxyAsserts(r, d, tk.User.ID) {
			return http.StatusUnauthorized, nil
		}

		canonicalizeRequestPath(r)
		return fn(w, r, d)
	}
}

func withAdmin(fn handleFunc) handleFunc {
	return withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
		if !d.user.Perm.Admin {
			return http.StatusForbidden, nil
		}

		return fn(w, r, d)
	})
}

func loginHandler(tokenExpireTime time.Duration) handleFunc {
	return func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
		api := r.Header.Get("X-FB-Client") == "api"
		if api {
			if !apiEntry(r) {
				return http.StatusForbidden, nil
			}
		} else if !validLoginCSRF(r, d) {
			return http.StatusForbidden, nil
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxAuthBodySize)
		}

		auther, err := d.store.Auth.Get(d.settings.AuthMethod)
		if err != nil {
			return http.StatusInternalServerError, err
		}

		user, err := auther.Auth(r, d.store.Users, d.settings, d.server)
		switch {
		case errors.Is(err, os.ErrPermission):
			return http.StatusForbidden, nil
		case err != nil:
			return http.StatusInternalServerError, err
		}

		if api {
			return printToken(w, r, d, user, tokenExpireTime)
		}
		return issueBrowser(w, r, d, user)
	}
}

type signupBody struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

var signupHandler = func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	if !apiEntry(r) && !validLoginCSRF(r, d) {
		return http.StatusForbidden, nil
	}
	if !d.settings.Signup {
		return http.StatusMethodNotAllowed, nil
	}

	if !d.settings.CreateUserDir {
		return http.StatusForbidden, errors.New("signup requires a separate home directory for each user (createUserDir)")
	}

	if r.Body == nil {
		return http.StatusBadRequest, nil
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBodySize)

	info := &signupBody{}
	err := json.NewDecoder(r.Body).Decode(info)
	if err != nil {
		return http.StatusBadRequest, err
	}

	if info.Password == "" || info.Username == "" {
		return http.StatusBadRequest, nil
	}

	user := &users.User{
		Username: info.Username,
	}

	d.settings.Defaults.Apply(user)

	// Users signed up via the signup handler should never become admins, even
	// if that is the default permission.
	user.Perm.Admin = false

	// Self-registered users should not inherit execution capabilities from
	// default settings, regardless of what the administrator has configured
	// as the default. Execution rights must be explicitly granted by an admin.
	user.Perm.Execute = false
	user.Commands = []string{}

	pwd, err := users.ValidateAndHashPwd(info.Password, d.settings.MinimumPasswordLength)
	if err != nil {
		return http.StatusBadRequest, err
	}

	user.Password = pwd

	derivedScope, err := d.settings.CreateUserHome(user, d.server.Root, false)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	log.Printf("new user: %s, home dir: [%s].", user.Username, user.Scope)

	err = d.store.Users.SaveProvisioned(user, derivedScope)
	if errors.Is(err, fberrors.ErrExist) {
		return http.StatusConflict, err
	} else if err != nil {
		return http.StatusInternalServerError, err
	}

	return http.StatusOK, nil
}

func renewHandler(tokenExpireTime time.Duration) handleFunc {
	return withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
		w.Header().Set("X-Renew-Token", "false")
		if d.browser {
			if err := refreshBrowser(d); err != nil {
				if errors.Is(err, fberrors.ErrNotExist) {
					return http.StatusUnauthorized, nil
				}
				return http.StatusInternalServerError, err
			}
			return writeBrowserState(w, r, d)
		}
		return printToken(w, r, d, d.user, tokenExpireTime)
	})
}

func printToken(w http.ResponseWriter, r *http.Request, d *data, user *users.User, tokenExpirationTime time.Duration) (int, error) {
	if d.store.Sessions == nil || tokenExpirationTime <= 0 {
		return http.StatusInternalServerError, errors.New("invalid session configuration")
	}
	now := time.Now()
	session := &sessions.Session{ID: rand.Text(), Kind: sessions.API, UserID: user.ID, Credential: sessions.Credential(user), Expires: now.Add(tokenExpirationTime).Unix()}
	claims := &authToken{
		User: userInfo{
			ID:                    user.ID,
			Locale:                user.Locale,
			ViewMode:              user.ViewMode,
			SingleClick:           user.SingleClick,
			RedirectAfterCopyMove: user.RedirectAfterCopyMove,
			Perm:                  user.Perm,
			LockPassword:          user.LockPassword,
			Commands:              user.Commands,
			HideDotfiles:          user.HideDotfiles,
			DateFormat:            user.DateFormat,
			Username:              user.Username,
			AceEditorTheme:        user.AceEditorTheme,
		},
		RegisteredClaims: jwt.RegisteredClaims{
			Audience:  jwt.ClaimStrings{apiAudience},
			ID:        session.ID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(tokenExpirationTime)),
			Issuer:    "filebrowser-ng",
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(d.settings.Key)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	if d.session != nil {
		err = d.store.Sessions.Rotate(d.session.ID, session)
	} else {
		err = d.store.Sessions.Create(session)
	}
	if errors.Is(err, fberrors.ErrNotExist) {
		return http.StatusUnauthorized, nil
	}
	if err != nil {
		return http.StatusInternalServerError, err
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/plain")
	if _, err := w.Write([]byte(signed)); err != nil {
		return http.StatusInternalServerError, err
	}
	return 0, nil
}

var logoutHandler = withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	if err := d.store.Sessions.Delete(d.session.ID); err != nil {
		return http.StatusInternalServerError, err
	}
	// Do not clear a cookie by name: a delayed logout response could otherwise
	// erase a newer login in another tab. The persisted revocation is decisive.
	w.Header().Set("Cache-Control", "no-store")
	return http.StatusNoContent, nil
})
