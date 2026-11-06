package api

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	registryv1 "go.pilab.hu/agenthub/gen/go/agenthub/registry/v1"
	"go.pilab.hu/agenthub/internal/authz"
	"go.pilab.hu/agenthub/internal/catalog"
	"go.pilab.hu/agenthub/internal/registry"
)

// newAuthzRegistry builds a Registry with an exposed role store bound to
// hub "hub1" so tests can seed bindings directly.
func newAuthzRegistry() (*Registry, *authz.MemStore) {
	roles := authz.NewMemStore()
	return NewRegistryWithRoles(registry.NewMemStore(), catalog.NewMemStore(), roles, "hub1"), roles
}

func registerKey(t *testing.T, h *Registry, ctx context.Context, namespace, kid string) error {
	t.Helper()
	_, err := h.RegisterSigningKey(ctx, connect.NewRequest(&registryv1.RegisterSigningKeyRequest{
		Namespace: namespace,
		Kid:       kid,
		PublicKey: "pk",
	}))
	return err
}

func TestCreateNamespaceRequiresIdentity(t *testing.T) {
	h := NewRegistry()
	_, err := h.CreateNamespace(context.Background(), connect.NewRequest(&registryv1.CreateNamespaceRequest{
		Slug: "pilab",
	}))
	if err == nil {
		t.Fatal("expected error for anonymous caller")
	}
	if code := connect.CodeOf(err); code != connect.CodeUnauthenticated {
		t.Fatalf("code: %v, want %v", code, connect.CodeUnauthenticated)
	}
}

func TestCreateNamespaceGrantsOwner(t *testing.T) {
	h, roles := newAuthzRegistry()

	if _, err := h.CreateNamespace(identCtx("alice"), connect.NewRequest(&registryv1.CreateNamespaceRequest{
		Slug: "pilab",
	})); err != nil {
		t.Fatalf("create: %v", err)
	}

	// the creator is the owner of the namespace it created
	got, err := h.GetNamespace(identCtx("alice"), connect.NewRequest(&registryv1.GetNamespaceRequest{Slug: "pilab"}))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Msg.CallerRole != string(authz.RoleOwner) {
		t.Fatalf("creator role: %q, want owner", got.Msg.CallerRole)
	}
	role, ok := roles.GetRole("hub1", "pilab", "alice")
	if !ok || role != authz.RoleOwner {
		t.Fatalf("binding: role=%q ok=%v, want owner", role, ok)
	}

	// an unbound subject sees no role
	bob, err := h.GetNamespace(identCtx("bob"), connect.NewRequest(&registryv1.GetNamespaceRequest{Slug: "pilab"}))
	if err != nil {
		t.Fatalf("get as bob: %v", err)
	}
	if bob.Msg.CallerRole != "" {
		t.Fatalf("bob role: %q, want empty", bob.Msg.CallerRole)
	}

	// hub admins report owner without a binding
	admin, err := h.GetNamespace(identCtx("", authz.ScopeAdmin), connect.NewRequest(&registryv1.GetNamespaceRequest{Slug: "pilab"}))
	if err != nil {
		t.Fatalf("get as admin: %v", err)
	}
	if admin.Msg.CallerRole != string(authz.RoleOwner) {
		t.Fatalf("admin role: %q, want owner", admin.Msg.CallerRole)
	}
}

func TestCreateNamespaceAdminScopeWithoutSubject(t *testing.T) {
	h, roles := newAuthzRegistry()
	if _, err := h.CreateNamespace(identCtx("", authz.ScopeAdmin), connect.NewRequest(&registryv1.CreateNamespaceRequest{
		Slug: "ops",
	})); err != nil {
		t.Fatalf("create: %v", err)
	}
	// no subject -> no owner binding; admins bypass the role check anyway
	if bindings := roles.ListRoles("hub1", "ops"); len(bindings) != 0 {
		t.Fatalf("unexpected bindings: %+v", bindings)
	}
}

func TestRegisterSigningKeyRequiresPublisher(t *testing.T) {
	h, roles := newAuthzRegistry()
	if _, err := h.CreateNamespace(identCtx("alice"), connect.NewRequest(&registryv1.CreateNamespaceRequest{
		Slug: "ns",
	})); err != nil {
		t.Fatalf("create: %v", err)
	}

	// anonymous caller
	err := registerKey(t, h, context.Background(), "ns", "k0")
	if code := connect.CodeOf(err); code != connect.CodeUnauthenticated {
		t.Fatalf("anonymous code: %v, want %v", code, connect.CodeUnauthenticated)
	}

	// subject without a binding
	err = registerKey(t, h, identCtx("bob"), "ns", "k1")
	if code := connect.CodeOf(err); code != connect.CodePermissionDenied {
		t.Fatalf("unbound code: %v, want %v", code, connect.CodePermissionDenied)
	}

	// reader is below publisher
	if err := roles.Grant("hub1", "ns", "carol", authz.RoleReader); err != nil {
		t.Fatal(err)
	}
	err = registerKey(t, h, identCtx("carol"), "ns", "k2")
	if code := connect.CodeOf(err); code != connect.CodePermissionDenied {
		t.Fatalf("reader code: %v, want %v", code, connect.CodePermissionDenied)
	}

	// publisher passes
	if err := roles.Grant("hub1", "ns", "dave", authz.RolePublisher); err != nil {
		t.Fatal(err)
	}
	if err := registerKey(t, h, identCtx("dave"), "ns", "k3"); err != nil {
		t.Fatalf("publisher register: %v", err)
	}

	// hub admin bypasses the role check
	if err := registerKey(t, h, identCtx("", authz.ScopeAdmin), "ns", "k4"); err != nil {
		t.Fatalf("admin register: %v", err)
	}

	// the owner (creator) passes too
	if err := registerKey(t, h, identCtx("alice"), "ns", "k5"); err != nil {
		t.Fatalf("owner register: %v", err)
	}
}

func TestRegisterSigningKeyUnknownNamespace(t *testing.T) {
	h, _ := newAuthzRegistry()
	// namespace existence is checked before roles: unknown stays NotFound even
	// for a caller with no binding at all
	err := registerKey(t, h, identCtx("bob"), "nope", "k1")
	if code := connect.CodeOf(err); code != connect.CodeNotFound {
		t.Fatalf("code: %v, want %v", code, connect.CodeNotFound)
	}
}

func TestListNamespacesCallerRole(t *testing.T) {
	h, roles := newAuthzRegistry()
	for _, slug := range []string{"alpha", "beta"} {
		if _, err := h.CreateNamespace(identCtx("alice"), connect.NewRequest(&registryv1.CreateNamespaceRequest{
			Slug: slug,
		})); err != nil {
			t.Fatal(err)
		}
	}
	if err := roles.Grant("hub1", "beta", "bob", authz.RolePublisher); err != nil {
		t.Fatal(err)
	}

	resp, err := h.ListNamespaces(identCtx("bob"), connect.NewRequest(&registryv1.ListNamespacesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, ns := range resp.Msg.Namespaces {
		got[ns.Namespace.Slug] = ns.CallerRole
	}
	want := map[string]string{"alpha": "", "beta": string(authz.RolePublisher)}
	if len(got) != len(want) || got["alpha"] != want["alpha"] || got["beta"] != want["beta"] {
		t.Fatalf("caller roles: %+v, want %+v", got, want)
	}
}
