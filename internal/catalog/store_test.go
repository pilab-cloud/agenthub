package catalog

import (
	"errors"
	"testing"

	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"
)

func TestMemStoreAssetVersion(t *testing.T) {
	s := NewMemStore()
	if err := s.UpsertAsset(&Asset{Namespace: "ns", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "a", DisplayName: "A"}); err != nil {
		t.Fatal(err)
	}
	got, ok := s.GetAsset("ns", commonv1.AssetKind_ASSET_KIND_AGENT, "a")
	if !ok || got.DisplayName != "A" {
		t.Fatal("get asset failed")
	}
	if _, ok := s.GetAsset("ns", commonv1.AssetKind_ASSET_KIND_AGENT, "nope"); ok {
		t.Fatal("expected not found")
	}
	list := s.ListAssets()
	if len(list) != 1 {
		t.Fatalf("list len: %d", len(list))
	}

	if err := s.UpsertVersion(&Version{Namespace: "ns", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "a", Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	versions := s.ListVersions("ns", commonv1.AssetKind_ASSET_KIND_AGENT, "a")
	if len(versions) != 1 {
		t.Fatalf("versions len: %d", len(versions))
	}
	v, ok := s.GetVersion("ns", commonv1.AssetKind_ASSET_KIND_AGENT, "a", "1.0.0")
	if !ok || v.Version != "1.0.0" {
		t.Fatal("get version failed")
	}
	if _, ok := s.GetVersion("ns", commonv1.AssetKind_ASSET_KIND_AGENT, "a", "nope"); ok {
		t.Fatal("expected not found")
	}
}

func TestMemStoreNotFoundSentinel(t *testing.T) {
	// ErrNotFound exists
	if ErrNotFound == nil {
		t.Fatal("ErrNotFound nil")
	}
	if !errors.Is(ErrNotFound, ErrNotFound) {
		t.Fatal("errors.Is failed")
	}
}
