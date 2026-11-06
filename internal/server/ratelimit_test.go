package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"go.pilab.hu/agenthub/internal/audit"
	"go.pilab.hu/agenthub/internal/config"
)

func doGet(t *testing.T, e *echo.Echo, path, ip string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = ip + ":4242"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func serverWithRateLimit(perSec int) *echo.Echo {
	cfg := &config.Config{}
	cfg.Server.RateLimitPerSec = perSec
	return New(cfg, nil, nil, nil)
}

func TestRateLimitAllowsThenBlocks(t *testing.T) {
	e := serverWithRateLimit(3)
	const ip = "203.0.113.10"

	for i := 1; i <= 3; i++ {
		rec := doGet(t, e, "/.well-known/agenthub.json", ip)
		require.Equal(t, http.StatusOK, rec.Code, "request %d", i)
		require.Equal(t, "3", rec.Header().Get("RateLimit-Limit"))
	}

	rec := doGet(t, e, "/.well-known/agenthub.json", ip)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Equal(t, "3", rec.Header().Get("RateLimit-Limit"))
	require.Equal(t, "0", rec.Header().Get("RateLimit-Remaining"))
	require.Equal(t, "1", rec.Header().Get("Retry-After"))

	// a different client IP has its own bucket
	other := doGet(t, e, "/.well-known/agenthub.json", "203.0.113.11")
	require.Equal(t, http.StatusOK, other.Code)
}

func TestRateLimitSkipsHealthAndStatic(t *testing.T) {
	e := serverWithRateLimit(1)
	const ip = "203.0.113.20"

	for i := 0; i < 5; i++ {
		require.Equal(t, http.StatusOK, doGet(t, e, "/healthz", ip).Code, "healthz %d", i)
		require.Equal(t, http.StatusOK, doGet(t, e, "/readyz", ip).Code, "readyz %d", i)
		// static assets are never throttled (404 for a missing file is fine)
		require.NotEqual(t, http.StatusTooManyRequests, doGet(t, e, "/static/missing.css", ip).Code, "static %d", i)
	}

	// dynamic routes are still limited: first request allowed, second blocked
	require.Equal(t, http.StatusOK, doGet(t, e, "/.well-known/agenthub.json", ip).Code)
	require.Equal(t, http.StatusTooManyRequests, doGet(t, e, "/.well-known/agenthub.json", ip).Code)
}

func TestRateLimitDisabled(t *testing.T) {
	e := serverWithRateLimit(0)
	const ip = "203.0.113.30"
	for i := 0; i < 10; i++ {
		require.Equal(t, http.StatusOK, doGet(t, e, "/.well-known/agenthub.json", ip).Code, "request %d", i)
	}
}

func TestRateLimitSkippedPaths(t *testing.T) {
	require.True(t, rateLimitSkipped("/healthz"))
	require.True(t, rateLimitSkipped("/readyz"))
	require.True(t, rateLimitSkipped("/static/app.css"))
	require.False(t, rateLimitSkipped("/static"))
	require.False(t, rateLimitSkipped("/healthzz"))
	require.False(t, rateLimitSkipped("/ns/pilab"))
	require.False(t, rateLimitSkipped("/.well-known/agenthub.json"))
}

func TestRateLimitBlockedRequestsAreAudited(t *testing.T) {
	store := audit.NewMemStore()
	e := echo.New()
	e.Use(rateLimitMiddleware(1, store))
	e.GET("/probe", func(c echo.Context) error { return c.NoContent(http.StatusOK) })

	require.Equal(t, http.StatusOK, doGet(t, e, "/probe", "198.51.100.1").Code)
	require.Equal(t, http.StatusTooManyRequests, doGet(t, e, "/probe", "198.51.100.1").Code)

	entries, err := store.List(audit.Query{})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "ratelimit.blocked", entries[0].Action)
	require.Equal(t, "198.51.100.1", entries[0].Actor)
	require.Contains(t, entries[0].Detail, "/probe")
	require.Contains(t, entries[0].Detail, "retry_after_seconds")
}

func TestTokenBucketLimiter(t *testing.T) {
	l := newTokenBucketLimiter(2)
	now := time.Now()

	// burst of 2
	ok, remaining, _ := l.allow("198.51.100.1", now)
	require.True(t, ok)
	require.InDelta(t, 1, remaining, 1e-9)
	ok, _, _ = l.allow("198.51.100.1", now)
	require.True(t, ok)

	// bucket empty: denied, next token in 0.5s at 2 tokens/sec
	ok, _, retry := l.allow("198.51.100.1", now)
	require.False(t, ok)
	require.InDelta(t, 0.5, retry, 1e-9)

	// refilled after 500ms
	ok, _, _ = l.allow("198.51.100.1", now.Add(500*time.Millisecond))
	require.True(t, ok)

	// other keys are independent
	ok, _, _ = l.allow("198.51.100.2", now)
	require.True(t, ok)
}
