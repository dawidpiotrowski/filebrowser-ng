package storage

import (
	"github.com/dawidpiotrowski/filebrowser-ng/v2/auth"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/sessions"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/settings"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/share"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/users"
)

// Storage is a storage powered by a Backend which makes the necessary
// verifications when fetching and saving data to ensure consistency.
type Storage struct {
	Sessions sessions.Store
	Users    users.Store
	Share    *share.Storage
	Auth     *auth.Storage
	Settings *settings.Storage
}
