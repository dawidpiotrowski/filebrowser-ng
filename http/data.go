package fbhttp

import (
	"fmt"
	"log"
	"net/http"
	gopath "path"
	"strconv"

	"github.com/tomasen/realip"

	"github.com/dawidpiotrowski/filebrowser-ng/v2/files"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/rules"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/runner"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/sessions"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/settings"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/storage"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/users"
)

type handleFunc func(w http.ResponseWriter, r *http.Request, d *data) (int, error)

type data struct {
	*runner.Runner
	settings *settings.Settings
	server   *settings.Server
	store    *storage.Storage
	user     *users.User
	raw      interface{}
	session  *sessions.Session

	// checkerPrefix is prepended to every path before evaluating rules. It is
	// set when the user's filesystem has been rebased onto a subdirectory (as
	// done for public shares), so that rules — which are relative to the user's
	// original scope — are still matched against the real path instead of the
	// rebased one. Empty for regular requests.
	checkerPrefix string
}

// Check implements rules.Checker.
func (d *data) Check(path string) bool {
	return d.checkPath(path, true)
}

// CheckRules ignores the dotfile display preference for recursive operations.
func (d *data) CheckRules(path string) bool {
	return d.checkPath(path, false)
}

func (d *data) checkPath(path string, hidden bool) bool {
	if !d.checkRulePath(d.rulePath(path), hidden) {
		return false
	}
	// Rules apply to both the requested name and the actual symlink target.
	// Resolve against the owner's original root even for rebased public shares.
	if d.user.Fs == nil {
		return true
	}
	resolved, err := files.ResolvedVirtualPath(d.user.Fs, path)
	if err != nil {
		return false
	}
	return d.checkRulePath(d.rulePath(resolved), hidden)
}

func (d *data) checkRulePath(path string, hidden bool) bool {
	if hidden && d.user.HideDotfiles && rules.MatchHidden(path) {
		return false
	}
	allow := true
	for _, rule := range d.settings.Rules {
		if rule.Matches(path, d.server.CaseInsensitiveFs) {
			allow = rule.Allow
		}
	}
	for _, rule := range d.user.Rules {
		if rule.Matches(path, d.server.CaseInsensitiveFs) {
			allow = rule.Allow
		}
	}
	return allow
}

// rulePath canonicalizes path into the form the rules are written in.
func (d *data) rulePath(path string) string {
	// Rules are written as "/"-separated virtual paths, but callers hand us
	// paths built by the OS as well as ones taken from the request: afero.Walk
	// and filepath.Join use "\" on Windows, where the filesystem also treats it
	// as a separator. Canonicalize first so the authorization decision does not
	// depend on which separator the caller happened to use.
	path = slashClean(path)

	// When the filesystem has been rebased (e.g. a public share rooted at a
	// subdirectory), the incoming path is relative to that root. Resolve it
	// back to the user's original scope before matching rules, otherwise rules
	// targeting paths below the share root would be silently bypassed.
	if d.checkerPrefix != "" {
		path = gopath.Join(d.checkerPrefix, path)
	}

	return path
}

func handle(fn handleFunc, prefix string, store *storage.Storage, server *settings.Server) http.Handler {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range globalHeaders {
			w.Header().Set(k, v)
		}

		settings, err := store.Settings.Get()
		if err != nil {
			log.Fatalf("ERROR: couldn't get settings: %v\n", err)
			return
		}

		status, err := fn(w, r, &data{
			Runner:   &runner.Runner{Enabled: server.EnableExec, Settings: settings},
			store:    store,
			settings: settings,
			server:   server,
		})

		if status >= 400 || err != nil {
			clientIP := realip.FromRequest(r)
			log.Printf("request path=%q status=%d peer=%q error=%q", r.URL.Path, status, clientIP, fmt.Sprint(err))
		}

		if status != 0 {
			txt := http.StatusText(status)
			if status == http.StatusBadRequest && err != nil {
				txt += " (" + err.Error() + ")"
			}
			http.Error(w, strconv.Itoa(status)+" "+txt, status)
			return
		}
	})

	return stripPrefix(prefix, handler)
}
