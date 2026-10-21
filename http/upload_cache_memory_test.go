package fbhttp

import (
	"github.com/dawidpiotrowski/filebrowser-ng/v2/files"
	"github.com/spf13/afero"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUploadExpiryNeverDeletesAReplacedFile(t *testing.T) {
	c := newMemoryUploadCache()
	t.Cleanup(c.Close)
	path := filepath.Join(t.TempDir(), "upload.txt")
	if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.cache.Set(path, UploadInfo{Length: 1}, time.Nanosecond)
	deadline := time.Now().Add(time.Second)
	for {
		c.cache.DeleteExpired()
		if _, err := c.Get(path); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("expired upload still active")
		}
		time.Sleep(10 * time.Millisecond) // Windows wall-clock resolution can exceed 1 ns.
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "replacement" {
		t.Fatalf("replacement removed: %q %v", content, err)
	}
}

func TestUploadExpiryDiscardsOnlyThePrivateStage(t *testing.T) {
	root := t.TempDir()
	name := filepath.Join(root, "target")
	if err := os.WriteFile(name, []byte("visible old file"), 0o600); err != nil {
		t.Fatal(err)
	}
	stage, err := files.NewScopedFs(afero.NewOsFs(), root).BeginAtomicWrite("/target", files.WriteOptions{Replace: true})
	if err != nil {
		t.Fatal(err)
	}
	stage.WriteString("abandoned partial")
	c := newMemoryUploadCache()
	c.cache.Set(name, UploadInfo{Length: 100, Stage: stage}, time.Nanosecond)
	deadline := time.Now().Add(time.Second)
	for {
		c.cache.DeleteExpired()
		if _, err = c.Get(name); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("upload did not expire")
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.Close() // Waits for eviction callbacks before checking cleanup.
	if b, _ := os.ReadFile(name); string(b) != "visible old file" {
		t.Fatal("expiry damaged destination", string(b))
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 {
		t.Fatal("expired stage remains", entries)
	}
}
