package fbhttp

import (
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
	c.cache.Set(path, memoryUploadEntry{size: 1, remove: func() error { return os.Remove(path) }}, time.Nanosecond)
	deadline := time.Now().Add(time.Second)
	for {
		c.cache.DeleteExpired()
		if _, err := c.GetLength(path); err != nil {
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
