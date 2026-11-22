package httpx

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"go.opentelemetry.io/otel/trace"

	"github.com/antonyrejith/portun/internal/platform/scope"
)

// Middleware wraps a handler.
type Middleware = func(http.Handler) http.Handler

// Chain wraps h with mws. The first middleware is the outermost: it sees the
// request first and the response last.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

type ctxKey int

const stateKey ctxKey = 0

// requestState is shared by this package's middleware for one request. The
// first middleware that needs it creates it; the others reuse it. A mutex
// guards it because a handler may pass the request to other goroutines.
type requestState struct {
	mu               sync.Mutex
	route            string
	scheme           string
	inboundRequestID string
	request          scope.Request
	hasRequest       bool
	spanContext      trace.SpanContext
	logger           *slog.Logger
	maxBodyBytes     int64
}

// withState returns r with a request state, creating one if needed.
func withState(r *http.Request) (*http.Request, *requestState) {
	if st := stateFrom(r.Context()); st != nil {
		return r, st
	}
	st := &requestState{}
	return r.WithContext(context.WithValue(r.Context(), stateKey, st)), st
}

func stateFrom(ctx context.Context) *requestState {
	st, _ := ctx.Value(stateKey).(*requestState)
	return st
}

func (st *requestState) setRoute(route string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.route = route
}

func (st *requestState) getRoute() string {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.route
}

func (st *requestState) setLoggerIfUnset(l *slog.Logger) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.logger == nil {
		st.logger = l
	}
}

func (st *requestState) setRequest(req scope.Request, scheme, inbound string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.request, st.hasRequest = req, true
	st.scheme = scheme
	st.inboundRequestID = inbound
}

func (st *requestState) setSpanContext(sc trace.SpanContext) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.spanContext = sc
}

func (st *requestState) setMaxBodyBytes(n int64) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.maxBodyBytes = n
}

func (st *requestState) getMaxBodyBytes() int64 {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.maxBodyBytes
}

func (st *requestState) getInboundRequestID() string {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.inboundRequestID
}

// enrich adds the request scope and span context recorded by inner
// middleware to ctx when ctx lacks them. Outer middleware (Recover) use it so
// that their logs and problems carry the request and trace IDs.
func (st *requestState) enrich(ctx context.Context) context.Context {
	st.mu.Lock()
	req, hasReq, sc := st.request, st.hasRequest, st.spanContext
	st.mu.Unlock()
	if _, ok := scope.RequestFrom(ctx); !ok && hasReq {
		ctx = scope.WithRequest(ctx, req)
	}
	if !trace.SpanContextFromContext(ctx).IsValid() && sc.IsValid() {
		ctx = trace.ContextWithSpanContext(ctx, sc)
	}
	return ctx
}

// schemeOf returns "https" or "http" for r: the scheme RequestID derived
// (which honors X-Forwarded-Proto from trusted proxies), or the connection's.
func schemeOf(r *http.Request) string {
	if st := stateFrom(r.Context()); st != nil {
		st.mu.Lock()
		s := st.scheme
		st.mu.Unlock()
		if s != "" {
			return s
		}
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// loggerOf returns the logger a middleware registered for this request, or
// the default logger, so that errors are never dropped silently.
func loggerOf(r *http.Request) *slog.Logger {
	if r != nil {
		if st := stateFrom(r.Context()); st != nil {
			st.mu.Lock()
			l := st.logger
			st.mu.Unlock()
			if l != nil {
				return l
			}
		}
	}
	return slog.Default()
}

// Route returns the route template of the pattern that matched r, such as
// "/v1/users/{user_id}", or "" if no pattern matched (yet). The template has
// no method or host, so it is safe as a low-cardinality metric attribute.
func Route(r *http.Request) string {
	if st := stateFrom(r.Context()); st != nil {
		if route := st.getRoute(); route != "" {
			return route
		}
	}
	return routeTemplate(r.Pattern)
}

// routeTemplate strips the method and host from a ServeMux pattern
// ("GET example.com/v1/x/{id}" becomes "/v1/x/{id}").
func routeTemplate(pattern string) string {
	if i := strings.IndexAny(pattern, " \t"); i >= 0 {
		pattern = strings.TrimLeft(pattern[i:], " \t")
	}
	if i := strings.IndexByte(pattern, '/'); i > 0 {
		pattern = pattern[i:]
	}
	return pattern
}

// responseRecorder records the status and size of a response. Middleware
// reuse one recorder per request (recorderFor) instead of stacking wrappers.
type responseRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
}

func recorderFor(w http.ResponseWriter) *responseRecorder {
	if rec, ok := w.(*responseRecorder); ok {
		return rec
	}
	return &responseRecorder{ResponseWriter: w}
}

// WriteHeader records the first final status code.
func (rw *responseRecorder) WriteHeader(code int) {
	if !rw.wroteHeader && code >= http.StatusOK {
		rw.status = code
		rw.wroteHeader = true
	}
	rw.ResponseWriter.WriteHeader(code)
}

// Write records the body size; a write without WriteHeader implies 200.
func (rw *responseRecorder) Write(b []byte) (int, error) {
	if !rw.wroteHeader {
		rw.status = http.StatusOK
		rw.wroteHeader = true
	}
	n, err := rw.ResponseWriter.Write(b)
	rw.bytes += int64(n)
	return n, err //nolint:wrapcheck // a pass-through writer must return the underlying error unchanged
}

// Flush sends buffered data to the client, if the underlying writer can.
func (rw *responseRecorder) Flush() {
	if !rw.wroteHeader {
		rw.status = http.StatusOK
		rw.wroteHeader = true
	}
	_ = http.NewResponseController(rw.ResponseWriter).Flush()
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (rw *responseRecorder) Unwrap() http.ResponseWriter { return rw.ResponseWriter }

// statusCode returns the recorded status; net/http sends 200 when a handler
// writes nothing.
func (rw *responseRecorder) statusCode() int {
	if !rw.wroteHeader {
		return http.StatusOK
	}
	return rw.status
}

// baseWriter returns the innermost writer, which net/http needs to see for
// http.MaxBytesReader to close the connection after an oversized body.
func baseWriter(w http.ResponseWriter) http.ResponseWriter {
	for {
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return w
		}
		w = u.Unwrap()
	}
}

// truncate shortens s to at most n bytes on a UTF-8 boundary and replaces
// invalid UTF-8, for values copied from requests into logs and scopes.
func truncate(s string, n int) string {
	s = strings.ToValidUTF8(s, "�")
	if len(s) <= n {
		return s
	}
	for n > 0 && !isRuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
