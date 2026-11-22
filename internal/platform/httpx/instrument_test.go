package httpx_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/antonyrejith/portun/internal/platform/httpx"
)

const (
	inboundTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	inboundSpanID  = "00f067aa0ba902b7"
	traceparent    = "00-" + inboundTraceID + "-" + inboundSpanID + "-01"
)

type instrumented struct {
	handler http.Handler
	spans   *tracetest.SpanRecorder
	reader  *sdkmetric.ManualReader
}

func newInstrumented(t *testing.T, trusted bool) instrumented {
	t.Helper()
	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	inst, err := httpx.Instrument(httpx.InstrumentConfig{
		TracerProvider: tp, MeterProvider: mp,
		Propagator:               propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}),
		TrustInboundTraceContext: trusted,
	})
	if err != nil {
		t.Fatal(err)
	}
	logger, _ := newLogger()
	rt := httpx.NewRouter()
	rt.HandleFunc("GET /v1/things/{id}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(trace.SpanContextFromContext(r.Context()).TraceID().String()))
	})
	rt.HandleFunc("GET /v1/baggage", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(baggage.FromContext(r.Context()).String()))
	})
	rt.HandleFunc("GET /v1/fail", func(http.ResponseWriter, *http.Request) { panic("boom") })
	rt.HandleFunc("GET /v1/canceled", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteProblem(w, r, fmt.Errorf("query: %w", context.Canceled))
	})
	return instrumented{
		handler: httpx.Chain(rt, httpx.Recover(logger), httpx.RequestID(httpx.RequestIDConfig{}), inst, httpx.AccessLog(logger)),
		spans:   spans,
		reader:  reader,
	}
}

func attrsOf(kvs []attribute.KeyValue) map[string]attribute.Value {
	m := map[string]attribute.Value{}
	for _, kv := range kvs {
		m[string(kv.Key)] = kv.Value
	}
	return m
}

// TestInstrument checks the server span (named by route, parented by the
// inbound traceparent when inbound trace context is trusted) and the RED
// metrics with the route attribute.
func TestInstrument(t *testing.T) {
	t.Parallel()
	in := newInstrumented(t, true)
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/things/123?secret=x", nil)
	r.Header.Set("Traceparent", traceparent)
	rec := serve(in.handler, r)
	if rec.Code != 200 || rec.Body.String() != inboundTraceID {
		t.Fatalf("handler did not see the inbound trace: %d %s", rec.Code, rec.Body)
	}
	serve(in.handler, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/things/456", nil))
	serve(in.handler, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/fail", nil))
	serve(in.handler, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/unknown/path", nil))
	serve(in.handler, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/canceled", nil))

	ended := in.spans.Ended()
	if len(ended) != 5 {
		t.Fatalf("spans = %d", len(ended))
	}
	first := ended[0]
	if first.Name() != "GET /v1/things/{id}" || first.SpanKind() != trace.SpanKindServer {
		t.Fatalf("span %q kind %v", first.Name(), first.SpanKind())
	}
	if first.Parent().TraceID().String() != inboundTraceID || first.Parent().SpanID().String() != inboundSpanID || !first.Parent().IsRemote() {
		t.Fatalf("parent = %v", first.Parent())
	}
	a := attrsOf(first.Attributes())
	if a["http.route"].AsString() != "/v1/things/{id}" || a["http.response.status_code"].AsInt64() != 200 ||
		a["url.path"].AsString() != "/v1/things/123" || a["portun.request.id"].AsString() != rec.Header().Get(httpx.HeaderRequestID) {
		t.Fatalf("attributes = %v", a)
	}
	if _, ok := a["url.query"]; ok {
		t.Fatal("query recorded")
	}
	if fail := ended[2]; fail.Status().Code != codes.Error || attrsOf(fail.Attributes())["error.type"].AsString() != "500" {
		t.Fatalf("failing span status %v attrs %v", fail.Status(), fail.Attributes())
	}
	if ended[3].Name() != "GET" {
		t.Fatalf("unmatched span name %q", ended[3].Name())
	}
	// A canceled request (499) is not a server error.
	if c := ended[4]; c.Status().Code == codes.Error || attrsOf(c.Attributes())["http.response.status_code"].AsInt64() != 499 {
		t.Fatalf("canceled span status %v attrs %v", c.Status(), c.Attributes())
	} else if _, ok := attrsOf(c.Attributes())["error.type"]; ok {
		t.Fatalf("canceled span has error.type: %v", c.Attributes())
	}

	var rm metricdata.ResourceMetrics
	if err := in.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	counts := map[string]uint64{}
	active := int64(-1)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch data := m.Data.(type) {
			case metricdata.Histogram[float64]:
				if m.Name != "http.server.request.duration" || m.Unit != "s" {
					t.Fatalf("histogram %s %s", m.Name, m.Unit)
				}
				for _, dp := range data.DataPoints {
					route, _ := dp.Attributes.Value("http.route")
					status, _ := dp.Attributes.Value("http.response.status_code")
					if _, ok := dp.Attributes.Value("url.path"); ok {
						t.Fatal("raw path in metric attributes")
					}
					counts[route.AsString()+" "+status.String()] += dp.Count
				}
			case metricdata.Sum[int64]:
				if m.Name == "http.server.active_requests" {
					active = 0
					for _, dp := range data.DataPoints {
						active += dp.Value
					}
				}
			}
		}
	}
	want := map[string]uint64{"/v1/things/{id} 200": 2, "/v1/fail 500": 1, " 404": 1, "/v1/canceled 499": 1}
	for k, v := range want {
		if counts[k] != v {
			t.Fatalf("request count %q = %d, want %d (all %v)", k, counts[k], v, counts)
		}
	}
	if active != 0 {
		t.Fatalf("active requests = %d, want 0", active)
	}
}

// TestInstrumentUntrustedTraceContext checks the default: inbound trace
// context is untrusted, so each request starts a new trace that links the
// caller's context, and inbound baggage does not reach the handler. With
// TrustInboundTraceContext the trace and baggage are adopted.
//
// Covers: SEC-09
func TestInstrumentUntrustedTraceContext(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		trusted bool
	}{
		{"default untrusted", false},
		{"trusted", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			in := newInstrumented(t, tt.trusted)
			r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/baggage", nil)
			r.Header.Set("Traceparent", traceparent)
			r.Header.Set("Baggage", "tenant=evil")
			rec := serve(in.handler, r)
			s := in.spans.Ended()[0]
			adopted := s.SpanContext().TraceID().String() == inboundTraceID && s.Parent().IsRemote()
			if adopted != tt.trusted {
				t.Fatalf("adopted the inbound trace = %v, want %v", adopted, tt.trusted)
			}
			if gotBaggage := rec.Body.String() != ""; gotBaggage != tt.trusted {
				t.Fatalf("handler baggage = %q, trusted %v", rec.Body.String(), tt.trusted)
			}
			if !tt.trusted && (len(s.Links()) != 1 || s.Links()[0].SpanContext.TraceID().String() != inboundTraceID) {
				t.Fatalf("links = %v, want the caller's span context", s.Links())
			}
		})
	}
	// The zero config is untrusted.
	if (httpx.InstrumentConfig{}).TrustInboundTraceContext {
		t.Fatal("inbound trace context must be untrusted by default")
	}
}

func TestInstrumentNoopProviders(t *testing.T) {
	t.Parallel()
	inst, err := httpx.Instrument(httpx.InstrumentConfig{})
	if err != nil {
		t.Fatal(err)
	}
	rec := serve(inst(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })),
		httptest.NewRequestWithContext(context.Background(), "BREW", "/", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d", rec.Code)
	}
}
