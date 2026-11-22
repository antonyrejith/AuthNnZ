package log_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/antonyrejith/portun/internal/platform/ids"
	plog "github.com/antonyrejith/portun/internal/platform/log"
	"github.com/antonyrejith/portun/internal/platform/scope"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

// TestContextAttributes: context attributes are top level even on a logger
// derived with WithGroup (review finding R9); call-site attributes stay in
// their group.
func TestContextAttributes(t *testing.T) {
	t.Parallel()
	tenant, err := ids.New[ids.TenantKind]()
	if err != nil {
		t.Fatal(err)
	}
	env, err := ids.New[ids.EnvironmentKind]()
	if err != nil {
		t.Fatal(err)
	}
	traceID := trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	spanID := trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8}
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled,
	}))
	ctx = scope.WithTenant(ctx, scope.Tenant{TenantID: tenant, EnvironmentID: env})
	ctx = scope.WithRequest(ctx, scope.Request{RequestID: "req_1"})

	var buf bytes.Buffer
	logger := plog.New(&buf, slog.LevelInfo).With(slog.String("component", "test")).WithGroup("g")
	logger.InfoContext(ctx, "hello", slog.Any("password", secret.NewString("s3cret")))

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("decode %s: %v", buf.String(), err)
	}
	g, _ := rec["g"].(map[string]any)
	if g == nil {
		t.Fatalf("group g missing: %s", buf.String())
	}
	want := map[string]string{
		"tenant_id":      tenant.String(),
		"environment_id": env.String(),
		"request_id":     "req_1",
		"trace_id":       traceID.String(),
		"span_id":        spanID.String(),
	}
	for k, v := range want {
		if rec[k] != v {
			t.Errorf("%s = %v, want %v at top level (record %s)", k, rec[k], v, buf.String())
		}
		if _, nested := g[k]; nested {
			t.Errorf("%s must not be inside the group (record %s)", k, buf.String())
		}
	}
	if rec["component"] != "test" || g["password"] != "[REDACTED]" {
		t.Errorf("unexpected record %s", buf.String())
	}
}

func TestNoContextNoExtraAttributes(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	plog.New(&buf, slog.LevelInfo).InfoContext(context.Background(), "hello")
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"tenant_id", "trace_id", "request_id"} {
		if _, ok := rec[k]; ok {
			t.Errorf("unexpected %s", k)
		}
	}
}

func TestLevelFiltering(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := plog.New(&buf, slog.LevelWarn)
	logger.Info("dropped")
	if buf.Len() != 0 {
		t.Fatalf("info logged at warn level: %s", buf.String())
	}
}

func TestParseLevel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want slog.Level
		ok   bool
	}{
		{"debug", slog.LevelDebug, true},
		{"INFO", slog.LevelInfo, true},
		{"", slog.LevelInfo, true},
		{" warn ", slog.LevelWarn, true},
		{"warning", slog.LevelWarn, true},
		{"error", slog.LevelError, true},
		{"verbose", slog.LevelInfo, false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			got, err := plog.ParseLevel(tt.in)
			if (err == nil) != tt.ok || got != tt.want {
				t.Fatalf("ParseLevel(%q) = %v, %v", tt.in, got, err)
			}
		})
	}
}

// noDuplicateKeys fails the test if the JSON object read from dec has a
// duplicate key at any depth.
func noDuplicateKeys(t *testing.T, dec *json.Decoder) {
	t.Helper()
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("expected object, got %v %v", tok, err)
	}
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		key, _ := tok.(string)
		if seen[key] {
			t.Fatalf("duplicate key %q", key)
		}
		seen[key] = true
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			t.Fatal(err)
		}
		if bytes.HasPrefix(raw, []byte("{")) {
			noDuplicateKeys(t, json.NewDecoder(bytes.NewReader(raw)))
		}
	}
	if _, err := dec.Token(); err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
}

