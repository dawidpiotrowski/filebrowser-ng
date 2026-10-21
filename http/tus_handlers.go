package fbhttp

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dawidpiotrowski/filebrowser-ng/v2/files"
)

// maxPatchDrainBytes bounds how much of a rejected PATCH body is discarded to
// keep the connection reusable. It comfortably covers a default-sized chunk;
// beyond that the body is not worth reading just to throw away.
const maxPatchDrainBytes = 32 << 20 // 32MB

// drainRequestBody discards what the client already put on the wire for a
// request the handler answered without reading. net/http only drains 256KiB on
// its own before giving up and closing the connection, and a connection closed
// while the client is still streaming a chunk reaches the browser as a transport
// error instead of the status we replied with. The client then cannot tell a
// conflict from a network fault, retries blindly, and the upload stalls.
func drainRequestBody(r *http.Request) {
	if r.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, maxPatchDrainBytes))
}

// keepUploadActive periodically touches the cache entry to prevent eviction during transfer
func keepUploadActive(cache UploadCache, filePath string) func() {
	stop := make(chan bool)

	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				cache.Touch(filePath)
			}
		}
	}()

	return func() {
		close(stop)
	}
}

func tusPostHandler(cache UploadCache) handleFunc {
	return withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
		if !d.user.Perm.Create || !d.Check(r.URL.Path) {
			return http.StatusForbidden, nil
		}
		length, err := getUploadLength(r)
		if err != nil || length < 0 {
			return http.StatusBadRequest, fmt.Errorf("invalid upload length")
		}
		unlock, err := lockUploadPath(cache, d, r.URL.Path)
		if err != nil {
			return http.StatusConflict, err
		}
		defer unlock()
		key, err := uploadPathKey(d, r.URL.Path)
		if err != nil {
			return errToStatus(err), err
		}
		if _, err = cache.Get(key); err == nil {
			return http.StatusConflict, fmt.Errorf("an upload is already active")
		}
		info, err := d.user.Fs.Stat(r.URL.Path)
		replace := err == nil
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return errToStatus(err), err
		}
		if replace {
			if !info.Mode().IsRegular() {
				return http.StatusBadRequest, nil
			}
			if r.URL.Query().Get("override") != "true" {
				return http.StatusConflict, nil
			}
			if !d.user.Perm.Modify {
				return http.StatusForbidden, nil
			}
		}
		if err = d.user.Fs.MkdirAll(filepath.Dir(r.URL.Path), d.settings.DirMode); err != nil {
			return errToStatus(err), err
		}
		precondition := r.Header.Get("If-Match")
		stage, err := files.BeginAtomicWrite(d.user.Fs, r.URL.Path, files.WriteOptions{Mode: d.settings.FileMode, Create: true, Replace: replace, Check: func(info os.FileInfo) error {
			if !files.MatchETag(precondition, info) {
				return files.ErrPrecondition
			}
			return nil
		}})
		if err != nil {
			return errToStatus(err), err
		}
		// Empty uploads still publish a complete (empty) file.
		if length == 0 {
			_, err = stage.Commit()
			_ = stage.Abort()
			if err != nil {
				return errToStatus(err), err
			}
		} else {
			cache.Register(key, UploadInfo{Length: length, OwnerID: d.user.ID, Stage: stage})
		}
		basePath := "/" + strings.Trim(strings.TrimSpace(d.server.BaseURL), "/")
		if basePath == "/" {
			basePath = ""
		}
		w.Header().Set("Location", basePath+"/api/tus"+r.URL.EscapedPath())
		return http.StatusCreated, nil
	})
}

func tusHeadHandler(cache UploadCache) handleFunc {
	return withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
		w.Header().Set("Cache-Control", "no-store")
		if !d.user.Perm.Create || !d.Check(r.URL.Path) {
			return http.StatusForbidden, nil
		}
		unlock, err := lockUploadPath(cache, d, r.URL.Path)
		if err != nil {
			return http.StatusConflict, err
		}
		defer unlock()
		key, err := uploadPathKey(d, r.URL.Path)
		if err != nil {
			return errToStatus(err), err
		}
		upload, status, err := uploadForUser(cache, key, d.user.ID)
		if status != 0 {
			return status, err
		}
		if err = upload.Stage.Validate(); err != nil {
			return errToStatus(err), err
		}
		info, err := upload.Stage.Stat()
		if err != nil {
			return errToStatus(err), err
		}
		w.Header().Set("Upload-Offset", strconv.FormatInt(info.Size(), 10))
		w.Header().Set("Upload-Length", strconv.FormatInt(upload.Length, 10))
		return http.StatusOK, nil
	})
}

func tusPatchHandler(cache UploadCache) handleFunc {
	return withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
		status, err := tusPatchUpload(w, r, d, cache)
		// A rejected chunk is still a chunk the client is streaming: read what is
		// left of it so the answer reaches the client on a connection that stays
		// usable, instead of being lost to a reset.
		if status >= 400 {
			drainRequestBody(r)
		}
		return status, err
	})
}

