package authz

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseRole(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Role
	}{
		{"owner", RoleOwner},
		{" Publisher ", RolePublisher},
		{"MAINTAINER", RoleMaintainer},
		{"reader", RoleReader},
	} {
		got, err := ParseRole(tc.in)
		require.NoError(t, err, tc.in)
		require.Equal(t, tc.want, got, tc.in)
	}
	for _, bad := range []string{"", "root", "owners"} {
		_, err := ParseRole(bad)
		require.ErrorIs(t, err, ErrInvalidRole, "%q", bad)
	}
}

func TestRoleAtLeast(t *testing.T) {
	require.True(t, RoleOwner.AtLeast(RoleReader))
	require.True(t, RoleOwner.AtLeast(RoleOwner))
	require.True(t, RoleMaintainer.AtLeast(RolePublisher))
	require.False(t, RoleMaintainer.AtLeast(RoleOwner))
	require.True(t, RolePublisher.AtLeast(RoleReader))
	require.False(t, RolePublisher.AtLeast(RoleMaintainer))
	require.True(t, RoleReader.AtLeast(RoleReader))
	require.False(t, RoleReader.AtLeast(RolePublisher))

	// unknown roles never satisfy a check
	require.False(t, Role("").AtLeast(RoleReader))
	require.False(t, RoleReader.AtLeast(Role("root")))

	require.True(t, RoleOwner.Valid())
	require.False(t, Role("root").Valid())
	require.Equal(t, 4, RoleOwner.Rank())
	require.Equal(t, 0, Role("root").Rank())
}

func TestIdentityAuthenticated(t *testing.T) {
	require.False(t, Identity{}.Authenticated())
	require.True(t, Identity{Subject: "alice"}.Authenticated())
	require.True(t, Identity{Scopes: []string{ScopeAdmin}}.Authenticated())
	require.False(t, Identity{Scopes: []string{"agenthub.read"}}.Authenticated())
}

func TestParseScopes(t *testing.T) {
	require.Equal(t, []string{"agenthub.read", "agenthub.admin"}, ParseScopes("agenthub.read, agenthub.admin"))
	require.Equal(t, []string{"a", "b", "c"}, ParseScopes("a b\tc"))
	require.Empty(t, ParseScopes(""))
	require.Empty(t, ParseScopes(" , "))
	require.NotNil(t, ParseScopes(""))
}

func TestIsAdmin(t *testing.T) {
	require.True(t, IsAdmin([]string{"agenthub.read", "agenthub.admin"}))
	require.True(t, IsAdmin([]string{"admin"}))
	require.False(t, IsAdmin([]string{"agenthub.read"}))
	require.False(t, IsAdmin(nil))
	require.True(t, Identity{Scopes: []string{ScopeAdmin}}.IsAdmin())
	require.False(t, Identity{Subject: "alice"}.IsAdmin())
}

func TestIdentityFromContext(t *testing.T) {
	require.Equal(t, Identity{}, IdentityFromContext(context.Background()))
	// defensive: a nil context must not panic
	var nilCtx context.Context
	require.Equal(t, Identity{}, IdentityFromContext(nilCtx))

	id := Identity{Subject: "alice", Scopes: []string{"agenthub.read"}}
	require.Equal(t, id, IdentityFromContext(WithIdentity(context.Background(), id)))
}
