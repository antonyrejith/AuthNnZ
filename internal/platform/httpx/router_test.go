package httpx_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/antonyrejith/portun/internal/platform/httpx"
)

func TestRouter(t *testing.T) {
	t.Parallel()
	rt := httpx.NewRouter()
	rt.HandleFunc("GET /v1/users/{user_id}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.PathValue("user_id")))
	})
	rt.HandleFunc("GET /v1/missing", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteProblem(w, r, errors.New("boom"))
	})
	logger, _ := newLogger()
	tests := []struct {
		name      string
		method    string
		path      string
		wantCode  int
		wantRoute string
		wantAllow string
		wantType  string
	}{
		{"match", http.MethodGet, "/v1/users/u1", 200, "/v1/users/{user_id}", "", "text/plain; charset=utf-8"},
		{"not found", http.MethodGet, "/nope", 404, "", "", httpx.ContentTypeProblem},
		{"method not allowed", http.MethodDelete, "/v1/users/u1", 405, "", "GET, HEAD", httpx.ContentTypeProblem},
		{"handler problem passes through", http.MethodGet, "/v1/missing", 500, "/v1/missing", "", httpx.ContentTypeProblem},
		{"path cleaning redirect", http.MethodGet, "/v1/../v1/users/u1", 307, "/v1/users/{user_id}", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var route string
			h := httpx.Chain(rt, httpx.Recover(logger), func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					next.ServeHTTP(w, r)
					route = httpx.Route(r)
				})
			}, httpx.RequestID(httpx.RequestIDConfig{}))
			rec := serve(h, httptest.NewRequestWithContext(context.Background(), tt.method, tt.path, nil))
			if rec.Code != tt.wantCode || route != tt.wantRoute {
				t.Fatalf("status %d route %q", rec.Code, route)
			}
			if tt.wantType != "" && rec.Header().Get("Content-Type") != tt.wantType {
				t.Fatalf("content type %q", rec.Header().Get("Content-Type"))
			}
			if rec.Header().Get("Allow") != tt.wantAllow {
				t.Fatalf("allow %q", rec.Header().Get("Allow"))
			}
			if rec.Header().Get("Content-Type") == httpx.ContentTypeProblem {
				p := decodeProblem(t, rec.Body)
				if p.Status != tt.wantCode || p.RequestID == "" {
					t.Fatalf("problem %+v", p)
				}
			}
		})
	}
}

func healthBody(t *testing.T, rec *httptest.ResponseRecorder) httpx.HealthReport {
	t.Helper()
	var rep httpx.HealthReport
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return rep
}

func TestHealth(t *testing.T) {
	t.Parallel()
	logger, logs := newLogger()
	hl := httpx.NewHealth(logger)
	var dbUp atomic.Bool
	hl.AddReadinessCheck("db", time.Second, func(context.Context) error {
		if dbUp.Load() {
			return nil
		}
		return errors.New("dial tcp 10.0.0.5:5432: connection refused")
	})
	hl.AddReadinessCheck("cache", 0, func(context.Context) error { return nil })
	var startupCalls atomic.Int32
	hl.AddStartupCheck("migrations", 0, func(context.Context) error {
		startupCalls.Add(1)
		return nil
	})
	rt := httpx.NewRouter()
	hl.Register(rt)
	get := func(path string) *httptest.ResponseRecorder {
		return serve(rt, httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil))
	}

	if rec := get("/healthz/live"); rec.Code != 200 || healthBody(t, rec).Status != "ok" {
		t.Fatalf("live: %d %s", rec.Code, rec.Body)
	}
	rec := get("/healthz/ready")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready with failing check: %d", rec.Code)
	}
	rep := healthBody(t, rec)
	want := []httpx.CheckStatus{{Name: "cache", Status: "ok"}, {Name: "db", Status: "failed"}}
	if rep.Status != "unavailable" || len(rep.Checks) != 2 || rep.Checks[0] != want[0] || rep.Checks[1] != want[1] {
		t.Fatalf("ready body: %+v", rep)
	}
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Fatalf("error text leaked: %s", rec.Body)
	}
	if logs.find(t, "health check failed")["error"] == nil {
		t.Fatal("failure not logged")
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("health must not be cached")
	}
	dbUp.Store(true)
	if rec := get("/healthz/ready"); rec.Code != 200 {
		t.Fatalf("ready: %d", rec.Code)
	}
	for range 3 {
		if rec := get("/healthz/startup"); rec.Code != 200 {
			t.Fatalf("startup: %d", rec.Code)
		}
	}
	if startupCalls.Load() != 1 {
		t.Fatalf("startup checks ran %d times after passing", startupCalls.Load())
	}
}

// TestHealthCheckTimeout checks that per-check timeouts are honored, also
// for checks that ignore their context.
func TestHealthCheckTimeout(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		logger, _ := newLogger()
		hl := httpx.NewHealth(logger)
		release := make(chan struct{})
		hl.AddReadinessCheck("polite", 100*time.Millisecond, func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		})
		hl.AddReadinessCheck("stubborn", 200*time.Millisecond, func(context.Context) error {
			<-release
			return nil
		})
		start := time.Now()
		rec := serve(hl.Ready(), httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil))
		if elapsed := time.Since(start); elapsed != 200*time.Millisecond {
			t.Fatalf("elapsed %v, want 200ms", elapsed)
		}
		rep := healthBody(t, rec)
		if rec.Code != 503 || rep.Checks[0].Status != "timeout" || rep.Checks[1].Status != "timeout" {
			t.Fatalf("report %+v", rep)
		}
		close(release)
		synctest.Wait()
	})
}