// TestReservedKeysAndDuplicates: call-site and With attributes that use a
// reserved key are renamed, never duplicate the context key, and no object
// in a record has a duplicate key (review finding R9).
func TestReservedKeysAndDuplicates(t *testing.T) {
	t.Parallel()
	tenant, err := ids.New[ids.TenantKind]()
	if err != nil {
		t.Fatal(err)
	}
	ctx := scope.WithTenant(context.Background(), scope.Tenant{TenantID: tenant})
	ctx = scope.WithRequest(ctx, scope.Request{RequestID: "req_ctx"})
	tests := []struct {
		name string
		log  func(l *slog.Logger)
		want map[string]any // expected top-level values
	}{
		{
			"call site reserved keys",
			func(l *slog.Logger) {
				l.InfoContext(ctx, "m", slog.String("tenant_id", "spoofed"), slog.String("msg", "x"), slog.String("level", "y"))
			},
			map[string]any{"tenant_id": tenant.String(), "attr_tenant_id": "spoofed", "msg": "m", "attr_msg": "x", "attr_level": "y"},
		},
		{
			"With reserved key",
			func(l *slog.Logger) {
				l.With(slog.String("request_id", "from_with")).InfoContext(ctx, "m")
			},
			map[string]any{"request_id": "req_ctx", "attr_request_id": "from_with"},
		},
		{
			"repeated key last wins",
			func(l *slog.Logger) {
				l.With(slog.String("k", "1")).InfoContext(ctx, "m", slog.String("k", "2"), slog.String("k", "3"))
			},
			map[string]any{"k": "3"},
		},
		{
			"reserved key inside a group is free",
			func(l *slog.Logger) {
				l.WithGroup("user").InfoContext(ctx, "m", slog.String("tenant_id", "other"), slog.String("tenant_id", "other2"))
			},
			map[string]any{"tenant_id": tenant.String(), "user": map[string]any{"tenant_id": "other2"}},
		},
		{
			"inline empty-key group",
			func(l *slog.Logger) {
				l.InfoContext(ctx, "m", slog.Group("", slog.String("a", "1")), slog.Group("empty"))
			},
			map[string]any{"a": "1"},
		},
		{
			"renamed key meets existing attr_ key",
			func(l *slog.Logger) {
				l.InfoContext(ctx, "m", slog.String("attr_span_id", "a"), slog.String("span_id", "b"))
			},
			map[string]any{"attr_span_id": "b"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			tt.log(plog.New(&buf, slog.LevelInfo))
			noDuplicateKeys(t, json.NewDecoder(strings.NewReader(buf.String())))
			var rec map[string]any
			if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
				t.Fatalf("decode %s: %v", buf.String(), err)
			}
			for k, v := range tt.want {
				got, _ := json.Marshal(rec[k])
				exp, _ := json.Marshal(v)
				if string(got) != string(exp) {
					t.Errorf("%s = %s, want %s (record %s)", k, got, exp, buf.String())
				}
			}
			if _, ok := rec["empty"]; ok {
				t.Errorf("empty group written: %s", buf.String())
			}
		})
	}
}

// TestHandleDoesNotModifyTheRecord: the caller's record is cloned, not
// extended (review finding R9).
func TestHandleDoesNotModifyTheRecord(t *testing.T) {
	t.Parallel()
	tenant, err := ids.New[ids.TenantKind]()
	if err != nil {
		t.Fatal(err)
	}
	ctx := scope.WithTenant(context.Background(), scope.Tenant{TenantID: tenant})
	h := plog.New(io.Discard, slog.LevelInfo).Handler()
	r := slog.NewRecord(time.Now(), slog.LevelInfo, "m", 0)
	r.AddAttrs(slog.String("a", "1"))
	if err := h.Handle(ctx, r); err != nil {
		t.Fatal(err)
	}
	if r.NumAttrs() != 1 {
		t.Fatalf("record has %d attributes after Handle, want 1", r.NumAttrs())
	}
}
