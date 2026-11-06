package metrics

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// startCollector runs a fake OTLP/HTTP collector counting the exports it
// receives on the standard OTLP paths.
func startCollector(t *testing.T, traces, metrics *atomic.Int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		switch r.URL.Path {
		case "/v1/traces":
			traces.Add(1)
		case "/v1/metrics":
			metrics.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPrometheusScrapeEndpoint(t *testing.T) {
	m, err := New(Options{Enabled: true})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, m.Shutdown(context.Background())) })

	m.Record("/healthz", "GET", 200, 0.25)

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "agenthub_http_requests_total")
	require.Contains(t, body, `route="/healthz"`)
	require.Contains(t, body, "agenthub_http_request_duration_seconds")
}

func TestNoOTLPEndpointIsNoop(t *testing.T) {
	t.Setenv(OTLPEndpointEnv, "")
	m, err := New(Options{Enabled: false, SampleRatio: 1})
	require.NoError(t, err)
	require.False(t, m.Enabled)
	require.Nil(t, m.TracerProvider)
	// Recording without any exporter must stay safe.
	m.Record("/healthz", "GET", 200, 0.01)
	require.NoError(t, m.Shutdown(context.Background()))
}

func TestOTLPEndpointExportsTracesAndMetrics(t *testing.T) {
	var traceExports, metricExports atomic.Int64
	srv := startCollector(t, &traceExports, &metricExports)
	t.Setenv(OTLPEndpointEnv, srv.URL)

	m, err := New(Options{Enabled: true, SampleRatio: 1})
	require.NoError(t, err)
	require.NotNil(t, m.TracerProvider)

	_, span := m.TracerProvider.Tracer("test").Start(context.Background(), "op")
	require.True(t, span.SpanContext().IsSampled())
	span.End()
	m.Record("/healthz", "GET", 200, 0.01)

	// Shutdown flushes the batched spans and the periodic metric reader.
	require.NoError(t, m.Shutdown(context.Background()))
	require.Positive(t, traceExports.Load(), "expected at least one trace export")
	require.Positive(t, metricExports.Load(), "expected at least one metric export")
}

func TestTracerProviderFollowsConfiguredEndpoint(t *testing.T) {
	var traces, exported atomic.Int64
	srv := startCollector(t, &traces, &exported)
	t.Setenv(OTLPEndpointEnv, "")

	m, err := New(Options{Enabled: true, OTelEndpoint: srv.URL, SampleRatio: 0.5})
	require.NoError(t, err)
	require.NotNil(t, m.TracerProvider)
	require.NoError(t, m.Shutdown(context.Background()))
}
