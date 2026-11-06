// Package registry holds the in-memory registry store used by the
// RegistryService handlers. A Postgres-backed implementation replaces this in
// a later M1 slice; the handler depends on the Store interface so the swap is
// mechanical.
package registry

import (
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"
)

// Sentinel errors returned by MemStore.
var (
	ErrInvalidSlug = errors.New("invalid namespace slug")
	ErrDuplicate   = errors.New("namespace already exists")
	ErrNotFound    = errors.New("namespace not found")
)

// Store is the persistence contract for namespaces.
type Store interface {
	Create(ns *commonv1.Namespace) (*commonv1.Namespace, error)
	Get(slug string) (*commonv1.Namespace, bool)
	List() []*commonv1.Namespace
}

// MemStore is an in-memory Store.
type MemStore struct {
	mu    sync.RWMutex
	items map[string]*commonv1.Namespace
}

// NewMemStore returns an empty in-memory store.
func NewMemStore() *MemStore { return &MemStore{items: map[string]*commonv1.Namespace{}} }

func (m *MemStore) Create(ns *commonv1.Namespace) (*commonv1.Namespace, error) {
	if ns == nil {
		return nil, errors.New("namespace is nil")
	}
	slug := strings.TrimSpace(ns.Slug)
	if !validSlug.MatchString(slug) {
		return nil, ErrInvalidSlug
	}
	if ns.DisplayName == "" {
		ns.DisplayName = slug
	}
	if ns.Visibility == commonv1.NamespaceVisibility_NAMESPACE_VISIBILITY_UNSPECIFIED {
		ns.Visibility = commonv1.NamespaceVisibility_NAMESPACE_VISIBILITY_PUBLIC
	}
	now := time.Now().UTC()
	if ns.CreatedAt == nil {
		ns.CreatedAt = timestamppb.New(now)
	}
	cp := clone(ns)
	cp.Slug = slug

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.items[slug]; ok {
		return nil, ErrDuplicate
	}
	m.items[slug] = cp
	return clone(cp), nil
}

func (m *MemStore) Get(slug string) (*commonv1.Namespace, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ns, ok := m.items[strings.TrimSpace(slug)]
	if !ok {
		return nil, false
	}
	return clone(ns), true
}

func (m *MemStore) List() []*commonv1.Namespace {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*commonv1.Namespace, 0, len(m.items))
	for _, ns := range m.items {
		out = append(out, clone(ns))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out
}

var validSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

func clone(ns *commonv1.Namespace) *commonv1.Namespace {
	if ns == nil {
		return nil
	}
	return proto.Clone(ns).(*commonv1.Namespace)
}
