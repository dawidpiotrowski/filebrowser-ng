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

func (s sessionBackend) CreateBrowser(oldID string, next *sessions.Session) error {
	if next.Kind != sessions.Browser || next.FamilyID == "" {
		return fberrors.ErrInvalidRequestParams
	}
	tx, err := s.db.Begin(true)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	err = tx.Select(q.Lte("Expires", time.Now().Unix())).Delete(&sessions.Session{})
	if err != nil && !errors.Is(err, storm.ErrNotFound) {
		return err
	}
	var previous []sessions.Session
	err = tx.Find("FamilyID", next.FamilyID, &previous)
	if err != nil && !errors.Is(err, storm.ErrNotFound) {
		return err
	}
	for i := range previous {
		if previous[i].Kind == sessions.Browser {
			if err = tx.DeleteStruct(&previous[i]); err != nil {
				return err
			}
		}
	}
	if oldID != "" {
		var old sessions.Session
		if err = tx.One("ID", oldID, &old); err == nil && old.Kind == sessions.Browser {
			if err = tx.DeleteStruct(&old); err != nil && !errors.Is(err, storm.ErrNotFound) {
				return err
			}
		} else if err != nil && !errors.Is(err, storm.ErrNotFound) {
			return err
		}
	}
	if err = tx.Save(next); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (s sessionBackend) Get(id string) (*sessions.Session, error) {
	var session sessions.Session
	if err := s.db.One("ID", id, &session); err != nil {
		if errors.Is(err, storm.ErrNotFound) {
			return nil, fberrors.ErrNotExist
		}
		return nil, err
	}
	if session.Expires <= time.Now().Unix() || (session.Kind == sessions.Browser && session.AbsoluteExpires <= time.Now().Unix()) {
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
	if old.Expires <= time.Now().Unix() || old.Kind != next.Kind || (old.Kind == sessions.Browser && old.AbsoluteExpires <= time.Now().Unix()) {
		return fberrors.ErrNotExist
	}
	if old.UserID != next.UserID || old.Credential != next.Credential {
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

func (s sessionBackend) Refresh(id, credential string, expires int64) (*sessions.Session, error) {
	tx, err := s.db.Begin(true)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var current sessions.Session
	if err = tx.One("ID", id, &current); err != nil {
		if errors.Is(err, storm.ErrNotFound) {
			return nil, fberrors.ErrNotExist
		}
		return nil, err
	}
	now := time.Now().Unix()
	if current.Kind != sessions.Browser || current.Credential != credential || current.Expires <= now || current.AbsoluteExpires <= now {
		return nil, fberrors.ErrNotExist
	}
	current.Expires = min(current.AbsoluteExpires, max(current.Expires, expires))
	current.Refreshed = now
	if err = tx.Save(&current); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &current, nil
}

func (s sessionBackend) Delete(id string) error {
	err := s.db.DeleteStruct(&sessions.Session{ID: id})
	if errors.Is(err, storm.ErrNotFound) {
		return nil
	}
	return err
}
