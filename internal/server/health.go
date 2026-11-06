package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/uptrace/bun"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	oteltrace "go.opentelemetry.io/otel/trace"

	"go.pilab.hu/agenthub/internal/metrics"
	"go.pilab.hu/agenthub/internal/version"
)

type healthHandler struct {
	db *bun.DB
}

func (h *healthHandler) healthz(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]any{
		"status":  "ok",
		"version": version.String(),
	})
}

func (h *healthHandler) readyz(c echo.Context) error {
	checks := map[string]string{}
	status := "ready"
	code := http.StatusOK
	if h.db != nil {
		ctx, cancel := context.WithTimeout(c.Request().Context(), 2*time.Second)
		defer cancel()
		if err := h.db.PingContext(ctx); err != nil {
			checks["database"] = "unreachable: " + err.Error()
			status = "unavailable"
			code = http.StatusServiceUnavailable
		} else {
			checks["database"] = "ok"
		}
	} else {
		checks["database"] = "not configured"
	}
	w := c.Response().Writer
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "checks": checks})
	return nil
}

func accessLogMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			return next(c)
		}
	}
}

func metricsMiddleware(m *metrics.Metrics) echo.MiddlewareFunc {
	var tracer oteltrace.Tracer
	if m != nil && m.TracerProvider != nil {
		tracer = m.TracerProvider.Tracer("go.pilab.hu/agenthub/server")
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			var span oteltrace.Span
			if tracer != nil {
				var ctx context.Context
				ctx, span = tracer.Start(c.Request().Context(), spanName(c))
				c.SetRequest(c.Request().WithContext(ctx))
			}

			err := next(c)
			route := c.Path()
			if route == "" {
				route = c.Request().URL.Path
			}
			status := c.Response().Status
			if err != nil {
				var he *echo.HTTPError
				if errors.As(err, &he) {
					status = he.Code
				}
			}
			// M3.7: the span carries the same low-cardinality attributes as
			// the Prometheus instruments (registered route template).
			if span != nil {
				span.SetAttributes(
					semconv.HTTPRequestMethodKey.String(c.Request().Method),
					semconv.HTTPRouteKey.String(route),
					semconv.HTTPResponseStatusCodeKey.Int(status),
				)
				if status >= http.StatusInternalServerError {
					span.SetStatus(codes.Error, http.StatusText(status))
				}
				span.End()
			}
			if m != nil {
				m.Record(route, c.Request().Method, status, time.Since(start).Seconds())
			}
			return err
		}
	}
}

// spanName builds the server span name from the matched route template
// ("GET /healthz"), falling back to the bare method for unmatched requests so
// the span name stays low-cardinality.
func spanName(c echo.Context) string {
	if p := c.Path(); p != "" {
		return c.Request().Method + " " + p
	}
	return c.Request().Method
}
