package keystore

import (
	"fmt"
	"testing"
	"time"
)

func entryFor(key, id string) cacheEntry {
	return cacheEntry{key: key, id: id, stored: time.Unix(1, 0)}
}

// TestLRUEvictsLeastRecentlyUsed: the cache holds at most max entries, a
// read refreshes an entry, and a put of an existing key replaces its value
// without growing the cache.
//
// Covers: SEC-08
func TestLRUEvictsLeastRecentlyUsed(t *testing.T) {
	t.Parallel()
	c := newLRU(2)
	c.put(entryFor("a", "1"))
	c.put(entryFor("b", "2"))
	if _, ok := c.get("a"); !ok { // a becomes most recently used
		t.Fatal("a missing")
	}
	c.put(entryFor("c", "3")) // evicts b
	if _, ok := c.get("b"); ok {
		t.Error("b survived although it was least recently used")
	}
	for _, k := range []string{"a", "c"} {
		if _, ok := c.get(k); !ok {
			t.Errorf("%s evicted", k)
		}
	}
	c.put(entryFor("a", "replaced"))
	if e, ok := c.get("a"); !ok || e.id != "replaced" {
		t.Errorf("a after replace = %+v, %v", e, ok)
	}
	if n := c.order.Len(); n != 2 || len(c.items) != 2 {
		t.Errorf("cache holds %d/%d entries, want 2", n, len(c.items))
	}
}

// TestLRURemoveIf: removeIf drops exactly the matching entries.
//
// Covers: SEC-08
func TestLRURemoveIf(t *testing.T) {
	t.Parallel()
	c := newLRU(10)
	for i := range 6 {
		c.put(entryFor(fmt.Sprintf("k%d", i), fmt.Sprintf("%d", i%2)))
	}
	c.removeIf(func(e cacheEntry) bool { return e.id == "0" })
	for i := range 6 {
		_, ok := c.get(fmt.Sprintf("k%d", i))
		if want := i%2 == 1; ok != want {
			t.Errorf("k%d present = %v, want %v", i, ok, want)
		}
	}
	if c.order.Len() != 3 {
		t.Errorf("%d entries remain, want 3", c.order.Len())
	}
}

// TestTenantOfCacheKey: the tenant label is recovered from subject and key
// cache keys, and a malformed key yields no label.
//
// Covers: SEC-08
func TestTenantOfCacheKey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		key  string
		want string
	}{
		{"subject key", subjectCacheKey("tnt_1", "usr_a"), "tnt_1"},
		{"id key", idCacheKey("platform", "key_1"), "platform"},
		{"subject containing separator", subjectCacheKey("tnt_2", "a\x00b"), "tnt_2"},
		{"malformed", "no-separator", ""},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := tenantOfCacheKey(c.key); got != c.want {
				t.Errorf("tenantOfCacheKey(%q) = %q, want %q", c.key, got, c.want)
			}
		})
	}
}
