package fileutils

import (
	"errors"
	"io/fs"
	"os"

	"github.com/spf13/afero"
)

// CopyDir copies a directory from source to dest and all
// of its sub-directories. It doesn't stop if it finds an error
// during the copy. Returns an error if any.
func CopyDir(afs afero.Fs, source, dest string, fileMode, dirMode fs.FileMode) error {
	return copyDir(afs, source, dest, fileMode, dirMode, os.O_WRONLY|os.O_CREATE)
}

func copyDir(afs afero.Fs, source, dest string, fileMode, dirMode fs.FileMode, flags int) error {
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
			err = copyDir(afs, fsource, fdest, fileMode, dirMode, flags)
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
