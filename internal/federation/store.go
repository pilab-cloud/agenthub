package federation

import (
	"sort"
	"sync"

	"google.golang.org/protobuf/proto"

	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"
)

// UpstreamAsset is one asset synced from an upstream hub.
type UpstreamAsset struct {
	Hub            string
	Ref            *commonv1.AssetRef
	ManifestDigest *commonv1.Digest
	BundleDigest   *commonv1.Digest
	Manifest       []byte
}

// UpstreamStore persists synced upstream assets.
type UpstreamStore interface {
	Upsert(a UpstreamAsset) error
	Get(hub, ns string, kind commonv1.AssetKind, name string) (UpstreamAsset, bool)
	List(hub string) []UpstreamAsset
	DeleteHub(hub string)
}

// MemUpstreamStore is an in-memory UpstreamStore.
type MemUpstreamStore struct {
	mu    sync.RWMutex
	items map[string]UpstreamAsset
}

// NewMemUpstreamStore returns an empty store.
func NewMemUpstreamStore() *MemUpstreamStore {
	return &MemUpstreamStore{items: map[string]UpstreamAsset{}}
}

func upstreamKey(hub, ns string, kind commonv1.AssetKind, name string) string {
	return hub + "\x00" + ns + "\x00" + kind.String() + "\x00" + name
}

func (m *MemUpstreamStore) Upsert(a UpstreamAsset) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a.Ref == nil {
		a.Ref = &commonv1.AssetRef{}
	} else {
		a.Ref = proto.Clone(a.Ref).(*commonv1.AssetRef)
	}
	m.items[upstreamKey(a.Hub, a.Ref.Namespace, a.Ref.Kind, a.Ref.Name)] = a
	return nil
}

func (m *MemUpstreamStore) Get(hub, ns string, kind commonv1.AssetKind, name string) (UpstreamAsset, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	a, ok := m.items[upstreamKey(hub, ns, kind, name)]
	if ok {
		a.Ref = proto.Clone(a.Ref).(*commonv1.AssetRef)
	}
	return a, ok
}

func (m *MemUpstreamStore) List(hub string) []UpstreamAsset {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]UpstreamAsset, 0)
	for _, a := range m.items {
		if hub == "" || a.Hub == hub {
			cp := a
			cp.Ref = proto.Clone(a.Ref).(*commonv1.AssetRef)
			out = append(out, cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref.Name < out[j].Ref.Name })
	return out
}

func (m *MemUpstreamStore) DeleteHub(hub string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, a := range m.items {
		if a.Hub == hub {
			delete(m.items, k)
		}
	}
}
