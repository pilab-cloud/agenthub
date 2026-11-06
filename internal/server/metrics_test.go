package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"go.pilab.hu/agenthub/internal/metrics"
)

// spanAttrs flattens a span's attributes into a lookup map for assertions.
func spanAttrs(span sdktrace.ReadOnlySpan) map[string]any {
	attrs := map[string]any{}
	for _, kv := range span.Attributes() {
		attrs[string(kv.Key)] = kv.Value.AsInterface()
	}
	return attrs
}

// TestMetricsMiddleware checks M3.7 end to end: one request produces a
// Prometheus sample with the route template and one server span with the
// semconv attributes.
func TestMetricsMiddleware(t *testing.T) {
	m, err := metrics.New(metrics.Options{Enabled: true, SampleRatio: 1})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, m.Shutdown(context.Background())) })

	// Observe spans without a collector: swap in a recorder-backed provider.
	spans := tracetest.NewSpanRecorder()
	m.TracerProvider = sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(spans),
	)

	e := New(nil, nil, m, nil)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	promRec := httptest.NewRecorder()
	m.Handler().ServeHTTP(promRec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, promRec.Code)
	body := promRec.Body.String()
	require.Contains(t, body, "agenthub_http_requests_total")
	require.Contains(t, body, `route="/healthz"`)

	require.Len(t, spans.Ended(), 1)
	span := spans.Ended()[0]
	require.Equal(t, "GET /healthz", span.Name())
	require.Equal(t, codes.Unset, span.Status().Code)
	attrs := spanAttrs(span)
	require.Equal(t, "GET", attrs["http.request.method"])
	require.Equal(t, "/healthz", attrs["http.route"])
	require.Equal(t, int64(http.StatusOK), attrs["http.response.status_code"])
}

// TestMetricsMiddlewareErrorStatus: an HTTPError is recorded with its own
// status code and the span is marked as error.
func TestMetricsMiddlewareErrorStatus(t *testing.T) {
	m, err := metrics.New(metrics.Options{Enabled: true})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, m.Shutdown(context.Background())) })

	spans := tracetest.NewSpanRecorder()
	m.TracerProvider = sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(spans),
	)

	e := New(nil, nil, m, nil)
	e.GET("/boom", func(c echo.Context) error {
		return echo.NewHTTPError(http.StatusInternalServerError, "boom")
	})
	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusInternalServerError, rec.Code)

	require.Len(t, spans.Ended(), 1)
	span := spans.Ended()[0]
	require.Equal(t, "GET /boom", span.Name())
	require.Equal(t, codes.Error, span.Status().Code)
	require.Equal(t, "/boom", spanAttrs(span)["http.route"])
}