func tusPatchUpload(w http.ResponseWriter, r *http.Request, d *data, cache UploadCache) (int, error) {
	if !d.user.Perm.Create || !d.Check(r.URL.Path) {
		return http.StatusForbidden, nil
	}
	if r.Header.Get("Content-Type") != "application/offset+octet-stream" {
		return http.StatusUnsupportedMediaType, nil
	}

	uploadOffset, err := getUploadOffset(r)
	if err != nil {
		return http.StatusBadRequest, fmt.Errorf("invalid upload offset")
	}

	unlock, err := lockUploadPath(cache, d, r.URL.Path)
	if err != nil {
		return http.StatusConflict, err
	}
	defer unlock()

	key, err := uploadPathKey(d, r.URL.Path)
	if err != nil {
		return errToStatus(err), err
	}
	upload, status, err := uploadForUser(cache, key, d.user.ID)
	if status != 0 {
		return status, err
	}
	if err = upload.Stage.Validate(); err != nil {
		return errToStatus(err), err
	}
	uploadLength := upload.Length
	if uploadOffset > uploadLength {
		return http.StatusBadRequest, fmt.Errorf("upload offset exceeds declared length")
	}
	stop := keepUploadActive(cache, key)
	defer stop()
	openFile := upload.Stage
	info, err := openFile.Stat()
	if err != nil {
		return errToStatus(err), err
	}
	if info.Size() != uploadOffset {
		return http.StatusConflict, fmt.Errorf("upload offset does not match staged data")
	}
	_, err = openFile.Seek(uploadOffset, 0)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("could not seek file: %w", err)
	}

	// The server closes the request body once the handler returns; closing it
	// here would run before the caller gets to drain a rejected chunk, so every
	// status raised below this point would still cost the connection.
	//
	// Enforce the declared Upload-Length: never write more than the bytes
	// still expected for this upload. Reading one byte past the remainder
	// lets an over-length body be detected and rejected. Without this bound a
	// PATCH could stream arbitrary data to disk regardless of the length the
	// client declared when the upload was created.
	remaining := uploadLength - uploadOffset
	bytesWritten, err := io.Copy(openFile, io.LimitReader(&contextReader{ctx: r.Context(), Reader: r.Body}, remaining))
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("could not write to file: %w", err)
	}
	var extra [1]byte
	n, readErr := io.ReadFull(r.Body, extra[:])
	if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
		return http.StatusBadRequest, readErr
	}
	if n > 0 {
		// The client sent more than it declared; roll this chunk back so the
		// file stays consistent with the tracked offset, and reject it.
		if truncErr := openFile.Truncate(uploadOffset); truncErr != nil {
			return http.StatusInternalServerError, fmt.Errorf("could not truncate file: %w", truncErr)
		}
		return http.StatusRequestEntityTooLarge, fmt.Errorf("upload exceeds declared length of %d bytes", uploadLength)
	}

	// Sync the file to ensure all data is written to storage
	// to prevent file corruption.
	if err := openFile.Sync(); err != nil {
		return http.StatusInternalServerError, fmt.Errorf("could not sync file: %w", err)
	}

	newOffset := uploadOffset + bytesWritten
	w.Header().Set("Upload-Offset", strconv.FormatInt(newOffset, 10))

	if newOffset >= uploadLength {
		if err := r.Context().Err(); err != nil {
			return errToStatus(err), err
		}
		_, err := openFile.Commit()
		cache.Complete(key)
		_ = openFile.Abort()
		if err != nil {
			return errToStatus(err), err
		}
		_ = d.RunHook(func() error { return nil }, "upload", r.URL.Path, "", d.user)
	}

	return http.StatusNoContent, nil
}

func tusDeleteHandler(cache UploadCache) handleFunc {
	return withUser(func(_ http.ResponseWriter, r *http.Request, d *data) (int, error) {
		if r.URL.Path == "/" || !d.user.Perm.Delete || !d.Check(r.URL.Path) {
			return http.StatusForbidden, nil
		}
		unlock, err := lockUploadPath(cache, d, r.URL.Path)
		if err != nil {
			return http.StatusConflict, err
		}
		defer unlock()
		key, err := uploadPathKey(d, r.URL.Path)
		if err != nil {
			return errToStatus(err), err
		}
		upload, status, err := uploadForUser(cache, key, d.user.ID)
		if status != 0 {
			return status, err
		}
		// Cancellation removes only private staged bytes, never the visible target.
		err = upload.Stage.Abort()
		cache.Complete(key)
		if err != nil {
			return errToStatus(err), err
		}
		return http.StatusNoContent, nil
	})
}

func uploadForUser(cache UploadCache, path string, ownerID uint) (UploadInfo, int, error) {
	upload, err := cache.Get(path)
	if err != nil {
		return UploadInfo{}, http.StatusNotFound, err
	}
	if upload.OwnerID != ownerID {
		return UploadInfo{}, http.StatusForbidden, nil
	}
	cache.Touch(path)
	return upload, 0, nil
}

func getUploadLength(r *http.Request) (int64, error) {
	uploadOffset, err := strconv.ParseInt(r.Header.Get("Upload-Length"), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid upload length")
	}
	return uploadOffset, nil
}

func getUploadOffset(r *http.Request) (int64, error) {
	uploadOffset, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	if err != nil || uploadOffset < 0 {
		return 0, fmt.Errorf("invalid upload offset")
	}
	return uploadOffset, nil
}

func uploadPathKey(d *data, path string) (string, error) {
	resolved, err := files.ResolveMissingPath(d.user.FullPath(path))
	if err != nil {
		return "", err
	}
	if d.server.CaseInsensitiveFs {
		resolved = strings.ToLower(resolved)
	}
	return resolved, nil
}

func lockUploadPath(cache UploadCache, d *data, path string) (func(), error) {
	key, err := uploadPathKey(d, path)
	if err != nil {
		return nil, err
	}
	return cache.Lock(key)
}
