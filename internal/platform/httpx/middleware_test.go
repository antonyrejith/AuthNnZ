package httpx_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/antonyrejith/portun/internal/platform/httpx"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/scope"
)

// TestRecover checks that a panic becomes a 500 problem without the panic
// value, is logged with its stack, and that the server keeps serving.
//
// Covers: SEC-09
func TestRecover(t *testing.T) {
	t.Parallel()
	logger, logs := newLogger()
	rt := httpx.NewRouter()
	rt.HandleFunc("GET /panic", func(http.ResponseWriter, *http.Request) { panic("secret-panic-value") })
	rt.HandleFunc("GET /ok", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	srv := httptest.NewServer(httpx.Chain(rt, httpx.Recover(logger), httpx.RequestID(httpx.RequestIDConfig{})))
	t.Cleanup(srv.Close)

	for i := range 2 {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/panic", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		if strings.Contains(string(body), "secret-panic-value") {
			t.Fatalf("panic value leaked: %s", body)
		}
		p := decodeProblem(t, strings.NewReader(string(body)))
		if p.Code != "internal" || p.RequestID == "" || p.RequestID != resp.Header.Get(httpx.HeaderRequestID) {
			t.Fatalf("problem = %+v", p)
		}
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/ok", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("server stopped serving: %d", resp.StatusCode)
	}
	rec := logs.find(t, "panic serving request")
	if rec["level"] != "ERROR" || !strings.Contains(rec["stack"].(string), "goroutine") || rec["request_id"] == nil {
		t.Fatalf("panic log = %v", rec)
	}
}

// TestRequestID checks new IDs per request, the response header, that the
// inbound header is recorded but not trusted, and the request scope.
//
// Covers: SEC-09
func TestRequestID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		inbound     string
		wantInbound string
	}{
		{"no inbound", "", ""},
		{"safe inbound", "abc-123_x.y:z", "abc-123_x.y:z"},
		{"unsafe inbound", "evil\"value<script>", ""},
		{"too long inbound", strings.Repeat("a", 129), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			logger, logs := newLogger()
			var got scope.Request
			h := httpx.Chain(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				got, _ = scope.RequestFrom(r.Context())
			}), httpx.RequestID(httpx.RequestIDConfig{}), httpx.AccessLog(logger))
			seen := map[string]bool{}
			for range 3 {
				r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/x", nil)
				r.RemoteAddr = "198.51.100.7:4321"
				r.Header.Set("User-Agent", "test-agent/1.0")
				if tt.inbound != "" {
					r.Header.Set(httpx.HeaderRequestID, tt.inbound)
				}
				rec := serve(h, r)
				id := rec.Header().Get(httpx.HeaderRequestID)
				if _, err := ids.Parse[ids.RequestKind](id); err != nil {
					t.Fatalf("response request id %q: %v", id, err)
				}
				if seen[id] || id == tt.inbound {
					t.Fatalf("request id %q reused or adopted", id)
				}
				seen[id] = true
				want := scope.Request{RequestID: id, Channel: "api", IP: "198.51.100.7", UserAgent: "test-agent/1.0"}
				if got != want {
					t.Fatalf("scope = %+v, want %+v", got, want)
				}
			}
			rec := logs.find(t, "http request")
			if tt.wantInbound == "" {
				if _, ok := rec["inbound_request_id"]; ok {
					t.Fatalf("unsafe inbound id recorded: %v", rec)
				}
			} else if rec["inbound_request_id"] != tt.wantInbound {
				t.Fatalf("inbound_request_id = %v", rec["inbound_request_id"])
			}
		})
	}
}

