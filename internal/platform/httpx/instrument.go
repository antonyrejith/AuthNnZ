package httpx

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/semconv/v1.43.0/httpconv"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/version"
)

const instrumentationName = "github.com/antonyrejith/portun/internal/platform/httpx"

// InstrumentConfig configures Instrument. Providers come from the telemetry
// package (telemetry.Telemetry); nil providers mean no-op, and a nil
// propagator means inbound trace headers are ignored.
type InstrumentConfig struct {
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
	Propagator     propagation.TextMapPropagator
	// TrustInboundTraceContext adopts the caller's trace context (the span
	// becomes a child of the inbound traceparent) and baggage. Set it only
	// where every caller is trusted, for example behind a gateway that is the
	// trust boundary for trace headers. By default inbound trace context is
	// untrusted: every request starts a new trace that links the caller's
	// span context, and inbound baggage is dropped, so clients can neither
	// choose our trace IDs, force sampling, nor inject baggage.
	TrustInboundTraceContext bool
}

// Instrument returns middleware that creates a server span per request and
// records RED metrics, following the OpenTelemetry HTTP semantic conventions
// (semconv v1.43.0):
//
//   - http.server.request.duration (histogram, seconds): its count is the
//     request count (rate) and the 5xx share of it the error rate;
//   - http.server.active_requests (up-down counter): requests in flight.
//
// The span is named "{method} {route}" once the Router matched a pattern
// (just "{method}" otherwise). Metric attributes have bounded cardinality:
// normalized method (unknown methods become "_OTHER"), url.scheme, route
// template (never the raw path), status code, protocol version and
// error.type for 5xx. The client-controlled Host header is never used.
//
// The tracing middleware is written against the OpenTelemetry API instead of
// using contrib's otelhttp, because otelhttp starts the span before routing
// and records server.address from the Host header (unbounded cardinality),
// and adds a dependency (httpsnoop) for features we already have.
func Instrument(cfg InstrumentConfig) (Middleware, error) {
	tp := cfg.TracerProvider
	if tp == nil {
		tp = tracenoop.NewTracerProvider()
	}
	mp := cfg.MeterProvider
	if mp == nil {
		mp = metricnoop.NewMeterProvider()
	}
	v := version.Get().Version
	meter := mp.Meter(instrumentationName, metric.WithInstrumentationVersion(v), metric.WithSchemaURL(semconv.SchemaURL))
	duration, err := httpconv.NewServerRequestDuration(meter)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "create request duration histogram")
	}
	active, err := httpconv.NewServerActiveRequests(meter)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "create active requests counter")
	}
	in := &instrumenter{
		tracer:     tp.Tracer(instrumentationName, trace.WithInstrumentationVersion(v), trace.WithSchemaURL(semconv.SchemaURL)),
		propagator: cfg.Propagator,
		trusted:    cfg.TrustInboundTraceContext,
		duration:   duration,
		active:     active,
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { in.serve(next, w, r) }) //nolint:contextcheck // the request carries the context
	}, nil
}

type instrumenter struct {
	tracer     trace.Tracer
	propagator propagation.TextMapPropagator
	trusted    bool
	duration   httpconv.ServerRequestDuration
	active     httpconv.ServerActiveRequests
}

func (in *instrumenter) serve(next http.Handler, w http.ResponseWriter, r *http.Request) {
	r, st := withState(r)
	rec := recorderFor(w)
	method := methodAttr(r.Method)
	scheme := schemeOf(r)
	proto := protocolVersion(r)

	ctx, opts := in.parent(r)
	opts = append(opts,
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(in.startAttributes(r, method, scheme, proto, st)...),
	)
	ctx, span := in.tracer.Start(ctx, spanName(method, ""), opts...)
	st.setSpanContext(span.SpanContext())
	in.active.Add(ctx, 1, method, scheme)

	start := time.Now()
	inner := r.WithContext(ctx)
	completed := false
	defer func() {
		status := rec.statusCode()
		if !completed {
			status = http.StatusInternalServerError
		}
		route := st.getRoute()
		if route == "" {
			route = routeTemplate(inner.Pattern)
		}
		in.finish(ctx, span, finishInfo{
			method: method, scheme: scheme, proto: proto, route: route,
			status: status, elapsed: time.Since(start),
		})
	}()
	next.ServeHTTP(rec, inner)
	completed = true
}

