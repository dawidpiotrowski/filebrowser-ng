package fbhttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	fberrors "github.com/dawidpiotrowski/filebrowser-ng/v2/errors"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/files"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/fileutils"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/spf13/afero"
)

var resourceGetHandler = withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	file, err := files.NewFileInfo(&files.FileOptions{
		Fs:         d.user.Fs,
		Path:       r.URL.Path,
		Modify:     d.user.Perm.Modify,
		Expand:     true,
		ReadHeader: d.server.TypeDetectionByHeader,
		Checker:    d,
		Content:    d.user.Perm.Download,
	})
	if err != nil {
		return errToStatus(err), err
	}
	w.Header().Set("ETag", file.ETag())

	encoding := r.Header.Get("X-Encoding")
	if file.IsDir {
		file.Sorting = d.user.Sorting
		file.ApplySort()
		return renderJSON(w, r, file)
	} else if encoding == "true" {
		if !d.user.Perm.Download {
			return http.StatusAccepted, nil
		}
		if file.Type != "text" {
			return renderJSON(w, r, file)
		}

		f, err := d.user.Fs.Open(r.URL.Path)
		if err != nil {
			return errToStatus(err), err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return errToStatus(err), err
		}
		w.Header().Set("ETag", files.FileETag(info))

		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, err = io.Copy(w, f)
		return 0, err
	}

	if checksum := r.URL.Query().Get("checksum"); checksum != "" {
		if !d.user.Perm.Download {
			return http.StatusAccepted, nil
		}

		err := file.Checksum(checksum)
		if errors.Is(err, fberrors.ErrInvalidOption) {
			return http.StatusBadRequest, nil
		} else if err != nil {
			return http.StatusInternalServerError, err
		}

		// do not waste bandwidth if we just want the checksum
		file.Content = ""
	}

	return renderJSON(w, r, file)
})

func resourceDeleteHandler(fileCache FileCache) handleFunc {
	return withUser(func(_ http.ResponseWriter, r *http.Request, d *data) (int, error) {
		shareMutation.Lock()
		defer shareMutation.Unlock()
		if r.URL.Path == "/" || !d.user.Perm.Delete {
			return http.StatusForbidden, nil
		}

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

		if err = checkDescendants(d, r.URL.Path, ""); err != nil {
			return errToStatus(err), err
		}

		err = revokeSharesForPath(d, file.Path)
		if err != nil {
			log.Printf("WARNING: Error(s) occurred while deleting associated shares with file: %s", err)
		}

		// delete thumbnails
		err = delThumbs(r.Context(), fileCache, file)
		if err != nil {
			return errToStatus(err), err
		}

		err = d.RunHook(func() error {
			return d.user.Fs.RemoveAll(r.URL.Path)
		}, "delete", r.URL.Path, "", d.user)

		if err != nil {
			return errToStatus(err), err
		}

		return http.StatusNoContent, nil
	})
}

func resourcePostHandler(fileCache FileCache) handleFunc {
	return withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
		if !d.user.Perm.Create || !d.Check(r.URL.Path) {
			return http.StatusForbidden, nil
		}

		// Directories creation on POST.
		if strings.HasSuffix(r.URL.Path, "/") {
			err := d.RunHook(func() error {
				return d.user.Fs.MkdirAll(r.URL.Path, d.settings.DirMode)
			}, "upload", r.URL.Path, "", d.user)
			return errToStatus(err), err
		}

		file, err := files.NewFileInfo(&files.FileOptions{
			Fs:         d.user.Fs,
			Path:       r.URL.Path,
			Modify:     d.user.Perm.Modify,
			Expand:     false,
			ReadHeader: d.server.TypeDetectionByHeader,
			Checker:    d,
		})
		if err != nil && !os.IsNotExist(err) {
			return errToStatus(err), err
		}
		flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
		if err == nil {
			flags = os.O_WRONLY | os.O_TRUNC
			if file.IsDir || !file.Mode.IsRegular() {
				return http.StatusBadRequest, nil
			}
			if r.URL.Query().Get("override") != "true" {
				return http.StatusConflict, nil
			}

			// Permission for overwriting the file
			if !d.user.Perm.Modify {
				return http.StatusForbidden, nil
			}

			err = delThumbs(r.Context(), fileCache, file)
			if err != nil {
				return errToStatus(err), err
			}
		}

		err = d.RunHook(func() error {
			info, writeErr := writeFileConditional(r.Context(), d.user.Fs, r.URL.Path, r.Body, d.settings.FileMode, d.settings.DirMode, flags, r.Header.Get("If-Match"))
			if writeErr != nil {
				return writeErr
			}

			etag := files.FileETag(info)
			w.Header().Set("ETag", etag)
			return nil
		}, "upload", r.URL.Path, "", d.user)

		return errToStatus(err), err
	})
}

