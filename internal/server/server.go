// Package server builds the AgentHub HTTP server.
package server

import (
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/uptrace/bun"

	"go.pilab.hu/agenthub/gen/go/agenthub/catalog/v1/catalogv1connect"
	"go.pilab.hu/agenthub/gen/go/agenthub/federation/v1/federationv1connect"
	"go.pilab.hu/agenthub/gen/go/agenthub/identity/v1/identityv1connect"
	"go.pilab.hu/agenthub/gen/go/agenthub/registry/v1/registryv1connect"
	"go.pilab.hu/agenthub/internal/api"
	"go.pilab.hu/agenthub/internal/config"
	"go.pilab.hu/agenthub/internal/metrics"
	"go.pilab.hu/agenthub/internal/web"
)

// New builds an echo server with routes and connect mounts.
func New(cfg *config.Config, database *bun.DB, m *metrics.Metrics, w *web.Handler) *echo.Echo {
	if cfg == nil {
		cfg = &config.Config{}
		cfg.Server.HTTPAddr = ":8080"
		cfg.Server.MetricsAddr = ":9100"
		cfg.Server.MaxRequestBytes = 4194304
		cfg.Server.PublicURL = "http://localhost:8080"
	}
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

	if cfg.Server.MaxRequestBytes > 0 {
		e.Use(middleware.BodyLimit(fmt.Sprintf("%d", cfg.Server.MaxRequestBytes)))
	}
	e.Use(middleware.Recover())
	e.Use(middleware.RequestID())
	e.Use(accessLogMiddleware())
	if m != nil {
		e.Use(metricsMiddleware(m))
	}

	health := &healthHandler{db: database}
	e.GET("/healthz", health.healthz)
	e.GET("/readyz", health.readyz)

	if w != nil {
		e.GET("/", w.Index)
	}
	e.StaticFS("/static", web.StaticFS())

	catalogH := api.NewCatalog()
	registryH := api.NewRegistry()
	federationH := api.NewFederation(cfgHubID(cfg), cfg.Server.PublicURL)
	identityH := api.NewIdentity()

	mount := func(prefix string, path string, h http.Handler) {
		e.Any(path+"/*", echo.WrapHandler(h))
		e.Any(path, echo.WrapHandler(h))
	}
	cp, ch := catalogv1connect.NewCatalogServiceHandler(catalogH)
	mount(cp, cp, ch)
	rp, rh := registryv1connect.NewRegistryServiceHandler(registryH)
	mount(rp, rp, rh)
	fp, fh := federationv1connect.NewFederationServiceHandler(federationH)
	mount(fp, fp, fh)
	ip, ih := identityv1connect.NewIdentityServiceHandler(identityH)
	mount(ip, ip, ih)

	e.GET("/.well-known/agenthub.json", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{
			"id":                cfgHubID(cfg),
			"base_url":          cfg.Server.PublicURL,
			"protocol":          1,
			"signing_algorithm": "ed25519",
		})
	})

	return e
}

func cfgHubID(cfg *config.Config) string {
	u := cfg.Server.PublicURL
	if i := indexOf(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := indexOf(u, "/"); i >= 0 {
		u = u[:i]
	}
	if u == "" {
		u = "agenthub.pilab.hu"
	}
	return u
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
