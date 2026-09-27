package cache

import (
	"container/list"
	"fmt"
	"sync"
	"time"
)

type memoryItem struct {
	key    string
	value  *CachedMsg
	weight int
}

// MemoryCache keeps only fresh, frequently queried DNS replies in memory.
// The underlying cache still retains expired data for the existing stale-on-
// upstream-failure behavior. Both entries and approximate bytes are bounded.
type MemoryCache struct {
	backing    Cache
	maxEntries int
	maxBytes   int

	mu         sync.Mutex
	items      map[string]*list.Element
	lru        list.List
	bytes      int
	hits       uint64
	misses     uint64
	generation uint64
}

func NewMemoryCache(backing Cache, maxEntries, maxBytes int) *MemoryCache {
	return &MemoryCache{
		backing:    backing,
		maxEntries: maxEntries,
		maxBytes:   maxBytes,
		items:      make(map[string]*list.Element),
	}
}

func (c *MemoryCache) Get(key string) (*CachedMsg, bool) {
	now := time.Now()
	c.mu.Lock()
	if elem, ok := c.items[key]; ok {
		item := elem.Value.(*memoryItem)
		if item.value.Expires.After(now) {
			c.lru.MoveToFront(elem)
			c.hits++
			c.mu.Unlock()
			return item.value, true // Read-only; callers copy before rewriting TTLs.
		}
		c.remove(elem)
	}
	c.misses++
	version := c.generation
	c.mu.Unlock()

	value, ok := c.backing.Get(key)
	if ok && value != nil && value.Msg != nil && value.Expires.After(time.Now()) {
		weight := entryWeight(key, value)
		c.mu.Lock()
		// A concurrent Set/Delete must not be undone by an older disk read.
		if c.generation == version && c.items[key] == nil {
			c.insert(key, &CachedMsg{Msg: value.Msg.Copy(), Expires: value.Expires}, weight)
		}
		c.mu.Unlock()
	}
	return value, ok
}

func (c *MemoryCache) Set(key string, value *CachedMsg, ttl time.Duration) error {
	if err := c.backing.Set(key, value, ttl); err != nil {
		return err
	}
	var weight int
	if value != nil && value.Msg != nil {
		weight = entryWeight(key, value)
	}
	c.mu.Lock()
	c.generation++
	if elem := c.items[key]; elem != nil {
		c.remove(elem)
	}
	if value != nil && value.Msg != nil && value.Expires.After(time.Now()) {
		c.insert(key, &CachedMsg{Msg: value.Msg.Copy(), Expires: value.Expires}, weight)
	}
	c.mu.Unlock()
	return nil
}

func (c *MemoryCache) Delete(key string) error {
	if err := c.backing.Delete(key); err != nil {
		return err
	}
	c.mu.Lock()
	c.generation++
	if elem := c.items[key]; elem != nil {
		c.remove(elem)
	}
	c.mu.Unlock()
	return nil
}

func (c *MemoryCache) Close() error { return c.backing.Close() }

func (c *MemoryCache) Stats() string {
	c.mu.Lock()
	stats := fmt.Sprintf("hot cache: %d/%d entries, approx %d/%d bytes, hits: %d, misses: %d", len(c.items), c.maxEntries, c.bytes, c.maxBytes, c.hits, c.misses)
	c.mu.Unlock()
	return stats + "; " + c.backing.Stats()
}

// The budget covers wire size, key, and a fixed entry overhead. Go map/list
// allocation overhead varies by runtime and is not a hard RSS limit.
func entryWeight(key string, value *CachedMsg) int {
	return len(key) + value.Msg.Len() + 128
}

func (c *MemoryCache) insert(key string, value *CachedMsg, weight int) {
	if c.maxEntries <= 0 || c.maxBytes <= 0 || weight > c.maxBytes {
		return
	}
	elem := c.lru.PushFront(&memoryItem{key: key, value: value, weight: weight})
	c.items[key] = elem
	c.bytes += weight
	for len(c.items) > c.maxEntries || c.bytes > c.maxBytes {
		c.remove(c.lru.Back())
	}
}

func (c *MemoryCache) remove(elem *list.Element) {
	item := elem.Value.(*memoryItem)
	delete(c.items, item.key)
	c.bytes -= item.weight
	c.lru.Remove(elem)
}
