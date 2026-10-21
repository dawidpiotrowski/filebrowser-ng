package fbhttp

import (
	"net/http"
	"os"
	"testing"

	"github.com/dawidpiotrowski/filebrowser-ng/v2/diskcache"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/files"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/settings"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/users"
	"github.com/spf13/afero"
)

type concurrentCopyDestination struct {
	afero.Fs
	target  string
	created bool
}

type beforeCopyCommit struct {
	files.AtomicFile
	before func() error
}

func (f *beforeCopyCommit) Commit() (os.FileInfo, error) {
	if err := f.before(); err != nil {
		return nil, err
	}
	return f.AtomicFile.Commit()
}
func (f *concurrentCopyDestination) BeginAtomicWrite(name string, opts files.WriteOptions) (files.AtomicFile, error) {
	stage, err := files.BeginAtomicWrite(f.Fs, name, opts)
	if err != nil {
		return nil, err
	}
	return &beforeCopyCommit{AtomicFile: stage, before: func() error {
		if name == f.target && !f.created {
			f.created = true
			return afero.WriteFile(f.Fs, name, []byte("concurrent-owner-data"), 0o600)
		}
		return nil
	}}, nil
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
			mem := files.NewScopedFs(afero.NewOsFs(), t.TempDir())
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
			st.Users = &customFSUser{Store: st.Users, fs: racing, followExternal: true}
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
