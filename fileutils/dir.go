package fileutils

import (
	"errors"
	fberrors "github.com/dawidpiotrowski/filebrowser-ng/v2/errors"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/files"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
)

// CopyDir copies a directory from source to dest and all
// of its sub-directories. It doesn't stop if it finds an error
// during the copy. Returns an error if any.
func CopyDir(afs afero.Fs, source, dest string, fileMode, dirMode fs.FileMode) error {
	if err := checkCopyDirectory(afs, source, dest); err != nil {
		return err
	}
	return copyDir(afs, source, dest, fileMode, dirMode, os.O_WRONLY|os.O_CREATE, 0)
}

func copyDir(afs afero.Fs, source, dest string, fileMode, dirMode fs.FileMode, flags, depth int) error {
	if depth > 128 {
		return fberrors.ErrInvalidRequestParams
	}
	// Get properties of source.
	srcinfo, err := afs.Stat(source)
	if err != nil {
		return err
	}

	// Create the destination directory.
	err = afs.MkdirAll(dest, srcinfo.Mode())
	if err != nil {
		return err
	}

	dir, err := afs.Open(source)
	if err != nil {
		return err
	}
	defer dir.Close()

	obs, err := dir.Readdir(-1)
	if err != nil {
		return err
	}

	var errs []error

	for _, obj := range obs {
		fsource := source + "/" + obj.Name()
		fdest := dest + "/" + obj.Name()

		if obj.IsDir() {
			// Create sub-directories, recursively.
			err = copyDir(afs, fsource, fdest, fileMode, dirMode, flags, depth+1)
			if err != nil {
				errs = append(errs, err)
			}
		} else {
			// Perform the file copy.
			err = copyFile(afs, fsource, fdest, fileMode, dirMode, flags)
			if err != nil {
				errs = append(errs, err)
			}
		}
	}

	return errors.Join(errs...)
}

func checkCopyDirectory(afs afero.Fs, src, dst string) error {
	from, err := files.ResolvedVirtualPath(afs, src)
	if err != nil {
		return err
	}
	to, err := files.ResolvedVirtualPath(afs, dst)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(filepath.FromSlash(from), filepath.FromSlash(to))
	if err != nil {
		return err
	}
	if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)) {
		return fberrors.ErrSourceIsParent
	}
	return nil
}
