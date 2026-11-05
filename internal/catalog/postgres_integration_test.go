//go:build integration

package catalog

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/uptrace/bun"

	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"
	"go.pilab.hu/agenthub/internal/config"
	"go.pilab.hu/agenthub/internal/db"
	"go.pilab.hu/agenthub/internal/registry"
)

func openTestDBCat(t *testing.T) *bun.DB {
	t.Helper()
	dsn := os.Getenv("AGENTHUB_TEST_DSN")
	if dsn == "" {
		t.Skip("AGENTHUB_TEST_DSN not set")
	}
	cfg := config.DatabaseConfig{DSN: dsn}
	database, err := db.Open(context.Background(), cfg)
	if err != nil {
		t.Skipf("db open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close(database) })
	return database
}

func TestPostgresCatalogStoreBasic(t *testing.T) {
	database := openTestDBCat(t)
	slug := fmt.Sprintf("acme-%d", time.Now().UnixNano())
	regStore := registry.NewPostgresNamespaceStore(database, "local")
	if _, err := regStore.Create(&commonv1.Namespace{Slug: slug, DisplayName: "Acme"}); err != nil {
		t.Fatal(err)
	}
	catStore := NewPostgresCatalogStore(database, "local")
	err := catStore.UpsertAsset(&Asset{Namespace: slug, Kind: commonv1.AssetKind_ASSET_KIND_SKILL, Name: "foo", DisplayName: "Foo", Summary: "s"})
	if err != nil {
		t.Fatal(err)
	}
	a, ok := catStore.GetAsset(slug, commonv1.AssetKind_ASSET_KIND_SKILL, "foo")
	if !ok || a.Name != "foo" {
		t.Fatal("get asset failed")
	}
	if len(catStore.ListAssets()) == 0 {
		t.Fatal("list empty")
	}
	err = catStore.UpsertVersion(&Version{Namespace: slug, Kind: commonv1.AssetKind_ASSET_KIND_SKILL, Name: "foo", Version: "1.0.0", Status: commonv1.VersionStatus_VERSION_STATUS_PUBLISHED, Manifest: []byte(fmt.Sprintf(`{"t":%d}`, time.Now().UnixNano()))})
	if err != nil {
		t.Fatal(err)
	}
	v, ok := catStore.GetVersion(slug, commonv1.AssetKind_ASSET_KIND_SKILL, "foo", "1.0.0")
	if !ok || v.Version != "1.0.0" {
		t.Fatal("get version failed")
	}
	if len(catStore.ListVersions(slug, commonv1.AssetKind_ASSET_KIND_SKILL, "foo")) == 0 {
		t.Fatal("list versions empty")
	}
}
