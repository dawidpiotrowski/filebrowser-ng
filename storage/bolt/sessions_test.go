package bolt

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/asdine/storm/v3"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/sessions"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/share"
)

func TestSessionRotationAndRevocationSurviveRestart(t *testing.T) {
	name := filepath.Join(t.TempDir(), "db")
	db, err := storm.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStorage(db)
	if err != nil {
		t.Fatal(err)
	}
	old := &sessions.Session{ID: "old", UserID: 1, Credential: "credential", Expires: time.Now().Add(time.Hour).Unix()}
	if err := store.Sessions.Create(old); err != nil {
		t.Fatal(err)
	}
	next := *old
	next.ID = "new"
	if err := store.Sessions.Rotate(old.ID, &next); err != nil {
		t.Fatal(err)
	}
	if err := store.Sessions.Rotate(old.ID, &next); err == nil {
		t.Fatal("renewal replay accepted")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = storm.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err = NewStorage(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Sessions.Get("old"); err == nil {
		t.Fatal("old session resurrected after restart")
	}
	if _, err := store.Sessions.Get("new"); err != nil {
		t.Fatal("new session lost after restart", err)
	}
	if err := store.Sessions.Delete("new"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Sessions.Get("new"); err == nil {
		t.Fatal("deleted session returned")
	}
}

func TestConsecutiveExpiredSharesAreRemovedWithoutSliceMutation(t *testing.T) {
	db, err := storm.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewStorage(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, link := range []*share.Link{
		{Hash: "expired-one", UserID: 1, Path: "/file", Expire: 1},
		{Hash: "expired-two", UserID: 1, Path: "/file", Expire: 1},
		{Hash: "valid", UserID: 1, Path: "/file"},
	} {
		if err := store.Share.Save(link); err != nil {
			t.Fatal(err)
		}
	}
	links, err := store.Share.All()
	if err != nil || len(links) != 1 || links[0].Hash != "valid" {
		t.Fatalf("filtered: %#v %v", links, err)
	}
}
