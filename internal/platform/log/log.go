// Package log configures structured JSON logging with log/slog. Every record
// carries the tenant and trace from the context, so call sites do not repeat
// them. Secrets are redacted by their types (package secret).
//
// # Record layout
//
// The context attributes tenant_id, environment_id, request_id, trace_id and
// span_id are always top-level keys, also on loggers derived with WithGroup,
// so log queries find them in one place. These keys, and the keys the JSON
// handler writes itself (time, level, msg, source), are reserved: a top-level
// attribute that uses one, from a call site or from Logger.With, is renamed
// to "attr_<key>" (for example attr_tenant_id); inside a group the keys are
// free. Within every object each key appears once; when an attribute repeats
// a key, the last value wins. Empty-key groups are inlined, as slog does.
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

// Reserved top-level keys.
const (
	KeyTenantID      = "tenant_id"
	KeyEnvironmentID = "environment_id"
	KeyRequestID     = "request_id"
	KeyTraceID       = "trace_id"
	KeySpanID        = "span_id"

	// RenamedPrefix is put before a reserved key used by a call site.
	RenamedPrefix = "attr_"
)

// reserved reports whether key may not be used by call sites at the top
// level.
func reserved(key string) bool {
	switch key {
	case KeyTenantID, KeyEnvironmentID, KeyRequestID, KeyTraceID, KeySpanID,
		slog.TimeKey, slog.LevelKey, slog.MessageKey, slog.SourceKey:
		return true
	}
	return false
}

// New returns a JSON logger that writes to w at the given level.
func New(w io.Writer, level slog.Level) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(&contextHandler{base: h})
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

// step is one WithAttrs (attrs) or WithGroup (group) call.
type step struct {
	group string
	attrs []slog.Attr
}

// contextHandler records WithAttrs and WithGroup calls instead of passing
// them to the base handler, and lays out each record itself: context
// attributes first and top level, then the attributes in their groups.
// The base handler never has groups of its own.
type contextHandler struct {
	base  slog.Handler
	steps []step
}

func (h *contextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.base.Enabled(ctx, level)
}

func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	return h.with(step{attrs: append([]slog.Attr(nil), attrs...)})
}

func (h *contextHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return h.with(step{group: name})
}

func (h *contextHandler) with(s step) *contextHandler {
	steps := make([]step, len(h.steps), len(h.steps)+1)
	copy(steps, h.steps)
	return &contextHandler{base: h.base, steps: append(steps, s)}
}

// Handle builds a new record (the caller's record is never modified) and
// passes it to the base handler.
func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	inner := make([]slog.Attr, 0, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		inner = append(inner, a)
		return true
	})
	for i := len(h.steps) - 1; i >= 0; i-- {
		s := h.steps[i]
		if s.group != "" {
			inner = []slog.Attr{{Key: s.group, Value: slog.GroupValue(inner...)}}
			continue
		}
		inner = append(append([]slog.Attr(nil), s.attrs...), inner...)
	}
	top := normalize(inner)
	for i := range top {
		if reserved(top[i].Key) {
			top[i].Key = RenamedPrefix + top[i].Key
		}
	}
	top = dedupe(top) // a renamed key can meet an existing attr_ key

	out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	out.AddAttrs(contextAttrs(ctx)...)
	out.AddAttrs(top...)
	return h.base.Handle(ctx, out) //nolint:wrapcheck // pass-through of the wrapped handler's result
}

// contextAttrs returns the tenant, environment, request and trace
// identifiers in ctx.
func contextAttrs(ctx context.Context) []slog.Attr {
	var out []slog.Attr
	if t, err := scope.TenantFrom(ctx); err == nil {
		out = append(out, slog.String(KeyTenantID, t.TenantID.String()))
		if !t.EnvironmentID.IsZero() {
			out = append(out, slog.String(KeyEnvironmentID, t.EnvironmentID.String()))
		}
	}
	if req, ok := scope.RequestFrom(ctx); ok && req.RequestID != "" {
		out = append(out, slog.String(KeyRequestID, req.RequestID))
	}
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		out = append(out, slog.String(KeyTraceID, sc.TraceID().String()), slog.String(KeySpanID, sc.SpanID().String()))
	}
	return out
}

// normalize resolves values, inlines empty-key groups, drops empty groups
// and removes duplicate keys, recursively.
func normalize(attrs []slog.Attr) []slog.Attr {
	out := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		a.Value = a.Value.Resolve()
		if a.Value.Kind() != slog.KindGroup {
			if a.Equal(slog.Attr{}) {
				continue // slog ignores the empty attribute
			}
			out = append(out, a)
			continue
		}
		children := normalize(a.Value.Group())
		switch {
		case len(children) == 0:
		case a.Key == "":
			out = append(out, children...)
		default:
			out = append(out, slog.Attr{Key: a.Key, Value: slog.GroupValue(children...)})
		}
	}
	return dedupe(out)
}

// dedupe keeps one attribute per key: the last value, at the position of the
// first occurrence.
func dedupe(attrs []slog.Attr) []slog.Attr {
	pos := make(map[string]int, len(attrs))
	out := attrs[:0]
	for _, a := range attrs {
		if i, ok := pos[a.Key]; ok {
			out[i] = a
			continue
		}
		pos[a.Key] = len(out)
		out = append(out, a)
	}
	return out
}
