//go:build linux || darwin

package files

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestAtomicReplacementPreservesOwnershipAndExtendedAttributes(t *testing.T) {
	afs, root := testAtomicFS(t)
	name := filepath.Join(root, "target")
	if err := os.WriteFile(name, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	const attribute = "user.filebrowser-ng-test"
	if err := unix.Setxattr(name, attribute, []byte("metadata"), 0); err != nil {
		if errors.Is(err, unix.ENOTSUP) {
			t.Skip(err)
		}
		t.Fatal(err)
	}
	before, _ := os.Stat(name)
	tx, err := afs.BeginAtomicWrite("/target", WriteOptions{Replace: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Abort()
	tx.WriteString("new")
	if _, err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(name)
	a, b := before.Sys().(*syscall.Stat_t), after.Sys().(*syscall.Stat_t)
	if a.Uid != b.Uid || a.Gid != b.Gid || before.Mode() != after.Mode() {
		t.Fatal("ownership or permissions changed", a, b)
	}
	buf := make([]byte, 100)
	n, err := unix.Getxattr(name, attribute, buf)
	if err != nil || string(buf[:n]) != "metadata" {
		t.Fatal("metadata lost", err)
	}
}

func TestAtomicReplacementDoesNotBypassReadOnlyFileMode(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses ordinary file mode checks")
	}
	afs, root := testAtomicFS(t)
	name := filepath.Join(root, "target")
	if err := os.WriteFile(name, []byte("keep"), 0o400); err != nil {
		t.Fatal(err)
	}
	if tx, err := afs.BeginAtomicWrite("/target", WriteOptions{Replace: true}); !errors.Is(err, os.ErrPermission) {
		if tx != nil {
			tx.Abort()
		}
		t.Fatal("read-only target writable", err)
	}
	assertBytes(t, name, "keep")
}
