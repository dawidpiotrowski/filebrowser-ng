package bolt

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/asdine/storm/v3"

	fberrors "github.com/dawidpiotrowski/filebrowser-ng/v2/errors"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/users"
)

// GetByScope must match case-insensitively: on a case-insensitive filesystem
// two scopes that differ only in case resolve to the same home directory, so
// the provisioning collision check has to treat them as the same scope.
func TestGetByScopeCaseInsensitive(t *testing.T) {
	db, err := storm.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	st, err := NewStorage(db)
	if err != nil {
		t.Fatalf("new storage: %v", err)
	}
	if err := st.Users.Save(&users.User{Username: "CaseVictim", Password: "pw", Scope: "/users/CaseVictim"}); err != nil {
		t.Fatalf("save: %v", err)
	}

	for _, scope := range []string{"/users/CaseVictim", "/users/casevictim", "/USERS/CASEVICTIM"} {
		u, err := st.Users.GetByScope(scope)
		if err != nil {
			t.Errorf("GetByScope(%q) unexpected error: %v", scope, err)
			continue
		}
		if u.Username != "CaseVictim" {
			t.Errorf("GetByScope(%q) returned %q, want CaseVictim", scope, u.Username)
		}
	}

	// A genuinely different scope must still miss (and the regex-escaped query
	// must not match a superstring).
	for _, scope := range []string{"/users/other", "/users/CaseVictimX"} {
		if _, err := st.Users.GetByScope(scope); !errors.Is(err, fberrors.ErrNotExist) {
			t.Errorf("GetByScope(%q) expected ErrNotExist, got %v", scope, err)
		}
	}
}

func TestAdminDeletionAndDemotionKeepOneAdministrator(t *testing.T) {
	db, err := storm.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	st, err := NewStorage(db)
	if err != nil {
		t.Fatal(err)
	}
	first := &users.User{Username: "first", Password: "pw", Perm: users.Permissions{Admin: true}}
	second := &users.User{Username: "second", Password: "pw", Perm: users.Permissions{Admin: true}}
	for _, u := range []*users.User{first, second} {
		if err := st.Users.Save(u); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []uint{first.ID, second.ID} {
		wg.Go(func() { results <- st.Users.Delete(id) })
	}
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("concurrent deletion must remove exactly one admin, removed %d", succeeded)
	}
	remaining, err := st.Users.Gets("", false)
	if err != nil || len(remaining) != 1 || !remaining[0].Perm.Admin {
		t.Fatalf("administrator lost: %v %v", remaining, err)
	}
	remaining[0].Perm.Admin = false
	if err := st.Users.Update(remaining[0], "Perm"); err == nil {
		t.Fatal("last administrator can be demoted")
	}
	if err := st.Users.Save(remaining[0]); err == nil {
		t.Fatal("full replacement can demote last administrator")
	}
}

func TestUserFieldUpdateRollsBackWhenLaterFieldFails(t *testing.T) {
	db, err := storm.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	back := usersBackend{db: db}
	first := &users.User{Username: "first", Password: "pw", Locale: "en"}
	if err := back.Save(first); err != nil {
		t.Fatal(err)
	}
	update := &users.User{ID: first.ID, Locale: "pl"}
	if err := back.Update(update, "Locale", "DoesNotExist"); err == nil {
		t.Fatal("expected invalid-field failure")
	}
	stored, err := back.GetBy(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Locale != "en" || stored.Username != "first" {
		t.Fatalf("failed update partially persisted: locale=%q username=%q", stored.Locale, stored.Username)
	}
}
