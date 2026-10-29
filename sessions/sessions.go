// Package sessions defines persistent, revocable login sessions.
package sessions

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	"github.com/dawidpiotrowski/filebrowser-ng/v2/users"
)

const Browser = "browser-v1"
const API = "api-v1"

type Session struct {
	ID              string `storm:"id"`
	UserID          uint   `storm:"index"`
	Credential      string
	Expires         int64 `storm:"index"`
	Kind            string
	PublicID        string
	FamilyID        string `storm:"index"`
	Created         int64
	Refreshed       int64
	AbsoluteExpires int64
}

type Store interface {
	Create(*Session) error
	// CreateBrowser replaces earlier logins from the same HttpOnly challenge
	// family, including a login whose cookie response was lost or delayed.
	CreateBrowser(oldID string, next *Session) error
	Get(string) (*Session, error)
	// Rotate consumes the old session and creates its replacement atomically.
	Rotate(string, *Session) error
	// Refresh extends a browser session without replacing its credential. This
	// makes concurrent tabs/retries safe and never revives revoked sessions.
	Refresh(id, credential string, expires int64) (*Session, error)
	Delete(string) error
}

// Credential binds a session to the persisted credentials, including password
// changes made by the CLI or another process. No password material enters JWTs.
func Credential(u *users.User) string {
	sum := sha256.Sum256([]byte(strconv.FormatUint(uint64(u.ID), 10) + "\x00" + u.Username + "\x00" + u.Password))
	return hex.EncodeToString(sum[:])
}
