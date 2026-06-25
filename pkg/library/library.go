// Package library provides a shared, background-refreshed cache of the Google
// Photos library listing. Listing the whole library is the slow part of S3
// ListObjects and WebDAV PROPFIND, so every surface that needs "all items" or
// "look up by media key" goes through one Cache instead of re-paginating Google
// on each request.
//
// The cache can be backed by a Store (see pkg/cachedb) that persists the last
// snapshot to disk. With a Store, names are served instantly on startup from
// the saved snapshot while a fresh listing loads in the background
// (stale-while-revalidate).
package library

import (
	"context"
	"sort"
	"sync"
	"time"

	"gpix/pkg/gpmc"
)

// Store persists library snapshots (implemented by pkg/cachedb).
type Store interface {
	Load() ([]gpmc.MediaItem, time.Time, error)
	Save(items []gpmc.MediaItem) error
}

// Cache holds the most recent full library listing with a TTL and optional
// background refresh + persistence. It is safe for concurrent use.
type Cache struct {
	gp    *gpmc.Client
	ttl   time.Duration
	store Store

	mu        sync.RWMutex
	items     []gpmc.MediaItem // newest-first
	byKey     map[string]gpmc.MediaItem
	fetchedAt time.Time

	flightMu sync.Mutex
	flight   chan struct{} // non-nil while a refresh is in progress
}

// New returns a cache with the given freshness TTL (defaults to 60s) and an
// optional persistent Store (may be nil). When a Store has a saved snapshot it
// is loaded immediately so the cache is warm from the first request.
func New(gp *gpmc.Client, ttl time.Duration, store Store) *Cache {
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	c := &Cache{
		gp:    gp,
		ttl:   ttl,
		store: store,
		byKey: map[string]gpmc.MediaItem{},
	}
	if store != nil {
		if items, savedAt, err := store.Load(); err == nil && len(items) > 0 {
			c.set(items, savedAt)
		}
	}
	return c
}

func (c *Cache) set(items []gpmc.MediaItem, at time.Time) {
	byKey := make(map[string]gpmc.MediaItem, len(items))
	for _, it := range items {
		byKey[it.MediaKey] = it
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Mtime.After(items[j].Mtime) })
	c.mu.Lock()
	c.items = items
	c.byKey = byKey
	c.fetchedAt = at
	c.mu.Unlock()
}

func (c *Cache) fresh() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items) > 0 && time.Since(c.fetchedAt) < c.ttl
}

func (c *Cache) hasData() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items) > 0
}

// ensure makes sure data is available. If fresh, returns immediately. If stale
// but present, it serves the stale data and refreshes in the background
// (stale-while-revalidate). Only the very first load (no data at all) blocks.
func (c *Cache) ensure(ctx context.Context) error {
	if c.fresh() {
		return nil
	}
	if c.hasData() {
		c.refreshAsync()
		return nil
	}
	return c.refreshBlocking(ctx)
}

func (c *Cache) refreshAsync() {
	c.flightMu.Lock()
	if c.flight != nil {
		c.flightMu.Unlock()
		return
	}
	ch := make(chan struct{})
	c.flight = ch
	c.flightMu.Unlock()
	go func() {
		_ = c.Refresh(context.Background())
		c.flightMu.Lock()
		c.flight = nil
		close(ch)
		c.flightMu.Unlock()
	}()
}

func (c *Cache) refreshBlocking(ctx context.Context) error {
	c.flightMu.Lock()
	if c.flight != nil {
		ch := c.flight
		c.flightMu.Unlock()
		select {
		case <-ch:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ch := make(chan struct{})
	c.flight = ch
	c.flightMu.Unlock()

	err := c.Refresh(ctx)

	c.flightMu.Lock()
	c.flight = nil
	close(ch)
	c.flightMu.Unlock()
	return err
}

// Refresh unconditionally re-reads the entire library and persists it.
func (c *Cache) Refresh(ctx context.Context) error {
	items, err := c.gp.ListRecent(ctx, 0) // 0 = everything
	if err != nil {
		return err
	}
	c.set(items, time.Now())
	if c.store != nil {
		_ = c.store.Save(items) // best-effort persistence
	}
	return nil
}

// All returns the cached library (refreshing if stale). The slice must not be
// mutated by the caller.
func (c *Cache) All(ctx context.Context) ([]gpmc.MediaItem, error) {
	if err := c.ensure(ctx); err != nil {
		return nil, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.items, nil
}

// Get looks up a single item by media key (refreshing if stale).
func (c *Cache) Get(ctx context.Context, mediaKey string) (gpmc.MediaItem, bool, error) {
	c.mu.RLock()
	it, ok := c.byKey[mediaKey]
	c.mu.RUnlock()
	if ok && c.fresh() {
		return it, true, nil
	}
	if err := c.ensure(ctx); err != nil {
		return gpmc.MediaItem{}, false, err
	}
	c.mu.RLock()
	it, ok = c.byKey[mediaKey]
	c.mu.RUnlock()
	return it, ok, nil
}

// Peek returns an item from whatever is currently in memory WITHOUT triggering
// a refresh. Use it on hot paths (e.g. per-thumbnail requests) that must never
// block on a full library listing; a miss just means "not known yet".
func (c *Cache) Peek(mediaKey string) (gpmc.MediaItem, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	it, ok := c.byKey[mediaKey]
	return it, ok
}

// Invalidate forces the next access to refresh (call after uploads/deletes).
func (c *Cache) Invalidate() {
	c.mu.Lock()
	c.fetchedAt = time.Time{}
	c.mu.Unlock()
}

// Start runs a background refresh loop until ctx is cancelled, keeping the cache
// warm. interval defaults to the TTL.
func (c *Cache) Start(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = c.ttl
	}
	go func() {
		_ = c.Refresh(ctx)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_ = c.Refresh(ctx)
			}
		}
	}()
}
