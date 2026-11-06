package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	registryv1 "go.pilab.hu/agenthub/gen/go/agenthub/registry/v1"
	registryv1connect "go.pilab.hu/agenthub/gen/go/agenthub/registry/v1/registryv1connect"
	"go.pilab.hu/agenthub/internal/authz"
)

func TestHeaderAuthenticatorSetsIdentity(t *testing.T) {
	e := echo.New()
	e.Use(HeaderAuthenticator{}.Authenticate)

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
	req.Header.Set(SubjectHeader, "  alice  ")
	req.Header.Set(ScopesHeader, "agenthub.read, agenthub.admin")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, "alice", echoSubject)
	require.Equal(t, []string{"agenthub.read", "agenthub.admin"}, echoScopes)
	require.Equal(t, authz.Identity{
		Subject: "alice",
		Scopes:  []string{"agenthub.read", "agenthub.admin"},
	}, id)
	require.True(t, id.IsAdmin())
}

func TestHeaderAuthenticatorAnonymous(t *testing.T) {
	e := echo.New()
	e.Use(HeaderAuthenticator{}.Authenticate)

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
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, "", echoSubject)
	require.Equal(t, []string{}, echoScopes)
	require.Equal(t, authz.Identity{Scopes: []string{}}, id)
	require.False(t, id.Authenticated())
}

// TestRegistryRBACOverHTTP exercises the full M3.2 path: the header
// interceptor on the echo server, the identity propagated into the Connect
// handler, and the Registry RBAC checks.
func TestRegistryRBACOverHTTP(t *testing.T) {
	e := New(nil, nil, nil, nil)
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	client := registryv1connect.NewRegistryServiceClient(srv.Client(), srv.URL)
	ctx := context.Background()

	// anonymous CreateNamespace is rejected
	_, err := client.CreateNamespace(ctx, connect.NewRequest(&registryv1.CreateNamespaceRequest{Slug: "pilab"}))
	require.Error(t, err)
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	// an authenticated subject creates a namespace and becomes its owner
	create := connect.NewRequest(&registryv1.CreateNamespaceRequest{Slug: "pilab", DisplayName: "PiLab"})
	create.Header().Set(SubjectHeader, "alice")
	resp, err := client.CreateNamespace(ctx, create)
	require.NoError(t, err)
	require.Equal(t, "pilab", resp.Msg.Namespace.Slug)

	// the owner can register a signing key (owner >= publisher)
	key := connect.NewRequest(&registryv1.RegisterSigningKeyRequest{Namespace: "pilab", Kid: "k1", PublicKey: "pk"})
	key.Header().Set(SubjectHeader, "alice")
	_, err = client.RegisterSigningKey(ctx, key)
	require.NoError(t, err)

	// a subject without a binding is denied
	stranger := connect.NewRequest(&registryv1.RegisterSigningKeyRequest{Namespace: "pilab", Kid: "k2", PublicKey: "pk"})
	stranger.Header().Set(SubjectHeader, "bob")
	_, err = client.RegisterSigningKey(ctx, stranger)
	require.Error(t, err)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	// the admin scope alone (no subject) may create namespaces
	admin := connect.NewRequest(&registryv1.CreateNamespaceRequest{Slug: "ops"})
	admin.Header().Set(ScopesHeader, authz.ScopeAdmin)
	_, err = client.CreateNamespace(ctx, admin)
	require.NoError(t, err)

	// anonymous register on an existing namespace is unauthenticated
	anon := connect.NewRequest(&registryv1.RegisterSigningKeyRequest{Namespace: "pilab", Kid: "k3", PublicKey: "pk"})
	_, err = client.RegisterSigningKey(ctx, anon)
	require.Error(t, err)
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}
