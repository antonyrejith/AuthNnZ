package app

import (
	"container/list"
	"sync"
	"time"

	"github.com/antonyrejith/portun/internal/modules/tenancy/internal/domain"
)

// Host cache limits.
const (
	hostCacheSize = 1024
	hostCacheTTL  = 30 * time.Second
)

type hostEntry struct {
	host    string
	target  domain.Host
	tenant  domain.Tenant
	expires time.Time
}

// hostCache is a bounded LRU with a time-to-live for positive host
// resolutions. clear invalidates every entry and also discards the result of
// a lookup that started before it (the generation check), so a read that
// raced with a change cannot repopulate the cache with stale data.
type hostCache struct {
	mu    sync.Mutex
	max   int
	ttl   time.Duration
	gen   uint64
	order *list.List // front = most recently used
	items map[string]*list.Element
}

func newHostCache(size int, ttl time.Duration) *hostCache {
	return &hostCache{max: size, ttl: ttl, order: list.New(), items: map[string]*list.Element{}}
}

// generation returns the current generation, to pass to put.
func (c *hostCache) generation() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen
}

func (c *hostCache) get(host string, now time.Time) (hostEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[host]
	if !ok {
		return hostEntry{}, false
	}
	e, _ := el.Value.(hostEntry)
	if !now.Before(e.expires) {
		c.order.Remove(el)
		delete(c.items, host)
		return hostEntry{}, false
	}
	c.order.MoveToFront(el)
	return e, true
}

// put stores an entry looked up at generation gen; it is dropped when the
// cache was cleared since.
func (c *hostCache) put(gen uint64, host string, target domain.Host, tenant domain.Tenant, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.gen {
		return
	}
	e := hostEntry{host: host, target: target, tenant: tenant, expires: now.Add(c.ttl)}
	if el, ok := c.items[host]; ok {
		el.Value = e
		c.order.MoveToFront(el)
		return
	}
	c.items[host] = c.order.PushFront(e)
	for c.order.Len() > c.max {
		last := c.order.Back()
		old, _ := last.Value.(hostEntry)
		delete(c.items, old.host)
		c.order.Remove(last)
	}
}

func (c *hostCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	c.order.Init()
	c.items = map[string]*list.Element{}
}
