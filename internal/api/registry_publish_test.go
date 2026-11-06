package api

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"connectrpc.com/connect"

	catalogv1 "go.pilab.hu/agenthub/gen/go/agenthub/catalog/v1"
	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"
	registryv1 "go.pilab.hu/agenthub/gen/go/agenthub/registry/v1"
)

func TestPublishHappyPath(t *testing.T) {
	h := NewRegistry()
	ctx := identCtx("alice")
	if _, err := h.CreateNamespace(ctx, connect.NewRequest(&registryv1.CreateNamespaceRequest{Slug: "ns"})); err != nil {
		t.Fatal(err)
	}
	begin, err := h.BeginPublish(ctx, connect.NewRequest(&registryv1.BeginPublishRequest{
		Namespace:    "ns",
		Kind:         commonv1.AssetKind_ASSET_KIND_AGENT,
		Name:         "foo",
		Version:      "1.0.0",
		BundleDigest: &commonv1.Digest{Algorithm: "sha256", Value: "bundle"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if begin.Msg.PublishId == "" {
		t.Fatal("missing publish id")
	}
	if len(begin.Msg.Uploads) != 0 {
		t.Fatalf("expected no uploads, got %d", len(begin.Msg.Uploads))
	}

	manifest := []byte("manifest-bytes")
	sum := sha256.Sum256(manifest)
	md := hex.EncodeToString(sum[:])

	commit, err := h.CommitPublish(ctx, connect.NewRequest(&registryv1.CommitPublishRequest{
		PublishId:      begin.Msg.PublishId,
		Manifest:       manifest,
		ManifestDigest: &commonv1.Digest{Algorithm: "sha256", Value: md},
	}))
	if err != nil {
		t.Fatal(err)
	}
	ra := commit.Msg.Asset
	if ra == nil {
		t.Fatal("missing asset")
	}
	if ra.Ref.Namespace != "ns" || ra.Ref.Name != "foo" || ra.Ref.Version != "1.0.0" {
		t.Fatalf("ref mismatch: %+v", ra.Ref)
	}
	if ra.OriginHub != "" {
		t.Fatalf("origin hub: %s", ra.OriginHub)
	}
	if ra.ManifestDigest == nil || ra.ManifestDigest.Value != md {
		t.Fatalf("manifest digest mismatch")
	}
	if ra.BundleDigest == nil || ra.BundleDigest.Value != "bundle" {
		t.Fatalf("bundle digest mismatch")
	}
	_ = catalogv1.ResolvedAsset{}
}

func TestPublishUnknownNamespace(t *testing.T) {
	h := NewRegistry()
	ctx := identCtx("alice")
	_, err := h.BeginPublish(ctx, connect.NewRequest(&registryv1.BeginPublishRequest{
		Namespace: "nope",
		Kind:      commonv1.AssetKind_ASSET_KIND_AGENT,
		Name:      "foo",
		Version:   "1.0.0",
	}))
	if err == nil {
		t.Fatal("expected error")
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("code: %v", connect.CodeOf(err))
	}
}

func TestPublishDigestMismatch(t *testing.T) {
	h := NewRegistry()
	ctx := identCtx("alice")
	if _, err := h.CreateNamespace(ctx, connect.NewRequest(&registryv1.CreateNamespaceRequest{Slug: "ns"})); err != nil {
		t.Fatal(err)
	}
	begin, err := h.BeginPublish(ctx, connect.NewRequest(&registryv1.BeginPublishRequest{
		Namespace: "ns",
		Kind:      commonv1.AssetKind_ASSET_KIND_AGENT,
		Name:      "foo",
		Version:   "1.0.0",
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.CommitPublish(ctx, connect.NewRequest(&registryv1.CommitPublishRequest{
		PublishId:      begin.Msg.PublishId,
		Manifest:       []byte("manifest"),
		ManifestDigest: &commonv1.Digest{Algorithm: "sha256", Value: "deadbeef"},
	}))
	if err == nil {
		t.Fatal("expected error")
	}
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("code: %v", connect.CodeOf(err))
	}
}

func TestPublishIdempotency(t *testing.T) {
	h := NewRegistry()
	ctx := identCtx("alice")
	if _, err := h.CreateNamespace(ctx, connect.NewRequest(&registryv1.CreateNamespaceRequest{Slug: "ns"})); err != nil {
		t.Fatal(err)
	}
	begin, err := h.BeginPublish(ctx, connect.NewRequest(&registryv1.BeginPublishRequest{
		Namespace: "ns",
		Kind:      commonv1.AssetKind_ASSET_KIND_AGENT,
		Name:      "foo",
		Version:   "1.0.0",
	}))
	if err != nil {
		t.Fatal(err)
	}
	manifest := []byte("manifest")
	sum := sha256.Sum256(manifest)
	md := hex.EncodeToString(sum[:])
	digest := &commonv1.Digest{Algorithm: "sha256", Value: md}

	if _, err := h.CommitPublish(ctx, connect.NewRequest(&registryv1.CommitPublishRequest{
		PublishId:      begin.Msg.PublishId,
		Manifest:       manifest,
		ManifestDigest: digest,
	})); err != nil {
		t.Fatal(err)
	}
	// second commit (same publish id) is idempotent
	resp, err := h.CommitPublish(ctx, connect.NewRequest(&registryv1.CommitPublishRequest{
		PublishId:      begin.Msg.PublishId,
		Manifest:       manifest,
		ManifestDigest: digest,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.Asset == nil {
		t.Fatal("missing asset")
	}
}

func TestPublishUnknownPublishID(t *testing.T) {
	h := NewRegistry()
	ctx := identCtx("alice")
	_, err := h.CommitPublish(ctx, connect.NewRequest(&registryv1.CommitPublishRequest{
		PublishId:      "does-not-exist",
		Manifest:       []byte("m"),
		ManifestDigest: &commonv1.Digest{Algorithm: "sha256", Value: "x"},
	}))
	if err == nil {
		t.Fatal("expected error")
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("code: %v", connect.CodeOf(err))
	}
}
