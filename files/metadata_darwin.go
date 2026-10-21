package files

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// macOS ACLs are not ordinary listxattr entries. Copy the bounded extended
// security attribute using file descriptors (never an unchecked pathname).
func preserveACL(src, dst *os.File, _ bool) error {
	attrs := unix.Attrlist{Bitmapcount: 5, Commonattr: unix.ATTR_CMN_EXTENDED_SECURITY}
	buf := make([]byte, 64<<10)
	_, _, errno := unix.Syscall6(unix.SYS_FGETATTRLIST, src.Fd(), uintptr(unsafe.Pointer(&attrs)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 0)
	runtime.KeepAlive(src)
	if errno != 0 {
		return fmt.Errorf("read file ACL: %w", errno)
	}
	size := int(binary.NativeEndian.Uint32(buf[:4]))
	if size < 12 || size > len(buf) {
		return errors.ErrUnsupported
	}
	// Removing the leading length preserves the relative attrreference offset.
	data := buf[4:size]
	if binary.NativeEndian.Uint32(data[4:8]) == 0 {
		// getattrlist represents no ACL with an empty reference; setattrlist
		// requires a complete kauth_filesec carrying KAUTH_FILESEC_NOACL.
		data = make([]byte, 8+44)
		binary.NativeEndian.PutUint32(data[0:4], 8)
		binary.NativeEndian.PutUint32(data[4:8], 44)
		binary.NativeEndian.PutUint32(data[8:12], 0x012cc16d)
		binary.NativeEndian.PutUint32(data[44:48], 0xffffffff)
	}
	_, _, errno = unix.Syscall6(unix.SYS_FSETATTRLIST, dst.Fd(), uintptr(unsafe.Pointer(&attrs)), uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), 0, 0)
	runtime.KeepAlive(dst)
	if errno != 0 {
		return fmt.Errorf("preserve file ACL (size %d): %w", size, errno)
	}
	return nil
}
