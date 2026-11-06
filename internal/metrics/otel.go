package metrics

import (
	"context"
	"strings"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// signalURL appends the OTLP signal path to a collector base URL, following
// the OTEL_EXPORTER_OTLP_ENDPOINT semantics ("http://host:4318" →
// "http://host:4318/v1/traces"). The exporter's WithEndpointURL option uses
// the path as-is, so the signal path has to be added here.
func signalURL(endpoint, signal string) string {
	return strings.TrimRight(endpoint, "/") + "/v1/" + signal
}

// newOTLPMetricExporter builds the periodic OTLP/HTTP metric reader for
// endpoint. Export is asynchronous, so a reachable collector is not required
// to construct it.
func newOTLPMetricExporter(ctx context.Context, endpoint string) (metric.Reader, error) {
	exporter, err := otlpmetrichttp.New(ctx, otlpmetrichttp.WithEndpointURL(signalURL(endpoint, "metrics")))
	if err != nil {
		return nil, err
	}
	return metric.NewPeriodicReader(exporter), nil
}

// newTracerProvider builds the OTLP/HTTP tracer provider: batch export with
// parent-based trace-ID ratio sampling, so a sampled upstream trace stays
// sampled while new root spans follow ratio. The ratio is clamped to [0,1].
func newTracerProvider(ctx context.Context, endpoint string, ratio float64) (*sdktrace.TracerProvider, error) {
	exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(signalURL(endpoint, "traces")))
	if err != nil {
		return nil, err
	}
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	return sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))),
	), nil
}
