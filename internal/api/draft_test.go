package api

import (
	"testing"

	"connectrpc.com/connect"

	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"
	registryv1 "go.pilab.hu/agenthub/gen/go/agenthub/registry/v1"
)

func TestDraftLifecycle(t *testing.T) {
	h := NewRegistry()
	ctx := identCtx("alice")
	if _, err := h.CreateNamespace(ctx, connect.NewRequest(&registryv1.CreateNamespaceRequest{Slug: "ns"})); err != nil {
		t.Fatal(err)
	}

	cr, err := h.CreateDraft(ctx, connect.NewRequest(&registryv1.CreateDraftRequest{
		Namespace: "ns",
		Kind:      commonv1.AssetKind_ASSET_KIND_AGENT,
		Name:      "drafty",
		Manifest:  []byte("m"),
		Files:     []*registryv1.DraftFile{{Path: "a.txt", Content: []byte("hi")}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cr.Msg.DraftId == "" {
		t.Fatal("missing draft id")
	}
	id := cr.Msg.DraftId

	if _, err := h.UpdateDraft(ctx, connect.NewRequest(&registryv1.UpdateDraftRequest{
		DraftId:  id,
		Manifest: []byte("m2"),
	})); err != nil {
		t.Fatal(err)
	}

	pub, err := h.PublishDraft(ctx, connect.NewRequest(&registryv1.PublishDraftRequest{
		DraftId: id,
		Version: "1.0.0",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if pub.Msg.Asset == nil || pub.Msg.Asset.Ref.Version != "1.0.0" {
		t.Fatalf("bad asset: %+v", pub.Msg.Asset)
	}

	if _, err := h.PublishDraft(ctx, connect.NewRequest(&registryv1.PublishDraftRequest{DraftId: id, Version: "1.0.1"})); err == nil {
		t.Fatal("expected not found")
	}
}

func TestDiscardAndDeleteDraft(t *testing.T) {
	h := NewRegistry()
	ctx := identCtx("alice")
	if _, err := h.CreateNamespace(ctx, connect.NewRequest(&registryv1.CreateNamespaceRequest{Slug: "ns"})); err != nil {
		t.Fatal(err)
	}
	cr, err := h.CreateDraft(ctx, connect.NewRequest(&registryv1.CreateDraftRequest{
		Namespace: "ns",
		Kind:      commonv1.AssetKind_ASSET_KIND_AGENT,
		Name:      "d",
	}))
	if err != nil {
		t.Fatal(err)
	}
	id := cr.Msg.DraftId
	if _, err := h.DiscardDraft(ctx, connect.NewRequest(&registryv1.DiscardDraftRequest{DraftId: id})); err != nil {
		t.Fatal(err)
	}
	if _, err := h.DeleteDraft(ctx, connect.NewRequest(&registryv1.DeleteDraftRequest{DraftId: id})); err == nil {
		t.Fatal("expected not found")
	}
}
