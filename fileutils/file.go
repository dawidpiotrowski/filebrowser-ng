package fileutils

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	fberrors "github.com/dawidpiotrowski/filebrowser-ng/v2/errors"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/files"
	"github.com/spf13/afero"
)

// MoveFile moves file from src to dst.
// By default the rename filesystem system call is used. If src and dst point to different volumes
// the file copy is used as a fallback
func MoveFile(afs afero.Fs, src, dst string, fileMode, dirMode fs.FileMode) error {
	return MoveFileWithPolicy(afs, src, dst, fileMode, dirMode, true)
}

func MoveFileWithPolicy(afs afero.Fs, src, dst string, fileMode, dirMode fs.FileMode, overwrite bool) error {
	var err error
	if overwrite {
		err = afs.Rename(src, dst)
	} else {
		err = files.RenameNoReplace(afs, src, dst)
	}
	if err == nil {
		return nil
	} else if !isCrossDevice(err) {
		// Copy/delete must not bypass a failed rename's permission, sharing,
		// read-only-filesystem or other errors.
		return err
	}
	info, err := afs.Stat(src)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.ErrUnsupported
	}
	// fallback
	if overwrite {
		err = Copy(afs, src, dst, fileMode, dirMode)
	} else {
		err = CopyExclusive(afs, src, dst, fileMode, dirMode)
	}
	if err != nil {
		// The destination can predate this move. An unsuccessful stage never
		// replaces it; keep the complete source for recovery.
		return err
	}
	return files.RemoveIfUnchanged(afs, src, info)
}

// CopyFile copies a file from source to dest and returns
// an error if any.
func CopyFile(afs afero.Fs, source, dest string, fileMode, dirMode fs.FileMode) error {
	return copyFile(afs, source, dest, fileMode, dirMode, os.O_WRONLY|os.O_CREATE)
}

func copyFile(afs afero.Fs, source, dest string, fileMode, dirMode fs.FileMode, flags int) error {
	if filepath.Clean(source) == filepath.Clean(dest) {
		return fberrors.ErrInvalidRequestParams
	}
	// Open the source file.
	src, err := afs.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fberrors.ErrInvalidRequestParams
	}

	// Makes the directory needed to create the dst
	// file.
	err = afs.MkdirAll(filepath.Dir(dest), dirMode)
	if err != nil {
		return err
	}

	if destInfo, err := afs.Stat(dest); err == nil && os.SameFile(info, destInfo) {
		return fberrors.ErrInvalidRequestParams
	}
	dst, err := files.BeginAtomicWrite(afs, dest, files.WriteOptions{Mode: info.Mode(), Create: true, Replace: flags&os.O_EXCL == 0, Check: func(_ os.FileInfo) error {
		current, err := afs.Stat(source)
		if err != nil || !os.SameFile(info, current) || info.Size() != current.Size() || !info.ModTime().Equal(current.ModTime()) {
			return files.ErrFileChanged
		}
		return nil
	}})
	if err != nil {
		return err
	}
	defer dst.Abort()

	// Copy the contents of the file.
	_, err = io.Copy(dst, src)
	if err != nil {
		return err
	}
	current, err := src.Stat()
	if err != nil || current.Size() != info.Size() || !current.ModTime().Equal(info.ModTime()) {
		return files.ErrFileChanged
	}

	_, err = dst.Commit()
	return err
}

// CommonPrefix returns common directory path of provided files
func CommonPrefix(sep byte, paths ...string) string {
	// Handle special cases.
	switch len(paths) {
	case 0:
		return ""
	case 1:
		return path.Clean(paths[0])
	}

	// Note, we treat string as []byte, not []rune as is often
	// done in Go. (And sep as byte, not rune). This is because
	// most/all supported OS' treat paths as string of non-zero
	// bytes. A filename may be displayed as a sequence of Unicode
	// runes (typically encoded as UTF-8) but paths are
	// not required to be valid UTF-8 or in any normalized form
	// (e.g. "é" (U+00C9) and "é" (U+0065,U+0301) are different
	// file names.
	c := []byte(path.Clean(paths[0]))

	// We add a trailing sep to handle the case where the
	// common prefix directory is included in the path list
	// (e.g. /home/user1, /home/user1/foo, /home/user1/bar).
	// path.Clean will have cleaned off trailing / separators with
	// the exception of the root directory, "/" (in which case we
	// make it "//", but this will get fixed up to "/" below).
	c = append(c, sep)

	// Ignore the first path since it's already in c
	for _, v := range paths[1:] {
		// Clean up each path before testing it
		v = path.Clean(v) + string(sep)

		// Find the first non-common byte and truncate c
		if len(v) < len(c) {
			c = c[:len(v)]
		}
		for i := 0; i < len(c); i++ {
			if v[i] != c[i] {
				c = c[:i]
				break
			}
		}
	}

	// Remove trailing non-separator characters and the final separator
	for i := len(c) - 1; i >= 0; i-- {
		if c[i] == sep {
			c = c[:i]
			break
		}
	}

	return string(c)
}
