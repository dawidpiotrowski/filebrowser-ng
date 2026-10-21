package fileutils

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	fberrors "github.com/dawidpiotrowski/filebrowser-ng/v2/errors"
	"github.com/spf13/afero"
)

// MoveFile moves file from src to dst.
// By default the rename filesystem system call is used. If src and dst point to different volumes
// the file copy is used as a fallback
func MoveFile(afs afero.Fs, src, dst string, fileMode, dirMode fs.FileMode) error {
	if err := afs.Rename(src, dst); err == nil {
		return nil
	} else if !isCrossDevice(err) {
		// Copy/delete must not bypass a failed rename's permission, sharing,
		// read-only-filesystem or other errors.
		return err
	}
	// fallback
	err := Copy(afs, src, dst, fileMode, dirMode)
	if err != nil {
		// The destination can predate this move. Never delete it on failure;
		// leave partial copies for recovery and retain the complete source.
		return err
	}
	if err := afs.RemoveAll(src); err != nil {
		return err
	}
	return nil
}

// CopyFile copies a file from source to dest and returns
// an error if any.
func CopyFile(afs afero.Fs, source, dest string, fileMode, dirMode fs.FileMode) error {
	return copyFile(afs, source, dest, fileMode, dirMode, os.O_WRONLY|os.O_CREATE)
}

func copyFile(afs afero.Fs, source, dest string, fileMode, dirMode fs.FileMode, flags int) (retErr error) {
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

	// Create the destination file.
	// Open without O_TRUNC so identity checks apply to the actual destination
	// handle. Checking names or stat'ing before open misses hardlinks, symlinks
	// and a replacement inserted between the check and open.
	dst, err := afs.OpenFile(dest, flags, fileMode)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, dst.Close()) }()
	destInfo, err := dst.Stat()
	if err != nil {
		return err
	}
	if os.SameFile(info, destInfo) || !destInfo.Mode().IsRegular() {
		return fberrors.ErrInvalidRequestParams
	}
	if flags&os.O_EXCL == 0 {
		if err := dst.Truncate(0); err != nil {
			return err
		}
	}

	// Copy the contents of the file.
	_, err = io.Copy(dst, src)
	if err != nil {
		return err
	}

	// OS-backed ScopedFs returns an *os.File: set its mode through the same
	// handle, rather than a pathname which another writer might replace.
	if file, ok := dst.(interface{ Chmod(os.FileMode) error }); ok {
		err = file.Chmod(info.Mode())
	} else {
		err = afs.Chmod(dest, info.Mode())
	}
	if err != nil {
		return err
	}

	// A cross-device move may only delete its source after flush AND close
	// succeed. The deferred close contributes its error to the return value.
	return dst.Sync()
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
