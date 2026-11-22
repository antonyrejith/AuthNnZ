package httpx

import (
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

// DefaultCORSMaxAge is how long browsers may cache a preflight result.
const DefaultCORSMaxAge = 10 * time.Minute

// CORSConfig configures CORS for one route group.
type CORSConfig struct {
	// AllowedOrigins are exact origins ("https://app.example.com"), compared
	// after normalization (lowercase scheme and host, default port removed).
	// "*" allows any origin and must be the only entry; it cannot be combined
	// with AllowCredentials. "null" is never allowed.
	AllowedOrigins []string
	// AllowedMethods defaults to GET, HEAD and POST.
	AllowedMethods []string
	// AllowedHeaders lists request headers a browser may send (compared
	// case-insensitively). It defaults to Content-Type.
	AllowedHeaders []string
	// ExposedHeaders lists response headers scripts may read.
	ExposedHeaders []string
	// AllowCredentials lets browsers send cookies and HTTP authentication.
	AllowCredentials bool
	// MaxAge defaults to DefaultCORSMaxAge.
	MaxAge time.Duration
}

type cors struct {
	any         bool
	origins     map[string]struct{}
	methods     []string
	headers     map[string]struct{}
	allowMeth   string
	allowHead   string
	expose      string
	credentials bool
	maxAge      string
}

// CORS returns middleware that implements CORS with an exact-origin
// allowlist. Only the canonical form of an allowed origin is accepted and
// echoed (lowercase, no default port, no path). Responses always carry Vary: Origin. A request from an origin
// that is not allowed gets no CORS headers (the browser then blocks it); a
// preflight from such an origin, or asking for a method or header that is
// not allowed, gets a 403 permission_denied problem. Allowed preflights get
// 204 and do not reach the handler.
func CORS(cfg CORSConfig) (Middleware, error) {
	c, err := newCORS(cfg)
	if err != nil {
		return nil, err
	}
	return c.wrap, nil
}

func newCORS(cfg CORSConfig) (*cors, error) {
	c := &cors{origins: map[string]struct{}{}, headers: map[string]struct{}{}, credentials: cfg.AllowCredentials}
	if len(cfg.AllowedOrigins) == 0 {
		return nil, errs.New(errs.CodeInvalidRequest, "CORS needs at least one allowed origin")
	}
	for _, o := range cfg.AllowedOrigins {
		if o == "*" {
			c.any = true
			continue
		}
		n, ok := normalizeOrigin(o)
		if !ok {
			return nil, errs.New(errs.CodeInvalidRequest, "invalid CORS origin "+strconv.Quote(o))
		}
		c.origins[n] = struct{}{}
	}
	if c.any && (len(c.origins) > 0 || cfg.AllowCredentials) {
		return nil, errs.New(errs.CodeInvalidRequest, `CORS origin "*" must be the only origin and cannot allow credentials`)
	}
	c.methods = cfg.AllowedMethods
	if len(c.methods) == 0 {
		c.methods = []string{http.MethodGet, http.MethodHead, http.MethodPost}
	}
	headers := cfg.AllowedHeaders
	if len(headers) == 0 {
		headers = []string{"Content-Type"}
	}
	for _, h := range headers {
		c.headers[strings.ToLower(strings.TrimSpace(h))] = struct{}{}
	}
	maxAge := cfg.MaxAge
	if maxAge <= 0 {
		maxAge = DefaultCORSMaxAge
	}
	c.allowMeth = strings.Join(c.methods, ", ")
	c.allowHead = strings.Join(headers, ", ")
	c.expose = strings.Join(cfg.ExposedHeaders, ", ")
	c.maxAge = strconv.FormatInt(int64(maxAge/time.Second), 10)
	return c, nil
}

// normalizeOrigin returns scheme://host[:port] in lowercase without the
// scheme's default port, or false if s is not an http(s) origin.
func normalizeOrigin(s string) (string, bool) {
	u, err := url.Parse(s)
	if err != nil || u.User != nil || u.Opaque != "" || (u.Path != "" && u.Path != "/") ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Host == "" {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if host == "" || (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if host == "" {
		return "", false
	}
	if port == "" {
		if strings.Contains(host, ":") {
			return scheme + "://[" + host + "]", true
		}
		return scheme + "://" + host, true
	}
	return scheme + "://" + net.JoinHostPort(host, port), true
}

func (c *cors) allowed(origin string) (string, bool) {
	if origin == "" || origin == "null" {
		return "", false
	}
	if c.any {
		return "*", true
	}
	// Browsers send origins in canonical form. A non-canonical form (upper
	// case, default port, trailing slash) is not a browser's and is refused,
	// so the echoed value is always exactly the canonical allowed origin.
	n, ok := normalizeOrigin(origin)
	if !ok || n != origin {
		return "", false
	}
	if _, ok := c.origins[n]; !ok {
		return "", false
	}
	return n, true
}

func (c *cors) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Add("Vary", "Origin")
		origin := r.Header.Get("Origin")
		preflight := r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
		if preflight {
			h.Add("Vary", "Access-Control-Request-Method")
			h.Add("Vary", "Access-Control-Request-Headers")
			c.preflight(w, r, origin)
			return
		}
		if origin != "" {
			if allow, ok := c.allowed(origin); ok {
				h.Set("Access-Control-Allow-Origin", allow)
				if c.credentials {
					h.Set("Access-Control-Allow-Credentials", "true")
				}
				if c.expose != "" {
					h.Set("Access-Control-Expose-Headers", c.expose)
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (c *cors) preflight(w http.ResponseWriter, r *http.Request, origin string) {
	allow, ok := c.allowed(origin)
	if !ok {
		WriteProblem(w, r, errs.New(errs.CodePermissionDenied, "Origin is not allowed."))
		return
	}
	if !slices.Contains(c.methods, r.Header.Get("Access-Control-Request-Method")) {
		WriteProblem(w, r, errs.New(errs.CodePermissionDenied, "Method is not allowed for cross-origin requests."))
		return
	}
	for _, list := range r.Header.Values("Access-Control-Request-Headers") {
		for name := range strings.SplitSeq(list, ",") {
			name = strings.ToLower(strings.TrimSpace(name))
			if _, ok := c.headers[name]; name != "" && !ok {
				WriteProblem(w, r, errs.New(errs.CodePermissionDenied, "Header is not allowed for cross-origin requests."))
				return
			}
		}
	}
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", allow)
	h.Set("Access-Control-Allow-Methods", c.allowMeth)
	h.Set("Access-Control-Allow-Headers", c.allowHead)
	h.Set("Access-Control-Max-Age", c.maxAge)
	if c.credentials {
		h.Set("Access-Control-Allow-Credentials", "true")
	}
	w.WriteHeader(http.StatusNoContent)
}
