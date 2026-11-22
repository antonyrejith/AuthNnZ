package app

import (
	"testing"
	"time"

	"github.com/antonyrejith/portun/internal/modules/tenancy/internal/domain"
)

// TestHostCache checks the bounded LRU, the time-to-live and the
// invalidation that also discards lookups that raced with a change.
//
// Covers: TEN-01
func TestHostCache(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tenant := domain.Tenant{Slug: "acme"}

	t.Run("hit and expiry", func(t *testing.T) {
		t.Parallel()
		c := newHostCache(4, 30*time.Second)
		if _, ok := c.get("a", now); ok {
			t.Fatal("hit on an empty cache")
		}
		c.put(c.generation(), "a", domain.Host{Host: "a"}, tenant, now)
		e, ok := c.get("a", now.Add(29*time.Second))
		if !ok || e.target.Host != "a" || e.tenant.Slug != "acme" {
			t.Fatalf("get = %+v, %v", e, ok)
		}
		if _, ok := c.get("a", now.Add(30*time.Second)); ok {
			t.Fatal("entry served after its time-to-live")
		}
		if _, ok := c.get("a", now); ok {
			t.Fatal("expired entry was kept")
		}
	})

	t.Run("least recently used is evicted", func(t *testing.T) {
		t.Parallel()
		c := newHostCache(2, time.Minute)
		g := c.generation()
		c.put(g, "a", domain.Host{}, tenant, now)
		c.put(g, "b", domain.Host{}, tenant, now)
		if _, ok := c.get("a", now); !ok { // a is now the most recent
			t.Fatal("a missing")
		}
		c.put(g, "c", domain.Host{}, tenant, now)
		if _, ok := c.get("b", now); ok {
			t.Error("b survived although it was the least recently used")
		}
		for _, h := range []string{"a", "c"} {
			if _, ok := c.get(h, now); !ok {
				t.Errorf("%s evicted", h)
			}
		}
		c.put(g, "c", domain.Host{Host: "c2"}, tenant, now) // update in place
		if e, _ := c.get("c", now); e.target.Host != "c2" || len(c.items) != 2 {
			t.Errorf("update = %+v, %d items", e, len(c.items))
		}
	})

	t.Run("clear drops entries and stale puts", func(t *testing.T) {
		t.Parallel()
		c := newHostCache(4, time.Minute)
		before := c.generation()
		c.put(before, "a", domain.Host{}, tenant, now)
		c.clear()
		if _, ok := c.get("a", now); ok {
			t.Fatal("entry survived clear")
		}
		c.put(before, "b", domain.Host{}, tenant, now) // a lookup that started before the change
		if _, ok := c.get("b", now); ok {
			t.Fatal("a stale lookup repopulated the cache")
		}
		c.put(c.generation(), "b", domain.Host{}, tenant, now)
		if _, ok := c.get("b", now); !ok {
			t.Fatal("a fresh lookup was not cached")
		}
	})
}
