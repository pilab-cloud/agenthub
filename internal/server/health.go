package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/uptrace/bun"

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

func metricsMiddleware(m interface {
	Record(route, method string, status int, dur float64)
},
) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
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
			m.Record(route, c.Request().Method, status, time.Since(start).Seconds())
			return err
		}
	}
}
