package httpx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultCheckTimeout bounds a health check registered without a timeout.
const DefaultCheckTimeout = 2 * time.Second

// Check reports whether a dependency is usable. It must honor ctx.
type Check func(ctx context.Context) error

// Health status values in responses.
const (
	HealthOK          = "ok"
	HealthUnavailable = "unavailable"
	HealthFailed      = "failed"
	HealthTimeout     = "timeout"
)

// HealthReport is the body of a health response. It lists check names and
// their status only; error text is logged, never returned.
type HealthReport struct {
	Status string        `json:"status"`
	Checks []CheckStatus `json:"checks,omitempty"`
}

// CheckStatus is the result of one named check.
type CheckStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type namedCheck struct {
	name    string
	timeout time.Duration
	fn      Check
}

// Health serves the Kubernetes probes /healthz/startup, /healthz/live and
// /healthz/ready (plan 12 §1.3).
//
//   - Live always answers 200: the process is serving HTTP. It never depends
//     on external systems, so a database outage does not restart every pod.
//   - Startup runs the startup checks until they all pass once; after that it
//     answers 200 without running them again.
//   - Ready runs the readiness checks on every probe.
//
// Checks run concurrently, each with its own timeout. A check that ignores
// its context is abandoned after the timeout (it finishes in the background).
type Health struct {
	logger  *slog.Logger
	mu      sync.RWMutex
	startup []namedCheck
	ready   []namedCheck
	started atomic.Bool
}

// NewHealth returns a Health with no checks.
func NewHealth(logger *slog.Logger) *Health { return &Health{logger: logger} }

// AddStartupCheck registers a startup check. A zero timeout means
// DefaultCheckTimeout. It panics on an empty or duplicate name or a nil check
// (programmer errors found at startup).
func (h *Health) AddStartupCheck(name string, timeout time.Duration, c Check) {
	h.add(&h.startup, name, timeout, c)
}

// AddReadinessCheck registers a readiness check (see AddStartupCheck).
func (h *Health) AddReadinessCheck(name string, timeout time.Duration, c Check) {
	h.add(&h.ready, name, timeout, c)
}

func (h *Health) add(list *[]namedCheck, name string, timeout time.Duration, c Check) {
	if strings.TrimSpace(name) == "" || c == nil {
		panic("httpx: health check needs a name and a function")
	}
	if timeout <= 0 {
		timeout = DefaultCheckTimeout
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if slices.ContainsFunc(*list, func(n namedCheck) bool { return n.name == name }) {
		panic("httpx: duplicate health check " + name)
	}
	*list = append(*list, namedCheck{name: name, timeout: timeout, fn: c})
}

// Register adds GET /healthz/startup, /healthz/live and /healthz/ready to a
// Router or http.ServeMux.
func (h *Health) Register(mux interface{ Handle(string, http.Handler) }) {
	mux.Handle("GET /healthz/startup", h.Startup())
	mux.Handle("GET /healthz/live", h.Live())
	mux.Handle("GET /healthz/ready", h.Ready())
}

// Live answers the liveness probe.
func (h *Health) Live() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.respond(w, r, HealthReport{Status: HealthOK})
	})
}

// Startup answers the startup probe.
func (h *Health) Startup() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.started.Load() {
			h.respond(w, r, HealthReport{Status: HealthOK})
			return
		}
		h.mu.RLock()
		checks := slices.Clone(h.startup)
		h.mu.RUnlock()
		rep := h.run(r.Context(), "startup", checks)
		if rep.Status == HealthOK {
			h.started.Store(true)
		}
		h.respond(w, r, rep)
	})
}

// Ready answers the readiness probe.
func (h *Health) Ready() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.RLock()
		checks := slices.Clone(h.ready)
		h.mu.RUnlock()
		h.respond(w, r, h.run(r.Context(), "ready", checks))
	})
}

func (h *Health) run(ctx context.Context, probe string, checks []namedCheck) HealthReport {
	rep := HealthReport{Status: HealthOK, Checks: make([]CheckStatus, len(checks))}
	var wg sync.WaitGroup
	for i, c := range checks {
		wg.Go(func() {
			start := time.Now()
			status, err := runCheck(ctx, c)
			rep.Checks[i] = CheckStatus{Name: c.name, Status: status}
			if err != nil {
				h.logger.LogAttrs(ctx, slog.LevelWarn, "health check failed",
					slog.String("probe", probe),
					slog.String("check", c.name),
					slog.String("status", status),
					slog.String("error", err.Error()),
					slog.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000),
				)
			}
		})
	}
	wg.Wait()
	for _, c := range rep.Checks {
		if c.Status != HealthOK {
			rep.Status = HealthUnavailable
		}
	}
	slices.SortFunc(rep.Checks, func(a, b CheckStatus) int { return strings.Compare(a.Name, b.Name) })
	return rep
}

func runCheck(parent context.Context, c namedCheck) (string, error) {
	ctx, cancel := context.WithTimeout(parent, c.timeout)
	defer cancel()
	done := make(chan error, 1) // buffered: an abandoned check can still finish
	go func() {
		defer func() {
			if v := recover(); v != nil {
				done <- fmt.Errorf("health check panicked: %v", v)
			}
		}()
		done <- c.fn(ctx)
	}()
	select {
	case err := <-done:
		if err == nil {
			return HealthOK, nil
		}
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil {
			return HealthTimeout, err
		}
		return HealthFailed, err
	case <-ctx.Done():
		return HealthTimeout, fmt.Errorf("health check %s: %w", c.name, ctx.Err())
	}
}

func (h *Health) respond(w http.ResponseWriter, r *http.Request, rep HealthReport) {
	status := http.StatusOK
	if rep.Status != HealthOK {
		status = http.StatusServiceUnavailable
	}
	w.Header().Set("Cache-Control", "no-store")
	if err := WriteJSON(w, status, rep); err != nil {
		h.logger.LogAttrs(r.Context(), slog.LevelDebug, "write health response", slog.String("error", err.Error()))
	}
}
