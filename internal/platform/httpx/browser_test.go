package httpx_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/antonyrejith/portun/internal/platform/httpx"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

// TestCORS checks the exact-origin allowlist, preflights and Vary.
//
// Covers: SEC-09
func TestCORS(t *testing.T) {
	t.Parallel()
	mw, err := httpx.CORS(httpx.CORSConfig{
		AllowedOrigins:   []string{"https://App.Example.com:443", "http://localhost:5173"},
		AllowedMethods:   []string{http.MethodGet, http.MethodPost, http.MethodPatch},
		AllowedHeaders:   []string{"Content-Type", "Authorization"},
		ExposedHeaders:   []string{httpx.HeaderRequestID},
		AllowCredentials: true,
		MaxAge:           time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := mw(okHandler)
	tests := []struct {
		name       string
		method     string
		origin     string
		reqMethod  string
		reqHeaders string
		wantStatus int
		wantACAO   string
	}{
		{"allowed origin", http.MethodGet, "https://app.example.com", "", "", 200, "https://app.example.com"},
		{"allowed with port", http.MethodPost, "http://localhost:5173", "", "", 200, "http://localhost:5173"},
		{"no origin", http.MethodGet, "", "", "", 200, ""},
		{"disallowed origin", http.MethodGet, "https://evil.example", "", "", 200, ""},
		{"suffix attack", http.MethodGet, "https://app.example.com.evil.example", "", "", 200, ""},
		{"scheme mismatch", http.MethodGet, "http://app.example.com", "", "", 200, ""},
		{"null origin", http.MethodGet, "null", "", "", 200, ""},
		{"preflight allowed", http.MethodOptions, "https://app.example.com", "PATCH", "content-type, authorization", 204, "https://app.example.com"},
		{"preflight disallowed origin", http.MethodOptions, "https://evil.example", "GET", "", 403, ""},
		{"preflight null origin", http.MethodOptions, "null", "GET", "", 403, ""},
		{"preflight bad method", http.MethodOptions, "https://app.example.com", "DELETE", "", 403, ""},
		{"preflight bad header", http.MethodOptions, "https://app.example.com", "GET", "x-evil", 403, ""},
		{"plain options", http.MethodOptions, "https://app.example.com", "", "", 200, "https://app.example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequestWithContext(context.Background(), tt.method, "/", nil)
			if tt.origin != "" {
				r.Header.Set("Origin", tt.origin)
			}
			if tt.reqMethod != "" {
				r.Header.Set("Access-Control-Request-Method", tt.reqMethod)
			}
			if tt.reqHeaders != "" {
				r.Header.Set("Access-Control-Request-Headers", tt.reqHeaders)
			}
			rec := serve(h, r)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			got := rec.Header()
			if got.Get("Access-Control-Allow-Origin") != tt.wantACAO {
				t.Fatalf("ACAO = %q, want %q", got.Get("Access-Control-Allow-Origin"), tt.wantACAO)
			}
			if !strings.Contains(strings.Join(got.Values("Vary"), ","), "Origin") {
				t.Fatal("missing Vary: Origin")
			}
			if tt.wantACAO == "" {
				for k := range got {
					if strings.HasPrefix(k, "Access-Control-") {
						t.Fatalf("CORS header %s on a disallowed request", k)
					}
				}
				return
			}
			if got.Get("Access-Control-Allow-Credentials") != "true" {
				t.Fatal("missing credentials header")
			}
			if tt.wantStatus == 204 && (got.Get("Access-Control-Max-Age") != "3600" || got.Get("Access-Control-Allow-Methods") != "GET, POST, PATCH") {
				t.Fatalf("preflight headers %v", got)
			}
		})
	}
}

// TestCORSConfig checks construction errors, including a credentialed
// wildcard, and the non-credentialed wildcard.
//
// Covers: SEC-09
func TestCORSConfig(t *testing.T) {
	t.Parallel()
	invalid := map[string]httpx.CORSConfig{
		"credentialed wildcard": {AllowedOrigins: []string{"*"}, AllowCredentials: true},
		"wildcard with others":  {AllowedOrigins: []string{"*", "https://a.example"}},
		"empty":                 {},
		"null":                  {AllowedOrigins: []string{"null"}},
		"path":                  {AllowedOrigins: []string{"https://a.example/path"}},
		"scheme":                {AllowedOrigins: []string{"ftp://a.example"}},
		"userinfo":              {AllowedOrigins: []string{"https://u@a.example"}},
	}
	for name, cfg := range invalid {
		if _, err := httpx.CORS(cfg); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	mw, err := httpx.CORS(httpx.CORSConfig{AllowedOrigins: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	r.Header.Set("Origin", "https://anyone.example")
	rec := serve(mw(okHandler), r)
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" || rec.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("wildcard headers %v", rec.Header())
	}
}

// TestCSRF checks cross-origin protection for cookie-authenticated
// endpoints.
//
// Covers: SEC-09
func TestCSRF(t *testing.T) {
	t.Parallel()
	mw, err := httpx.CSRF(httpx.CSRFConfig{TrustedOrigins: []string{"https://Console.Example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	h := mw(okHandler)
	tests := []struct {
		name     string
		method   string
		site     string
		origin   string
		wantCode int
	}{
		{"cross-site post", http.MethodPost, "cross-site", "https://evil.example", 403},
		{"cross-site delete via origin", http.MethodDelete, "", "https://evil.example", 403},
		{"same-origin post", http.MethodPost, "same-origin", "https://auth.example.com", 200},
		{"same origin by host", http.MethodPost, "", "https://auth.example.com", 200},
		{"cross-site get is safe", http.MethodGet, "cross-site", "https://evil.example", 200},
		{"cross-site head is safe", http.MethodHead, "cross-site", "https://evil.example", 200},
		{"trusted origin", http.MethodPost, "cross-site", "https://console.example.com", 200},
		{"non-browser client", http.MethodPost, "", "", 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequestWithContext(context.Background(), tt.method, "https://auth.example.com/session", nil)
			if tt.site != "" {
				r.Header.Set("Sec-Fetch-Site", tt.site)
			}
			if tt.origin != "" {
				r.Header.Set("Origin", tt.origin)
			}
			rec := serve(h, r)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			if tt.wantCode == 403 {
				if p := decodeProblem(t, rec.Body); p.Code != "permission_denied" {
					t.Fatalf("problem = %+v", p)
				}
			}
		})
	}
	if _, err := httpx.CSRF(httpx.CSRFConfig{TrustedOrigins: []string{"not an origin"}}); err == nil {
		t.Fatal("invalid trusted origin accepted")
	}
}

// TestSetCookie checks the hardened cookie rules.
//
// Covers: SEC-09
func TestSetCookie(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cookie  httpx.Cookie
		want    []string
		notWant []string
		wantErr bool
	}{
		{
			name:    "defaults",
			cookie:  httpx.Cookie{Name: "sid", Value: secret.NewString("abc")},
			want:    []string{"sid=abc", "Path=/", "Secure", "HttpOnly", "SameSite=Lax"},
			notWant: []string{"Max-Age", "Domain"},
		},
		{
			name:   "strict with max age",
			cookie: httpx.Cookie{Name: "sid", Value: secret.NewString("abc"), SameSite: http.SameSiteStrictMode, MaxAge: time.Hour},
			want:   []string{"SameSite=Strict", "Max-Age=3600", "Secure", "HttpOnly"},
		},
		{
			name:   "none is always secure",
			cookie: httpx.Cookie{Name: "sid", Value: secret.NewString("abc"), SameSite: http.SameSiteNoneMode},
			want:   []string{"SameSite=None", "Secure"},
		},
		{
			name:    "js readable csrf token",
			cookie:  httpx.Cookie{Name: "csrf", Value: secret.NewString("t"), JSReadable: true},
			want:    []string{"Secure", "SameSite=Lax"},
			notWant: []string{"HttpOnly"},
		},
		{
			name:   "delete",
			cookie: httpx.Cookie{Name: "sid", MaxAge: -1},
			want:   []string{"Max-Age=0"},
		},
		{
			name:   "host prefix",
			cookie: httpx.Cookie{Name: "__Host-sid", Value: secret.NewString("abc")},
			want:   []string{"__Host-sid=abc", "Path=/", "Secure"},
		},
		{name: "host prefix with domain", cookie: httpx.Cookie{Name: "__Host-sid", Domain: "example.com"}, wantErr: true},
		{name: "host prefix with path", cookie: httpx.Cookie{Name: "__host-sid", Path: "/app"}, wantErr: true},
		{name: "default mode", cookie: httpx.Cookie{Name: "sid", SameSite: http.SameSiteDefaultMode}, wantErr: true},
		{name: "bad name", cookie: httpx.Cookie{Name: "s id"}, wantErr: true},
		{name: "empty name", cookie: httpx.Cookie{}, wantErr: true},
		{name: "bad value", cookie: httpx.Cookie{Name: "sid", Value: secret.NewString("a;b")}, wantErr: true},
		{name: "too large", cookie: httpx.Cookie{Name: "sid", Value: secret.NewString(strings.Repeat("a", 4096))}, wantErr: true},
		{name: "unknown samesite", cookie: httpx.Cookie{Name: "sid", SameSite: 42}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			err := httpx.SetCookie(rec, tt.cookie)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			got := rec.Header().Get("Set-Cookie")
			if tt.wantErr {
				if got != "" {
					t.Fatalf("cookie set despite error: %q", got)
				}
				return
			}
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Fatalf("Set-Cookie %q lacks %q", got, w)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(got, w) {
					t.Fatalf("Set-Cookie %q has %q", got, w)
				}
			}
		})
	}
}
