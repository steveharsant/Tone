package check

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"sync"
)

// Cache is a bounded LRU of segment-check results, keyed by
// (model, categories, segment text). It is what makes typing feel instant:
// on each debounced check only segments whose hash changed hit the model.
// Empty results are cached too — "this sentence is fine" is the common case.
type Cache struct {
	mu  sync.Mutex
	cap int
	ll  *list.List
	m   map[string]*list.Element
}

type cacheEntry struct {
	key  string
	raws []RawSuggestion
}

func NewCache(capacity int) *Cache {
	return &Cache{cap: capacity, ll: list.New(), m: make(map[string]*list.Element)}
}

func CacheKey(model, optsKey, segText string) string {
	h := sha256.New()
	h.Write([]byte(model))
	h.Write([]byte{0})
	h.Write([]byte(optsKey))
	h.Write([]byte{0})
	h.Write([]byte(segText))
	return hex.EncodeToString(h.Sum(nil))
}

func (c *Cache) Get(key string) ([]RawSuggestion, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.m[key]
	if !ok {
		return nil, false
	}
	c.ll.MoveToFront(el)
	return el.Value.(*cacheEntry).raws, true
}

func (c *Cache) Put(key string, raws []RawSuggestion) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[key]; ok {
		el.Value.(*cacheEntry).raws = raws
		c.ll.MoveToFront(el)
		return
	}
	c.m[key] = c.ll.PushFront(&cacheEntry{key: key, raws: raws})
	for c.ll.Len() > c.cap {
		last := c.ll.Back()
		c.ll.Remove(last)
		delete(c.m, last.Value.(*cacheEntry).key)
	}
}
