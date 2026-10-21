package fbhttp

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/dawidpiotrowski/filebrowser-ng/v2/diskcache"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/files"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/settings"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/share"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/users"
	"github.com/spf13/afero"
)

type racingRenameFs struct{ afero.Fs }

func (f racingRenameFs) plant(dst string) error {
	return afero.WriteFile(f.Fs, dst, []byte("concurrent-owner-data"), 0o600)
}

func (f racingRenameFs) Rename(src, dst string) error {
	if err := f.plant(dst); err != nil {
		return err
	}
	return f.Fs.Rename(src, dst)
}

func (f racingRenameFs) RenameNoReplace(src, dst string) error {
	if err := f.plant(dst); err != nil {
		return err
	}
	if native, ok := f.Fs.(interface{ RenameNoReplace(string, string) error }); ok {
		return native.RenameNoReplace(src, dst)
	}
	return errors.ErrUnsupported
}

func TestRenameCannotOverwriteAConcurrentDestination(t *testing.T) {
	for _, suffix := range []string{"", "&rename=true", "&override=true&rename=true"} {
		t.Run(suffix, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "source"), []byte("source-data"), 0o600); err != nil {
				t.Fatal(err)
			}
			key := []byte("rename-race")
			perm := users.Permissions{Rename: true, Modify: true}
			st := scopedUserStorage(t, root, perm, key)
			st.Share = nil // This fixture isolates filesystem commit behavior.
			st.Users = &customFSUser{Store: st.Users, fs: racingRenameFs{files.NewScopedFs(afero.NewOsFs(), root)}, followExternal: true}
			w := securityRequest(t, st, &settings.Server{}, resourcePatchHandler(diskcache.NewNoOp()), "PATCH", "/source?action=rename&destination=%2Fdestination"+suffix, signToken(t, st, perm, key), nil)
			if w.Code != http.StatusConflict {
				t.Errorf("rename: %d %s", w.Code, w.Body)
			}
			for name, want := range map[string]string{"source": "source-data", "destination": "concurrent-owner-data"} {
				if b, err := os.ReadFile(filepath.Join(root, name)); err != nil || string(b) != want {
					t.Errorf("%s: %q %v", name, b, err)
				}
			}
		})
	}
}

type brokenWriteBody struct{ sent bool }

func (b *brokenWriteBody) Read(p []byte) (int, error) {
	if b.sent {
		return 0, io.ErrUnexpectedEOF
	}
	b.sent = true
	return copy(p, "partial"), nil
}

func TestFailedEditorAndUploadPreservePreviousFile(t *testing.T) {
	for _, method := range []string{"PUT", "POST"} {
		t.Run(method, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("original-data"), 0o640); err != nil {
				t.Fatal(err)
			}
			key := []byte("atomic-body")
			perm := users.Permissions{Create: true, Modify: true}
			st := scopedUserStorage(t, root, perm, key)
			fn := resourcePutHandler
			if method == "POST" {
				fn = resourcePostHandler(diskcache.NewNoOp())
			}
			w := securityRequest(t, st, &settings.Server{}, fn, method, "/file.txt?override=true", signToken(t, st, perm, key), &brokenWriteBody{})
			if w.Code != http.StatusInternalServerError {
				t.Errorf("broken upload accepted: %d", w.Code)
			}
			if b, err := os.ReadFile(filepath.Join(root, "file.txt")); err != nil || string(b) != "original-data" {
				t.Fatalf("old data lost: %q %v", b, err)
			}
		})
	}
}

type readWithMutation struct {
	mutate func()
	reader io.Reader
	done   bool
}

func (r *readWithMutation) Read(p []byte) (int, error) {
	if !r.done {
		r.done = true
		r.mutate()
	}
	return r.reader.Read(p)
}