var resourcePutHandler = withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	if !d.user.Perm.Modify || !d.Check(r.URL.Path) {
		return http.StatusForbidden, nil
	}

	// Only allow PUT for files.
	if strings.HasSuffix(r.URL.Path, "/") {
		return http.StatusMethodNotAllowed, nil
	}

	exists, err := afero.Exists(d.user.Fs, r.URL.Path)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	if !exists {
		return http.StatusNotFound, nil
	}

	err = d.RunHook(func() error {
		info, writeErr := writeFileConditional(r.Context(), d.user.Fs, r.URL.Path, r.Body, d.settings.FileMode, d.settings.DirMode, os.O_WRONLY|os.O_TRUNC, r.Header.Get("If-Match"))
		if writeErr != nil {
			return writeErr
		}

		etag := files.FileETag(info)
		w.Header().Set("ETag", etag)
		return nil
	}, "save", r.URL.Path, "", d.user)

	return errToStatus(err), err
})

func resourcePatchHandler(fileCache FileCache) handleFunc {
	return withUser(func(_ http.ResponseWriter, r *http.Request, d *data) (int, error) {
		src := r.URL.Path
		dst := r.URL.Query().Get("destination")
		action := r.URL.Query().Get("action")
		dst, err := url.QueryUnescape(dst)
		dst = slashClean(dst)
		src = slashClean(src)
		if !d.Check(src) || !d.Check(dst) {
			return http.StatusForbidden, nil
		}
		if err != nil {
			return errToStatus(err), err
		}
		if dst == "/" || src == "/" {
			return http.StatusForbidden, nil
		}

		err = checkParent(src, dst)
		if err != nil {
			return http.StatusBadRequest, err
		}

		srcInfo, _ := d.user.Fs.Stat(src)
		dstInfo, _ := d.user.Fs.Stat(dst)
		same := os.SameFile(srcInfo, dstInfo)

		if action != "rename" || !same {
			override := r.URL.Query().Get("override") == "true"
			rename := r.URL.Query().Get("rename") == "true"
			if !override && !rename {
				if _, err = d.user.Fs.Stat(dst); err == nil {
					return http.StatusConflict, nil
				}
			}
			if rename {
				dst = addVersionSuffix(dst, d.user.Fs)
			}

			if override && !d.user.Perm.Modify {
				return http.StatusForbidden, nil
			}
		}

		if err = checkDescendants(d, src, dst); err != nil {
			return errToStatus(err), err
		}

		err = d.RunHook(func() error {
			overwrite := r.URL.Query().Get("override") == "true" && r.URL.Query().Get("rename") != "true"
			return patchAction(r.Context(), action, src, dst, d, fileCache, overwrite)
		}, action, src, dst, d.user)

		return errToStatus(err), err
	})
}

// checkDescendants reports an error if the rules deny any path inside the src
// tree or, when dst is not empty, the location that tree would land on. The
// handlers only authorize the root of the operation, but copy, rename and
// delete then recurse through the whole subtree, so a rule denying a descendant
// of an allowed directory would be bypassed by operating on the parent instead.
func checkDescendants(d *data, src, dst string) error {
	if len(d.settings.Rules) == 0 && len(d.user.Rules) == 0 {
		return nil
	}

	return afero.Walk(d.user.Fs, src, func(fPath string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !d.CheckRules(fPath) {
			return fberrors.ErrPermissionDenied
		}

		if dst == "" {
			return nil
		}

		rel, err := filepath.Rel(src, fPath)
		if err != nil {
			return err
		}

		if !d.CheckRules(filepath.Join(dst, rel)) {
			return fberrors.ErrPermissionDenied
		}

		return nil
	})
}

func checkParent(src, dst string) error {
	rel, err := filepath.Rel(src, dst)
	if err != nil {
		return err
	}

	rel = filepath.ToSlash(rel)
	if !strings.HasPrefix(rel, "../") && rel != ".." && rel != "." {
		return fberrors.ErrSourceIsParent
	}

	return nil
}

func addVersionSuffix(source string, afs afero.Fs) string {
	counter := 1
	dir, name := path.Split(source)
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)

	for {
		if _, err := afs.Stat(source); err != nil {
			break
		}
		renamed := fmt.Sprintf("%s(%d)%s", base, counter, ext)
		source = path.Join(dir, renamed)
		counter++
	}

	return source
}

func writeFile(afs afero.Fs, dst string, in io.Reader, fileMode, dirMode fs.FileMode, flags int) (os.FileInfo, error) {
	return writeFileConditional(context.Background(), afs, dst, in, fileMode, dirMode, flags, "")
}

func writeFileConditional(ctx context.Context, afs afero.Fs, dst string, in io.Reader, fileMode, dirMode fs.FileMode, flags int, ifMatch string) (os.FileInfo, error) {
	if flags&os.O_CREATE != 0 {
		dir, _ := path.Split(dst)
		if err := afs.MkdirAll(dir, dirMode); err != nil {
			return nil, err
		}
	}
	file, err := files.BeginAtomicWrite(afs, dst, files.WriteOptions{Mode: fileMode, Create: flags&os.O_CREATE != 0, Replace: flags&os.O_EXCL == 0, Check: func(info os.FileInfo) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !files.MatchETag(ifMatch, info) {
			return files.ErrPrecondition
		}
		return nil
	}})
	if err != nil {
		return nil, err
	}
	defer file.Abort()

	_, err = io.Copy(file, &contextReader{ctx: ctx, Reader: in})
	if err != nil {
		return nil, err
	}

	return file.Commit()
}

