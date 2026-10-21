package diskcache

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/afero"
)

type pausedCacheFs struct {
	afero.Fs
	armed            atomic.Bool
	started, release chan struct{}
}

type pausedCacheFile struct {
	afero.File
	fs *pausedCacheFs
}

func (f *pausedCacheFs) OpenFile(name string, flags int, mode os.FileMode) (afero.File, error) {
	file, err := f.Fs.OpenFile(name, flags, mode)
	if err == nil && flags&os.O_TRUNC != 0 && f.armed.Swap(false) {
		return &pausedCacheFile{File: file, fs: f}, nil
	}
	return file, err
}

func (f *pausedCacheFile) Write(b []byte) (int, error) {
	close(f.fs.started)
	<-f.fs.release
	return f.File.Write(b)
}

func TestCacheReadersNeverObserveAnInProgressWrite(t *testing.T) {
	fs := &pausedCacheFs{Fs: afero.NewMemMapFs(), started: make(chan struct{}), release: make(chan struct{})}
	cache := New(fs, "/cache")
	if err := cache.Store(context.Background(), "photo", []byte("old-image")); err != nil {
		t.Fatal(err)
	}
	fs.armed.Store(true)
	written := make(chan error, 1)
	go func() { written <- cache.Store(context.Background(), "photo", []byte("complete-new-image")) }()
	<-fs.started
	t.Cleanup(func() { close(fs.release) })
	type result struct {
		data []byte
		ok   bool
		err  error
	}
	read := make(chan result, 1)
	go func() {
		b, ok, err := cache.Load(context.Background(), "photo")
		read <- result{b, ok, err}
	}()
	select {
	case r := <-read:
		t.Fatalf("reader observed truncated cache during write: %q %v", r.data, r.err)
	case <-time.After(50 * time.Millisecond):
	}
	// Release the writer exactly once, including on a failed assertion above.
	fs.release <- struct{}{}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	r := <-read
	if r.err != nil || !r.ok || string(r.data) != "complete-new-image" {
		t.Fatalf("cache read after write: %q %v", r.data, r.err)
	}
}
