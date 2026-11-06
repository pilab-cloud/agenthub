package server

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"go.pilab.hu/agenthub/internal/authz"
)

// TestNewRoleStore picks the backend: without a database the in-memory store
// keeps the M3.2 default, with one the bindings persist in
// agenthub.namespace_roles (M3.6). The bun handle is never used to query.
func TestNewRoleStore(t *testing.T) {
	require.IsType(t, authz.NewMemStore(), newRoleStore(nil))
	database := bun.NewDB(new(sql.DB), pgdialect.New())
	require.IsType(t, authz.NewPostgresRoleStore(database), newRoleStore(database))
}
