//go:build integration

package authz

import (
	"context"
	"os"
	"testing"

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

// cleanupBindings removes every binding the test wrote. namespace_roles has
// no foreign key on purpose (MemStore parity: a binding never requires a
// namespace row to exist), so cleanup is a plain delete on the test hub.
func cleanupBindings(t *testing.T, database *bun.DB) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = database.NewDelete().Model((*db.NamespaceRole)(nil)).
			Where("hub_id IN ('authz-test-hub', 'other-hub')").
			Exec(context.Background())
	})
}

func TestPostgresRoleStoreRoundTrip(t *testing.T) {
	database := openTestDB(t)
	cleanupBindings(t, database)
	store := NewPostgresRoleStore(database)

	_, ok := store.GetRole("authz-test-hub", "pgns", "alice")
	require.False(t, ok)

	require.NoError(t, store.Grant("authz-test-hub", "pgns", "alice", RoleOwner))
	role, ok := store.GetRole("authz-test-hub", "pgns", "alice")
	require.True(t, ok)
	require.Equal(t, RoleOwner, role)

	// Grant replaces an existing binding, like MemStore.
	require.NoError(t, store.Grant("authz-test-hub", "pgns", "alice", RoleReader))
	role, ok = store.GetRole("authz-test-hub", "pgns", "alice")
	require.True(t, ok)
	require.Equal(t, RoleReader, role)

	require.NoError(t, store.Revoke("authz-test-hub", "pgns", "alice"))
	_, ok = store.GetRole("authz-test-hub", "pgns", "alice")
	require.False(t, ok)
	require.ErrorIs(t, store.Revoke("authz-test-hub", "pgns", "alice"), ErrBindingNotFound)
}

func TestPostgresRoleStoreValidation(t *testing.T) {
	database := openTestDB(t)
	cleanupBindings(t, database)
	store := NewPostgresRoleStore(database)

	require.ErrorIs(t, store.Grant("authz-test-hub", "ns", "", RoleOwner), ErrInvalidSubject)
	require.ErrorIs(t, store.Grant("authz-test-hub", "", "alice", RoleOwner), ErrInvalidNamespace)
	require.ErrorIs(t, store.Grant("authz-test-hub", "ns", "alice", Role("root")), ErrInvalidRole)
	require.ErrorIs(t, store.Grant("authz-test-hub", "ns", "alice", Role("")), ErrInvalidRole)
}

func TestPostgresRoleStoreListRolesScopedAndSorted(t *testing.T) {
	database := openTestDB(t)
	cleanupBindings(t, database)
	store := NewPostgresRoleStore(database)

	require.NoError(t, store.Grant("authz-test-hub", "pgns2", "bob", RoleReader))
	require.NoError(t, store.Grant("authz-test-hub", "pgns2", "alice", RoleOwner))
	require.NoError(t, store.Grant("authz-test-hub", "pgns2", "carol", RolePublisher))
	require.NoError(t, store.Grant("other-hub", "pgns2", "dave", RoleOwner))
	require.NoError(t, store.Grant("authz-test-hub", "other-ns", "eve", RoleOwner))

	got := store.ListRoles("authz-test-hub", "pgns2")
	require.Len(t, got, 3)
	require.Equal(t, []string{"alice", "bob", "carol"},
		[]string{got[0].Subject, got[1].Subject, got[2].Subject})
	require.Equal(t, Binding{Hub: "authz-test-hub", Namespace: "pgns2", Subject: "alice", Role: RoleOwner}, got[0])

	require.Len(t, store.ListRoles("other-hub", "pgns2"), 1)
	require.Len(t, store.ListRoles("authz-test-hub", "other-ns"), 1)
	require.Empty(t, store.ListRoles("authz-test-hub", "missing"))
}

// TestPostgresRoleStoreCheck exercises the read path the handlers use: a
// persisted binding must satisfy authz.Check, and a missing one must deny.
func TestPostgresRoleStoreCheck(t *testing.T) {
	database := openTestDB(t)
	cleanupBindings(t, database)
	store := NewPostgresRoleStore(database)

	require.NoError(t, store.Grant("authz-test-hub", "pgns3", "publisher", RolePublisher))

	require.NoError(t, Check(store, "authz-test-hub", "pgns3", Identity{Subject: "publisher"}, RoleReader))
	require.NoError(t, Check(store, "authz-test-hub", "pgns3", Identity{Subject: "publisher"}, RolePublisher))
	require.ErrorIs(t, Check(store, "authz-test-hub", "pgns3", Identity{Subject: "publisher"}, RoleMaintainer), ErrDenied)
	require.ErrorIs(t, Check(store, "authz-test-hub", "pgns3", Identity{Subject: "stranger"}, RoleReader), ErrDenied)
	require.ErrorIs(t, Check(store, "authz-test-hub", "pgns3", Identity{}, RoleReader), ErrUnauthenticated)
	require.NoError(t, Check(store, "authz-test-hub", "pgns3",
		Identity{Subject: "root", Scopes: []string{ScopeAdmin}}, RoleOwner))
}

// TestPostgresRoleStoreNilDatabase fails closed instead of panicking.
func TestPostgresRoleStoreNilDatabase(t *testing.T) {
	store := NewPostgresRoleStore(nil)
	_, ok := store.GetRole("h", "ns", "alice")
	require.False(t, ok)
	require.Empty(t, store.ListRoles("h", "ns"))
	require.ErrorIs(t, store.Grant("h", "ns", "alice", RoleOwner), ErrNilDatabase)
	require.ErrorIs(t, store.Revoke("h", "ns", "alice"), ErrNilDatabase)
}
