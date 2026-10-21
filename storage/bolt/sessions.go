package bolt

import (
	"errors"
	"time"

	"github.com/asdine/storm/v3"
	"github.com/asdine/storm/v3/q"
	fberrors "github.com/dawidpiotrowski/filebrowser-ng/v2/errors"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/sessions"
)

type sessionBackend struct{ db *storm.DB }

func (s sessionBackend) Create(session *sessions.Session) error {
	// Keep expired records from accumulating even on installations with frequent logins.
	err := s.db.Select(q.Lte("Expires", time.Now().Unix())).Delete(&sessions.Session{})
	if err != nil && !errors.Is(err, storm.ErrNotFound) {
		return err
	}
	return s.db.Save(session)
}

func (s sessionBackend) Get(id string) (*sessions.Session, error) {
	var session sessions.Session
	if err := s.db.One("ID", id, &session); err != nil {
		if errors.Is(err, storm.ErrNotFound) {
			return nil, fberrors.ErrNotExist
		}
		return nil, err
	}
	if session.Expires <= time.Now().Unix() {
		if err := s.Delete(id); err != nil {
			return nil, err
		}
		return nil, fberrors.ErrNotExist
	}
	return &session, nil
}

func (s sessionBackend) Rotate(oldID string, next *sessions.Session) error {
	tx, err := s.db.Begin(true)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var old sessions.Session
	if err := tx.One("ID", oldID, &old); err != nil {
		return fberrors.ErrNotExist
	}
	if old.Expires <= time.Now().Unix() || old.UserID != next.UserID || old.Credential != next.Credential {
		return fberrors.ErrNotExist
	}
	if err := tx.DeleteStruct(&old); err != nil {
		return err
	}
	if err := tx.Save(next); err != nil {
		return err
	}
	return tx.Commit()
}

func (s sessionBackend) Delete(id string) error {
	err := s.db.DeleteStruct(&sessions.Session{ID: id})
	if errors.Is(err, storm.ErrNotFound) {
		return nil
	}
	return err
}
