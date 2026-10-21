package files

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestAtomicReplacementPreservesLinuxPOSIXACL(t *testing.T) {
	afs, root := testAtomicFS(t)
	name := filepath.Join(root, "target")
	if err := os.WriteFile(name, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	acl := make([]byte, 4+5*8)
	binary.LittleEndian.PutUint32(acl, 2)
	for i, e := range []struct {
		tag, perm uint16
		id        uint32
	}{{1, 6, 0xffffffff}, {2, 4, 65001}, {4, 0, 0xffffffff}, {16, 4, 0xffffffff}, {32, 0, 0xffffffff}} {
		offset := 4 + i*8
		binary.LittleEndian.PutUint16(acl[offset:], e.tag)
		binary.LittleEndian.PutUint16(acl[offset+2:], e.perm)
		binary.LittleEndian.PutUint32(acl[offset+4:], e.id)
	}
	if err := unix.Setxattr(name, "system.posix_acl_access", acl, 0); err != nil {
		if errors.Is(err, unix.ENOTSUP) {
			t.Skip(err)
		}
		t.Fatal(err)
	}
	tx, err := afs.BeginAtomicWrite("/target", WriteOptions{Replace: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Abort()
	tx.WriteString("new")
	if _, err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 100)
	n, err := unix.Getxattr(name, "system.posix_acl_access", buf)
	if err != nil || !bytes.Equal(acl, buf[:n]) {
		t.Fatalf("ACL changed: %x %v", buf[:n], err)
	}
}
