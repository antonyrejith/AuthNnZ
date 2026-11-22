package app

import (
	"container/list"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/antonyrejith/portun/internal/platform/bus"
	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/scope"
)

// cacheKey identifies one tenant environment.
type cacheKey struct {
	tenant ids.TenantID
	env    ids.EnvironmentID
}

// cacheEntry holds the current bundle of one tenant environment behind an
// atomic pointer: readers never lock, a reload swaps the pointer.
type cacheEntry struct {
	key    cacheKey
	ptr    atomic.Pointer[Bundle]
	loadMu sync.Mutex // serializes the first load
}

// swapIfNewer stores b unless the entry already holds the same or a newer
// version.
func (e *cacheEntry) swapIfNewer(b *Bundle) {
	for {
		cur := e.ptr.Load()
		if cur != nil && b.Version <= cur.Version {
			return
		}
		if e.ptr.CompareAndSwap(cur, b) {
			return
		}
	}
}

// bundleCache is a size-bounded LRU of cache entries: memory does not grow
// with the number of tenants (ADR-0003).
type bundleCache struct {
	mu  sync.Mutex
	max int
	ll  *list.List // front = most recently used; values are *cacheEntry
	m   map[cacheKey]*list.Element
}

func newBundleCache(limit int) *bundleCache {
	return &bundleCache{max: limit, ll: list.New(), m: map[cacheKey]*list.Element{}}
}

// get returns the entry for k, creating it (and evicting the least recently
// used one beyond the bound) when it is not cached.
func (c *bundleCache) get(k cacheKey) *cacheEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[k]; ok {
		c.ll.MoveToFront(el)
		return el.Value.(*cacheEntry) //nolint:errcheck,forcetypeassert // the list only holds *cacheEntry
	}
	e := &cacheEntry{key: k}
	c.m[k] = c.ll.PushFront(e)
	for c.ll.Len() > c.max {
		last := c.ll.Back()
		c.ll.Remove(last)
		delete(c.m, last.Value.(*cacheEntry).key) //nolint:errcheck,forcetypeassert // the list only holds *cacheEntry
	}
	return e
}

// peek returns the entry for k without creating or promoting it.
func (c *bundleCache) peek(k cacheKey) *cacheEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[k]; ok {
		return el.Value.(*cacheEntry) //nolint:errcheck,forcetypeassert // the list only holds *cacheEntry
	}
	return nil
}

func (c *bundleCache) keys() []cacheKey {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]cacheKey, 0, len(c.m))
	for k := range c.m {
		out = append(out, k)
	}
	return out
}

// Bundle returns the current bundle of the scope's tenant environment. It is
// served from the cache; the first call loads the latest stored bundle, or
// compiles an in-memory bundle (version 0) of the effective settings when
// the environment has never published.
func (s *Service) Bundle(ctx context.Context) (*Bundle, error) {
	t, err := envScope(ctx)
	if err != nil {
		return nil, err
	}
	e := s.cache.get(cacheKey{t.TenantID, t.EnvironmentID})
	if b := e.ptr.Load(); b != nil {
		return b, nil
	}
	e.loadMu.Lock()
	defer e.loadMu.Unlock()
	if b := e.ptr.Load(); b != nil {
		return b, nil
	}
	b, err := s.loadLatest(ctx, t)
	if err != nil {
		return nil, err
	}
	e.swapIfNewer(b)
	return e.ptr.Load(), nil
}

// loadLatest reads the environment's newest stored bundle and compiles it.
func (s *Service) loadLatest(ctx context.Context, t scope.Tenant) (*Bundle, error) {
	var b *Bundle
	err := s.read(ctx, func(ctx context.Context, q db.Querier) error {
		row, found, err := s.repo.LatestBundle(ctx, q, t.TenantID, t.EnvironmentID)
		if err != nil {
			return err //nolint:wrapcheck // already an *errs.Error
		}
		if found {
			b, err = s.reg.buildBundle(row.Payload, row.Checksum)
			return err
		}
		settings, err := s.effectiveForEnvironment(ctx, q, t)
		if err != nil {
			return err
		}
		raw, sum, err := encodePayload(t.TenantID, t.EnvironmentID, 0, settings, nil)
		if err != nil {
			return err
		}
		b, err = s.reg.buildBundle(raw, sum)
		return err
	})
	return b, err
}