// TestClientIP checks that X-Forwarded-For is honored only from trusted
// proxies, read from the right.
//
// Covers: SEC-09
func TestClientIP(t *testing.T) {
	t.Parallel()
	proxies, err := httpx.ParsePrefixes([]string{"10.0.0.0/8", "192.0.2.1"})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		remote string
		xff    []string
		want   string
	}{
		{"direct client", "203.0.113.9:1000", nil, "203.0.113.9"},
		{"spoofed xff from untrusted peer", "203.0.113.9:1000", []string{"1.2.3.4"}, "203.0.113.9"},
		{"trusted proxy", "10.1.2.3:1000", []string{"198.51.100.20"}, "198.51.100.20"},
		{"client prepends fake", "10.1.2.3:1000", []string{"1.2.3.4, 198.51.100.20"}, "198.51.100.20"},
		{"proxy chain", "10.1.2.3:1000", []string{"198.51.100.20, 192.0.2.1", "10.9.9.9"}, "198.51.100.20"},
		{"all trusted", "10.1.2.3:1000", []string{"10.2.2.2"}, "10.2.2.2"},
		{"garbage stops walk", "10.1.2.3:1000", []string{"198.51.100.20, garbage, 10.4.4.4"}, "10.4.4.4"},
		{"ipv6 mapped peer", "[::ffff:10.1.2.3]:1000", []string{"2001:db8::1"}, "2001:db8::1"},
		{"trusted proxy without xff", "10.1.2.3:1000", nil, "10.1.2.3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got string
			h := httpx.RequestID(httpx.RequestIDConfig{TrustedProxies: proxies})(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				req, _ := scope.RequestFrom(r.Context())
				got = req.IP
			}))
			r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
			r.RemoteAddr = tt.remote
			for _, v := range tt.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			serve(h, r)
			if got != tt.want {
				t.Fatalf("client ip = %q, want %q", got, tt.want)
			}
		})
	}
	if _, err := httpx.ParsePrefixes([]string{"not-a-network"}); err == nil {
		t.Fatal("invalid prefix accepted")
	}
	if p, _ := httpx.ParsePrefixes([]string{"10.1.2.3/8"}); p[0] != netip.MustParsePrefix("10.0.0.0/8") {
		t.Fatalf("prefix not masked: %v", p)
	}
	if p, _ := httpx.ParsePrefixes([]string{"::ffff:10.0.0.0/104"}); p[0] != netip.MustParsePrefix("10.0.0.0/8") {
		t.Fatalf("mapped prefix not unmapped: %v", p)
	}
}

// TestClientIPMappedTrustedPrefix proves that an IPv4-mapped trusted prefix
// (::ffff:10.0.0.0/104), from configuration or set in code, trusts the IPv4
// proxies it denotes, whether the peer address arrives mapped or not.
//
// Covers: SEC-09
func TestClientIPMappedTrustedPrefix(t *testing.T) {
	t.Parallel()
	parsed, err := httpx.ParsePrefixes([]string{"::ffff:10.0.0.0/104"})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		proxies []netip.Prefix
		remote  string
	}{
		{"parsed, ipv4 peer", parsed, "10.1.2.3:1000"},
		{"parsed, mapped peer", parsed, "[::ffff:10.1.2.3]:1000"},
		{"in code, ipv4 peer", []netip.Prefix{netip.MustParsePrefix("::ffff:10.0.0.0/104")}, "10.1.2.3:1000"},
		{"in code, mapped peer", []netip.Prefix{netip.MustParsePrefix("::ffff:10.0.0.0/104")}, "[::ffff:10.1.2.3]:1000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got string
			h := httpx.RequestID(httpx.RequestIDConfig{TrustedProxies: tt.proxies})(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				req, _ := scope.RequestFrom(r.Context())
				got = req.IP
			}))
			r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
			r.RemoteAddr = tt.remote
			r.Header.Set("X-Forwarded-For", "198.51.100.20, 10.9.9.9")
			serve(h, r)
			if got != "198.51.100.20" {
				t.Fatalf("client ip = %q, want the forwarded client 198.51.100.20", got)
			}
		})
	}
}

// TestAccessLog checks the record keys and that query strings and
// credentials never reach the log.
//
// Covers: SEC-09
func TestAccessLog(t *testing.T) {
	t.Parallel()
	logger, logs := newLogger()
	rt := httpx.NewRouter()
	rt.HandleFunc("POST /v1/things/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("hello"))
	})
	h := httpx.Chain(rt, httpx.Recover(logger), httpx.RequestID(httpx.RequestIDConfig{}), httpx.AccessLog(logger))
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/things/42?access_token=SECRET-QUERY", nil)
	r.Header.Set("Authorization", "Bearer SECRET-BEARER")
	r.Header.Set("Cookie", "session=SECRET-COOKIE")
	rec := serve(h, r)

	out := logs.String()
	for _, s := range []string{"SECRET-QUERY", "SECRET-BEARER", "SECRET-COOKIE", "access_token"} {
		if strings.Contains(out, s) {
			t.Fatalf("log contains %q: %s", s, out)
		}
	}
	entry := logs.find(t, "http request")
	want := map[string]any{
		"method": "POST", "route": "/v1/things/{id}", "path": "/v1/things/42",
		"status": float64(201), "bytes": float64(5), "request_id": rec.Header().Get(httpx.HeaderRequestID),
		"level": "INFO", "client_ip": "192.0.2.1",
	}
	for k, v := range want {
		if entry[k] != v {
			t.Fatalf("%s = %v, want %v (record %v)", k, entry[k], v, entry)
		}
	}
	if _, ok := entry["duration_ms"].(float64); !ok {
		t.Fatalf("duration_ms missing: %v", entry)
	}
}

