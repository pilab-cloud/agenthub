package api

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	catalogv1 "go.pilab.hu/agenthub/gen/go/agenthub/catalog/v1"
	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"
	"go.pilab.hu/agenthub/internal/catalog"
)

func seedCatalog(t *testing.T) *Catalog {
	t.Helper()
	s := catalog.NewMemStore()
	now := timestamppb.Now()
	if err := s.UpsertAsset(&catalog.Asset{
		Namespace:   "pilab",
		Kind:        commonv1.AssetKind_ASSET_KIND_AGENT,
		Name:        "foo",
		DisplayName: "Foo Agent",
		Summary:     "does foo",
		Tags:        []string{"go", "agent"},
		Categories:  []string{"cat"},
		License:     "MIT",
		Repository:  "https://example.com/foo",
		LatestVer:   "1.0.0",
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertVersion(&catalog.Version{
		Namespace:      "pilab",
		Kind:           commonv1.AssetKind_ASSET_KIND_AGENT,
		Name:           "foo",
		Version:        "1.0.0",
		Status:         commonv1.VersionStatus_VERSION_STATUS_PUBLISHED,
		Manifest:       []byte("manifest-bytes"),
		ManifestDigest: &commonv1.Digest{Algorithm: "sha256", Value: "abc"},
		BundleDigest:   &commonv1.Digest{Algorithm: "sha256", Value: "def"},
		BundleSize:     42,
		PublishedAt:    now,
		Readme:         "readme",
		Changelog:      "changelog",
		Verified:       true,
	}); err != nil {
		t.Fatal(err)
	}
	return NewCatalogWithStore(s)
}

func TestCatalogSearchAssets(t *testing.T) {
	h := seedCatalog(t)
	ctx := context.Background()
	resp, err := h.SearchAssets(ctx, connect.NewRequest(&catalogv1.SearchAssetsRequest{Query: "foo"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Msg.Assets) != 1 {
		t.Fatalf("want 1, got %d", len(resp.Msg.Assets))
	}
	if resp.Msg.Assets[0].LatestVersion != "1.0.0" {
		t.Fatalf("latest version mismatch: %s", resp.Msg.Assets[0].LatestVersion)
	}
	if resp.Msg.Assets[0].Ref.Name != "foo" {
		t.Fatalf("name mismatch")
	}
}

func TestCatalogSearchAssetsFilters(t *testing.T) {
	h := seedCatalog(t)
	ctx := context.Background()
	resp, err := h.SearchAssets(ctx, connect.NewRequest(&catalogv1.SearchAssetsRequest{
		Kinds: []commonv1.AssetKind{commonv1.AssetKind_ASSET_KIND_TOOL},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Msg.Assets) != 0 {
		t.Fatalf("want 0, got %d", len(resp.Msg.Assets))
	}

	resp, err = h.SearchAssets(ctx, connect.NewRequest(&catalogv1.SearchAssetsRequest{
		Tags: []string{"go"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Msg.Assets) != 1 {
		t.Fatalf("want 1, got %d", len(resp.Msg.Assets))
	}

	resp, err = h.SearchAssets(ctx, connect.NewRequest(&catalogv1.SearchAssetsRequest{
		Namespaces: []string{"other"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Msg.Assets) != 0 {
		t.Fatalf("want 0, got %d", len(resp.Msg.Assets))
	}
}

func TestCatalogGetAsset(t *testing.T) {
	h := seedCatalog(t)
	ctx := context.Background()
	resp, err := h.GetAsset(ctx, connect.NewRequest(&catalogv1.GetAssetRequest{
		Ref: &commonv1.AssetRef{Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "foo"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.License != "MIT" {
		t.Fatalf("license: %s", resp.Msg.License)
	}
	if resp.Msg.Repository != "https://example.com/foo" {
		t.Fatalf("repo: %s", resp.Msg.Repository)
	}
	if len(resp.Msg.Categories) != 1 {
		t.Fatalf("categories: %v", resp.Msg.Categories)
	}
	if resp.Msg.CreatedAt == nil {
		t.Fatal("created_at nil")
	}
}

func TestCatalogGetAssetNotFound(t *testing.T) {
	h := seedCatalog(t)
	ctx := context.Background()
	_, err := h.GetAsset(ctx, connect.NewRequest(&catalogv1.GetAssetRequest{
		Ref: &commonv1.AssetRef{Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "nope"},
	}))
	if err == nil {
		t.Fatal("expected error")
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("code: %v", connect.CodeOf(err))
	}
}

func TestCatalogListVersions(t *testing.T) {
	h := seedCatalog(t)
	ctx := context.Background()
	resp, err := h.ListVersions(ctx, connect.NewRequest(&catalogv1.ListVersionsRequest{
		Ref: &commonv1.AssetRef{Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "foo"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Msg.Versions) != 1 {
		t.Fatalf("want 1, got %d", len(resp.Msg.Versions))
	}
	if resp.Msg.Versions[0].Verified != true {
		t.Fatal("verified mismatch")
	}
}

func TestCatalogListVersionsNotFound(t *testing.T) {
	h := seedCatalog(t)
	ctx := context.Background()
	_, err := h.ListVersions(ctx, connect.NewRequest(&catalogv1.ListVersionsRequest{
		Ref: &commonv1.AssetRef{Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "nope"},
	}))
	if err == nil {
		t.Fatal("expected error")
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("code: %v", connect.CodeOf(err))
	}
}

func TestCatalogGetVersion(t *testing.T) {
	h := seedCatalog(t)
	ctx := context.Background()
	resp, err := h.GetVersion(ctx, connect.NewRequest(&catalogv1.GetVersionRequest{
		Ref: &commonv1.AssetRef{Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "foo", Version: "1.0.0"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.Msg.Manifest) != "manifest-bytes" {
		t.Fatalf("manifest: %s", resp.Msg.Manifest)
	}
	if resp.Msg.Readme != "readme" || resp.Msg.Changelog != "changelog" {
		t.Fatalf("readme/changelog mismatch")
	}
	if resp.Msg.Version.ManifestDigest == nil || resp.Msg.Version.ManifestDigest.Value != "abc" {
		t.Fatalf("digest mismatch")
	}
}

func TestCatalogGetVersionNotFound(t *testing.T) {
	h := seedCatalog(t)
	ctx := context.Background()
	_, err := h.GetVersion(ctx, connect.NewRequest(&catalogv1.GetVersionRequest{
		Ref: &commonv1.AssetRef{Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "foo", Version: "9.9.9"},
	}))
	if err == nil {
		t.Fatal("expected error")
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("code: %v", connect.CodeOf(err))
	}
}

func TestCatalogResolveFound(t *testing.T) {
	h := seedCatalog(t)
	ctx := context.Background()
	resp, err := h.Resolve(ctx, connect.NewRequest(&catalogv1.ResolveRequest{
		Desired: []*catalogv1.DesiredAsset{{Ref: &commonv1.AssetRef{Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "foo"}}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Msg.Assets) != 1 {
		t.Fatalf("want 1, got %d", len(resp.Msg.Assets))
	}
	if resp.Msg.Assets[0].Ref.Version != "1.0.0" {
		t.Fatalf("version: %s", resp.Msg.Assets[0].Ref.Version)
	}
	if resp.Msg.Assets[0].OriginHub != "" {
		t.Fatalf("origin_hub not empty")
	}
	if resp.Msg.Assets[0].ManifestDigest == nil || resp.Msg.Assets[0].ManifestDigest.Value != "abc" {
		t.Fatalf("manifest digest mismatch")
	}
	if resp.Msg.Assets[0].BundleDigest == nil || resp.Msg.Assets[0].BundleDigest.Value != "def" {
		t.Fatalf("bundle digest mismatch")
	}
	if len(resp.Msg.Warnings) != 0 {
		t.Fatalf("warnings: %d", len(resp.Msg.Warnings))
	}
}

func TestCatalogResolveNotFound(t *testing.T) {
	h := seedCatalog(t)
	ctx := context.Background()
	resp, err := h.Resolve(ctx, connect.NewRequest(&catalogv1.ResolveRequest{
		Desired: []*catalogv1.DesiredAsset{{Ref: &commonv1.AssetRef{Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "nope"}}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Msg.Assets) != 0 {
		t.Fatalf("want 0, got %d", len(resp.Msg.Assets))
	}
	if len(resp.Msg.Warnings) != 1 {
		t.Fatalf("want 1 warning, got %d", len(resp.Msg.Warnings))
	}
}
