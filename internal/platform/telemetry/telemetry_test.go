package telemetry_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/antonyrejith/portun/internal/platform/config"
	"github.com/antonyrejith/portun/internal/platform/telemetry"
)

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

func TestSetupWithoutEndpoint(t *testing.T) {
	t.Parallel()
	tel, err := telemetry.Setup(t.Context(), telemetry.Config{SamplingRatio: 0}, discard())
	if err != nil {
		t.Fatal(err)
	}
	ctx, span := tel.TracerProvider().Tracer("test").Start(t.Context(), "op")
	if !span.SpanContext().IsValid() {
		t.Fatal("span context must be valid for log correlation even without export")
	}
	carrier := propagation.MapCarrier{}
	tel.Propagator().Inject(ctx, carrier)
	if !strings.Contains(carrier["traceparent"], span.SpanContext().TraceID().String()) {
		t.Fatalf("traceparent not propagated: %v", carrier)
	}
	span.End()
	counter, err := tel.MeterProvider().Meter("test").Int64Counter("c")
	if err != nil {
		t.Fatal(err)
	}
	counter.Add(t.Context(), 1)
	if err := tel.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestConfigValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     telemetry.Config
		wantErr bool
	}{
		{"host port", telemetry.Config{Endpoint: "otel:4318", SamplingRatio: 1}, false},
		{"https url", telemetry.Config{Endpoint: "https://otel.example:4318/base/", SamplingRatio: 0.5}, false},
		{"http url insecure", telemetry.Config{Endpoint: "http://localhost:4318", Insecure: true, SamplingRatio: 1}, false},
		{"http url without insecure", telemetry.Config{Endpoint: "http://localhost:4318", SamplingRatio: 1}, true},
		{"https with insecure", telemetry.Config{Endpoint: "https://otel:4318", Insecure: true, SamplingRatio: 1}, true},
		{"bad scheme", telemetry.Config{Endpoint: "grpc://otel:4317", SamplingRatio: 1}, true},
		{"query", telemetry.Config{Endpoint: "https://otel:4318?x=1", SamplingRatio: 1}, true},
		{"path without scheme", telemetry.Config{Endpoint: "otel:4318/x", SamplingRatio: 1}, true},
		{"ratio above one", telemetry.Config{SamplingRatio: 1.5}, true},
		{"negative ratio", telemetry.Config{SamplingRatio: -0.1}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tel, err := telemetry.Setup(t.Context(), tt.cfg, discard())
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if tel != nil {
				ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
				defer cancel()
				_ = tel.Shutdown(ctx) // endpoints are unreachable; only setup matters here
			}
		})
	}
}

func TestConfigLoad(t *testing.T) {
	t.Parallel()
	env := map[string]string{"OTEL_OTLP_ENDPOINT": "otel:4318", "OTEL_SAMPLING_RATIO": "0.25"}
	var cfg telemetry.Config
	if err := config.Load("OTEL_", &cfg, func(k string) (string, bool) { v, ok := env[k]; return v, ok }); err != nil {
		t.Fatal(err)
	}
	want := telemetry.Config{
		ServiceName: "portun", Endpoint: "otel:4318", SamplingRatio: 0.25,
		ExportTimeout: 10 * time.Second, MetricInterval: 60 * time.Second,
	}
	if cfg != want {
		t.Fatalf("cfg = %+v, want %+v", cfg, want)
	}
}

// collector records OTLP/HTTP export requests.
type collector struct {
	mu    sync.Mutex
	paths map[string]int
	spans []string
}

func (c *collector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	if r.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err == nil {
			body, _ = io.ReadAll(zr)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.paths[r.URL.Path]++
	if r.URL.Path == "/v1/traces" {
		var req collectortrace.ExportTraceServiceRequest
		if err := proto.Unmarshal(body, &req); err == nil {
			for _, rs := range req.GetResourceSpans() {
				for _, ss := range rs.GetScopeSpans() {
					for _, s := range ss.GetSpans() {
						c.spans = append(c.spans, s.GetName())
					}
				}
			}
		}
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
}

func TestShutdownFlushes(t *testing.T) {
	t.Parallel()
	col := &collector{paths: map[string]int{}}
	srv := httptest.NewServer(col)
	t.Cleanup(srv.Close)
	tel, err := telemetry.Setup(t.Context(), telemetry.Config{
		Endpoint: srv.URL, Insecure: true, SamplingRatio: 1,
		MetricInterval: time.Hour, // only Shutdown can export
	}, discard())
	if err != nil {
		t.Fatal(err)
	}
	_, span := tel.TracerProvider().Tracer("test").Start(t.Context(), "flush-me", trace.WithSpanKind(trace.SpanKindServer))
	span.End()
	counter, _ := tel.MeterProvider().Meter("test").Int64Counter("requests")
	counter.Add(t.Context(), 3)

	if err := tel.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	col.mu.Lock()
	defer col.mu.Unlock()
	if col.paths["/v1/traces"] == 0 || col.paths["/v1/metrics"] == 0 {
		t.Fatalf("exports = %v", col.paths)
	}
	if len(col.spans) != 1 || col.spans[0] != "flush-me" {
		t.Fatalf("exported spans = %v", col.spans)
	}
}

// TestInstall is the only test that touches the OpenTelemetry globals.
func TestInstall(t *testing.T) {
	t.Parallel()
	tel, err := telemetry.Setup(t.Context(), telemetry.Config{SamplingRatio: 1}, discard())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	tel.Install()
	if otel.GetTracerProvider() != tel.TracerProvider() {
		t.Fatal("tracer provider not installed")
	}
	fields := strings.Join(otel.GetTextMapPropagator().Fields(), ",")
	if !strings.Contains(fields, "traceparent") || !strings.Contains(fields, "baggage") {
		t.Fatalf("propagator fields = %s", fields)
	}
}
