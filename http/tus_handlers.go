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
	"github.com/spf13/afero"
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

		uploadLength, err := getUploadLength(r)
		if err != nil || uploadLength < 0 {
			return http.StatusBadRequest, fmt.Errorf("invalid upload length")
		}
		// Check capability before creating or truncating any data. Production
		// OS-backed roots provide stable file IDs; an abstract adapter may not.
		rootInfo, err := d.user.Fs.Stat("/")
		if err != nil {
			return errToStatus(err), err
		}
		if !os.SameFile(rootInfo, rootInfo) {
			return http.StatusNotImplemented, fmt.Errorf("resumable uploads require stable filesystem identity; use regular uploads with this filesystem")
		}

		unlock, err := lockUploadPath(cache, d, r.URL.Path)
		if err != nil {
			return http.StatusConflict, err
		}
		defer unlock()

		file, err := files.NewFileInfo(&files.FileOptions{
			Fs:         d.user.Fs,
			Path:       r.URL.Path,
			Modify:     d.user.Perm.Modify,
			Expand:     false,
			ReadHeader: d.server.TypeDetectionByHeader,
			Checker:    d,
		})
		switch {
		case errors.Is(err, afero.ErrFileNotFound):
			dirPath := filepath.Dir(r.URL.Path)
			if _, statErr := d.user.Fs.Stat(dirPath); os.IsNotExist(statErr) {
				if mkdirErr := d.user.Fs.MkdirAll(dirPath, d.settings.DirMode); mkdirErr != nil {
					return http.StatusInternalServerError, err
				}
			}
		case err != nil:
			return errToStatus(err), err
		}

		fileFlags := os.O_CREATE | os.O_WRONLY

		// if file exists
		if file != nil {
			if file.IsDir {
				return http.StatusBadRequest, fmt.Errorf("cannot upload to a directory %s", file.RealPath())
			}

			// Existing files will remain untouched unless explicitly instructed to override
			if r.URL.Query().Get("override") != "true" {
				return http.StatusConflict, nil
			}

			// Permission for overwriting the file
			if !d.user.Perm.Modify {
				return http.StatusForbidden, nil
			}

			fileFlags |= os.O_TRUNC
		} else {
			fileFlags |= os.O_EXCL
		}

		openFile, err := d.user.Fs.OpenFile(r.URL.Path, fileFlags, d.settings.FileMode)
		if err != nil {
			return errToStatus(err), err
		}
		defer openFile.Close()
		identity, err := openFile.Stat()
		if err != nil {
			return http.StatusInternalServerError, err
		}
		if !os.SameFile(identity, identity) {
			return http.StatusNotImplemented, fmt.Errorf("resumable uploads require stable filesystem identity")
		}

		file, err = files.NewFileInfo(&files.FileOptions{
			Fs:         d.user.Fs,
			Path:       r.URL.Path,
			Modify:     d.user.Perm.Modify,
			Expand:     false,
			ReadHeader: false,
			Checker:    d,
			Content:    false,
		})
		if err != nil {
			return errToStatus(err), err
		}

		cache.Register(file.RealPath(), UploadInfo{Length: uploadLength, OwnerID: d.user.ID, Identity: identity})

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

		file, err := files.NewFileInfo(&files.FileOptions{
			Fs:         d.user.Fs,
			Path:       r.URL.Path,
			Modify:     d.user.Perm.Modify,
			Expand:     false,
			ReadHeader: d.server.TypeDetectionByHeader,
			Checker:    d,
		})
		if err != nil {
			return errToStatus(err), err
		}

		upload, status, err := uploadForUser(cache, file.RealPath(), d.user.ID)
		if status != 0 {
			return status, err
		}
		identity, err := d.user.Fs.Stat(r.URL.Path)
		if err != nil {
			return errToStatus(err), err
		}
		if !os.SameFile(upload.Identity, identity) {
			return http.StatusConflict, fmt.Errorf("upload target was replaced")
		}

		w.Header().Set("Upload-Offset", strconv.FormatInt(file.Size, 10))
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

	file, err := files.NewFileInfo(&files.FileOptions{
		Fs:         d.user.Fs,
		Path:       r.URL.Path,
		Modify:     d.user.Perm.Modify,
		Expand:     false,
		ReadHeader: d.server.TypeDetectionByHeader,
		Checker:    d,
	})

	switch {
	case errors.Is(err, afero.ErrFileNotFound):
		return http.StatusNotFound, nil
	case err != nil:
		return errToStatus(err), err
	}

	upload, status, err := uploadForUser(cache, file.RealPath(), d.user.ID)
	if status != 0 {
		return status, err
	}
	uploadLength := upload.Length

	if uploadOffset > uploadLength {
		return http.StatusBadRequest, fmt.Errorf("upload offset %d exceeds declared length %d", uploadOffset, uploadLength)
	}

	// Prevent the upload from being evicted during the transfer
	stop := keepUploadActive(cache, file.RealPath())
	defer stop()

	switch {
	case file.IsDir:
		return http.StatusBadRequest, fmt.Errorf("cannot upload to a directory %s", file.RealPath())
	case file.Size != uploadOffset:
		return http.StatusConflict, fmt.Errorf(
			"%s file size doesn't match the provided offset: %d",
			file.RealPath(),
			uploadOffset,
		)
	}

	openFile, err := d.user.Fs.OpenFile(r.URL.Path, os.O_WRONLY, d.settings.FileMode)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("could not open file: %w", err)
	}
	defer openFile.Close()
	identity, err := openFile.Stat()
	if err != nil {
		return http.StatusInternalServerError, err
	}
	if !os.SameFile(upload.Identity, identity) {
		return http.StatusConflict, fmt.Errorf("upload target was replaced")
	}
	if identity.Size() != uploadOffset {
		return http.StatusConflict, fmt.Errorf("upload offset changed before write")
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
	bytesWritten, err := io.Copy(openFile, io.LimitReader(r.Body, remaining))
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
		cache.Complete(file.RealPath())
		_ = d.RunHook(func() error { return nil }, "upload", r.URL.Path, "", d.user)
	}

	return http.StatusNoContent, nil
}

