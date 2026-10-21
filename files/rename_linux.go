package files

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func renameAt(src *os.File, from string, dst *os.File, to string, replace bool) error {
	flags := uint(unix.RENAME_NOREPLACE)
	if replace {
		flags = 0
	}
	err := unix.Renameat2(int(src.Fd()), from, int(dst.Fd()), to, flags)
	if !replace && (errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EOPNOTSUPP)) {
		return errors.Join(errors.ErrUnsupported, err)
	}
	return err
}

func syncDirectory(dir *os.File) error { return dir.Sync() }
