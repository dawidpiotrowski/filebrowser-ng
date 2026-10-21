package fbhttp

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/dawidpiotrowski/filebrowser-ng/v2/diskcache"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/settings"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/users"
	"github.com/spf13/afero"
)

type concurrentCopyDestination struct {
	afero.Fs
	target  string
	created bool
}

func (f *concurrentCopyDestination) OpenFile(name string, flags int, mode os.FileMode) (afero.File, error) {
	if filepath.Clean(name) == filepath.FromSlash(f.target) && flags&(os.O_WRONLY|os.O_RDWR) != 0 && !f.created {
		f.created = true
		if err := afero.WriteFile(f.Fs, name, []byte("concurrent-owner-data"), 0o600); err != nil {
			return nil, err
		}
	}
	return f.Fs.OpenFile(name, flags, mode)
}

func TestCopyWithoutOverrideCannotOverwriteConcurrentDestination(t *testing.T) {
	for _, tc := range []struct{ directory, modify bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		directory := tc.directory
		name := "file"
		if directory {
			name = "directory"
		}
		if tc.modify {
			name += "-with-modify-permission"
		}
		t.Run(name, func(t *testing.T) {
			mem := afero.NewBasePathFs(afero.NewMemMapFs(), string(filepath.Separator))
			source, target := "/source", "/destination"
			if directory {
				if err := mem.MkdirAll(source, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := afero.WriteFile(mem, source+"/file.txt", []byte("replacement"), 0o600); err != nil {
					t.Fatal(err)
				}
				target += "/file.txt"
			} else if err := afero.WriteFile(mem, source, []byte("replacement"), 0o600); err != nil {
				t.Fatal(err)
			}
			racing := &concurrentCopyDestination{Fs: mem, target: target}
			key := []byte("copy-race-key")
			perm := users.Permissions{Create: true, Modify: tc.modify}
			st := scopedUserStorage(t, t.TempDir(), perm, key)
			st.Users = &customFSUser{Store: st.Users, fs: racing}
			w := securityRequest(t, st, &settings.Server{}, resourcePatchHandler(diskcache.NewNoOp()), "PATCH", source+"?action=copy&destination=%2Fdestination", signToken(t, st, perm, key), nil)
			if w.Code != http.StatusConflict {
				t.Errorf("copy overwrote a concurrent file: %d %s", w.Code, w.Body)
			}
			if content, err := afero.ReadFile(mem, target); err != nil || string(content) != "concurrent-owner-data" {
				t.Fatalf("concurrent destination was changed: %q %v", content, err)
			}
		})
	}
}
