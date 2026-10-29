package bolt

import (
	"path/filepath"
	"sync"
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

func TestBrowserRefreshAndRevocationSurviveRestart(t *testing.T) {
	name := filepath.Join(t.TempDir(), "db")
	db, err := storm.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	backend := sessionBackend{db: db}
	now := time.Now().Unix()
	old := &sessions.Session{ID: "browser-hash", Kind: sessions.Browser, PublicID: "public-view", FamilyID: "family", UserID: 1, Credential: "hash", Expires: now + 600, AbsoluteExpires: now + 1000}
	if err = backend.CreateBrowser("", old); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			next, e := backend.Refresh(old.ID, old.Credential, now+10000)
			if e != nil {
				t.Error(e)
			} else if next.Expires != old.AbsoluteExpires {
				t.Error("absolute expiry exceeded")
			}
		}()
	}
	wg.Wait()
	if _, err = backend.Refresh(old.ID, "wrong", now+900); err == nil {
		t.Fatal("wrong credential refreshed")
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = storm.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	backend.db = db
	if next, err := backend.Get(old.ID); err != nil || next.PublicID != old.PublicID || next.Expires != old.AbsoluteExpires {
		t.Fatal("refresh not persisted", err)
	}
	if err = backend.Delete(old.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = backend.Refresh(old.ID, old.Credential, now+900); err == nil {
		t.Fatal("revoked browser session revived")
	}
	// Even a future idle expiry cannot bypass the absolute cap.
	old.AbsoluteExpires = now - 1
	old.Expires = now + 1000
	if err = db.Save(old); err != nil {
		t.Fatal(err)
	}
	if _, err = backend.Get(old.ID); err == nil {
		t.Fatal("absolute expiry ignored")
	}
}
