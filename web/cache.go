package web

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"sync/atomic"
)

// responseCache stores marshaled JSON responses in a bounded LRU.
type responseCache struct {
	mu     sync.Mutex
	max    int
	order  *list.List
	items  map[string]*list.Element
	hits   atomic.Int64
	misses atomic.Int64
}

type cacheEntry struct {
	key  string
	body []byte
}

func newResponseCache(maxEntries int) *responseCache {
	return &responseCache{
		max:   maxEntries,
		order: list.New(),
		items: make(map[string]*list.Element, maxEntries),
	}
}

func cacheKey(endpoint string, data []byte) string {
	digest := sha256.New()
	digest.Write([]byte(endpoint))
	digest.Write([]byte{0})
	digest.Write(data)
	return hex.EncodeToString(digest.Sum(nil))
}

func (c *responseCache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.items[key]
	if !ok {
		c.misses.Add(1)
		return nil, false
	}
	c.order.MoveToFront(element)
	c.hits.Add(1)
	return cacheEntryOf(element).body, true
}

func (c *responseCache) put(key string, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.items[key]; ok {
		cacheEntryOf(element).body = body
		c.order.MoveToFront(element)
		return
	}
	element := c.order.PushFront(&cacheEntry{key: key, body: body})
	c.items[key] = element
	for c.order.Len() > c.max {
		oldest := c.order.Back()
		if oldest == nil {
			return
		}
		c.order.Remove(oldest)
		delete(c.items, cacheEntryOf(oldest).key)
	}
}

func cacheEntryOf(element *list.Element) *cacheEntry {
	entry, _ := element.Value.(*cacheEntry)
	return entry
}
