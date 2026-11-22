// Package telemetry sets up OpenTelemetry tracing and metrics: an SDK tracer
// provider and meter provider exporting over OTLP/HTTP, W3C trace-context
// and baggage propagation, and a resource naming the service, its version
// and this instance.
//
// Setup creates the providers but changes no global state. Code receives the
// providers explicitly (TracerProvider, MeterProvider, Propagator). Install
// is the one place that sets the OpenTelemetry globals, for libraries that
// only read them; call it once from main.
//
// The OTLP exporters also honor the standard OTEL_EXPORTER_OTLP_* variables
// (for example OTEL_EXPORTER_OTLP_HEADERS for collector authentication), and
// the resource honors OTEL_RESOURCE_ATTRIBUTES (for example Kubernetes pod
// attributes from the downward API); Config values take precedence.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/version"
)

// Defaults.
const (
	DefaultServiceName    = "portun"
	DefaultExportTimeout  = 10 * time.Second
	DefaultMetricInterval = 60 * time.Second
	DefaultSamplingRatio  = 1.0
)

// Config configures telemetry. It can be loaded with package config.
type Config struct {
	// ServiceName is the service.name resource attribute.
	ServiceName string `env:"SERVICE_NAME" default:"portun"`
	// ServiceInstanceID is service.instance.id; empty means a random UUID per
	// process, as the semantic conventions recommend.
	ServiceInstanceID string `env:"SERVICE_INSTANCE_ID"`
	// Endpoint is the OTLP/HTTP collector: "host:port" or a URL
	// "https://host:port[/base]". Empty disables export; spans still get
	// valid span contexts for log correlation and propagation.
	Endpoint string `env:"OTLP_ENDPOINT"`
	// Insecure allows plain HTTP to the collector (local development).
	// An "http://" endpoint requires it.
	Insecure bool `env:"OTLP_INSECURE" default:"false"`
	// SamplingRatio is the share of new traces that are sampled, in [0, 1].
	// Sampling is parent-based: a sampled parent's decision is followed.
	// Zero means the default, DefaultSamplingRatio (1.0), so that a Config
	// built in code samples like a loaded one; to sample no new traces, set
	// SamplingDisabled.
	SamplingRatio float64 `env:"SAMPLING_RATIO" default:"1"`
	// SamplingDisabled samples no new traces (ratio 0), whatever
	// SamplingRatio says. Span contexts stay valid for log correlation.
	SamplingDisabled bool `env:"SAMPLING_DISABLED" default:"false"`
	// ExportTimeout bounds each export.
	ExportTimeout time.Duration `env:"EXPORT_TIMEOUT" default:"10s"`
	// MetricInterval is the metric export interval.
	MetricInterval time.Duration `env:"METRIC_INTERVAL" default:"60s"`
}

// Telemetry holds the providers. Shut it down to flush telemetry.
type Telemetry struct {
	tp         *sdktrace.TracerProvider
	mp         *sdkmetric.MeterProvider
	propagator propagation.TextMapPropagator
	logger     *slog.Logger
}

// endpoint is a parsed collector address.
type endpoint struct {
	host     string
	basePath string
	insecure bool
}

// Setup creates the tracer and meter providers. It sets no globals.
func Setup(ctx context.Context, cfg Config, logger *slog.Logger) (*Telemetry, error) {
	cfg, ep, err := cfg.validate()
	if err != nil {
		return nil, err
	}
	res, err := newResource(ctx, cfg)
	if err != nil {
		return nil, err
	}
	tpOpts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SamplingRatio))),
	}
	mpOpts := []sdkmetric.Option{sdkmetric.WithResource(res)}
	if ep != nil {
		texp, err := otlptracehttp.New(ctx, traceOptions(*ep, cfg)...)
		if err != nil {
			return nil, errs.Wrap(errs.CodeInternal, err, "create OTLP trace exporter")
		}
		mexp, err := otlpmetrichttp.New(ctx, metricOptions(*ep, cfg)...)
		if err != nil {
			return nil, errors.Join(errs.Wrap(errs.CodeInternal, err, "create OTLP metric exporter"), texp.Shutdown(ctx))
		}
		tpOpts = append(tpOpts, sdktrace.WithBatcher(texp, sdktrace.WithExportTimeout(cfg.ExportTimeout)))
		mpOpts = append(mpOpts, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(mexp,
			sdkmetric.WithInterval(cfg.MetricInterval), sdkmetric.WithTimeout(cfg.ExportTimeout))))
	}
	t := &Telemetry{
		tp:         sdktrace.NewTracerProvider(tpOpts...),
		mp:         sdkmetric.NewMeterProvider(mpOpts...),
		propagator: propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}),
		logger:     logger,
	}
	logger.LogAttrs(ctx, slog.LevelInfo, "telemetry configured",
		slog.String("service_name", cfg.ServiceName),
		slog.Bool("export", ep != nil),
		slog.Float64("sampling_ratio", cfg.SamplingRatio),
	)
	return t, nil
}

