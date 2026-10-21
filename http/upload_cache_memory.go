package fbhttp

import (
	"context"
	"fmt"
	"hash/fnv"
	"sync"
	"time"

	"github.com/dawidpiotrowski/filebrowser-ng/v2/files"
	"github.com/jellydator/ttlcache/v3"
)

const uploadCacheTTL = 3 * time.Minute

// UploadCache tracks private staged uploads in one writer process.
type UploadCache interface {
	// Lock serializes mutations of one upload across stat/check/write/complete.
	Lock(filePath string) (unlock func(), err error)

	// Register binds an upload to its creator and private stage. Expiry aborts
	// that stage; it never removes the visible destination.
	Register(filePath string, upload UploadInfo)

	// Complete removes an upload from the cache
	Complete(filePath string)

	// Get returns server-only metadata for an active upload.
	Get(filePath string) (UploadInfo, error)

	// Touch refreshes the TTL for an active upload
	Touch(filePath string)

	// Close cleans up any resources
	Close()
}

// UploadInfo is server-only authorization state. Stage owns pinned handles and
// the original destination version; it is never served as a public file.
type UploadInfo struct {
	Length  int64
	OwnerID uint
	Stage   files.AtomicFile
}

// memoryUploadCache is an upload cache for single replica deployments
type memoryUploadCache struct {
	cache        *ttlcache.Cache[string, UploadInfo]
	stopEviction func()
}

func newMemoryUploadCache() *memoryUploadCache {
	cache := ttlcache.New[string, UploadInfo](ttlcache.WithDisableTouchOnHit[string, UploadInfo]())
	stop := cache.OnEviction(func(_ context.Context, _ ttlcache.EvictionReason, item *ttlcache.Item[string, UploadInfo]) {
		if stage := item.Value().Stage; stage != nil {
			_ = stage.Abort()
		}
	})

	go cache.Start()

	return &memoryUploadCache{cache: cache, stopEviction: stop}
}

func (c *memoryUploadCache) Register(filePath string, upload UploadInfo) {
	c.cache.Set(filePath, upload, uploadCacheTTL)
}

func (c *memoryUploadCache) Complete(filePath string) {
	c.cache.Delete(filePath)
}

func (c *memoryUploadCache) Get(filePath string) (UploadInfo, error) {
	item := c.cache.Get(filePath)
	if item == nil {
		return UploadInfo{}, fmt.Errorf("no active upload found for the given path")
	}
	return item.Value(), nil
}

func (c *memoryUploadCache) Touch(filePath string) {
	c.cache.Touch(filePath)
}

func (c *memoryUploadCache) Close() {
	c.cache.Stop()
	c.cache.DeleteAll()
	c.stopEviction()
}

// uploadLocks uses bounded striped locks; colliding paths can safely retry.
type uploadLocks struct{ stripes [256]sync.Mutex }

var cacheUploadLocks uploadLocks

func (l *uploadLocks) lock(path string) (func(), error) {
	h := fnv.New32a()
	_, _ = h.Write([]byte(path))
	m := &l.stripes[h.Sum32()%uint32(len(l.stripes))]
	if !m.TryLock() {
		return nil, fmt.Errorf("upload is busy")
	}
	return m.Unlock, nil
}

func (c *memoryUploadCache) Lock(path string) (func(), error) {
	return cacheUploadLocks.lock(path)
}

// NewUploadCache creates a cache for a single writer process. Redis-backed
// multi-writer uploads are rejected until a fencing protocol can protect writes.
func NewUploadCache(redisURL string) (UploadCache, error) {
	if redisURL != "" {
		return nil, fmt.Errorf("Redis uploads were disabled in filebrowser-ng: concurrent writers require filesystem fencing; run one writer process without --redis")
	}
	return newMemoryUploadCache(), nil
}