// parent returns the context to start the span in and the start options
// that follow from the inbound trace context.
func (in *instrumenter) parent(r *http.Request) (context.Context, []trace.SpanStartOption) {
	ctx := r.Context()
	if in.propagator == nil {
		return ctx, nil
	}
	extracted := in.propagator.Extract(ctx, propagation.HeaderCarrier(r.Header))
	if in.trusted {
		return extracted, nil
	}
	// Untrusted: keep the request context (no inbound baggage) and only link
	// the caller's span context.
	remote := trace.SpanContextFromContext(extracted)
	if !remote.IsValid() {
		return ctx, nil
	}
	return ctx, []trace.SpanStartOption{trace.WithNewRoot(), trace.WithLinks(trace.Link{SpanContext: remote})}
}

func (in *instrumenter) startAttributes(r *http.Request, method httpconv.RequestMethodAttr, scheme, proto string, st *requestState) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		semconv.HTTPRequestMethodKey.String(string(method)),
		semconv.URLScheme(scheme),
		semconv.URLPath(truncate(r.URL.Path, maxLoggedPath)),
		semconv.NetworkProtocolVersion(proto),
	}
	if ua := r.UserAgent(); ua != "" {
		attrs = append(attrs, semconv.UserAgentOriginal(truncate(ua, maxUserAgent)))
	}
	st.mu.Lock()
	req, hasReq, inbound := st.request, st.hasRequest, st.inboundRequestID
	st.mu.Unlock()
	if hasReq {
		attrs = append(attrs, attribute.String("portun.request.id", req.RequestID))
		if req.IP != "" {
			attrs = append(attrs, semconv.ClientAddress(req.IP))
		}
	}
	if inbound != "" {
		attrs = append(attrs, attribute.String("portun.request.inbound_id", inbound))
	}
	return attrs
}

type finishInfo struct {
	method  httpconv.RequestMethodAttr
	scheme  string
	proto   string
	route   string
	status  int
	elapsed time.Duration
}

func (in *instrumenter) finish(ctx context.Context, span trace.Span, f finishInfo) {
	metricAttrs := []attribute.KeyValue{
		in.duration.AttrResponseStatusCode(f.status),
		in.duration.AttrNetworkProtocolVersion(f.proto),
	}
	span.SetName(spanName(f.method, f.route))
	span.SetAttributes(semconv.HTTPResponseStatusCode(f.status))
	if f.route != "" {
		span.SetAttributes(semconv.HTTPRoute(f.route))
		metricAttrs = append(metricAttrs, in.duration.AttrRoute(f.route))
	}
	if f.status >= http.StatusInternalServerError {
		errType := strconv.Itoa(f.status)
		span.SetStatus(codes.Error, "")
		span.SetAttributes(semconv.ErrorTypeKey.String(errType))
		metricAttrs = append(metricAttrs, semconv.ErrorTypeKey.String(errType))
	}
	in.duration.Record(ctx, f.elapsed.Seconds(), f.method, f.scheme, metricAttrs...)
	in.active.Add(ctx, -1, f.method, f.scheme)
	span.End()
}

func spanName(method httpconv.RequestMethodAttr, route string) string {
	m := string(method)
	if method == httpconv.RequestMethodOther {
		m = "HTTP"
	}
	if route == "" {
		return m
	}
	return m + " " + route
}

func methodAttr(m string) httpconv.RequestMethodAttr {
	switch m {
	case http.MethodGet:
		return httpconv.RequestMethodGet
	case http.MethodHead:
		return httpconv.RequestMethodHead
	case http.MethodPost:
		return httpconv.RequestMethodPost
	case http.MethodPut:
		return httpconv.RequestMethodPut
	case http.MethodPatch:
		return httpconv.RequestMethodPatch
	case http.MethodDelete:
		return httpconv.RequestMethodDelete
	case http.MethodOptions:
		return httpconv.RequestMethodOptions
	case http.MethodConnect:
		return httpconv.RequestMethodConnect
	case http.MethodTrace:
		return httpconv.RequestMethodTrace
	default:
		return httpconv.RequestMethodOther
	}
}

func protocolVersion(r *http.Request) string {
	if r.ProtoMajor >= 2 {
		return strconv.Itoa(r.ProtoMajor)
	}
	return strconv.Itoa(r.ProtoMajor) + "." + strconv.Itoa(r.ProtoMinor)
}
