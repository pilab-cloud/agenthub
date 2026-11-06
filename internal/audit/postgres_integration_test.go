//go:build integration

package audit

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"go.pilab.hu/agenthub/internal/config"
	"go.pilab.hu/agenthub/internal/db"
)

func openTestDB(t *testing.T) *bun.DB {
	t.Helper()
	dsn := os.Getenv("AGENTHUB_TEST_DSN")
	if dsn == "" {
		t.Skip("AGENTHUB_TEST_DSN not set")
	}
	database, err := db.Open(context.Background(), config.DatabaseConfig{DSN: dsn})
	if err != nil {
		t.Skipf("db open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close(database) })
	return database
}

func TestPostgresStoreLogAndList(t *testing.T) {
	database := openTestDB(t)
	store := NewPostgresStore(database)

	action := fmt.Sprintf("test.action.%d", time.Now().UnixNano())
	require.NoError(t, store.Log(action, "tester", "pilab", "agent/foo", "1.0.0", `{"k":"v"}`))
	t.Cleanup(func() {
		_, _ = database.NewDelete().Model((*db.AuditLog)(nil)).Where("action = ?", action).Exec(context.Background())
	})

	entries, err := store.List(Query{Action: action})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	got := entries[0]
	require.Equal(t, action, got.Action)
	require.Equal(t, "tester", got.Actor)
	require.Equal(t, "pilab", got.Namespace)
	require.Equal(t, "agent/foo", got.Asset)
	require.Equal(t, "1.0.0", got.Version)
	require.JSONEq(t, `{"k":"v"}`, got.Detail)
	require.False(t, got.At.IsZero())

	// non-JSON detail is stored as a JSON string
	require.NoError(t, store.Log(action+".plain", "tester", "", "", "", "plain text"))
	entries, err = store.List(Query{Action: action + ".plain"})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.JSONEq(t, `"plain text"`, entries[0].Detail)
}

func TestPostgresStoreRequiresAction(t *testing.T) {
	database := openTestDB(t)
	store := NewPostgresStore(database)
	require.ErrorIs(t, store.Log("", "tester", "", "", "", ""), ErrActionRequired)
}
