// Package sessions defines persistent, revocable login sessions.
package sessions

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	"github.com/dawidpiotrowski/filebrowser-ng/v2/users"
)

type Session struct {
	ID         string `storm:"id"`
	UserID     uint   `storm:"index"`
	Credential string
	Expires    int64 `storm:"index"`
}

type Store interface {
	Create(*Session) error
	Get(string) (*Session, error)
	// Rotate consumes the old session and creates its replacement atomically.
	Rotate(string, *Session) error
	Delete(string) error
}

// Credential binds a session to the persisted credentials, including password
// changes made by the CLI or another process. No password material enters JWTs.
func Credential(u *users.User) string {
	sum := sha256.Sum256([]byte(strconv.FormatUint(uint64(u.ID), 10) + "\x00" + u.Username + "\x00" + u.Password))
	return hex.EncodeToString(sum[:])
}