// TestSecurityHeaders checks the secure defaults, HSTS only over HTTPS
// (direct or from a trusted proxy), and configuration validation.
//
// Covers: SEC-09
func TestSecurityHeaders(t *testing.T) {
	t.Parallel()
	proxies, _ := httpx.ParsePrefixes([]string{"10.0.0.0/8"})
	tests := []struct {
		name      string
		cfg       httpx.SecurityHeadersConfig
		remote    string
		proto     string
		wantHSTS  string
		wantFrame string
		wantCSP   string
	}{
		{"plain http", httpx.SecurityHeadersConfig{}, "203.0.113.1:1", "", "", "DENY", "default-src 'none'; frame-ancestors 'none'"},
		{"forwarded https from trusted proxy", httpx.SecurityHeadersConfig{}, "10.0.0.1:1", "https", "max-age=63072000", "DENY", "default-src 'none'; frame-ancestors 'none'"},
		{"forwarded https from untrusted peer", httpx.SecurityHeadersConfig{}, "203.0.113.1:1", "https", "", "DENY", "default-src 'none'; frame-ancestors 'none'"},
		{"hsts disabled", httpx.SecurityHeadersConfig{DisableHSTS: true}, "10.0.0.1:1", "https", "", "DENY", "default-src 'none'; frame-ancestors 'none'"},
		{
			"preload",
			httpx.SecurityHeadersConfig{HSTSIncludeSubdomains: true, HSTSPreload: true},
			"10.0.0.1:1", "https", "max-age=63072000; includeSubDomains; preload", "DENY", "default-src 'none'; frame-ancestors 'none'",
		},
		{"same origin framing", httpx.SecurityHeadersConfig{FrameOptions: httpx.FrameSameOrigin}, "203.0.113.1:1", "", "", "SAMEORIGIN", "default-src 'none'; frame-ancestors 'self'"},
		{
			"framing allowed with csp",
			httpx.SecurityHeadersConfig{FrameOptions: httpx.FrameAllow, ContentSecurityPolicy: "frame-ancestors https://a.example"},
			"203.0.113.1:1", "", "", "", "frame-ancestors https://a.example",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sh, err := httpx.SecurityHeaders(tt.cfg)
			if err != nil {
				t.Fatal(err)
			}
			h := httpx.Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
				httpx.RequestID(httpx.RequestIDConfig{TrustedProxies: proxies}), sh)
			r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
			r.RemoteAddr = tt.remote
			if tt.proto != "" {
				r.Header.Set("X-Forwarded-Proto", tt.proto)
			}
			got := serve(h, r).Header()
			want := map[string]string{
				"Strict-Transport-Security":    tt.wantHSTS,
				"X-Content-Type-Options":       "nosniff",
				"Referrer-Policy":              "no-referrer",
				"Content-Security-Policy":      tt.wantCSP,
				"X-Frame-Options":              tt.wantFrame,
				"Cross-Origin-Resource-Policy": "same-origin",
				"Cache-Control":                "no-store",
			}
			for k, v := range want {
				if got.Get(k) != v {
					t.Fatalf("%s = %q, want %q", k, got.Get(k), v)
				}
			}
		})
	}
	invalid := []httpx.SecurityHeadersConfig{
		{HSTSPreload: true},
		{HSTSMaxAge: -1},
		{FrameOptions: httpx.FrameAllow},
		{ContentSecurityPolicy: "default-src 'none'\r\nX-Evil: 1"},
		{FrameOptions: 99},
	}
	for i, cfg := range invalid {
		if _, err := httpx.SecurityHeaders(cfg); err == nil {
			t.Fatalf("invalid config %d accepted", i)
		}
	}
}

// TestMaxBytes checks the 413 response for declared and streamed bodies.
//
// Covers: SEC-09
func TestMaxBytes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		limit      int64
		body       string
		hideLength bool
		wantStatus int
	}{
		{"within limit", 10, "0123456789", false, http.StatusOK},
		{"declared too large", 10, "0123456789A", false, http.StatusRequestEntityTooLarge},
		{"streamed too large", 10, "0123456789A", true, http.StatusRequestEntityTooLarge},
		{"default limit", 0, strings.Repeat("a", 1<<20+1), false, http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := httpx.MaxBytes(tt.limit)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, err := io.ReadAll(r.Body); err != nil {
					httpx.WriteProblem(w, r, err)
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", strings.NewReader(tt.body))
			if tt.hideLength {
				r.ContentLength = -1
			}
			rec := serve(h, r)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantStatus == http.StatusRequestEntityTooLarge {
				if p := decodeProblem(t, rec.Body); p.Code != "payload_too_large" {
					t.Fatalf("problem = %+v", p)
				}
			}
		})
	}
}

func TestChainOrder(t *testing.T) {
	t.Parallel()
	var order []string
	mw := func(name string) httpx.Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	h := httpx.Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { order = append(order, "handler") }), mw("a"), mw("b"))
	serve(h, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil))
	if strings.Join(order, ",") != "a,b,handler" {
		t.Fatalf("order = %v", order)
	}
}
