package forgejo

import (
	"strings"
	"sync"
	"time"
)

type cacheEntry struct {
	val     any
	expires time.Time
}

// Cache is a small in-memory TTL cache. Keys are namespaced by the caller
// (section 5.5). A zero or negative ttl means "never expires".
type Cache struct {
	mu sync.Mutex
	m  map[string]cacheEntry
}

// NewCache returns an empty cache.
func NewCache() *Cache { return &Cache{m: map[string]cacheEntry{}} }

// Get returns the cached value for key or runs loader and stores its result.
// Errors are not cached.
func (c *Cache) Get(key string, ttl time.Duration, loader func() (any, error)) (any, error) {
	c.mu.Lock()
	if e, ok := c.m[key]; ok && (e.expires.IsZero() || time.Now().Before(e.expires)) {
		c.mu.Unlock()
		return e.val, nil
	}
	c.mu.Unlock()
	v, err := loader()
	if err != nil {
		return nil, err
	}
	e := cacheEntry{val: v}
	if ttl > 0 {
		e.expires = time.Now().Add(ttl)
	}
	c.mu.Lock()
	c.m[key] = e
	c.mu.Unlock()
	return v, nil
}

// Invalidate drops every key starting with prefix.
func (c *Cache) Invalidate(prefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.m {
		if strings.HasPrefix(k, prefix) {
			delete(c.m, k)
		}
	}
}
