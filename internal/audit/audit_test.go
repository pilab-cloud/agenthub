package audit

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMemStoreLogAndList(t *testing.T) {
	s := NewMemStore()

	require.NoError(t, s.Log("namespace.create", "alice", "pilab", "", "", `{"slug":"pilab"}`))
	require.NoError(t, s.Log("publish.commit", "bob", "pilab", "agent/foo", "1.0.0", ""))
	require.NoError(t, s.Log("namespace.create", "carol", "other", "", "", ""))

	entries, err := s.List(Query{})
	require.NoError(t, err)
	require.Len(t, entries, 3)
	// newest first
	require.Equal(t, "namespace.create", entries[0].Action)
	require.Equal(t, "carol", entries[0].Actor)
	require.Equal(t, "publish.commit", entries[1].Action)
	require.Equal(t, "agent/foo", entries[1].Asset)
	require.Equal(t, "1.0.0", entries[1].Version)
	require.Equal(t, `{"slug":"pilab"}`, entries[2].Detail)
	require.False(t, entries[0].At.IsZero())
	require.True(t, entries[0].At.After(entries[2].At) || entries[0].At.Equal(entries[2].At))
}

func TestMemStoreListFilters(t *testing.T) {
	s := NewMemStore()
	require.NoError(t, s.Log("namespace.create", "alice", "pilab", "", "", ""))
	require.NoError(t, s.Log("publish.commit", "alice", "pilab", "agent/foo", "1.0.0", ""))
	require.NoError(t, s.Log("namespace.create", "bob", "other", "", "", ""))

	byNamespace, err := s.List(Query{Namespace: "pilab"})
	require.NoError(t, err)
	require.Len(t, byNamespace, 2)

	byActor, err := s.List(Query{Actor: "bob"})
	require.NoError(t, err)
	require.Len(t, byActor, 1)
	require.Equal(t, "other", byActor[0].Namespace)

	byAction, err := s.List(Query{Action: "namespace.create"})
	require.NoError(t, err)
	require.Len(t, byAction, 2)

	// paging: newest first, so offset 1 skips the newest entry
	page, err := s.List(Query{Limit: 1, Offset: 1})
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Equal(t, "publish.commit", page[0].Action)

	beyond, err := s.List(Query{Limit: 1, Offset: 10})
	require.NoError(t, err)
	require.Empty(t, beyond)
}

func TestMemStoreLogRequiresAction(t *testing.T) {
	s := NewMemStore()
	require.ErrorIs(t, s.Log("", "alice", "", "", "", ""), ErrActionRequired)
	entries, err := s.List(Query{})
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestMemStoreDropsOldestAtCap(t *testing.T) {
	s := newMemStore(2)
	require.NoError(t, s.Log("a.1", "x", "", "", "", ""))
	require.NoError(t, s.Log("a.2", "x", "", "", "", ""))
	require.NoError(t, s.Log("a.3", "x", "", "", "", ""))

	entries, err := s.List(Query{})
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.Equal(t, "a.3", entries[0].Action)
	require.Equal(t, "a.2", entries[1].Action)
}

func TestMemStoreConcurrentLog(t *testing.T) {
	s := NewMemStore()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if err := s.Log(fmt.Sprintf("action.%d", j), "actor", "ns", "", "", ""); err != nil {
					t.Error(err)
					return
				}
			}
		}(i)
	}
	wg.Wait()

	entries, err := s.List(Query{Limit: maxListLimit})
	require.NoError(t, err)
	require.Len(t, entries, 400)
}

func TestNewPostgresStoreNilDatabase(t *testing.T) {
	s := NewPostgresStore(nil)
	require.ErrorIs(t, s.Log("a", "b", "", "", "", ""), ErrNilDatabase)
	_, err := s.List(Query{})
	require.ErrorIs(t, err, ErrNilDatabase)
}

func TestDetailJSON(t *testing.T) {
	require.Nil(t, detailJSON(""))
	require.JSONEq(t, `{"a":1}`, string(detailJSON(`{"a":1}`)))
	require.JSONEq(t, `"plain text"`, string(detailJSON("plain text")))
}