func TestEditorIfMatchIsCheckedAtCommit(t *testing.T) {
	root := t.TempDir()
	name := filepath.Join(root, "file.txt")
	if err := os.WriteFile(name, []byte("initial"), 0o600); err != nil {
		t.Fatal(err)
	}
	key := []byte("conditional-editor")
	perm := users.Permissions{Modify: true, Download: true}
	st := scopedUserStorage(t, root, perm, key)
	token := signToken(t, st, perm, key)
	server := &settings.Server{}
	get := securityRequest(t, st, server, resourceGetHandler, "GET", "/file.txt", token, nil)
	etag := get.Header().Get("ETag")
	if get.Code != 200 || etag == "" {
		t.Fatalf("GET: %d %s", get.Code, get.Body)
	}
	save := func(body io.Reader, tag string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("PUT", "/file.txt", body)
		r.Header.Set("X-Auth", token)
		r.Header.Set("If-Match", tag)
		w := httptest.NewRecorder()
		handle(resourcePutHandler, "", st, server).ServeHTTP(w, r)
		return w
	}
	body := &readWithMutation{reader: strings.NewReader("stale-editor"), mutate: func() {
		if w := save(strings.NewReader("newer-version"), etag); w.Code != 200 {
			t.Fatalf("winner: %d %s", w.Code, w.Body)
		}
	}}
	if w := save(body, etag); w.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale commit: %d %s", w.Code, w.Body)
	}
	if b, _ := os.ReadFile(name); string(b) != "newer-version" {
		t.Fatal("newer version lost", string(b))
	}
	fresh := securityRequest(t, st, server, resourceGetHandler, "GET", "/file.txt", token, nil).Header().Get("ETag")
	if w := save(strings.NewReader("latest"), fresh); w.Code != 200 || w.Header().Get("ETag") == fresh {
		t.Fatalf("valid save: %d %s", w.Code, w.Body)
	}
	if w := save(strings.NewReader("old"), etag); w.Code != http.StatusPreconditionFailed {
		t.Fatal("old token accepted", w.Code)
	}
}

func TestCancellationBeforeCommitPreservesOldContent(t *testing.T) {
	root := t.TempDir()
	name := filepath.Join(root, "file.txt")
	os.WriteFile(name, []byte("original"), 0o600)
	ctx, cancel := context.WithCancel(context.Background())
	body := &readWithMutation{reader: strings.NewReader("new"), mutate: cancel}
	_, err := writeFileConditional(ctx, files.NewScopedFs(afero.NewOsFs(), root), "/file.txt", body, 0o600, 0o700, os.O_WRONLY|os.O_TRUNC, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored", err)
	}
	if b, _ := os.ReadFile(name); string(b) != "original" {
		t.Fatal("cancelled save published", string(b))
	}
}

type diskFullStage struct{ files.AtomicFile }

func (f *diskFullStage) Write(p []byte) (int, error) {
	n, err := f.AtomicFile.Write(p[:min(len(p), 2)])
	if err != nil {
		return n, err
	}
	return n, syscall.ENOSPC
}

type diskFullFilesystem struct{ afero.Fs }

func (f diskFullFilesystem) BeginAtomicWrite(name string, opts files.WriteOptions) (files.AtomicFile, error) {
	stage, err := files.BeginAtomicWrite(f.Fs, name, opts)
	if err != nil {
		return nil, err
	}
	return &diskFullStage{stage}, nil
}

func TestDiskFullDuringEditorOrUploadDoesNotPublishPartialData(t *testing.T) {
	for _, method := range []string{"PUT", "POST"} {
		t.Run(method, func(t *testing.T) {
			root := t.TempDir()
			name := filepath.Join(root, "file.txt")
			if err := os.WriteFile(name, []byte("old-data"), 0o600); err != nil {
				t.Fatal(err)
			}
			key := []byte("disk-full-stage")
			perm := users.Permissions{Create: true, Modify: true}
			st := scopedUserStorage(t, root, perm, key)
			st.Users = &customFSUser{Store: st.Users, fs: diskFullFilesystem{files.NewScopedFs(afero.NewOsFs(), root)}, followExternal: true}
			fn := resourcePutHandler
			if method == "POST" {
				fn = resourcePostHandler(diskcache.NewNoOp())
			}
			w := securityRequest(t, st, &settings.Server{}, fn, method, "/file.txt?override=true", signToken(t, st, perm, key), strings.NewReader("new-data"))
			if w.Code != 500 {
				t.Fatal("disk-full write accepted", w.Code, w.Body)
			}
			if b, _ := os.ReadFile(name); string(b) != "old-data" {
				t.Fatal("old data damaged", string(b))
			}
			entries, _ := os.ReadDir(root)
			if len(entries) != 1 {
				t.Fatal("failed stage remains", entries)
			}
		})
	}
}

