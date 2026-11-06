//go:build integration

package registry

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
)

func openTestDB(t *testing.T) *bun.DB {
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

func TestPostgresNamespaceStoreBasic(t *testing.T) {
	database := openTestDB(t)
	store := NewPostgresNamespaceStore(database, "local")
	slug := fmt.Sprintf("acme-%d", time.Now().UnixNano())
	ns, err := store.Create(&commonv1.Namespace{Slug: slug, DisplayName: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	_ = slug
	if ns.Slug != slug {
		t.Fatalf("slug = %s", ns.Slug)
	}
	got, ok := store.Get(slug)
	if !ok || got.Slug != slug {
		t.Fatal("get failed")
	}
	if len(store.List()) == 0 {
		t.Fatal("list empty")
	}
}
