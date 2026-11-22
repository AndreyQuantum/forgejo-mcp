package forgejo

import (
	"strings"
	"sync"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/clock"
)

type cacheEntry struct {
	val     any
	expires time.Time
}

// Cache is a small in-memory TTL cache. Keys are namespaced by the caller
// (section 5.5). A zero or negative ttl means "never expires".
type Cache struct {
	mu  sync.Mutex
	m   map[string]cacheEntry
	clk clock.Clock
}

// NewCache returns an empty cache on the wall clock.
func NewCache() *Cache { return NewCacheClock(nil) }

// NewCacheClock returns an empty cache that reads time from clk (nil = real).
func NewCacheClock(clk clock.Clock) *Cache {
	if clk == nil {
		clk = clock.Real()
	}
	return &Cache{m: map[string]cacheEntry{}, clk: clk}
}

// Get returns the cached value for key or runs loader and stores its result.
// Errors are not cached.
func (c *Cache) Get(key string, ttl time.Duration, loader func() (any, error)) (any, error) {
	c.mu.Lock()
	if e, ok := c.m[key]; ok && (e.expires.IsZero() || c.clk.Now().Before(e.expires)) {
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
		e.expires = c.clk.Now().Add(ttl)
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

// Put stores val under key; a zero or negative ttl means "never expires".
func (c *Cache) Put(key string, val any, ttl time.Duration) {
	e := cacheEntry{val: val}
	if ttl > 0 {
		e.expires = c.clk.Now().Add(ttl)
	}
	c.mu.Lock()
	c.m[key] = e
	c.mu.Unlock()
}
