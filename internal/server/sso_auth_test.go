package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	jose "github.com/go-jose/go-jose/v4"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	registryv1 "go.pilab.hu/agenthub/gen/go/agenthub/registry/v1"
	registryv1connect "go.pilab.hu/agenthub/gen/go/agenthub/registry/v1/registryv1connect"
	"go.pilab.hu/agenthub/internal/auth/sso"
	"go.pilab.hu/agenthub/internal/authz"
	"go.pilab.hu/agenthub/internal/config"
)

const (
	ssoTestIssuer   = "https://sso.example.test"
	ssoTestAudience = "agenthub"
	ssoTestKid      = "kid-1"
)

// ssoTestEnv is a miniature Shadow SSO: an RSA key, a JWKS endpoint served
// over httptest, and a Verifier pointed at it.
type ssoTestEnv struct {
	priv *rsa.PrivateKey
	jwks *httptest.Server
}

func newSSOTestEnv(t *testing.T) *ssoTestEnv {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	env := &ssoTestEnv{priv: priv}
	env.jwks = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key: &priv.PublicKey, KeyID: ssoTestKid, Algorithm: string(jose.RS256), Use: "sig",
		}}})
	}))
	t.Cleanup(env.jwks.Close)
	return env
}

// verifier builds an sso.Verifier for the environment's JWKS.
func (e *ssoTestEnv) verifier(t *testing.T) *sso.Verifier {
	t.Helper()
	v, err := sso.New(sso.Config{
		Issuer:   ssoTestIssuer,
		Audience: ssoTestAudience,
		JWKSURL:  e.jwks.URL,
	})
	require.NoError(t, err)
	return v
}

// token signs an RS256 JWT; mutate adjusts the claims (sub, exp, aud, ...).
func (e *ssoTestEnv) token(t *testing.T, mutate func(map[string]any)) string {
	t.Helper()
	now := time.Now()
	claims := map[string]any{
		"iss":   ssoTestIssuer,
		"aud":   ssoTestAudience,
		"sub":   "alice",
		"scope": "agenthub.read agenthub.publish",
		"iat":   now.Unix(),
		"exp":   now.Add(5 * time.Minute).Unix(),
	}
	if mutate != nil {
		mutate(claims)
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: e.priv},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", ssoTestKid),
	)
	require.NoError(t, err)
	payload, err := json.Marshal(claims)
	require.NoError(t, err)
	tok, err := signer.Sign(payload)
	require.NoError(t, err)
	raw, err := tok.CompactSerialize()
	require.NoError(t, err)
	return raw
}

func (e *ssoTestEnv) config() config.AuthConfig {
	return config.AuthConfig{
		Issuer:   ssoTestIssuer,
		Audience: ssoTestAudience,
		JWKSURL:  e.jwks.URL,
	}
}

func TestSSOAuthenticatorSetsIdentity(t *testing.T) {
	env := newSSOTestEnv(t)
	e := echo.New()
	e.Use(SSOAuthenticator{Verifier: env.verifier(t)}.Authenticate)

	var echoSubject any
	var echoScopes any
	var id authz.Identity
	e.GET("/", func(c echo.Context) error {
		echoSubject = c.Get(SubjectKey)
		echoScopes = c.Get(ScopesKey)
		id = authz.IdentityFromContext(c.Request().Context())
		return c.NoContent(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(echo.HeaderAuthorization, "Bearer "+env.token(t, nil))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, "alice", echoSubject)
	require.Equal(t, []string{"agenthub.read", "agenthub.publish"}, echoScopes)
	require.Equal(t, authz.Identity{
		Subject: "alice",
		Scopes:  []string{"agenthub.read", "agenthub.publish"},
	}, id)
}

func TestSSOAuthenticatorRejectsInvalidTokens(t *testing.T) {
	cases := map[string]func(map[string]any){
		"expired":   func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() },
		"wrong aud": func(c map[string]any) { c["aud"] = "someone-else" },
		"wrong iss": func(c map[string]any) { c["iss"] = "https://evil.example.test" },
		"garbage":   nil,
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			env := newSSOTestEnv(t)
			e := echo.New()
			e.Use(SSOAuthenticator{Verifier: env.verifier(t)}.Authenticate)
			e.GET("/", func(c echo.Context) error { return c.NoContent(http.StatusNoContent) })

			token := env.token(t, mutate)
			if name == "garbage" {
				token = "not.a.jwt"
			}
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set(echo.HeaderAuthorization, "Bearer "+token)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			require.Equal(t, http.StatusUnauthorized, rec.Code)
		})
	}
}

// TestSSOAuthenticatorDoesNotTrustHeaders: with Shadow SSO enabled the M3.2
// proxy headers are no longer an identity source.
func TestSSOAuthenticatorDoesNotTrustHeaders(t *testing.T) {
	env := newSSOTestEnv(t)
	e := echo.New()
	e.Use(SSOAuthenticator{Verifier: env.verifier(t)}.Authenticate)

	var id authz.Identity
	e.GET("/", func(c echo.Context) error {
		id = authz.IdentityFromContext(c.Request().Context())
		return c.NoContent(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(SubjectHeader, "mallory")
	req.Header.Set(ScopesHeader, authz.ScopeAdmin)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, authz.Identity{}, id)
	require.False(t, id.Authenticated())
}

// TestServerUsesSSOWhenIssuerConfigured exercises the full path over HTTP:
// server.New picks SSOAuthenticator from auth.issuer, the connect handler
// sees the token's identity, and bad or missing credentials never become a
// subject.
func TestServerUsesSSOWhenIssuerConfigured(t *testing.T) {
	env := newSSOTestEnv(t)
	cfg := &config.Config{}
	cfg.Server.HTTPAddr = ":8080"
	cfg.Server.RateLimitPerSec = 100
	cfg.Auth = env.config()

	e := New(cfg, nil, nil, nil)
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	client := registryv1connect.NewRegistryServiceClient(srv.Client(), srv.URL)
	ctx := context.Background()

	// a valid bearer token creates the namespace and becomes its owner
	create := connect.NewRequest(&registryv1.CreateNamespaceRequest{Slug: "pilab", DisplayName: "PiLab"})
	create.Header().Set(echo.HeaderAuthorization, "Bearer "+env.token(t, nil))
	resp, err := client.CreateNamespace(ctx, create)
	require.NoError(t, err)
	require.Equal(t, "pilab", resp.Msg.Namespace.Slug)

	// an expired token never reaches a handler: the middleware answers 401
	req, err := http.NewRequest(http.MethodPost,
		srv.URL+"/agenthub.registry.v1.RegistryService/CreateNamespace",
		strings.NewReader(""))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/connect+json")
	req.Header.Set(echo.HeaderAuthorization, "Bearer "+env.token(t, func(c map[string]any) {
		c["exp"] = time.Now().Add(-time.Hour).Unix()
	}))
	httpResp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = httpResp.Body.Close() }()
	require.Equal(t, http.StatusUnauthorized, httpResp.StatusCode)

	// without a token the caller is anonymous, not authenticated as anyone
	anon := connect.NewRequest(&registryv1.CreateNamespaceRequest{Slug: "anon"})
	_, err = client.CreateNamespace(ctx, anon)
	require.Error(t, err)
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}
