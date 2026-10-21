package files

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"
)

func testAtomicFS(t *testing.T) (*ScopedFs, string) {
	t.Helper()
	root := t.TempDir()
	return NewScopedFs(afero.NewOsFs(), root), root
}
func assertBytes(t *testing.T, name, want string) {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil || string(b) != want {
		t.Fatalf("%s: %q %v", name, b, err)
	}
}

func TestAtomicWritePublishesOnlyCompleteContent(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "create", true: "replace"}[replace], func(t *testing.T) {
			afs, root := testAtomicFS(t)
			name := filepath.Join(root, "target")
			if replace {
				if err := os.WriteFile(name, []byte("old"), 0o640); err != nil {
					t.Fatal(err)
				}
			}
			tx, err := afs.BeginAtomicWrite("/target", WriteOptions{Create: true, Replace: replace, Mode: 0o600})
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Abort()
			if _, err = tx.WriteString("complete"); err != nil {
				t.Fatal(err)
			}
			if replace {
				assertBytes(t, name, "old")
			} else if _, err = os.Stat(name); !os.IsNotExist(err) {
				t.Fatal("partial create visible", err)
			}
			if _, err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			assertBytes(t, name, "complete")
			if replace {
				info, _ := os.Stat(name)
				if info.Mode().Perm() != tx.(*stagedFile).original.Mode().Perm() {
					t.Fatal("mode changed", info.Mode())
				}
			}
			entries, _ := os.ReadDir(root)
			if len(entries) != 1 {
				t.Fatal("staging was not cleaned", entries)
			}
		})
	}
}

type failAtomicFile struct {
	*os.File
	failSync, failClose bool
}

func (f *failAtomicFile) Sync() error {
	if f.failSync {
		return fs.ErrInvalid
	}
	return f.File.Sync()
}
func (f *failAtomicFile) Close() error {
	err := f.File.Close()
	if f.failClose {
		return errors.Join(err, fs.ErrInvalid)
	}
	return err
}

func TestAtomicWriteKeepsOldFileOnSyncOrCloseFailure(t *testing.T) {
	for _, where := range []string{"sync", "close"} {
		t.Run(where, func(t *testing.T) {
			afs, root := testAtomicFS(t)
			name := filepath.Join(root, "target")
			if err := os.WriteFile(name, []byte("valuable-old"), 0o600); err != nil {
				t.Fatal(err)
			}
			stage, err := afs.BeginAtomicWrite("/target", WriteOptions{Replace: true, Mode: 0o600})
			if err != nil {
				t.Fatal(err)
			}
			tx := stage.(*stagedFile)
			tx.File = &failAtomicFile{File: tx.File.(*os.File), failSync: where == "sync", failClose: where == "close"}
			if _, err = tx.WriteString("new"); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Commit(); !errors.Is(err, fs.ErrInvalid) {
				t.Fatal("storage error ignored", err)
			}
			if err = tx.Abort(); err != nil && where != "sync" {
				t.Fatal(err)
			}
			assertBytes(t, name, "valuable-old")
			entries, _ := os.ReadDir(root)
			if len(entries) != 1 {
				t.Fatal("abandoned stage remains", entries)
			}
		})
	}
}