func TestPrivateTusDataCannotLeakThroughSharesOrArchives(t *testing.T) {
	root := t.TempDir()
	key := []byte("private-staging")
	perm := users.Permissions{Create: true, Modify: true, Download: true, Share: true, Delete: true}
	st := scopedUserStorage(t, root, perm, key)
	token := signToken(t, st, perm, key)
	server := &settings.Server{}
	cache := newMemoryUploadCache()
	t.Cleanup(cache.Close)
	if w := tusOwnershipRequest(handle(tusPostHandler(cache), "", st, server), "POST", token, "0", ""); w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	if w := tusOwnershipRequest(handle(tusPatchHandler(cache), "", st, server), "PATCH", token, "0", "sec"); w.Code != 204 {
		t.Fatal(w.Code, w.Body)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatal(entries, err)
	}
	stage := entries[0].Name()
	for _, tc := range []struct {
		method string
		fn     handleFunc
	}{{"GET", resourceGetHandler}, {"GET", rawHandler}, {"POST", sharePostHandler}} {
		w := securityRequest(t, st, server, tc.fn, tc.method, "/"+stage+"/data", token, strings.NewReader(`{}`))
		if w.Code != 403 {
			t.Fatalf("private stage reachable: %s %d %s", tc.method, w.Code, w.Body)
		}
	}
	brand := httptest.NewRecorder()
	if served, err := serveBrandingFile(brand, httptest.NewRequest("GET", "/", nil), root, stage+"/data"); served || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("branding exposed stage: %v %v", served, err)
	}
	if err := st.Share.Save(&share.Link{Hash: "root", Path: "/", UserID: 1}); err != nil {
		t.Fatal(err)
	}
	publicRequest := func(fn handleFunc, target string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/public/"+target, nil)
		w := httptest.NewRecorder()
		handle(fn, "/api/public/", st, server).ServeHTTP(w, r)
		return w
	}
	listing := publicRequest(publicShareHandler, "root")
	if listing.Code != 200 || strings.Contains(listing.Body.String(), stage) {
		t.Fatalf("public listing: %d %s", listing.Code, listing.Body)
	}
	for _, fn := range []handleFunc{publicShareHandler, publicDlHandler} {
		w := publicRequest(fn, "root/"+stage+"/data")
		if w.Code != 403 {
			t.Fatalf("public staging access: %d %s", w.Code, w.Body)
		}
	}
	archive := publicRequest(publicDlHandler, "root?algo=zip")
	if archive.Code != 200 {
		t.Fatalf("archive: %d %s", archive.Code, archive.Body)
	}
	zipped, err := zip.NewReader(bytes.NewReader(archive.Body.Bytes()), int64(archive.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range zipped.File {
		if strings.Contains(entry.Name, stage) || entry.UncompressedSize64 != 0 {
			t.Fatal("staged bytes archived", entry.Name)
		}
	}
}

func TestTusCancelPreservesTheVisibleDestination(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "upload.txt"), []byte("keep"), 0o600)
	key := []byte("cancel-staging")
	perm := users.Permissions{Create: true, Modify: true, Delete: true}
	st := scopedUserStorage(t, root, perm, key)
	token := signToken(t, st, perm, key)
	cache := newMemoryUploadCache()
	t.Cleanup(cache.Close)
	r := httptest.NewRequest("POST", "/upload.txt?override=true", nil)
	r.Header.Set("Upload-Length", "5")
	r.Header.Set("X-Auth", token)
	w := httptest.NewRecorder()
	handle(tusPostHandler(cache), "", st, &settings.Server{}).ServeHTTP(w, r)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	w = tusOwnershipRequest(handle(tusDeleteHandler(cache), "", st, &settings.Server{}), "DELETE", token, "0", "")
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "upload.txt")); string(b) != "keep" {
		t.Fatal("cancel removed destination", string(b))
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 {
		t.Fatal("cancel did not remove staging", entries)
	}
}

func TestTusOverwriteDoesNotTruncateBeforeCompletion(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "upload.txt"), []byte("original-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	key := []byte("atomic-tus")
	perm := users.Permissions{Create: true, Modify: true}
	st := scopedUserStorage(t, root, perm, key)
	cache := newMemoryUploadCache()
	t.Cleanup(cache.Close)
	token := signToken(t, st, perm, key)
	r := httptest.NewRequest("POST", "/upload.txt?override=true", nil)
	r.Header.Set("X-Auth", token)
	r.Header.Set("Upload-Length", "5")
	w := httptest.NewRecorder()
	handle(tusPostHandler(cache), "", st, &settings.Server{}).ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	w = tusOwnershipRequest(handle(tusPatchHandler(cache), "", st, &settings.Server{}), "PATCH", token, "0", "he")
	if w.Code != http.StatusNoContent {
		t.Fatalf("chunk: %d %s", w.Code, w.Body)
	}
	if b, err := os.ReadFile(filepath.Join(root, "upload.txt")); err != nil || string(b) != "original-data" {
		t.Fatalf("partial upload replaced old data: %q %v", b, err)
	}
	w = tusOwnershipRequest(handle(tusPatchHandler(cache), "", st, &settings.Server{}), "PATCH", token, "2", "llo")
	if w.Code != http.StatusNoContent {
		t.Fatalf("finish: %d %s", w.Code, w.Body)
	}
	if b, err := os.ReadFile(filepath.Join(root, "upload.txt")); err != nil || string(b) != "hello" {
		t.Fatalf("commit: %q %v", b, err)
	}
}
