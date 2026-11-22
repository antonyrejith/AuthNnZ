package httpx

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

// FrameOptions controls who may frame responses (clickjacking protection).
type FrameOptions int

// Framing policies.
const (
	// FrameDeny forbids framing: X-Frame-Options: DENY and CSP
	// frame-ancestors 'none'. It is the default.
	FrameDeny FrameOptions = iota
	// FrameSameOrigin allows framing by the same origin only.
	FrameSameOrigin
	// FrameAllow omits X-Frame-Options; the ContentSecurityPolicy must then
	// state frame-ancestors explicitly.
	FrameAllow
)

// Security header defaults.
const (
	DefaultHSTSMaxAge   = 2 * 365 * 24 * time.Hour
	DefaultCacheControl = "no-store"
	cspDeny             = "default-src 'none'; frame-ancestors 'none'"
	cspSameOrigin       = "default-src 'none'; frame-ancestors 'self'"
	minPreloadMaxAge    = 365 * 24 * time.Hour
)

// SecurityHeadersConfig configures SecurityHeaders. The zero value is the
// secure default for API responses.
type SecurityHeadersConfig struct {
	// DisableHSTS turns off Strict-Transport-Security. HSTS is otherwise sent
	// on HTTPS requests, including HTTPS forwarded by a trusted proxy.
	DisableHSTS bool
	// HSTSMaxAge defaults to two years.
	HSTSMaxAge time.Duration
	// HSTSIncludeSubdomains is off by default: white-label custom domains
	// belong to customers, whose subdomains we must not constrain.
	HSTSIncludeSubdomains bool
	// HSTSPreload requires HSTSIncludeSubdomains and a max age of a year.
	HSTSPreload bool
	// ContentSecurityPolicy defaults to "default-src 'none';
	// frame-ancestors 'none'" (or 'self' with FrameSameOrigin).
	ContentSecurityPolicy string
	// FrameOptions defaults to FrameDeny.
	FrameOptions FrameOptions
	// CacheControl defaults to "no-store"; handlers may override it for
	// cacheable responses (JWKS, discovery).
	CacheControl string
}

// SecurityHeaders sets secure response headers before the handler runs:
// Strict-Transport-Security (HTTPS only), X-Content-Type-Options: nosniff,
// Referrer-Policy: no-referrer, Content-Security-Policy, X-Frame-Options,
// Cross-Origin-Resource-Policy: same-origin and Cache-Control: no-store.
// Invalid combinations are reported at construction.
func SecurityHeaders(cfg SecurityHeadersConfig) (Middleware, error) {
	cfg, frame, err := cfg.normalize()
	if err != nil {
		return nil, err
	}
	hsts := cfg.hsts()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			if hsts != "" && schemeOf(r) == "https" {
				h.Set("Strict-Transport-Security", hsts)
			}
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("Content-Security-Policy", cfg.ContentSecurityPolicy)
			if frame != "" {
				h.Set("X-Frame-Options", frame)
			}
			h.Set("Cross-Origin-Resource-Policy", "same-origin")
			h.Set("Cache-Control", cfg.CacheControl)
			next.ServeHTTP(w, r)
		})
	}, nil
}

// normalize applies defaults, validates, and returns the X-Frame-Options
// value ("" for none).
func (cfg SecurityHeadersConfig) normalize() (SecurityHeadersConfig, string, error) {
	if cfg.HSTSMaxAge < 0 {
		return cfg, "", errs.New(errs.CodeInvalidRequest, "HSTS max age must not be negative")
	}
	if cfg.HSTSMaxAge == 0 {
		cfg.HSTSMaxAge = DefaultHSTSMaxAge
	}
	if cfg.HSTSPreload && (!cfg.HSTSIncludeSubdomains || cfg.HSTSMaxAge < minPreloadMaxAge) {
		return cfg, "", errs.New(errs.CodeInvalidRequest, "HSTS preload requires includeSubDomains and a max age of at least one year")
	}
	frame := ""
	switch cfg.FrameOptions {
	case FrameDeny:
		frame = "DENY"
		if cfg.ContentSecurityPolicy == "" {
			cfg.ContentSecurityPolicy = cspDeny
		}
	case FrameSameOrigin:
		frame = "SAMEORIGIN"
		if cfg.ContentSecurityPolicy == "" {
			cfg.ContentSecurityPolicy = cspSameOrigin
		}
	case FrameAllow:
		if !strings.Contains(cfg.ContentSecurityPolicy, "frame-ancestors") {
			return cfg, "", errs.New(errs.CodeInvalidRequest, "FrameAllow needs a Content-Security-Policy with frame-ancestors")
		}
	default:
		return cfg, "", errs.New(errs.CodeInvalidRequest, "unknown frame option")
	}
	if cfg.CacheControl == "" {
		cfg.CacheControl = DefaultCacheControl
	}
	if !validHeaderValue(cfg.ContentSecurityPolicy) || !validHeaderValue(cfg.CacheControl) {
		return cfg, "", errs.New(errs.CodeInvalidRequest, "security header values must not contain control characters")
	}
	return cfg, frame, nil
}

func (cfg SecurityHeadersConfig) hsts() string {
	if cfg.DisableHSTS {
		return ""
	}
	hsts := "max-age=" + strconv.FormatInt(int64(cfg.HSTSMaxAge/time.Second), 10)
	if cfg.HSTSIncludeSubdomains {
		hsts += "; includeSubDomains"
	}
	if cfg.HSTSPreload {
		hsts += "; preload"
	}
	return hsts
}

func validHeaderValue(v string) bool {
	for i := range len(v) {
		if c := v[i]; c < 0x20 && c != '\t' || c == 0x7f {
			return false
		}
	}
	return true
}
