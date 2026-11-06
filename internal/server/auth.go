// Package server provides the AgentHub HTTP server.
//
// Authentication has two modes. With `auth.issuer` set the server validates
// Shadow SSO OIDC bearer tokens against the provider's JWKS (M3.3,
// SSOAuthenticator); without it the identity arrives in the X-Auth-Subject /
// X-Auth-Scopes headers set by a trusted front proxy (M3.2,
// HeaderAuthenticator, kept as the dev fallback).
package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"go.pilab.hu/agenthub/internal/auth/sso"
	"go.pilab.hu/agenthub/internal/authz"
)

// Authenticator validates a request and returns the caller's subject, scopes,
// and namespace roles. The server installs SSOAuthenticator when `auth.issuer`
// is configured and HeaderAuthenticator otherwise.
type Authenticator interface {
	// Authenticate returns nil on success and populates the echo context with
	// the caller identity (subject, scopes, roles) via context keys.
	Authenticate(next echo.HandlerFunc) echo.HandlerFunc
}

// Echo context keys holding the authenticated identity.
const (
	// SubjectKey is the echo context key for the authenticated subject.
	SubjectKey = "auth.subject"
	// ScopesKey is the echo context key for the authenticated scopes.
	ScopesKey = "auth.scopes"
)

// Request headers carrying the identity while Shadow SSO is not configured
// (dev and trusted-front-proxy deployments).
const (
	// SubjectHeader carries the authenticated subject id.
	SubjectHeader = "X-Auth-Subject"
	// ScopesHeader carries the comma- or space-separated OAuth scopes.
	ScopesHeader = "X-Auth-Scopes"
)

// setIdentity stores id on the echo context under SubjectKey/ScopesKey and
// mirrors it into the request context so Connect handlers (which only see a
// context.Context) can resolve the caller with authz.IdentityFromContext.
func setIdentity(c echo.Context, id authz.Identity) {
	c.Set(SubjectKey, id.Subject)
	c.Set(ScopesKey, id.Scopes)
	c.SetRequest(c.Request().WithContext(authz.WithIdentity(c.Request().Context(), id)))
}

// setAnonymous installs the empty identity used for requests that present
// no credentials.
func setAnonymous(c echo.Context) {
	c.Set(SubjectKey, "")
	c.Set(ScopesKey, []string{})
	c.SetRequest(c.Request().WithContext(
		authz.WithIdentity(c.Request().Context(), authz.Identity{}),
	))
}

// HeaderAuthenticator trusts the SubjectHeader/ScopesHeader request headers
// and installs them as the caller identity. It is the M3.2 placeholder and
// stays in use while no `auth.issuer` is configured: the headers are meant
// to be set by a trusted front proxy (or by a developer on localhost), so a
// deployment exposed directly to clients must configure Shadow SSO instead.
type HeaderAuthenticator struct{}

// Authenticate implements Authenticator.
func (HeaderAuthenticator) Authenticate(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		setIdentity(c, authz.Identity{
			Subject: strings.TrimSpace(c.Request().Header.Get(SubjectHeader)),
			Scopes:  authz.ParseScopes(c.Request().Header.Get(ScopesHeader)),
		})
		return next(c)
	}
}

// Middleware adapts an Authenticator to echo.MiddlewareFunc.
func (a HeaderAuthenticator) Middleware() echo.MiddlewareFunc {
	return a.Authenticate
}

// SSOAuthenticator validates Shadow SSO OIDC bearer tokens (M3.3). It is
// installed whenever `auth.issuer` is configured and replaces the header
// trust entirely: only a valid `Authorization: Bearer <jwt>` yields an
// identity, the subject comes from the token's `sub` claim and the scopes
// from its `scope`/`scopes` claims (falling back to the verifier's default
// scopes when the token carries none). Requests without a bearer token stay
// anonymous, so public reads keep working while every privileged RPC fails
// its authz check.
type SSOAuthenticator struct {
	// Verifier validates tokens against the provider's JWKS.
	Verifier *sso.Verifier
}

// Authenticate implements Authenticator.
func (a SSOAuthenticator) Authenticate(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		token, ok := bearerToken(c.Request().Header.Get(echo.HeaderAuthorization))
		if !ok {
			setAnonymous(c)
			return next(c)
		}
		claims, err := a.Verifier.Verify(token)
		if err != nil {
			// The cause (expired, wrong audience, unknown key, ...) stays
			// server-side; the client only learns that the token is bad.
			return echo.NewHTTPError(http.StatusUnauthorized, "invalid bearer token")
		}
		setIdentity(c, authz.Identity{Subject: claims.Subject, Scopes: claims.Scopes})
		return next(c)
	}
}

// Middleware adapts an Authenticator to echo.MiddlewareFunc.
func (a SSOAuthenticator) Middleware() echo.MiddlewareFunc {
	return a.Authenticate
}

// bearerToken extracts the token from an Authorization header value.
func bearerToken(header string) (string, bool) {
	const prefix = "bearer "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	return token, token != ""
}

// NoopAuthenticator accepts every request as anonymous. It backs
// SSOAuthenticator for requests that present no bearer token and is used
// directly by callers that deliberately disable authentication.
type NoopAuthenticator struct{}

// Authenticate implements Authenticator.
func (NoopAuthenticator) Authenticate(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		setAnonymous(c)
		return next(c)
	}
}

// Middleware adapts an Authenticator to echo.MiddlewareFunc.
func (a NoopAuthenticator) Middleware() echo.MiddlewareFunc {
	return a.Authenticate
}

// SubjectFromContext returns the subject stored by the auth interceptor in
// ctx, or "" for anonymous callers.
func SubjectFromContext(ctx context.Context) string {
	return authz.IdentityFromContext(ctx).Subject
}
