package api

import (
	"testing"

	"connectrpc.com/connect"

	registryv1 "go.pilab.hu/agenthub/gen/go/agenthub/registry/v1"
)

func TestCreateAndGetNamespace(t *testing.T) {
	h := NewRegistry()
	ctx := identCtx("alice")

	resp, err := h.CreateNamespace(ctx, connect.NewRequest(&registryv1.CreateNamespaceRequest{
		Slug:        "pilab",
		DisplayName: "PiLab",
	}))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if resp.Msg.Namespace.Slug != "pilab" {
		t.Fatalf("slug mismatch: %s", resp.Msg.Namespace.Slug)
	}

	got, err := h.GetNamespace(ctx, connect.NewRequest(&registryv1.GetNamespaceRequest{Slug: "pilab"}))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Msg.Namespace.DisplayName != "PiLab" {
		t.Fatalf("display name: %s", got.Msg.Namespace.DisplayName)
	}
}

func TestListNamespaces(t *testing.T) {
	h := NewRegistry()
	ctx := identCtx("alice")
	for _, s := range []string{"beta", "alpha"} {
		if _, err := h.CreateNamespace(ctx, connect.NewRequest(&registryv1.CreateNamespaceRequest{Slug: s})); err != nil {
			t.Fatal(err)
		}
	}
	resp, err := h.ListNamespaces(ctx, connect.NewRequest(&registryv1.ListNamespacesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Msg.Namespaces) != 2 {
		t.Fatalf("want 2, got %d", len(resp.Msg.Namespaces))
	}
	if resp.Msg.Namespaces[0].Namespace.Slug != "alpha" {
		t.Fatalf("expected sorted, got %s", resp.Msg.Namespaces[0].Namespace.Slug)
	}
}

func TestDuplicateNamespace(t *testing.T) {
	h := NewRegistry()
	ctx := identCtx("alice")
	if _, err := h.CreateNamespace(ctx, connect.NewRequest(&registryv1.CreateNamespaceRequest{Slug: "dup"})); err != nil {
		t.Fatal(err)
	}
	if _, err := h.CreateNamespace(ctx, connect.NewRequest(&registryv1.CreateNamespaceRequest{Slug: "dup"})); err == nil {
		t.Fatal("expected error on duplicate")
	}
}
