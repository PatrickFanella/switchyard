package dedup

import (
	"sync"
	"time"
)

// CacheEntry holds a cached non-streaming response body and its expiry time.
type CacheEntry struct {
	Body      []byte
	ExpiresAt time.Time
}

// Cache is a concurrency-safe LRU cache with per-entry TTL.
// It is intended for non-streaming responses only; streaming responses are never cached.
type Cache struct {
	mu         sync.Mutex
	entries    map[string]*cacheNode
	order      *lruList
	maxEntries int
	ttl        time.Duration
	hits       int64
	misses     int64
}

// NewCache creates a Cache with the given capacity and TTL.
// maxEntries <= 0 or ttl <= 0 produces a disabled no-op cache.
func NewCache(maxEntries int, ttl time.Duration) *Cache {
	return &Cache{
		entries:    make(map[string]*cacheNode, maxEntries),
		order:      newLRUList(),
		maxEntries: maxEntries,
		ttl:        ttl,
	}
}

// Enabled reports whether the cache is active (positive maxEntries and TTL).
func (c *Cache) Enabled() bool {
	return c.maxEntries > 0 && c.ttl > 0
}

// Get returns the cached body for key, or (nil, false) on miss or expiry.
func (c *Cache) Get(key string) ([]byte, bool) {
	if !c.Enabled() {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	node, ok := c.entries[key]
	if !ok || time.Now().After(node.entry.ExpiresAt) {
		if ok {
			// expired — evict lazily
			c.order.remove(node)
			delete(c.entries, key)
		}
		c.misses++
		return nil, false
	}
	c.order.moveToFront(node)
	c.hits++
	return node.entry.Body, true
}

// Set stores body under key, evicting the LRU entry if at capacity.
func (c *Cache) Set(key string, body []byte) {
	if !c.Enabled() {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if node, ok := c.entries[key]; ok {
		node.entry.Body = body
		node.entry.ExpiresAt = time.Now().Add(c.ttl)
		c.order.moveToFront(node)
		return
	}

	if c.order.len() >= c.maxEntries {
		// Evict least-recently-used entry.
		tail := c.order.back()
		if tail != nil {
			delete(c.entries, tail.key)
			c.order.remove(tail)
		}
	}

	node := &cacheNode{
		key: key,
		entry: CacheEntry{
			Body:      body,
			ExpiresAt: time.Now().Add(c.ttl),
		},
	}
	c.order.pushFront(node)
	c.entries[key] = node
}

// Stats returns (hits, misses, currentSize) since creation.
func (c *Cache) Stats() (hits, misses int64, size int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.misses, len(c.entries)
}

// --- minimal doubly-linked list for LRU tracking ---

type cacheNode struct {
	key   string
	entry CacheEntry
	prev  *cacheNode
	next  *cacheNode
}

type lruList struct {
	head *cacheNode // sentinel
	tail *cacheNode // sentinel
	size int
}

func newLRUList() *lruList {
	h := &cacheNode{}
	t := &cacheNode{}
	h.next = t
	t.prev = h
	return &lruList{head: h, tail: t}
}

func (l *lruList) len() int { return l.size }

func (l *lruList) pushFront(n *cacheNode) {
	n.prev = l.head
	n.next = l.head.next
	l.head.next.prev = n
	l.head.next = n
	l.size++
}

func (l *lruList) remove(n *cacheNode) {
	n.prev.next = n.next
	n.next.prev = n.prev
	n.prev = nil
	n.next = nil
	l.size--
}

func (l *lruList) moveToFront(n *cacheNode) {
	l.remove(n)
	l.pushFront(n)
}

func (l *lruList) back() *cacheNode {
	if l.tail.prev == l.head {
		return nil
	}
	return l.tail.prev
}
