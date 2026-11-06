// Package server builds the AgentHub HTTP server.
package server

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/uptrace/bun"

	"go.pilab.hu/agenthub/gen/go/agenthub/catalog/v1/catalogv1connect"
	"go.pilab.hu/agenthub/gen/go/agenthub/federation/v1/federationv1connect"
	"go.pilab.hu/agenthub/gen/go/agenthub/identity/v1/identityv1connect"
	"go.pilab.hu/agenthub/gen/go/agenthub/registry/v1/registryv1connect"
	"go.pilab.hu/agenthub/internal/api"
	"go.pilab.hu/agenthub/internal/audit"
	"go.pilab.hu/agenthub/internal/auth/sso"
	"go.pilab.hu/agenthub/internal/authz"
	"go.pilab.hu/agenthub/internal/catalog"
	"go.pilab.hu/agenthub/internal/config"
	"go.pilab.hu/agenthub/internal/metrics"
	"go.pilab.hu/agenthub/internal/registry"
	"go.pilab.hu/agenthub/internal/web"
)

// New builds an echo server with routes and connect mounts.
func New(cfg *config.Config, database *bun.DB, m *metrics.Metrics, w *web.Handler) *echo.Echo {
	if cfg == nil {
		cfg = &config.Config{}
		cfg.Server.HTTPAddr = ":8080"
		cfg.Server.MetricsAddr = ":9100"
		cfg.Server.MaxRequestBytes = 4194304
		cfg.Server.RateLimitPerSec = 100
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
	// M3.4: the rate limiter and the registry handlers share one audit
	// store, so ListAudit returns every recorded entry.
	auditStore := newAuditStore(database)
	e.Use(rateLimitMiddleware(cfg.Server.RateLimitPerSec, auditStore))
	e.Use(newAuthenticator(cfg).Authenticate)
	// M3.5: the BFF session cookie is a second identity source, consulted
	// only when no bearer token or proxy header produced one.
	bff := newBFF(cfg)
	if bff != nil {
		e.Use(bff.sessionMiddleware())
	}
	e.Use(accessLogMiddleware())
	if m != nil {
		e.Use(metricsMiddleware(m))
	}

	health := &healthHandler{db: database}
	e.GET("/healthz", health.healthz)
	e.GET("/readyz", health.readyz)

	// M3.5: OIDC BFF routes. Registered after the middleware above so the
	// session middleware applies to them too.
	if bff != nil {
		e.GET("/auth/login", bff.login)
		e.GET("/auth/callback", bff.callback)
		// GET is the browser-facing logout; the API table in
		// docs/architecture/api.md §4.2 documents POST for forms.
		e.GET("/auth/logout", bff.logout)
		e.POST("/auth/logout", bff.logout)
	}

	if w != nil {
		e.GET("/", w.Index)
		e.GET("/assets", w.Catalog)
		e.GET("/icon/:digest", w.Icon)
		e.GET("/ns/:slug", w.Namespace)
		e.GET("/a/:ns/:kind/:name", w.Asset)
		e.GET("/a/:ns/:kind/:name/:version", w.Version)
		e.POST("/dashboard/:namespace/:kind/:name/publish", w.PublishDraft)
	}
	e.StaticFS("/static", web.StaticFS())

	hubID := cfgHubID(cfg)
	var regStore registry.Store
	var catStore catalog.Store
	if database != nil {
		regStore = registry.NewPostgresNamespaceStore(database, hubID)
		catStore = catalog.NewPostgresCatalogStore(database, hubID)
	} else {
		regStore = registry.NewMemStore()
		catStore = catalog.NewMemStore()
	}
	// Share the stores (and an in-memory blob store) with the web handler so
	// the public catalog page reads the same data the API serves.
	blobs := catalog.NewMemBlobs()
	if w != nil {
		w.RegistryStore = regStore
		w.CatalogStore = catStore
		w.BlobStore = blobs
	}
	catalogH := api.NewCatalogWithStore(catStore).WithBlobs(blobs, cfg.Server.PublicURL)
	// M3.6: role bindings are persisted in agenthub.namespace_roles when a
	// database is configured, in-memory otherwise. M3.4: the registry shares
	// the audit store with the rate limiter above.
	registryH := api.NewRegistryWithAudit(regStore, catStore, newRoleStore(database), hubID, auditStore).
		WithBlobs(blobs, cfg.Server.PublicURL)
	// M4.1: the federation handler signs snapshots and sync pages with the
	// hub key (signing.hub_key_path, ephemeral in development) and reads the
	// same catalog store the CatalogService serves.
	hubKey, err := api.LoadHubKey(cfg.Signing.HubKeyPath)
	if err != nil {
		panic(fmt.Errorf("hub signing key: %w", err))
	}
	federationH := api.NewFederationWithBlobs(hubID, cfg.Server.PublicURL, catStore, hubKey, blobs)
	identityH := api.NewIdentity()

	mount := func(prefix string, h http.Handler) {
		// connect v2 returns the service prefix with a trailing slash;
		// normalize it so the wildcard route matches "/<prefix>/<Method>"
		// instead of the never-matching "//<Method>".
		p := strings.TrimSuffix(prefix, "/")
		e.Any(p+"/*", echo.WrapHandler(h))
		e.Any(p, echo.WrapHandler(h))
	}
	cp, ch := catalogv1connect.NewCatalogServiceHandler(catalogH)
	mount(cp, ch)
	rp, rh := registryv1connect.NewRegistryServiceHandler(registryH)
	mount(rp, rh)
	fp, fh := federationv1connect.NewFederationServiceHandler(federationH)
	mount(fp, fh)
	ip, ih := identityv1connect.NewIdentityServiceHandler(identityH)
	mount(ip, ih)

	e.GET(api.BlobPath+":sha", getBlob(blobs))
	e.PUT(api.BlobPath+":sha", putBlob(blobs, cfg.Server.MaxRequestBytes))

	e.GET("/.well-known/agenthub.json", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{
			"id":                cfgHubID(cfg),
			"base_url":          cfg.Server.PublicURL,
			"protocol":          1,
			"signing_algorithm": "ed25519",
			// M4.1: the key that signs catalog snapshots and sync pages, so
			// peers that do not speak ConnectRPC can pin it (federation.md §3.1).
			"public_key": base64.StdEncoding.EncodeToString(federationH.PublicKey()),
		})
	})

	return e
}

