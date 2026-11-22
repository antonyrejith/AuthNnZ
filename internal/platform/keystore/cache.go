package keystore

import (
	"container/list"
	"strings"
	"sync"
	"time"

	"github.com/antonyrejith/portun/internal/platform/kms/envelope"
)

// cacheEntry is one unwrapped data key.
type cacheEntry struct {
	key    string
	id     string // data key ID (TypeID)
	dek    envelope.DEK
	stored time.Time
}

// lru is a bounded least-recently-used cache of unwrapped data keys, safe
// for concurrent use.
type lru struct {
	mu    sync.Mutex
	max   int
	order *list.List // front = most recently used
	items map[string]*list.Element
}

func newLRU(maxEntries int) *lru {
	return &lru{max: maxEntries, order: list.New(), items: make(map[string]*list.Element, maxEntries)}
}

func (c *lru) get(key string) (cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return cacheEntry{}, false
	}
	c.order.MoveToFront(el)
	e, _ := el.Value.(cacheEntry)
	return e, true
}

func (c *lru) put(e cacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[e.key]; ok {
		el.Value = e
		c.order.MoveToFront(el)
		return
	}
	c.items[e.key] = c.order.PushFront(e)
	for c.order.Len() > c.max {
		last := c.order.Back()
		old, _ := last.Value.(cacheEntry)
		c.order.Remove(last)
		delete(c.items, old.key)
	}
}

// removeIf drops every entry for which match returns true.
func (c *lru) removeIf(match func(cacheEntry) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, el := range c.items {
		e, _ := el.Value.(cacheEntry)
		if match(e) {
			c.order.Remove(el)
			delete(c.items, k)
		}
	}
}

// Cache keys: subject entries ("s") resolve a subject to its active key;
// key entries ("k") hold a key by ID. Tenant labels contain no "\x00".
func subjectCacheKey(tenant, subject string) string { return "s\x00" + tenant + "\x00" + subject }
func idCacheKey(tenant, id string) string           { return "k\x00" + tenant + "\x00" + id }

func tenantOfCacheKey(key string) string {
	parts := strings.SplitN(key, "\x00", 3)
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}
