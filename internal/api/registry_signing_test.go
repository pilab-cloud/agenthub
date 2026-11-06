package api

import (
	"testing"

	"connectrpc.com/connect"

	registryv1 "go.pilab.hu/agenthub/gen/go/agenthub/registry/v1"
)

func TestSigningKeyHappyPath(t *testing.T) {
	h := NewRegistry()
	ctx := identCtx("alice")
	if _, err := h.CreateNamespace(ctx, connect.NewRequest(&registryv1.CreateNamespaceRequest{Slug: "ns"})); err != nil {
		t.Fatal(err)
	}
	resp, err := h.RegisterSigningKey(ctx, connect.NewRequest(&registryv1.RegisterSigningKeyRequest{
		Namespace: "ns",
		Kid:       "k1",
		PublicKey: "pk",
	}))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	key := resp.Msg.Key
	if key == nil {
		t.Fatal("missing key")
	}
	if key.Namespace != "ns" || key.Kid != "k1" {
		t.Fatalf("identity mismatch: %+v", key)
	}
	if key.Algorithm != "ed25519" {
		t.Fatalf("algorithm default: %s", key.Algorithm)
	}
	if key.Custody != "client" {
		t.Fatalf("custody default: %s", key.Custody)
	}
	if key.CreatedAt == nil || key.ValidFrom == nil {
		t.Fatal("missing timestamps")
	}
	if key.RevokedAt != nil {
		t.Fatal("should not be revoked")
	}
}

func TestSigningKeyDuplicate(t *testing.T) {
	h := NewRegistry()
	ctx := identCtx("alice")
	if _, err := h.CreateNamespace(ctx, connect.NewRequest(&registryv1.CreateNamespaceRequest{Slug: "ns"})); err != nil {
		t.Fatal(err)
	}
	if _, err := h.RegisterSigningKey(ctx, connect.NewRequest(&registryv1.RegisterSigningKeyRequest{Namespace: "ns", Kid: "k1"})); err != nil {
		t.Fatal(err)
	}
	_, err := h.RegisterSigningKey(ctx, connect.NewRequest(&registryv1.RegisterSigningKeyRequest{Namespace: "ns", Kid: "k1"}))
	if err == nil {
		t.Fatal("expected error on duplicate")
	}
	if connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("code: %v", connect.CodeOf(err))
	}
}

func TestSigningKeyUnknownNamespace(t *testing.T) {
	h := NewRegistry()
	ctx := identCtx("alice")
	_, err := h.RegisterSigningKey(ctx, connect.NewRequest(&registryv1.RegisterSigningKeyRequest{Namespace: "nope", Kid: "k1"}))
	if err == nil {
		t.Fatal("expected error")
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("code: %v", connect.CodeOf(err))
	}
}

func TestSigningKeyRevoke(t *testing.T) {
	h := NewRegistry()
	ctx := identCtx("alice")
	if _, err := h.CreateNamespace(ctx, connect.NewRequest(&registryv1.CreateNamespaceRequest{Slug: "ns"})); err != nil {
		t.Fatal(err)
	}
	if _, err := h.RegisterSigningKey(ctx, connect.NewRequest(&registryv1.RegisterSigningKeyRequest{Namespace: "ns", Kid: "k1"})); err != nil {
		t.Fatal(err)
	}
	if _, err := h.RevokeSigningKey(ctx, connect.NewRequest(&registryv1.RevokeSigningKeyRequest{Namespace: "ns", Kid: "k1"})); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	// revoke unknown
	_, err := h.RevokeSigningKey(ctx, connect.NewRequest(&registryv1.RevokeSigningKeyRequest{Namespace: "ns", Kid: "nope"}))
	if err == nil {
		t.Fatal("expected error")
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("code: %v", connect.CodeOf(err))
	}
	// unknown namespace
	_, err = h.RevokeSigningKey(ctx, connect.NewRequest(&registryv1.RevokeSigningKeyRequest{Namespace: "nope", Kid: "k1"}))
	if err == nil || connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("expected not found, got %v", err)
	}
}
