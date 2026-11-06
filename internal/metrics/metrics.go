// Package metrics wires OpenTelemetry instruments to a Prometheus exporter.
package metrics

import (
	"context"
	"errors"
	"net/http"
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
	promexporter "go.opentelemetry.io/otel/exporters/prometheus"
	otelmetric "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// OTLPEndpointEnv is the standard OTLP endpoint environment variable, used
// when Options.OTelEndpoint is empty.
const OTLPEndpointEnv = "OTEL_EXPORTER_OTLP_ENDPOINT"

// Options configures New.
type Options struct {
	// Enabled toggles the Prometheus exporter (observability.metrics_enabled).
	Enabled bool
	// OTelEndpoint is the OTLP/HTTP collector URL. When empty, the
	// OTEL_EXPORTER_OTLP_ENDPOINT environment variable is used; if that is
	// empty too, export is a no-op (no tracer provider, no OTLP metrics).
	OTelEndpoint string
	// SampleRatio is the trace-ID ratio sampler in [0,1], clamped
	// (observability.otel_sample_ratio); 0 samples no new root spans.
	SampleRatio float64
}

// Metrics holds the OTel meter/tracer providers and the Prometheus registry.
type Metrics struct {
	Enabled        bool
	Registry       *prometheus.Registry
	MeterProvider  *metric.MeterProvider
	TracerProvider *sdktrace.TracerProvider // nil without an OTLP endpoint: no-op tracing
	HTTPRequests   otelmetric.Int64Counter
	HTTPDuration   otelmetric.Float64Histogram
}

// New builds metrics and tracing instrumentation. Prometheus export follows
// opts.Enabled; OTLP export of both metrics and traces is enabled when an
// endpoint is configured (Options.OTelEndpoint or OTEL_EXPORTER_OTLP_ENDPOINT)
// and stays a no-op otherwise.
func New(opts Options) (*Metrics, error) {
	endpoint := opts.OTelEndpoint
	if endpoint == "" {
		endpoint = os.Getenv(OTLPEndpointEnv)
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	ctx := context.Background()
	m := &Metrics{Enabled: opts.Enabled, Registry: reg}

	var readers []metric.Reader
	if opts.Enabled {
		promExporter, err := promexporter.New(promexporter.WithRegisterer(reg))
		if err != nil {
			return nil, err
		}
		readers = append(readers, promExporter)
	}
	if endpoint != "" {
		otlpReader, err := newOTLPMetricExporter(ctx, endpoint)
		if err != nil {
			return nil, err
		}
		readers = append(readers, otlpReader)
	}
	if len(readers) == 0 {
		m.MeterProvider = metric.NewMeterProvider()
	} else {
		mpOpts := make([]metric.Option, 0, len(readers))
		for _, r := range readers {
			mpOpts = append(mpOpts, metric.WithReader(r))
		}
		m.MeterProvider = metric.NewMeterProvider(mpOpts...)
	}

	meter := m.MeterProvider.Meter("go.pilab.hu/agenthub")
	req, err := meter.Int64Counter("agenthub.http.requests", otelmetric.WithDescription("HTTP requests"))
	if err != nil {
		return nil, err
	}
	dur, err := meter.Float64Histogram("agenthub.http.request.duration", otelmetric.WithUnit("s"), otelmetric.WithDescription("HTTP request duration"))
	if err != nil {
		return nil, err
	}
	m.HTTPRequests, m.HTTPDuration = req, dur

	if endpoint != "" {
		m.TracerProvider, err = newTracerProvider(ctx, endpoint, opts.SampleRatio)
		if err != nil {
			return nil, err
		}
	}
	return m, nil
}

// Handler returns the Prometheus /metrics HTTP handler.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{Registry: m.Registry})
}

// Record records one HTTP request in the otel instruments.
func (m *Metrics) Record(route, method string, status int, dur float64) {
	if m == nil || m.HTTPRequests == nil {
		return
	}
	ctx := context.Background()
	attrs := attribute.NewSet(
		attribute.String("route", route),
		attribute.String("method", method),
		attribute.Int("status", status),
	)
	m.HTTPRequests.Add(ctx, 1, otelmetric.WithAttributeSet(attrs))
	if m.HTTPDuration != nil {
		dattrs := attribute.NewSet(
			attribute.String("route", route),
			attribute.String("method", method),
		)
		m.HTTPDuration.Record(ctx, dur, otelmetric.WithAttributeSet(dattrs))
	}
}

// Shutdown flushes and closes the tracer and meter providers.
func (m *Metrics) Shutdown(ctx context.Context) error {
	if m == nil {
		return nil
	}
	var err error
	if m.TracerProvider != nil {
		err = errors.Join(err, m.TracerProvider.Shutdown(ctx))
	}
	if m.MeterProvider != nil {
		err = errors.Join(err, m.MeterProvider.Shutdown(ctx))
	}
	return err
}
