package fbhttp

import (
	"fmt"
	"hash/fnv"
	"os"
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

	// Register binds an upload to its creator and the identity obtained from the
	// opened file handle. Expiry never removes filesystem data.
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

// UploadInfo is server-only authorization state. Identity comes from the opened
// file's Stat, not a pathname that another writer might replace.
type UploadInfo struct {
	Length   int64
	OwnerID  uint
	Identity os.FileInfo
}

// memoryUploadCache is an upload cache for single replica deployments
type memoryUploadCache struct {
	cache *ttlcache.Cache[string, UploadInfo]
}

func newMemoryUploadCache() *memoryUploadCache {
	cache := ttlcache.New[string, UploadInfo]()

	go cache.Start()

	return &memoryUploadCache{cache: cache}
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
