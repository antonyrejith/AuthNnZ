//go:build integration

package idempotency_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/db/dbtest"
	"github.com/antonyrejith/portun/internal/platform/db/migrate"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/httpx"
	"github.com/antonyrejith/portun/internal/platform/idempotency"
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(m, dbtest.Options{Sets: []migrate.Set{idempotency.Migrations()}}))
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type fixture struct {
	env   *dbtest.Env
	store *idempotency.Store
	clock *clock
}

func newFixture(t *testing.T, cfg idempotency.Config) *fixture {
	t.Helper()
	env := dbtest.New(t)
	clk := &clock{now: time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)}
	cfg.DB, cfg.Clock = env.DB, clk.Now
	store, err := idempotency.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{env: env, store: store, clock: clk}
}

func (f *fixture) rows(t *testing.T) int {
	t.Helper()
	var n int
	err := f.env.DB.ReadPlatform(t.Context(), func(ctx context.Context, q db.Querier) error {
		return q.QueryRow(ctx, `SELECT count(*) FROM api.idempotency_keys`).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// counting is a handler that answers 201 with a numbered body and records how
// often it ran.
type counting struct {
	calls  atomic.Int64
	status int
	hook   func()
}

func (c *counting) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := c.calls.Add(1)
	if c.hook != nil {
		c.hook()
	}
	body, _ := io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Thing", "thing-"+strconv.FormatInt(n, 10))
	status := c.status
	if status == 0 {
		status = http.StatusCreated
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"call":` + strconv.FormatInt(n, 10) + `,"echo":` + strconv.Itoa(len(body)) + `}`))
}

var requestSeq atomic.Int64

func post(h http.Handler, target, key, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, target, strings.NewReader(body))
	if key != "" {
		r.Header.Set(idempotency.HeaderKey, key)
	}
	rec := httptest.NewRecorder()
	rec.Header().Set(httpx.HeaderRequestID, "req_"+strconv.FormatInt(requestSeq.Add(1), 10))
	h.ServeHTTP(rec, r)
	return rec
}

func problem(t *testing.T, rec *httptest.ResponseRecorder) httpx.Problem {
	t.Helper()
	var p httpx.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("not a problem: %v: %s", err, rec.Body)
	}
	return p
}

// TestReplay: the first request runs and is stored; the same request again
// gets the stored status, headers and body, marked as replayed, without
// running the handler; a request without a key, or with another method, is
// never stored.
//
// Covers: SEC-09
func TestReplay(t *testing.T) {
	t.Parallel()
	f := newFixture(t, idempotency.Config{})
	h := &counting{}
	srv := f.store.Middleware(idempotency.PlatformScope)(h)

	first := post(srv, "/platform/v1/tenants", "key-1", `{"a":1}`)
	if first.Code != http.StatusCreated || first.Header().Get(idempotency.HeaderReplayed) != "" || f.rows(t) != 1 {
		t.Fatalf("first: %d %v rows %d", first.Code, first.Header(), f.rows(t))
	}
	second := post(srv, "/platform/v1/tenants", "key-1", `{"a":1}`)
	if second.Code != http.StatusCreated || second.Body.String() != first.Body.String() ||
		second.Header().Get(idempotency.HeaderReplayed) != "true" ||
		second.Header().Get("X-Thing") != first.Header().Get("X-Thing") ||
		second.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("replay: %d %v %s (first %s)", second.Code, second.Header(), second.Body, first.Body)
	}
	if second.Header().Get(httpx.HeaderRequestID) == "" || second.Header().Get(httpx.HeaderRequestID) == first.Header().Get(httpx.HeaderRequestID) {
		t.Fatalf("the replay must keep its own request ID: %v, first %v", second.Header(), first.Header())
	}
	if h.calls.Load() != 1 {
		t.Fatalf("handler ran %d times, want 1", h.calls.Load())
	}

	// No key, other keys and other methods run every time and store nothing new.
	post(srv, "/platform/v1/tenants", "", `{"a":1}`)
	post(srv, "/platform/v1/tenants", "", `{"a":1}`)
	get := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/platform/v1/tenants", nil)
	get.Header.Set(idempotency.HeaderKey, "key-1")
	srv.ServeHTTP(httptest.NewRecorder(), get)
	if h.calls.Load() != 4 || f.rows(t) != 1 {
		t.Fatalf("calls %d rows %d", h.calls.Load(), f.rows(t))
	}
	if third := post(srv, "/platform/v1/tenants", "key-2", `{"a":1}`); third.Header().Get(idempotency.HeaderReplayed) != "" || f.rows(t) != 2 {
		t.Fatalf("another key replayed: %v rows %d", third.Header(), f.rows(t))
	}
}

// TestClientErrorsAreStored: a 4xx response is a result like any other.
func TestClientErrorsAreStored(t *testing.T) {
	t.Parallel()
	f := newFixture(t, idempotency.Config{})
	h := &counting{status: http.StatusNotFound}
	srv := f.store.Middleware(idempotency.PlatformScope)(h)
	post(srv, "/x", "k", "")
	again := post(srv, "/x", "k", "")
	if again.Code != http.StatusNotFound || again.Header().Get(idempotency.HeaderReplayed) != "true" || h.calls.Load() != 1 {
		t.Fatalf("%d %v calls %d", again.Code, again.Header(), h.calls.Load())
	}
}

// TestKeyReuse: the same key with another method, path, query or body is a
// 422; an invalid key is a 422 and runs nothing.
func TestKeyReuse(t *testing.T) {
	t.Parallel()
	f := newFixture(t, idempotency.Config{})
	h := &counting{}
	srv := f.store.Middleware(idempotency.PlatformScope)(h)
	post(srv, "/a", "k", "one")
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"body":  post(srv, "/a", "k", "two"),
		"path":  post(srv, "/b", "k", "one"),
		"query": post(srv, "/a?x=1", "k", "one"),
	} {
		p := problem(t, rec)
		if rec.Code != http.StatusUnprocessableEntity || p.Code != errs.CodeValidationFailed || len(p.Errors) != 1 ||
			p.Errors[0].Pointer != "/headers/Idempotency-Key" || !strings.Contains(p.Errors[0].Detail, "reused with a different request") {
			t.Errorf("%s: %d %+v", name, rec.Code, p)
		}
	}
	if h.calls.Load() != 1 {
		t.Fatalf("handler ran %d times", h.calls.Load())
	}

	for name, key := range map[string]string{"too long": strings.Repeat("k", 256), "not printable": "a\x7fb"} {
		rec := post(srv, "/a", key, "one")
		if rec.Code != http.StatusUnprocessableEntity || problem(t, rec).Code != errs.CodeValidationFailed {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	// An empty header value (not the same as no header) and two headers.
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/a", nil)
	r.Header["Idempotency-Key"] = []string{""}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("empty key: %d", rec.Code)
	}
	r = httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/a", nil)
	r.Header["Idempotency-Key"] = []string{"a", "b"}
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnprocessableEntity || h.calls.Load() != 1 {
		t.Errorf("two keys: %d, calls %d", rec.Code, h.calls.Load())
	}
}

// TestInProgress: while the first request runs, a repeat is a 409; once it
// has finished the repeat replays.
func TestInProgress(t *testing.T) {
	t.Parallel()
	f := newFixture(t, idempotency.Config{})
	started, release := make(chan struct{}), make(chan struct{})
	h := &counting{hook: func() { close(started); <-release }}
	srv := f.store.Middleware(idempotency.PlatformScope)(h)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- post(srv, "/a", "k", "x") }()
	<-started
	rec := post(srv, "/a", "k", "x")
	if p := problem(t, rec); rec.Code != http.StatusConflict || p.Code != errs.CodeConflict {
		t.Fatalf("repeat while running: %d %+v", rec.Code, p)
	}
	close(release)
	if first := <-done; first.Code != http.StatusCreated {
		t.Fatalf("first: %d", first.Code)
	}
	if again := post(srv, "/a", "k", "x"); again.Code != http.StatusCreated || again.Header().Get(idempotency.HeaderReplayed) != "true" {
		t.Fatalf("after: %d %v", again.Code, again.Header())
	}
}

// TestNotStored: a server error, a panic and an oversize response release the
// key, so the client can retry and the handler runs again.
func TestNotStored(t *testing.T) {
	t.Parallel()
	t.Run("server error", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, idempotency.Config{})
		var fail atomic.Bool
		fail.Store(true)
		var calls atomic.Int64
		srv := f.store.Middleware(idempotency.PlatformScope)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if fail.Load() {
				httpx.WriteProblem(w, r, errs.New(errs.CodeInternal, "boom"))
				return
			}
			w.WriteHeader(http.StatusCreated)
		}))
		if rec := post(srv, "/a", "k", ""); rec.Code != http.StatusInternalServerError || f.rows(t) != 0 {
			t.Fatalf("first: %d rows %d", rec.Code, f.rows(t))
		}
		fail.Store(false)
		if rec := post(srv, "/a", "k", ""); rec.Code != http.StatusCreated || rec.Header().Get(idempotency.HeaderReplayed) != "" {
			t.Fatalf("retry: %d %v", rec.Code, rec.Header())
		}
		if rec := post(srv, "/a", "k", ""); rec.Header().Get(idempotency.HeaderReplayed) != "true" || calls.Load() != 2 {
			t.Fatalf("replay: %v calls %d", rec.Header(), calls.Load())
		}
	})
	t.Run("panic", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, idempotency.Config{})
		var calls atomic.Int64
		srv := f.store.Middleware(idempotency.PlatformScope)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if calls.Add(1) == 1 {
				panic("handler bug")
			}
			w.WriteHeader(http.StatusCreated)
		}))
		func() {
			defer func() {
				if recover() == nil {
					t.Error("the panic was swallowed")
				}
			}()
			post(srv, "/a", "k", "")
		}()
		if f.rows(t) != 0 {
			t.Fatalf("the key of a panicked request is still stored")
		}
		if rec := post(srv, "/a", "k", ""); rec.Code != http.StatusCreated {
			t.Fatalf("retry: %d", rec.Code)
		}
	})
	t.Run("oversize response", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, idempotency.Config{MaxResponseBytes: 16})
		h := &counting{}
		srv := f.store.Middleware(idempotency.PlatformScope)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			h.calls.Add(1)
			_, _ = w.Write(bytes.Repeat([]byte("x"), 64))
		}))
		post(srv, "/a", "k", "")
		if rec := post(srv, "/a", "k", ""); rec.Header().Get(idempotency.HeaderReplayed) != "" || h.calls.Load() != 2 || f.rows(t) != 0 {
			t.Fatalf("%v calls %d rows %d", rec.Header(), h.calls.Load(), f.rows(t))
		}
	})
}

// TestExpiry: a stored response is replayed for the TTL and replaced after
// it; the lease of a request that never finished ends the same way; Purge
// deletes expired rows.
func TestExpiry(t *testing.T) {
	t.Parallel()
	f := newFixture(t, idempotency.Config{})
	h := &counting{}
	srv := f.store.Middleware(idempotency.PlatformScope)(h)
	post(srv, "/a", "k", "x")
	f.clock.Advance(idempotency.DefaultTTL - time.Minute)
	if rec := post(srv, "/a", "k", "x"); rec.Header().Get(idempotency.HeaderReplayed) != "true" {
		t.Fatalf("within the TTL: %v", rec.Header())
	}
	f.clock.Advance(2 * time.Minute)
	// Expired rows are ignored, even for another request: the key is reusable.
	rec := post(srv, "/a", "k", "different body")
	if rec.Code != http.StatusCreated || rec.Header().Get(idempotency.HeaderReplayed) != "" || h.calls.Load() != 2 {
		t.Fatalf("after the TTL: %d %v calls %d", rec.Code, rec.Header(), h.calls.Load())
	}

	// A stale lease: insert the row of a request that crashed.
	started := make(chan struct{})
	block := make(chan struct{})
	crash := &counting{hook: func() { close(started); <-block }}
	crashed := f.store.Middleware(idempotency.PlatformScope)(crash)
	crashDone := make(chan struct{})
	go func() { defer close(crashDone); post(crashed, "/b", "lost", "x") }()
	<-started
	if rec := post(srv, "/b", "lost", "x"); rec.Code != http.StatusConflict {
		t.Fatalf("during the lease: %d", rec.Code)
	}
	f.clock.Advance(idempotency.DefaultInProgressTTL + time.Second)
	if rec := post(srv, "/b", "lost", "x"); rec.Code != http.StatusCreated {
		t.Fatalf("after the lease: %d", rec.Code)
	}
	close(block)
	<-crashDone

	f.clock.Advance(idempotency.DefaultTTL + time.Hour)
	n, err := f.store.Purge(t.Context())
	if err != nil || n == 0 || f.rows(t) != 0 {
		t.Fatalf("Purge() = %d, %v; rows %d", n, err, f.rows(t))
	}
	if n, err := f.store.Purge(t.Context()); err != nil || n != 0 {
		t.Fatalf("second Purge() = %d, %v", n, err)
	}
}

// TestScopes: the same key in different scopes is independent.
func TestScopes(t *testing.T) {
	t.Parallel()
	f := newFixture(t, idempotency.Config{})
	h := &counting{}
	scopeA, scopeB := uuid.New(), uuid.New()
	for _, s := range []uuid.UUID{scopeA, scopeB, scopeA} {
		srv := f.store.Middleware(func(*http.Request) uuid.UUID { return s })(h)
		post(srv, "/a", "k", "x")
	}
	if h.calls.Load() != 2 || f.rows(t) != 2 {
		t.Fatalf("calls %d rows %d", h.calls.Load(), f.rows(t))
	}
}

// TestConcurrentSameKey: many simultaneous requests with one key run the
// handler once; every other request is a replay or a 409, never a second
// execution, and afterwards the key replays.
//
// Covers: SEC-09
func TestConcurrentSameKey(t *testing.T) {
	t.Parallel()
	f := newFixture(t, idempotency.Config{})
	h := &counting{hook: func() { time.Sleep(50 * time.Millisecond) }}
	srv := f.store.Middleware(idempotency.PlatformScope)(h)
	const n = 24
	codes := make([]int, n)
	replayed := make([]bool, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rec := post(srv, "/a", "same", "body")
			codes[i], replayed[i] = rec.Code, rec.Header().Get(idempotency.HeaderReplayed) == "true"
		}()
	}
	close(start)
	wg.Wait()
	created := 0
	for i, c := range codes {
		switch {
		case c == http.StatusCreated && !replayed[i]:
			created++
		case c == http.StatusCreated && replayed[i], c == http.StatusConflict:
		default:
			t.Errorf("request %d: status %d", i, c)
		}
	}
	if created != 1 || h.calls.Load() != 1 {
		t.Fatalf("%d requests ran the handler (%d calls), want exactly 1", created, h.calls.Load())
	}
	if rec := post(srv, "/a", "same", "body"); rec.Header().Get(idempotency.HeaderReplayed) != "true" {
		t.Fatalf("after: %d %v", rec.Code, rec.Header())
	}
}

// TestBodyTooLarge: a body over MaxRequestBytes is a 413, runs nothing and
// stores nothing.
func TestBodyTooLarge(t *testing.T) {
	t.Parallel()
	f := newFixture(t, idempotency.Config{MaxRequestBytes: 8})
	h := &counting{}
	srv := f.store.Middleware(idempotency.PlatformScope)(h)
	rec := post(srv, "/a", "k", strings.Repeat("x", 100))
	if rec.Code != http.StatusRequestEntityTooLarge || h.calls.Load() != 0 || f.rows(t) != 0 {
		t.Fatalf("%d calls %d rows %d", rec.Code, h.calls.Load(), f.rows(t))
	}
}
