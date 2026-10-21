package fbhttp

import (
	"fmt"
	"hash/fnv"
	"sync"
	"time"

	"github.com/jellydator/ttlcache/v3"
)

const uploadCacheTTL = 3 * time.Minute

// UploadCache is an interface for tracking active uploads.
// Allows for different backends (e.g. in-memory or redis)
// to support both single instance and multi replica deployments.
type UploadCache interface {
	// Lock serializes mutations of one upload across stat/check/write/complete.
	Lock(filePath string) (unlock func(), err error)

	// Register stores an upload with its expected file size. The legacy removal
	// callback is ignored: expiry must not delete a subsequently replaced file.
	Register(filePath string, fileSize int64, remove func() error)

	// Complete removes an upload from the cache
	Complete(filePath string)

	// GetLength returns the expected file size for an active upload
	GetLength(filePath string) (int64, error)

	// Touch refreshes the TTL for an active upload
	Touch(filePath string)

	// Close cleans up any resources
	Close()
}

// memoryUploadEntry is the value stored for each active upload.
type memoryUploadEntry struct {
	size   int64
	remove func() error
}

// memoryUploadCache is an upload cache for single replica deployments
type memoryUploadCache struct {
	cache *ttlcache.Cache[string, memoryUploadEntry]
}

func newMemoryUploadCache() *memoryUploadCache {
	cache := ttlcache.New[string, memoryUploadEntry]()

	go cache.Start()

	return &memoryUploadCache{cache: cache}
}

func (c *memoryUploadCache) Register(filePath string, fileSize int64, _ func() error) {
	c.cache.Set(filePath, memoryUploadEntry{size: fileSize}, uploadCacheTTL)
}

func (c *memoryUploadCache) Complete(filePath string) {
	c.cache.Delete(filePath)
}

func (c *memoryUploadCache) GetLength(filePath string) (int64, error) {
	item := c.cache.Get(filePath)
	if item == nil {
		return 0, fmt.Errorf("no active upload found for the given path")
	}
	return item.Value().size, nil
}

func (c *memoryUploadCache) Touch(filePath string) {
	c.cache.Touch(filePath)
}

func (c *memoryUploadCache) Close() {
	c.cache.Stop()
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
