package webdav

import (
	"os"
	"sync"
	"time"
)

const fileCacheTTL = 10 * time.Minute

type cacheEntry struct {
	path string
	exp  time.Time
}

type fileCache struct {
	mu sync.Mutex
	m  map[string]cacheEntry
}

func newFileCache() *fileCache {
	c := &fileCache{m: make(map[string]cacheEntry)}
	go c.reap()
	return c
}

func (c *fileCache) get(mediaKey string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[mediaKey]
	if !ok || time.Now().After(e.exp) {
		return "", false
	}
	if _, err := os.Stat(e.path); err != nil {
		delete(c.m, mediaKey)
		return "", false
	}
	return e.path, true
}

func (c *fileCache) put(mediaKey, path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[mediaKey] = cacheEntry{path: path, exp: time.Now().Add(fileCacheTTL)}
}

func (c *fileCache) reap() {
	for range time.Tick(fileCacheTTL) {
		c.mu.Lock()
		now := time.Now()
		for k, e := range c.m {
			if now.After(e.exp) {
				_ = os.Remove(e.path)
				delete(c.m, k)
			}
		}
		c.mu.Unlock()
	}
}
