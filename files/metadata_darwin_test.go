package files

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestAtomicReplacementPreservesMacOSACL(t *testing.T) {
	afs, root := testAtomicFS(t)
	name := filepath.Join(root, "target")
	if err := os.WriteFile(name, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("/bin/chmod", "+a", "everyone allow read", name).CombinedOutput(); err != nil {
		t.Fatalf("set fixture ACL: %v %s", err, out)
	}
	before, err := exec.Command("/bin/ls", "-lde", name).Output()
	if err != nil {
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
	after, err := exec.Command("/bin/ls", "-lde", name).Output()
	if err != nil {
		t.Fatal(err)
	}
	// The first line includes size/timestamps; ACL entries follow it.
	acl := func(b []byte) string {
		for i, c := range b {
			if c == '\n' {
				return string(b[i+1:])
			}
		}
		return ""
	}
	if acl(before) == "" || acl(before) != acl(after) {
		t.Fatalf("ACL changed:\n%s\n%s", before, after)
	}
}
