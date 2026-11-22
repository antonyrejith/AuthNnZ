// Package log configures structured JSON logging with log/slog. Every record
// carries the tenant and trace from the context, so call sites do not repeat
// them. Secrets are redacted by their types (package secret).
package log

import (
	"context"
	"io"
	"log/slog"
	"strings"

	"go.opentelemetry.io/otel/trace"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/scope"
)

// New returns a JSON logger that writes to w at the given level.
func New(w io.Writer, level slog.Level) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(contextHandler{Handler: h})
}

// ParseLevel parses "debug", "info", "warn" or "error".
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, errs.New(errs.CodeInvalidRequest, "unknown log level "+s)
	}
}

// contextHandler adds tenant, environment, request and trace identifiers from
// the context to every record.
type contextHandler struct {
	slog.Handler
}

func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if t, err := scope.TenantFrom(ctx); err == nil {
		r.AddAttrs(slog.String("tenant_id", t.TenantID.String()))
		if !t.EnvironmentID.IsZero() {
			r.AddAttrs(slog.String("environment_id", t.EnvironmentID.String()))
		}
	}
	if req, ok := scope.RequestFrom(ctx); ok && req.RequestID != "" {
		r.AddAttrs(slog.String("request_id", req.RequestID))
	}
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return h.Handler.Handle(ctx, r) //nolint:wrapcheck // pass-through of the wrapped handler's result
}

func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{Handler: h.Handler.WithGroup(name)}
}