func (c Config) validate() (Config, *endpoint, error) {
	if c.ServiceName == "" {
		c.ServiceName = DefaultServiceName
	}
	if c.ExportTimeout <= 0 {
		c.ExportTimeout = DefaultExportTimeout
	}
	if c.MetricInterval <= 0 {
		c.MetricInterval = DefaultMetricInterval
	}
	if c.SamplingRatio < 0 || c.SamplingRatio > 1 || c.SamplingRatio != c.SamplingRatio {
		return c, nil, errs.New(errs.CodeInvalidRequest, "telemetry sampling ratio must be between 0 and 1")
	}
	switch {
	case c.SamplingDisabled:
		c.SamplingRatio = 0
	case c.SamplingRatio == 0:
		c.SamplingRatio = DefaultSamplingRatio
	}
	if c.ServiceInstanceID == "" {
		c.ServiceInstanceID = uuid.NewString()
	}
	if strings.TrimSpace(c.Endpoint) == "" {
		return c, nil, nil
	}
	ep, err := parseEndpoint(strings.TrimSpace(c.Endpoint), c.Insecure)
	if err != nil {
		return c, nil, err
	}
	return c, &ep, nil
}

func parseEndpoint(s string, insecure bool) (endpoint, error) {
	if !strings.Contains(s, "://") {
		u, err := url.Parse("//" + s)
		if err != nil || u.Host == "" || u.Path != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return endpoint{}, errs.New(errs.CodeInvalidRequest, "telemetry endpoint must be host:port or an http(s) URL")
		}
		return endpoint{host: u.Host, insecure: insecure}, nil
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return endpoint{}, errs.New(errs.CodeInvalidRequest, "telemetry endpoint must be host:port or an http(s) URL")
	}
	ep := endpoint{host: u.Host, basePath: strings.TrimRight(u.Path, "/")}
	switch u.Scheme {
	case "https":
		if insecure {
			return endpoint{}, errs.New(errs.CodeInvalidRequest, "telemetry endpoint uses https but insecure is set")
		}
	case "http":
		if !insecure {
			return endpoint{}, errs.New(errs.CodeInvalidRequest, "a plain http telemetry endpoint requires insecure=true")
		}
		ep.insecure = true
	default:
		return endpoint{}, errs.New(errs.CodeInvalidRequest, "telemetry endpoint scheme must be http or https")
	}
	return ep, nil
}

func newResource(ctx context.Context, cfg Config) (*resource.Resource, error) {
	res, err := resource.New(ctx,
		resource.WithSchemaURL(semconv.SchemaURL),
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(version.Get().Version),
			semconv.ServiceInstanceID(cfg.ServiceInstanceID),
		),
	)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInvalidRequest, err, "build telemetry resource")
	}
	return res, nil
}

func traceOptions(ep endpoint, cfg Config) []otlptracehttp.Option {
	opts := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(ep.host),
		otlptracehttp.WithURLPath(ep.basePath + "/v1/traces"),
		otlptracehttp.WithTimeout(cfg.ExportTimeout),
		otlptracehttp.WithCompression(otlptracehttp.GzipCompression),
	}
	if ep.insecure {
		opts = append(opts, otlptracehttp.WithInsecure())
	}
	return opts
}

func metricOptions(ep endpoint, cfg Config) []otlpmetrichttp.Option {
	opts := []otlpmetrichttp.Option{
		otlpmetrichttp.WithEndpoint(ep.host),
		otlpmetrichttp.WithURLPath(ep.basePath + "/v1/metrics"),
		otlpmetrichttp.WithTimeout(cfg.ExportTimeout),
		otlpmetrichttp.WithCompression(otlpmetrichttp.GzipCompression),
	}
	if ep.insecure {
		opts = append(opts, otlpmetrichttp.WithInsecure())
	}
	return opts
}

// TracerProvider returns the tracer provider.
func (t *Telemetry) TracerProvider() trace.TracerProvider { return t.tp }

// MeterProvider returns the meter provider.
func (t *Telemetry) MeterProvider() metric.MeterProvider { return t.mp }

// Propagator returns the W3C trace-context and baggage propagator.
func (t *Telemetry) Propagator() propagation.TextMapPropagator { return t.propagator }

// Install sets the OpenTelemetry globals (tracer provider, meter provider,
// propagator and error handler) to t. It is the only place this codebase
// sets them; call it once from main, for libraries that read the globals.
func (t *Telemetry) Install() {
	otel.SetTracerProvider(t.tp)
	otel.SetMeterProvider(t.mp)
	otel.SetTextMapPropagator(t.propagator)
	logger := t.logger
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		logger.LogAttrs(context.Background(), slog.LevelWarn, "opentelemetry error", slog.String("error", err.Error()))
	}))
}

// Shutdown flushes pending spans and metrics and stops the providers. Pass a
// context with a deadline; telemetry still pending at the deadline is lost.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	var errList []error
	if err := t.tp.Shutdown(ctx); err != nil {
		errList = append(errList, fmt.Errorf("shut down tracer provider: %w", err))
	}
	if err := t.mp.Shutdown(ctx); err != nil {
		errList = append(errList, fmt.Errorf("shut down meter provider: %w", err))
	}
	return errors.Join(errList...)
}
