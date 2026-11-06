package authz

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMemStoreGrantGetRevoke(t *testing.T) {
	s := NewMemStore()

	_, ok := s.GetRole("hub1", "ns", "alice")
	require.False(t, ok)

	require.NoError(t, s.Grant("hub1", "ns", "alice", RoleOwner))
	role, ok := s.GetRole("hub1", "ns", "alice")
	require.True(t, ok)
	require.Equal(t, RoleOwner, role)

	// Grant overwrites an existing binding.
	require.NoError(t, s.Grant("hub1", "ns", "alice", RoleReader))
	role, ok = s.GetRole("hub1", "ns", "alice")
	require.True(t, ok)
	require.Equal(t, RoleReader, role)

	require.NoError(t, s.Revoke("hub1", "ns", "alice"))
	_, ok = s.GetRole("hub1", "ns", "alice")
	require.False(t, ok)
	require.ErrorIs(t, s.Revoke("hub1", "ns", "alice"), ErrBindingNotFound)
}

func TestMemStoreGrantValidation(t *testing.T) {
	s := NewMemStore()
	require.ErrorIs(t, s.Grant("hub1", "ns", "", RoleOwner), ErrInvalidSubject)
	require.ErrorIs(t, s.Grant("hub1", "", "alice", RoleOwner), ErrInvalidNamespace)
	require.ErrorIs(t, s.Grant("hub1", "ns", "alice", Role("root")), ErrInvalidRole)
	require.ErrorIs(t, s.Grant("hub1", "ns", "alice", Role("")), ErrInvalidRole)
}

func TestMemStoreListRolesScopedAndSorted(t *testing.T) {
	s := NewMemStore()
	require.NoError(t, s.Grant("hub1", "ns", "bob", RoleReader))
	require.NoError(t, s.Grant("hub1", "ns", "alice", RoleOwner))
	require.NoError(t, s.Grant("hub1", "ns", "carol", RolePublisher))
	require.NoError(t, s.Grant("hub2", "ns", "dave", RoleOwner))
	require.NoError(t, s.Grant("hub1", "other", "eve", RoleOwner))

	got := s.ListRoles("hub1", "ns")
	require.Len(t, got, 3)
	require.Equal(t, []string{"alice", "bob", "carol"}, []string{got[0].Subject, got[1].Subject, got[2].Subject})
	require.Equal(t, Binding{Hub: "hub1", Namespace: "ns", Subject: "alice", Role: RoleOwner}, got[0])
	require.Equal(t, RoleReader, got[1].Role)
	require.Equal(t, RolePublisher, got[2].Role)

	require.Len(t, s.ListRoles("hub2", "ns"), 1)
	require.Len(t, s.ListRoles("hub1", "other"), 1)
	require.Empty(t, s.ListRoles("hub1", "missing"))
}

func TestMemStoreConcurrent(t *testing.T) {
	s := NewMemStore()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				subject := fmt.Sprintf("s%d-%d", i, j)
				_ = s.Grant("hub1", "ns", subject, RoleReader)
				_, _ = s.GetRole("hub1", "ns", subject)
				_ = s.ListRoles("hub1", "ns")
				_ = s.Revoke("hub1", "ns", subject)
			}
		}(i)
	}
	wg.Wait()
	require.Empty(t, s.ListRoles("hub1", "ns"))
}

func TestCheck(t *testing.T) {
	s := NewMemStore()
	require.NoError(t, s.Grant("hub1", "ns", "publisher", RolePublisher))
	require.NoError(t, s.Grant("hub1", "ns", "reader", RoleReader))

	// anonymous callers are unauthenticated, never denied-with-a-role
	require.ErrorIs(t, Check(s, "hub1", "ns", Identity{}, RoleReader), ErrUnauthenticated)
	require.ErrorIs(t, Check(s, "hub1", "ns", Identity{Scopes: []string{"agenthub.read"}}, RoleReader), ErrUnauthenticated)

	// a missing binding is a denial, not a fallback
	require.ErrorIs(t, Check(s, "hub1", "ns", Identity{Subject: "stranger"}, RoleReader), ErrDenied)

	// role hierarchy
	require.ErrorIs(t, Check(s, "hub1", "ns", Identity{Subject: "reader"}, RolePublisher), ErrDenied)
	require.NoError(t, Check(s, "hub1", "ns", Identity{Subject: "reader"}, RoleReader))
	require.NoError(t, Check(s, "hub1", "ns", Identity{Subject: "publisher"}, RoleReader))
	require.NoError(t, Check(s, "hub1", "ns", Identity{Subject: "publisher"}, RolePublisher))

	// hub admins bypass the namespace role check
	require.NoError(t, Check(s, "hub1", "ns", Identity{Subject: "root", Scopes: []string{ScopeAdmin}}, RoleOwner))
	require.NoError(t, Check(s, "hub1", "ns", Identity{Scopes: []string{"admin"}}, RoleOwner))

	// an unknown namespace is a denial even for a bound subject
	require.ErrorIs(t, Check(s, "hub1", "missing", Identity{Subject: "publisher"}, RoleReader), ErrDenied)
}
