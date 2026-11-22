package httpx

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/scope"
)

// DefaultMaxBodyBytes is the default request body limit (1 MiB).
const DefaultMaxBodyBytes int64 = 1 << 20

// maxLoggedPath bounds the request path copied into logs.
const maxLoggedPath = 2048

// Recover turns a panic in the handler into a 500 problem. The panic value
// and stack are logged at error level; the response never contains them.
// If the response was already started, the connection is aborted instead, so
// the client cannot mistake a truncated response for a complete one.
// http.ErrAbortHandler passes through unchanged.
func Recover(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r, st := withState(r)
			st.setLoggerIfUnset(logger)
			rec := recorderFor(w)
			defer func() { //nolint:contextcheck // the request carries the context
				v := recover()
				if v == nil {
					return
				}
				if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(v)
				}
				ctx := st.enrich(r.Context())
				logger.LogAttrs(ctx, slog.LevelError, "panic serving request",
					slog.String("panic", fmt.Sprint(v)),
					slog.String("stack", string(debug.Stack())),
					slog.String("method", truncate(r.Method, 32)),
					slog.String("route", Route(r)),
					slog.String("path", truncate(r.URL.Path, maxLoggedPath)),
				)
				if rec.wroteHeader {
					panic(http.ErrAbortHandler)
				}
				p, _ := problemFor(r.WithContext(ctx), errs.New(errs.CodeInternal, "Internal error."))
				sendProblem(rec, p)
			}()
			next.ServeHTTP(rec, r)
		})
	}
}

// AccessLog writes one info record per request with method, route, path
// (never the query string, which may carry secrets), status, bytes,
// duration_ms, client_ip and inbound_request_id. No request headers are
// logged. request_id, trace_id and span_id come from the context through the
// platform logger (package log), so place AccessLog inside RequestID and
// Instrument.
func AccessLog(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r, st := withState(r)
			st.setLoggerIfUnset(logger)
			rec := recorderFor(w)
			start := time.Now()
			completed := false
			defer func() { //nolint:contextcheck // the request carries the context
				status := rec.statusCode()
				if !completed {
					status = http.StatusInternalServerError
				}
				attrs := []slog.Attr{
					slog.String("method", truncate(r.Method, 32)),
					slog.String("route", Route(r)),
					slog.String("path", truncate(r.URL.Path, maxLoggedPath)),
					slog.Int("status", status),
					slog.Int64("bytes", rec.bytes),
					slog.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000),
				}
				ctx := st.enrich(r.Context())
				if req, ok := scope.RequestFrom(ctx); ok && req.IP != "" {
					attrs = append(attrs, slog.String("client_ip", req.IP))
				}
				if in := st.getInboundRequestID(); in != "" {
					attrs = append(attrs, slog.String("inbound_request_id", in))
				}
				logger.LogAttrs(ctx, slog.LevelInfo, "http request", attrs...)
			}()
			next.ServeHTTP(rec, r)
			completed = true
		})
	}
}

// MaxBytes limits request bodies to limit bytes (DefaultMaxBodyBytes when
// limit <= 0). A declared Content-Length over the limit is rejected at once
// with a 413 payload_too_large problem; a longer streamed body fails on read
// with *http.MaxBytesError, which DecodeJSON and WriteProblem map to 413.
func MaxBytes(limit int64) Middleware {
	if limit <= 0 {
		limit = DefaultMaxBodyBytes
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > limit {
				WriteProblem(w, r, tooLarge(limit, nil))
				return
			}
			r, st := withState(r)
			st.setMaxBodyBytes(limit)
			if r.Body != nil && r.Body != http.NoBody {
				r = r.WithContext(r.Context()) //nolint:contextcheck // shallow copy with the same context; the caller's request stays unmodified
				r.Body = http.MaxBytesReader(baseWriter(w), r.Body, limit)
			}
			next.ServeHTTP(w, r)
		})
	}
}
