//go:build linux || darwin

package files

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func etagIdentity(info os.FileInfo) string {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("%x-%x", stat.Dev, stat.Ino)
	}
	return ""
}

func preserveMetadata(src, dst *os.File, info os.FileInfo) error {
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.ErrUnsupported
	}
	current, err := dst.Stat()
	if err != nil {
		return err
	}
	st, ok := current.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.ErrUnsupported
	}
	if owner.Uid != st.Uid || owner.Gid != st.Gid {
		if err = dst.Chown(int(owner.Uid), int(owner.Gid)); err != nil {
			return err
		}
	}
	// Metadata is copied before content. Kernel write semantics can then clear
	// capability/set-id metadata, just as a normal content rewrite would.
	const maxMetadata = 1 << 20
	names := make([]byte, maxMetadata)
	n, err := unix.Flistxattr(int(src.Fd()), names)
	if errors.Is(err, unix.ENOTSUP) {
		return nil
	}
	if err != nil {
		return err
	}
	oldACL := false
	total := 0
	for _, name := range strings.Split(string(names[:n]), "\x00") {
		if name == "" {
			continue
		}
		if name == "system.posix_acl_access" {
			oldACL = true
		}
		size, err := unix.Fgetxattr(int(src.Fd()), name, nil)
		if err != nil {
			return err
		}
		if size < 0 || size > maxMetadata-total {
			return errors.ErrUnsupported
		}
		value := make([]byte, size)
		read, err := unix.Fgetxattr(int(src.Fd()), name, value)
		if err != nil {
			return err
		}
		if err = unix.Fsetxattr(int(dst.Fd()), name, value[:read], 0); err != nil {
			return err
		}
		total += read
	}
	if err = preserveACL(src, dst, oldACL); err != nil {
		return err
	}
	return dst.Chmod(info.Mode().Perm())
}