// refresh reloads a cached tenant environment when the stored bundle is newer
// than the cached one. An environment that is not cached is left alone: it
// loads lazily on first use. minVersion, when positive, is the version a
// notification announced; a cache already at it is not reloaded.
func (s *Service) refresh(ctx context.Context, k cacheKey, minVersion int64) error {
	e := s.cache.peek(k)
	if e == nil {
		return nil
	}
	if cur := e.ptr.Load(); cur != nil && minVersion > 0 && cur.Version >= minVersion {
		return nil
	}
	b, err := s.loadLatest(scope.WithTenant(ctx, scope.Tenant{TenantID: k.tenant, EnvironmentID: k.env}), scope.Tenant{TenantID: k.tenant, EnvironmentID: k.env})
	if err != nil {
		return err
	}
	e.swapIfNewer(b)
	return nil
}

// pollOnce checks every cached environment for a newer stored bundle. A
// failure keeps the last good bundle in service.
func (s *Service) pollOnce(ctx context.Context) {
	for _, k := range s.cache.keys() {
		if ctx.Err() != nil {
			return
		}
		e := s.cache.peek(k)
		if e == nil {
			continue
		}
		cur := e.ptr.Load()
		if cur == nil {
			continue
		}
		tctx := scope.WithTenant(ctx, scope.Tenant{TenantID: k.tenant, EnvironmentID: k.env})
		var latest int64
		err := s.read(tctx, func(ctx context.Context, q db.Querier) error {
			var err error
			latest, err = s.repo.MaxBundleVersion(ctx, q, k.tenant, k.env)
			return err //nolint:wrapcheck // already an *errs.Error
		})
		if err != nil {
			s.logger.WarnContext(ctx, "config bundle poll failed; serving the last good bundle",
				slog.String("tenant_id", k.tenant.String()), slog.String("environment_id", k.env.String()), slog.String("error", err.Error()))
			continue
		}
		if latest > cur.Version {
			if err := s.refresh(ctx, k, latest); err != nil {
				s.logger.WarnContext(ctx, "config bundle reload failed; serving the last good bundle",
					slog.String("tenant_id", k.tenant.String()), slog.String("environment_id", k.env.String()), slog.String("error", err.Error()))
			}
		}
	}
}

// Run keeps the cached bundles current until ctx is done: it consumes the
// CONFIG stream with the durable consumer "config-<InstanceID>" (new messages
// only) and, independently of the bus, polls for newer bundles of cached
// environments every PollInterval, so a bus outage delays an update but never
// blocks it (static stability). A bundle that fails to load is skipped and
// the last good one keeps serving. Run returns nil once ctx is done.
func (s *Service) Run(ctx context.Context) error {
	var cons *bus.Consumer
	connect := func() {
		if s.bus == nil || cons != nil {
			return
		}
		c, err := s.bus.Consume(ctx, bus.ConsumerSpec{
			Stream: bus.StreamConfig, Durable: "config-" + s.instanceID,
			FilterSubjects: []string{"config.>"}, DeliverPolicy: bus.DeliverNew,
		}, s.onNotice)
		if err != nil {
			s.logger.WarnContext(ctx, "config bus consumer not started; polling only", slog.String("error", err.Error()))
			return
		}
		cons = c
	}
	connect()
	tick := time.NewTicker(s.poll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			if cons != nil {
				cons.Stop()
			}
			return nil
		case <-tick.C:
			connect()
			s.pollOnce(ctx)
		}
	}
}

// onNotice handles one CONFIG message.
func (s *Service) onNotice(ctx context.Context, d bus.Delivery) error {
	parts := strings.Split(d.Subject, ".")
	if len(parts) != 3 {
		return fmt.Errorf("config notice subject %q: %w", d.Subject, bus.ErrPoison)
	}
	tenant, err := ids.Parse[ids.TenantKind](parts[1])
	if err != nil {
		return fmt.Errorf("config notice tenant: %w", bus.ErrPoison)
	}
	env, err := ids.Parse[ids.EnvironmentKind](parts[2])
	if err != nil {
		return fmt.Errorf("config notice environment: %w", bus.ErrPoison)
	}
	var n bundleNotice
	if err := json.Unmarshal(d.Data, &n); err != nil {
		return fmt.Errorf("config notice payload: %w", bus.ErrPoison)
	}
	if n.Type != NoticeBundlePublished {
		return nil
	}
	if err := s.refresh(ctx, cacheKey{tenant, env}, n.Version); err != nil {
		return errs.Wrap(errs.CodeOf(err), err, "config: reload after notification")
	}
	return nil
}