type contextReader struct {
	io.Reader
	ctx context.Context
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func delThumbs(ctx context.Context, fileCache FileCache, file *files.FileInfo) error {
	for _, previewSizeName := range PreviewSizeNames() {
		size, _ := ParsePreviewSize(previewSizeName)
		if err := fileCache.Delete(ctx, previewCacheKey(file, size)); err != nil {
			return err
		}
	}

	return nil
}

func patchAction(ctx context.Context, action, src, dst string, d *data, fileCache FileCache, overwrite bool) error {
	switch action {
	case "copy":
		if !d.user.Perm.Create || (overwrite && !d.user.Perm.Modify) {
			return fberrors.ErrPermissionDenied
		}

		if !overwrite {
			return fileutils.CopyExclusive(d.user.Fs, src, dst, d.settings.FileMode, d.settings.DirMode)
		}
		return fileutils.Copy(d.user.Fs, src, dst, d.settings.FileMode, d.settings.DirMode)
	case "rename":
		shareMutation.Lock()
		defer shareMutation.Unlock()
		if !d.user.Perm.Rename || (overwrite && !d.user.Perm.Modify) {
			return fberrors.ErrPermissionDenied
		}
		src = path.Clean("/" + src)
		dst = path.Clean("/" + dst)

		file, err := files.NewFileInfo(&files.FileOptions{
			Fs:         d.user.Fs,
			Path:       src,
			Modify:     d.user.Perm.Modify,
			Expand:     false,
			ReadHeader: false,
			Checker:    d,
		})
		if err != nil {
			return err
		}

		// delete thumbnails
		err = delThumbs(ctx, fileCache, file)
		if err != nil {
			return err
		}

		if err := revokeSharesForPath(d, src); err != nil {
			return err
		}
		if err := revokeSharesForPath(d, dst); err != nil {
			return err
		}
		return fileutils.MoveFileWithPolicy(d.user.Fs, src, dst, d.settings.FileMode, d.settings.DirMode, overwrite)
	default:
		return fmt.Errorf("unsupported action %s: %w", action, fberrors.ErrInvalidRequestParams)
	}
}

// RecursiveEntry is a single file/directory entry returned by the recursive listing endpoint.
type RecursiveEntry struct {
	Path    string    `json:"path"`
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modified"`
	IsDir   bool      `json:"isDir"`
}

// resourceGetRecursiveHandler returns a flat list of every file and directory
// under the requested path, walking the tree recursively on the server side
// so the client only needs a single HTTP call.
var resourceGetRecursiveHandler = withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	rootPath := r.URL.Path
	if rootPath == "" {
		rootPath = "/"
	}

	// Make sure the root itself exists and is a directory.
	info, err := d.user.Fs.Stat(rootPath)
	if err != nil {
		return errToStatus(err), err
	}
	if !info.IsDir() {
		return http.StatusBadRequest, fmt.Errorf("path is not a directory")
	}

	entries := make([]RecursiveEntry, 0)

	err = afero.Walk(d.user.Fs, rootPath, func(fPath string, info os.FileInfo, err error) error {
		// Walking a large tree is expensive, and every entry is stat'ed through
		// the scoped filesystem. Stop as soon as nobody is waiting for the
		// answer instead of finishing the walk for a client that has gone.
		if ctxErr := r.Context().Err(); ctxErr != nil {
			return ctxErr
		}

		if err != nil {
			return nil // skip entries we cannot read
		}

		// Skip the root directory itself.
		if fPath == rootPath {
			return nil
		}

		// Respect user rules.
		if !d.Check(fPath) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		entries = append(entries, RecursiveEntry{
			// afero.Walk joins paths with the OS separator, so on Windows fPath
			// uses backslashes. The web API contract is forward slashes, so
			// normalize it here (mirrors search/search.go).
			Path:    filepath.ToSlash(fPath),
			Name:    info.Name(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
			IsDir:   info.IsDir(),
		})
		return nil
	})
	if err != nil {
		// Once the request is done there is nobody left to answer, whatever the
		// walk reported. Asking for a status here only produces a second header
		// write on a connection that is already gone.
		if r.Context().Err() != nil {
			return 0, err
		}
		return http.StatusInternalServerError, err
	}

	return renderJSON(w, r, entries)
})

type DiskUsageResponse struct {
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
}

var diskUsage = withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	file, err := files.NewFileInfo(&files.FileOptions{
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
	fPath := file.RealPath()
	if !file.IsDir {
		return renderJSON(w, r, &DiskUsageResponse{
			Total: 0,
			Used:  0,
		})
	}

	usage, err := disk.UsageWithContext(r.Context(), fPath)
	if err != nil {
		return errToStatus(err), err
	}
	return renderJSON(w, r, &DiskUsageResponse{
		Total: usage.Total,
		Used:  usage.Used,
	})
})
