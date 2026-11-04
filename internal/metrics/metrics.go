// Package metrics wires OpenTelemetry instruments to a Prometheus exporter.
package metrics

import (
	"context"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
	promexporter "go.opentelemetry.io/otel/exporters/prometheus"
	otelmetric "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/sdk/metric"
)

// Metrics holds the OTel meter provider and the Prometheus registry.
type Metrics struct {
	Registry      *prometheus.Registry
	MeterProvider *metric.MeterProvider
	HTTPRequests  otelmetric.Int64Counter
	HTTPDuration  otelmetric.Float64Histogram
}

// New builds metrics instrumentation. If disabled, returns a no-op Metrics.
func New(enabled bool) (*Metrics, error) {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	if !enabled {
		mp := metric.NewMeterProvider()
		meter := mp.Meter("go.pilab.hu/agenthub")
		req, _ := meter.Int64Counter("agenthub.http.requests", otelmetric.WithDescription("HTTP requests"))
		dur, _ := meter.Float64Histogram("agenthub.http.request.duration", otelmetric.WithUnit("s"))
		return &Metrics{Registry: reg, MeterProvider: mp, HTTPRequests: req, HTTPDuration: dur}, nil
	}

	exporter, err := promexporter.New(promexporter.WithRegisterer(reg))
	if err != nil {
		return nil, err
	}
	mp := metric.NewMeterProvider(metric.WithReader(exporter))
	meter := mp.Meter("go.pilab.hu/agenthub")
	req, err := meter.Int64Counter("agenthub.http.requests", otelmetric.WithDescription("HTTP requests"))
	if err != nil {
		return nil, err
	}
	dur, err := meter.Float64Histogram("agenthub.http.request.duration", otelmetric.WithUnit("s"), otelmetric.WithDescription("HTTP request duration"))
	if err != nil {
		return nil, err
	}
	return &Metrics{Registry: reg, MeterProvider: mp, HTTPRequests: req, HTTPDuration: dur}, nil
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

// Shutdown flushes and closes the meter provider.
func (m *Metrics) Shutdown(ctx context.Context) error {
	if m != nil && m.MeterProvider != nil {
		return m.MeterProvider.Shutdown(ctx)
	}
	return nil
}