func tusDeleteHandler(cache UploadCache) handleFunc {
	return withUser(func(_ http.ResponseWriter, r *http.Request, d *data) (int, error) {
		shareMutation.Lock()
		defer shareMutation.Unlock()
		if r.URL.Path == "/" || !d.user.Perm.Delete {
			return http.StatusForbidden, nil
		}

		unlock, err := lockUploadPath(cache, d, r.URL.Path)
		if err != nil {
			return http.StatusConflict, err
		}
		defer unlock()

		file, err := files.NewFileInfo(&files.FileOptions{
			Fs:         d.user.Fs,
			Path:       r.URL.Path,
			Modify:     d.user.Perm.Modify,
			Expand:     false,
			ReadHeader: d.server.TypeDetectionByHeader,
			Checker:    d,
		})
		if err != nil {
			return errToStatus(err), err
		}

		upload, status, err := uploadForUser(cache, file.RealPath(), d.user.ID)
		if status != 0 {
			return status, err
		}
		identity, err := d.user.Fs.Stat(r.URL.Path)
		if err != nil {
			return errToStatus(err), err
		}
		if !os.SameFile(upload.Identity, identity) {
			return http.StatusConflict, fmt.Errorf("upload target was replaced")
		}

		if file.IsDir || !file.Mode.IsRegular() {
			return http.StatusBadRequest, nil
		}
		if err := revokeSharesForPath(d, r.URL.Path); err != nil {
			return errToStatus(err), err
		}
		err = d.user.Fs.Remove(r.URL.Path)
		if err != nil {
			return errToStatus(err), err
		}

		cache.Complete(file.RealPath())

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

func lockUploadPath(cache UploadCache, d *data, path string) (func(), error) {
	resolved, err := files.ResolveMissingPath(d.user.FullPath(path))
	if err != nil {
		return nil, err
	}
	if d.server.CaseInsensitiveFs {
		resolved = strings.ToLower(resolved)
	}
	return cache.Lock(resolved)
}