// newAuthenticator picks the authentication mode: Shadow SSO token
// validation when `auth.issuer` is configured, the trusted-proxy header
// fallback otherwise (dev). A broken SSO block aborts startup instead of
// silently downgrading to header trust; `serve --check-config` reports the
// same problem before the server binds a port.
func newAuthenticator(cfg *config.Config) Authenticator {
	if cfg.Auth.Issuer == "" {
		return HeaderAuthenticator{}
	}
	v, err := sso.New(sso.Config{
		Issuer:            cfg.Auth.Issuer,
		Audience:          cfg.Auth.Audience,
		AudienceAllowlist: cfg.Auth.AudienceAllowlist,
		JWKSURL:           cfg.Auth.JWKSURL,
		DiscoveryURL:      cfg.Auth.DiscoveryURL,
		ClockSkew:         cfg.Auth.ClockSkew,
		JWKSCacheTTL:      cfg.Auth.JWKSCacheTTL,
		// auth.scopes is the deployment's scope vocabulary, not a grant:
		// a token without a scope claim stays unprivileged (fail closed).
	})
	if err != nil {
		panic(fmt.Errorf("sso authentication: %w", err))
	}
	return SSOAuthenticator{Verifier: v}
}

// newAuditStore picks the audit backend: Postgres when a database is
// configured, the in-memory store otherwise (M3.1 default).
func newAuditStore(database *bun.DB) audit.AuditStore {
	if database != nil {
		return audit.NewPostgresStore(database)
	}
	return audit.NewMemStore()
}

// newRoleStore picks the RBAC backend: Postgres when a database is
// configured so bindings survive a restart, the in-memory store otherwise
// (M3.2 default).
func newRoleStore(database *bun.DB) authz.Store {
	if database != nil {
		return authz.NewPostgresRoleStore(database)
	}
	return authz.NewMemStore()
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
