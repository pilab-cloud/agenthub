package registry

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Sentinel errors for signing keys.
var (
	ErrSigningKeyDuplicate = ErrDuplicate
	ErrSigningKeyNotFound  = ErrNotFound
)

// SigningKey is an in-memory signing key record for a namespace.
type SigningKey struct {
	Namespace string
	Kid       string
	Algorithm string
	Custody   string
	PublicKey string
	KmsKeyRef string
	CreatedAt *timestamppb.Timestamp
	ValidFrom *timestamppb.Timestamp
	RevokedAt *timestamppb.Timestamp
}

// SigningKeyStore is the persistence contract for namespace signing keys.
type SigningKeyStore interface {
	Register(key *SigningKey) (*SigningKey, error)
	Create(key *SigningKey) (*SigningKey, error)
	Add(key *SigningKey) (*SigningKey, error)
	Upsert(key *SigningKey) (*SigningKey, error)
	Save(key *SigningKey) (*SigningKey, error)
	RegisterSigningKey(key *SigningKey) (*SigningKey, error)

	Revoke(namespace, kid string) (*SigningKey, error)
	Delete(namespace, kid string) (*SigningKey, error)
	Remove(namespace, kid string) (*SigningKey, error)
	RevokeSigningKey(namespace, kid string) (*SigningKey, error)

	Get(namespace, kid string) (*SigningKey, bool)
	Find(namespace, kid string) (*SigningKey, bool)
	GetSigningKey(namespace, kid string) (*SigningKey, bool)

	List(namespace string) []*SigningKey
	ListSigningKeys(namespace string) []*SigningKey
	ListAll() []*SigningKey
}

// MemSigningKeyStore is an in-memory SigningKeyStore.
type MemSigningKeyStore struct {
	mu    sync.RWMutex
	items map[string]*SigningKey
}

// NewMemSigningKeyStore returns an empty in-memory store.
func NewMemSigningKeyStore() *MemSigningKeyStore {
	return &MemSigningKeyStore{items: map[string]*SigningKey{}}
}

func signingKeyKey(namespace, kid string) string {
	return strings.TrimSpace(namespace) + "\x00" + strings.TrimSpace(kid)
}

func cloneSigningKey(k *SigningKey) *SigningKey {
	if k == nil {
		return nil
	}
	cp := &SigningKey{
		Namespace: k.Namespace,
		Kid:       k.Kid,
		Algorithm: k.Algorithm,
		Custody:   k.Custody,
		PublicKey: k.PublicKey,
		KmsKeyRef: k.KmsKeyRef,
	}
	if k.CreatedAt != nil {
		cp.CreatedAt = proto.Clone(k.CreatedAt).(*timestamppb.Timestamp)
	}
	if k.ValidFrom != nil {
		cp.ValidFrom = proto.Clone(k.ValidFrom).(*timestamppb.Timestamp)
	}
	if k.RevokedAt != nil {
		cp.RevokedAt = proto.Clone(k.RevokedAt).(*timestamppb.Timestamp)
	}
	return cp
}

func (m *MemSigningKeyStore) normalize(key *SigningKey) {
	if key == nil {
		return
	}
	if strings.TrimSpace(key.Namespace) != "" {
		key.Namespace = strings.TrimSpace(key.Namespace)
	}
	if strings.TrimSpace(key.Kid) != "" {
		key.Kid = strings.TrimSpace(key.Kid)
	}
	if key.Algorithm == "" {
		key.Algorithm = "ed25519"
	}
	if key.Custody == "" {
		key.Custody = "client"
	}
	now := time.Now().UTC()
	if key.CreatedAt == nil {
		key.CreatedAt = timestamppb.New(now)
	}
	if key.ValidFrom == nil {
		key.ValidFrom = timestamppb.New(now)
	}
}

