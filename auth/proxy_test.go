package auth

import (
	"net/http"
	"strings"
	"testing"

	fberrors "github.com/dawidpiotrowski/filebrowser-ng/v2/errors"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/settings"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/users"
)

type mockUserStore struct {
	users map[string]*users.User
}

func (m *mockUserStore) Get(_ string, _ bool, id interface{}) (*users.User, error) {
	if v, ok := id.(string); ok {
		if u, ok := m.users[v]; ok {
			return u, nil
		}
	}
	return nil, fberrors.ErrNotExist
}

func (m *mockUserStore) GetByScope(scope string) (*users.User, error) {
	for _, u := range m.users {
		if strings.EqualFold(u.Scope, scope) {
			return u, nil
		}
	}
	return nil, fberrors.ErrNotExist
}

func (m *mockUserStore) Gets(_ string, _ bool) ([]*users.User, error) { return nil, nil }
func (m *mockUserStore) Update(_ *users.User, _ ...string) error      { return nil }
func (m *mockUserStore) Save(user *users.User) error {
	m.users[user.Username] = user
	return nil
}

func (m *mockUserStore) SaveProvisioned(user *users.User, derivedScope bool) error {
	if derivedScope {
		if _, err := m.GetByScope(user.Scope); err == nil {
			return fberrors.ErrExist
		}
	}
	return m.Save(user)
}

func (m *mockUserStore) Delete(_ interface{}) error { return nil }
func (m *mockUserStore) LastUpdate(_ uint) int64    { return 0 }

func TestProxyAuthCreateUserRestrictsDefaults(t *testing.T) {
	t.Parallel()

	store := &mockUserStore{users: make(map[string]*users.User)}
	srv := &settings.Server{Root: t.TempDir()}

	s := &settings.Settings{
		Key:        []byte("key"),
		AuthMethod: MethodProxyAuth,
		Defaults: settings.UserDefaults{
			Perm: users.Permissions{
				Admin:    true,
				Execute:  true,
				Create:   true,
				Rename:   true,
				Modify:   true,
				Delete:   true,
				Share:    true,
				Download: true,
			},
			Commands: []string{"git", "ls", "cat", "id"},
		},
	}

	auth := ProxyAuth{Header: "X-Remote-User", TrustedProxies: []string{"127.0.0.1/32"}}
	req, _ := http.NewRequest(http.MethodGet, "/", http.NoBody)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Remote-User", "newproxyuser")

	user, err := auth.Auth(req, store, s, srv)
	if err != nil {
		t.Fatalf("Auth() error: %v", err)
	}

	if user.Perm.Admin {
		t.Error("auto-provisioned proxy user should not have Admin permission")
	}
	if user.Perm.Execute {
		t.Error("auto-provisioned proxy user should not have Execute permission")
	}
	if len(user.Commands) != 0 {
		t.Errorf("auto-provisioned proxy user should have empty Commands, got %v", user.Commands)
	}
	if !user.Perm.Create {
		t.Error("auto-provisioned proxy user should retain Create permission from defaults")
	}
}

// With CreateUserDir enabled, two distinct proxy-authenticated users must each
// receive their own home directory instead of both inheriting the server root.
func TestProxyAuthCreateUserDirIsolatesScope(t *testing.T) {
	t.Parallel()

	store := &mockUserStore{users: make(map[string]*users.User)}
	srv := &settings.Server{Root: t.TempDir()}
	s := &settings.Settings{
		Key:              []byte("key"),
		AuthMethod:       MethodProxyAuth,
		CreateUserDir:    true,
		UserHomeBasePath: "/users",
		Defaults: settings.UserDefaults{
			Scope: ".",
			Perm:  users.Permissions{Create: true},
		},
	}

	auth := ProxyAuth{Header: "X-Remote-User", TrustedProxies: []string{"127.0.0.1/32"}}
	provision := func(name string) *users.User {
		req, _ := http.NewRequest(http.MethodGet, "/", http.NoBody)
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("X-Remote-User", name)
		u, err := auth.Auth(req, store, s, srv)
		if err != nil {
			t.Fatalf("Auth(%q) error: %v", name, err)
		}
		return u
	}

	alice := provision("alice")
	bob := provision("bob")

	if alice.Scope == "/" || bob.Scope == "/" {
		t.Fatalf("provisioned users inherited the server root: alice=%q bob=%q", alice.Scope, bob.Scope)
	}
	if alice.Scope == bob.Scope {
		t.Fatalf("distinct users must get distinct scopes, both got %q", alice.Scope)
	}
	if alice.Scope != "/users/alice" {
		t.Errorf("expected /users/alice, got %q", alice.Scope)
	}
}

func TestProxyAuthenticationRequiresTrustedPeer(t *testing.T) {
	proxy := ProxyAuth{Header: "X-User", TrustedProxies: []string{"127.0.0.1/32", "::1/128"}}
	for _, tc := range []struct {
		peer    string
		trusted bool
	}{
		{"127.0.0.1:1234", true}, {"[::1]:1234", true}, {"203.0.113.5:1234", false}, {"", false},
	} {
		r, _ := http.NewRequest("POST", "/api/login", nil)
		r.RemoteAddr = tc.peer
		r.Header.Set("X-Forwarded-For", "127.0.0.1")
		r.Header.Set("X-User", "admin")
		if got := proxy.Trusted(r); got != tc.trusted {
			t.Errorf("peer %q trusted=%v", tc.peer, got)
		}
		if !tc.trusted {
			if _, err := proxy.Auth(r, nil, nil, nil); err == nil {
				t.Error("forged proxy identity accepted")
			}
		}
	}
}
