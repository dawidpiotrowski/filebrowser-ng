package files

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
)

func preserveACL(_ *os.File, dst *os.File, hasACL bool) error {
	if hasACL {
		return nil
	}
	err := unix.Fremovexattr(int(dst.Fd()), "system.posix_acl_access")
	if errors.Is(err, unix.ENODATA) || errors.Is(err, unix.ENOTSUP) {
		return nil
	}
	return err
}
