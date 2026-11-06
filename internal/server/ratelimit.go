package server

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"

	"go.pilab.hu/agenthub/internal/audit"
)

const (
	// rateLimitIdleTTL is how long an unused per-IP bucket stays in memory.
	rateLimitIdleTTL = 5 * time.Minute
	// rateLimitSweepInterval is the minimum spacing between bucket sweeps.
	rateLimitSweepInterval = time.Minute
)

// rateLimitSkipped reports whether a path bypasses rate limiting. Liveness
// probes and static assets must never be throttled.
func rateLimitSkipped(path string) bool {
	switch {
	case path == "/healthz", path == "/readyz":
		return true
	case strings.HasPrefix(path, "/static/"):
		return true
	}
	return false
}

// bucket holds one client's remaining tokens.
type bucket struct {
	tokens  float64
	updated time.Time
}

// tokenBucketLimiter is a simple per-key token bucket limiter: each key
// (client IP) refills at `rate` tokens per second up to `burst` tokens.
type tokenBucketLimiter struct {
	mu        sync.Mutex
	rate      float64 // tokens per second
	burst     float64
	buckets   map[string]*bucket
	lastSweep time.Time
}

// newTokenBucketLimiter builds a limiter allowing perSec requests per second
// per key, with a burst of perSec requests.
func newTokenBucketLimiter(perSec int) *tokenBucketLimiter {
	if perSec < 1 {
		perSec = 1
	}
	rate := float64(perSec)
	return &tokenBucketLimiter{
		rate:      rate,
		burst:     rate,
		buckets:   map[string]*bucket{},
		lastSweep: time.Now(),
	}
}

// allow consumes one token for key. It reports whether the request may
// proceed, the tokens left in the bucket and the seconds until one token is
// available again (only meaningful when the request is denied).
func (l *tokenBucketLimiter) allow(key string, now time.Time) (bool, float64, float64) {
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, updated: now}
		l.buckets[key] = b
	} else if elapsed := now.Sub(b.updated).Seconds(); elapsed > 0 {
		b.tokens = math.Min(l.burst, b.tokens+elapsed*l.rate)
		b.updated = now
	}
	l.sweep(now)

	if b.tokens >= 1 {
		b.tokens--
		return true, b.tokens, 0
	}
	return false, b.tokens, (1 - b.tokens) / l.rate
}

// sweep drops buckets idle for longer than rateLimitIdleTTL so an attacker
// cannot grow the map with spoofed addresses. Callers must hold l.mu.
func (l *tokenBucketLimiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < rateLimitSweepInterval {
		return
	}
	l.lastSweep = now
	for k, b := range l.buckets {
		if now.Sub(b.updated) > rateLimitIdleTTL {
			delete(l.buckets, k)
		}
	}
}

// rateLimitMiddleware rate-limits requests per client IP with a token bucket
// and sets RateLimit-* headers (RFC-ish draft). A non-positive perSec
// disables the limiter. Denied requests are recorded in store when non-nil;
// audit failures never fail the request.
func rateLimitMiddleware(perSec int, store audit.AuditStore) echo.MiddlewareFunc {
	if perSec <= 0 {
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return next
		}
	}
	limiter := newTokenBucketLimiter(perSec)
	limit := strconv.Itoa(perSec)

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			req := c.Request()
			if rateLimitSkipped(req.URL.Path) {
				return next(c)
			}
			ip := c.RealIP()
			allowed, remaining, retry := limiter.allow(ip, time.Now())

			h := c.Response().Header()
			h.Set("RateLimit-Limit", limit)
			if allowed {
				h.Set("RateLimit-Remaining", strconv.Itoa(int(remaining)))
				return next(c)
			}

			reset := int(math.Ceil(retry))
			if reset < 1 {
				reset = 1
			}
			h.Set("RateLimit-Remaining", "0")
			h.Set("RateLimit-Reset", strconv.Itoa(reset))
			h.Set("Retry-After", strconv.Itoa(reset))
			if store != nil {
				detail, err := json.Marshal(map[string]any{
					"method":              req.Method,
					"path":                req.URL.Path,
					"retry_after_seconds": reset,
				})
				if err == nil {
					_ = store.Log("ratelimit.blocked", ip, "", "", "", string(detail))
				}
			}
			return echo.NewHTTPError(http.StatusTooManyRequests, "rate limit exceeded")
		}
	}
}