func (m *MemSigningKeyStore) Register(key *SigningKey) (*SigningKey, error) {
	if key == nil {
		return nil, errors.New("signing key is nil")
	}
	if strings.TrimSpace(key.Namespace) == "" {
		return nil, errors.New("namespace is required")
	}
	if strings.TrimSpace(key.Kid) == "" {
		return nil, errors.New("kid is required")
	}
	cp := cloneSigningKey(key)
	m.normalize(cp)

	m.mu.Lock()
	defer m.mu.Unlock()
	k := signingKeyKey(cp.Namespace, cp.Kid)
	if _, ok := m.items[k]; ok {
		return nil, ErrSigningKeyDuplicate
	}
	m.items[k] = cloneSigningKey(cp)
	return cloneSigningKey(m.items[k]), nil
}

func (m *MemSigningKeyStore) Create(key *SigningKey) (*SigningKey, error) {
	return m.Register(key)
}

func (m *MemSigningKeyStore) Add(key *SigningKey) (*SigningKey, error) {
	return m.Register(key)
}

func (m *MemSigningKeyStore) Upsert(key *SigningKey) (*SigningKey, error) {
	if key == nil {
		return nil, errors.New("signing key is nil")
	}
	cp := cloneSigningKey(key)
	m.normalize(cp)
	m.mu.Lock()
	defer m.mu.Unlock()
	k := signingKeyKey(cp.Namespace, cp.Kid)
	existing, ok := m.items[k]
	if ok && existing != nil && existing.CreatedAt != nil {
		cp.CreatedAt = cloneTimestamp(existing.CreatedAt)
	}
	m.items[k] = cloneSigningKey(cp)
	return cloneSigningKey(m.items[k]), nil
}

func (m *MemSigningKeyStore) Save(key *SigningKey) (*SigningKey, error) {
	return m.Register(key)
}

func (m *MemSigningKeyStore) RegisterSigningKey(key *SigningKey) (*SigningKey, error) {
	return m.Register(key)
}

func (m *MemSigningKeyStore) Revoke(namespace, kid string) (*SigningKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := signingKeyKey(namespace, kid)
	key, ok := m.items[k]
	if !ok || key == nil {
		return nil, ErrSigningKeyNotFound
	}
	if key.RevokedAt == nil {
		key.RevokedAt = timestamppb.New(time.Now().UTC())
	}
	m.items[k] = key
	return cloneSigningKey(key), nil
}

func (m *MemSigningKeyStore) Delete(namespace, kid string) (*SigningKey, error) {
	return m.Revoke(namespace, kid)
}

func (m *MemSigningKeyStore) Remove(namespace, kid string) (*SigningKey, error) {
	return m.Revoke(namespace, kid)
}

func (m *MemSigningKeyStore) RevokeSigningKey(namespace, kid string) (*SigningKey, error) {
	return m.Revoke(namespace, kid)
}

func (m *MemSigningKeyStore) Get(namespace, kid string) (*SigningKey, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	key, ok := m.items[signingKeyKey(namespace, kid)]
	if !ok || key == nil {
		return nil, false
	}
	return cloneSigningKey(key), true
}

func (m *MemSigningKeyStore) Find(namespace, kid string) (*SigningKey, bool) {
	return m.Get(namespace, kid)
}

func (m *MemSigningKeyStore) GetSigningKey(namespace, kid string) (*SigningKey, bool) {
	return m.Get(namespace, kid)
}

func (m *MemSigningKeyStore) List(namespace string) []*SigningKey {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*SigningKey, 0)
	for _, v := range m.items {
		if strings.TrimSpace(namespace) != "" && v.Namespace != strings.TrimSpace(namespace) {
			continue
		}
		out = append(out, cloneSigningKey(v))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Kid < out[j].Kid
	})
	return out
}

func (m *MemSigningKeyStore) ListSigningKeys(namespace string) []*SigningKey {
	return m.List(namespace)
}

func (m *MemSigningKeyStore) ListAll() []*SigningKey {
	return m.List("")
}

func cloneTimestamp(ts *timestamppb.Timestamp) *timestamppb.Timestamp {
	if ts == nil {
		return nil
	}
	return proto.Clone(ts).(*timestamppb.Timestamp)
}
