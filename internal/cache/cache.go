// Package cache provides a generic TTL-LRU cache and a singleflight helper.
package cache

import (
	"container/list"
	"sync"
	"time"
)

type cacheEntry[V any] struct {
	key     string
	value   V
	expires time.Time
}

// Cache is a size-bounded LRU with per-entry TTL.
type Cache[V any] struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	lru     *list.List
	max     int
	ttl     time.Duration
}

// New creates a cache holding at most max entries, each living for ttl.
// max <= 0 disables the cache (Get always misses, Set is a no-op).
func New[V any](max int, ttl time.Duration) *Cache[V] {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &Cache[V]{
		entries: make(map[string]*list.Element, max),
		lru:     list.New(),
		max:     max,
		ttl:     ttl,
	}
}

// Get returns the cached value if present and not expired.
func (c *Cache[V]) Get(key string) (V, bool) {
	var zero V
	if c == nil || c.max <= 0 {
		return zero, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return zero, false
	}
	ent := el.Value.(*cacheEntry[V])
	if time.Now().After(ent.expires) {
		c.lru.Remove(el)
		delete(c.entries, key)
		return zero, false
	}
	c.lru.MoveToFront(el)
	return ent.value, true
}

// Set stores a value, evicting the least recently used entry when full.
func (c *Cache[V]) Set(key string, value V) {
	if c == nil || c.max <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		ent := el.Value.(*cacheEntry[V])
		ent.value = value
		ent.expires = time.Now().Add(c.ttl)
		c.lru.MoveToFront(el)
		return
	}
	el := c.lru.PushFront(&cacheEntry[V]{key: key, value: value, expires: time.Now().Add(c.ttl)})
	c.entries[key] = el
	for c.lru.Len() > c.max {
		last := c.lru.Back()
		if last == nil {
			break
		}
		ent := last.Value.(*cacheEntry[V])
		c.lru.Remove(last)
		delete(c.entries, ent.key)
	}
}

// Cleanup drops expired entries. It is safe to call periodically.
func (c *Cache[V]) Cleanup() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	removed := 0
	for key, el := range c.entries {
		ent := el.Value.(*cacheEntry[V])
		if now.After(ent.expires) {
			c.lru.Remove(el)
			delete(c.entries, key)
			removed++
		}
	}
	return removed
}

// Len returns the number of stored entries.
func (c *Cache[V]) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lru.Len()
}

type call[V any] struct {
	done chan struct{}
	val  V
	err  error
}

// Group deduplicates concurrent calls that share a key.
type Group[V any] struct {
	mu    sync.Mutex
	calls map[string]*call[V]
}

// Do runs fn once per key while a call is in flight.
func Do[V any](g *Group[V], key string, fn func() (V, error)) (V, error) {
	g.mu.Lock()
	if g.calls == nil {
		g.calls = make(map[string]*call[V])
	}
	if c, ok := g.calls[key]; ok {
		g.mu.Unlock()
		<-c.done
		return c.val, c.err
	}
	c := &call[V]{done: make(chan struct{})}
	g.calls[key] = c
	g.mu.Unlock()

	c.val, c.err = fn()
	close(c.done)

	g.mu.Lock()
	delete(g.calls, key)
	g.mu.Unlock()
	return c.val, c.err
}
