package runner

import (
	"github.com/dawidpiotrowski/filebrowser-ng/v2/settings"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/users"
)

// Runner retains the configuration shape for database compatibility.
// Filebrowser-ng never runs unsandboxed commands or event hooks.
type Runner struct {
	Enabled bool
	*settings.Settings
}

func (r *Runner) RunHook(fn func() error, _, _, _ string, _ *users.User) error {
	return fn()
}
