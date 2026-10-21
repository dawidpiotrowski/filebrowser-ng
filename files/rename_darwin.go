package files

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func renameAt(src *os.File, from string, dst *os.File, to string, replace bool) error {
	flags := uint32(unix.RENAME_EXCL)
	if replace {
		flags = 0
	}
	err := unix.RenameatxNp(int(src.Fd()), from, int(dst.Fd()), to, flags)
	if !replace && (errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EINVAL)) {
		return errors.Join(errors.ErrUnsupported, err)
	}
	return err
}

func syncDirectory(dir *os.File) error { return dir.Sync() }