func TestAtomicWritesRejectAChangedTargetOrParent(t *testing.T) {
	for _, change := range []string{"competing-commit", "new-file", "parent-rename"} {
		t.Run(change, func(t *testing.T) {
			afs, root := testAtomicFS(t)
			if err := afs.Mkdir("/dir", 0o700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, "dir", "target")
			replace := change != "new-file"
			if replace {
				if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			first, err := afs.BeginAtomicWrite("/dir/target", WriteOptions{Create: true, Replace: replace, Mode: 0o600})
			if err != nil {
				t.Fatal(err)
			}
			defer first.Abort()
			first.WriteString("stale")
			switch change {
			case "competing-commit":
				second, err := afs.BeginAtomicWrite("/dir/target", WriteOptions{Replace: true})
				if err != nil {
					t.Fatal(err)
				}
				defer second.Abort()
				second.WriteString("winner")
				if _, err = second.Commit(); err != nil {
					t.Fatal(err)
				}
			case "new-file":
				if err := os.WriteFile(target, []byte("winner"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "parent-rename":
				if err := afs.RenameNoReplace("/dir", "/moved"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = first.Commit(); !errors.Is(err, ErrFileChanged) {
				t.Fatal("changed target accepted", err)
			}
			if change == "parent-rename" {
				assertBytes(t, filepath.Join(root, "moved", "target"), "old")
			} else {
				assertBytes(t, target, "winner")
			}
		})
	}
}

func TestNoReplaceRenameHasOneWinnerAndKeepsFileIdentity(t *testing.T) {
	afs, root := testAtomicFS(t)
	for _, name := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	before := make(map[string]os.FileInfo)
	for _, name := range []string{"a", "b"} {
		before[name], _ = afs.Stat("/" + name)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, name := range []string{"a", "b"} {
		wg.Add(1)
		go func(name string) { defer wg.Done(); <-start; results <- afs.RenameNoReplace("/"+name, "/winner") }(name)
	}
	close(start)
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, os.ErrExist) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("winners %d conflicts %d", success, conflicts)
	}
	data, err := os.ReadFile(filepath.Join(root, "winner"))
	if err != nil {
		t.Fatal(err)
	}
	after, _ := afs.Stat("/winner")
	if !os.SameFile(before[string(data)], after) {
		t.Fatal("rename copied rather than moved the file")
	}
}

func TestStagingIsInvisibleEvenThroughAliasesAndSharedRoots(t *testing.T) {
	for _, external := range []bool{false, true} {
		t.Run(map[bool]string{false: "confined", true: "external-enabled"}[external], func(t *testing.T) {
			_, root := testAtomicFS(t)
			afs := NewFs(afero.NewOsFs(), root, external).(*ScopedFs)
			tx, err := afs.BeginAtomicWrite("/target", WriteOptions{Create: true, Mode: 0o600})
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Abort()
			tx.WriteString("private-bytes")
			real, _ := os.ReadDir(root)
			if len(real) != 1 || !strings.HasPrefix(real[0].Name(), stagingPrefix) {
				t.Fatal(real)
			}
			stage := real[0].Name()
			if entries, err := afero.ReadDir(afs, "/"); err != nil || len(entries) != 0 {
				t.Fatal("stage listed", entries, err)
			}
			dir, err := afs.Open("/")
			if err != nil {
				t.Fatal(err)
			}
			names, err := dir.Readdirnames(-1)
			dir.Close()
			if err != nil || len(names) != 0 {
				t.Fatal("stage name leaked", names, err)
			}
			for _, name := range []string{"/" + stage, "/" + stage + "/data"} {
				if f, err := afs.Open(name); !errors.Is(err, os.ErrPermission) {
					if f != nil {
						f.Close()
					}
					t.Fatal("stage readable", name, err)
				}
			}
			if err := os.Symlink(filepath.Join(root, stage), filepath.Join(root, "alias")); err != nil {
				t.Skip(err)
			}
			if f, err := afs.Open("/alias/data"); !errors.Is(err, os.ErrPermission) {
				if f != nil {
					f.Close()
				}
				t.Fatal("alias readable", err)
			}
			shared := NewFs(afs, "/"+stage, external)
			if f, err := shared.Open("/data"); !errors.Is(err, os.ErrPermission) {
				if f != nil {
					f.Close()
				}
				t.Fatal("rebased stage readable", err)
			}
		})
	}
}

func TestUnsupportedAndReadOnlyAdaptersNeverGainWriteAccess(t *testing.T) {
	afs, root := testAtomicFS(t)
	if err := os.WriteFile(filepath.Join(root, "target"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = afs
	opaque := afero.NewBasePathFs(afero.NewReadOnlyFs(afero.NewOsFs()), root)
	scoped := NewScopedFs(opaque, "/")
	if _, err := scoped.BeginAtomicWrite("/target", WriteOptions{Replace: true}); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatal(err)
	}
	if err := scoped.RenameNoReplace("/target", "/other"); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatal(err)
	}
	if f, err := scoped.OpenFile("/target", os.O_WRONLY|os.O_TRUNC, 0o600); err == nil {
		f.Close()
		t.Fatal("read-only adapter bypassed")
	}
	assertBytes(t, filepath.Join(root, "target"), "keep")
}

func TestAbortingAStageDoesNotDeleteAReplacementPayload(t *testing.T) {
	afs, _ := testAtomicFS(t)
	stage, err := afs.BeginAtomicWrite("/target", WriteOptions{Create: true, Mode: 0o600})
	if err != nil {
		t.Fatal(err)
	}
	tx := stage.(*stagedFile)
	if err = tx.stage.Rename("data", "original-data"); err != nil {
		t.Fatal(err)
	}
	if err = tx.stage.WriteFile("data", []byte("unrelated replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(tx.stage.Name(), "data")
	if _, err = tx.Commit(); !errors.Is(err, ErrFileChanged) {
		t.Fatal("replacement payload published", err)
	}
	if err = tx.Abort(); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, replacement, "unrelated replacement")
}

func TestFileETagDistinguishesReplacementsWithIdenticalTimeAndSize(t *testing.T) {
	afs, root := testAtomicFS(t)
	name := filepath.Join(root, "target")
	stamp := time.Unix(1700000000, 0)
	if err := os.WriteFile(name, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(name, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	before, _ := afs.Stat("/target")
	stage, err := afs.BeginAtomicWrite("/target", WriteOptions{Replace: true})
	if err != nil {
		t.Fatal(err)
	}
	defer stage.Abort()
	stage.WriteString("new")
	if _, err = stage.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = os.Chtimes(name, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	after, _ := afs.Stat("/target")
	if FileETag(before) == FileETag(after) {
		t.Fatal("replacement reused an earlier version tag")
	}
}

type replaceBeforeContentRead struct {
	afero.Fs
	replaced bool
}

func (f *replaceBeforeContentRead) Open(name string) (afero.File, error) {
	if name == "/target.txt" && !f.replaced {
		f.replaced = true
		tx, err := BeginAtomicWrite(f.Fs, name, WriteOptions{Replace: true})
		if err != nil {
			return nil, err
		}
		defer tx.Abort()
		if _, err = tx.WriteString("new"); err != nil {
			return nil, err
		}
		if _, err = tx.Commit(); err != nil {
			return nil, err
		}
	}
	return f.Fs.Open(name)
}

func TestEditorTagDescribesTheHandleThatSuppliesItsContents(t *testing.T) {
	afs, root := testAtomicFS(t)
	if err := os.WriteFile(filepath.Join(root, "target.txt"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := NewFileInfo(&FileOptions{Fs: &replaceBeforeContentRead{Fs: afs}, Path: "/target.txt", Modify: true, Expand: true, Content: true, Checker: allowAllChecker{}})
	if err != nil {
		t.Fatal(err)
	}
	current, _ := afs.Stat("/target.txt")
	if info.Content != "new" || info.ETag() != FileETag(current) {
		t.Fatalf("content/version mismatch: %q %q %q", info.Content, info.ETag(), FileETag(current))
	}
}
