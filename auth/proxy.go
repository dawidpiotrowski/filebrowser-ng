package auth

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"os"

	fberrors "github.com/dawidpiotrowski/filebrowser-ng/v2/errors"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/settings"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/users"
)

// MethodProxyAuth is used to identify no auth.
const MethodProxyAuth settings.AuthMethod = "proxy"

// ProxyAuth is a proxy implementation of an auther.
type ProxyAuth struct {
	Header         string   `json:"header"`
	TrustedProxies []string `json:"trustedProxies"`
}

// Auth authenticates the user via an HTTP header.
func (a ProxyAuth) Auth(r *http.Request, usr users.Store, setting *settings.Settings, srv *settings.Server) (*users.User, error) {
	if !a.Trusted(r) {
		return nil, os.ErrPermission
	}
	username := r.Header.Get(a.Header)
	if username == "" {
		return nil, os.ErrPermission
	}
	user, err := usr.Get(srv.Root, srv.FollowExternalSymlinks, username)
	if errors.Is(err, fberrors.ErrNotExist) {
		return a.createUser(usr, setting, srv, username)
	}
	return user, err
}

func (a ProxyAuth) createUser(usr users.Store, setting *settings.Settings, srv *settings.Server, username string) (*users.User, error) {
	const randomPasswordLength = settings.DefaultMinimumPasswordLength + 10
	pwd, err := users.RandomPwd(randomPasswordLength)
	if err != nil {
		return nil, err
	}

	var hashedRandomPassword string
	hashedRandomPassword, err = users.ValidateAndHashPwd(pwd, setting.MinimumPasswordLength)
	if err != nil {
		return nil, err
	}

	user := &users.User{
		Username:     username,
		Password:     hashedRandomPassword,
		LockPassword: true,
	}
	setting.Defaults.Apply(user)
	user.Perm.Admin = false
	user.Perm.Execute = false
	user.Commands = []string{}

	var derivedScope bool
	if derivedScope, err = setting.CreateUserHome(user, srv.Root, false); err != nil {
		return nil, err
	}

	if err = usr.SaveProvisioned(user, derivedScope); err != nil {
		return nil, err
	}

	return user, nil
}

// LoginPage tells that proxy auth doesn't require a login page.
func (a ProxyAuth) LoginPage() bool {
	return false
}

// Trusted checks the TCP peer, never a client-controlled forwarding header.
// An empty allowlist fails closed on imported upstream configurations.
func (a ProxyAuth) Trusted(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	peer = peer.Unmap()
	for _, network := range a.TrustedProxies {
		prefix, err := netip.ParsePrefix(network)
		if err != nil {
			continue
		}
		if prefix.Contains(peer) {
			return true
		}
	}
	return false
}
