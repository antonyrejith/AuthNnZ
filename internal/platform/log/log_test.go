package log_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel/trace"

	"github.com/antonyrejith/portun/internal/platform/ids"
	plog "github.com/antonyrejith/portun/internal/platform/log"
	"github.com/antonyrejith/portun/internal/platform/scope"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

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
	want := map[string]string{
		"tenant_id":      tenant.String(),
		"environment_id": env.String(),
		"request_id":     "req_1",
		"trace_id":       traceID.String(),
		"span_id":        spanID.String(),
	}
	for k, v := range want {
		if g[k] != v {
			t.Errorf("%s = %v, want %v (record %s)", k, g[k], v, buf.String())
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
