// Package catalog holds the in-memory catalog store used by the CatalogService
// handlers.
package catalog

import (
	"errors"
	"sort"
	"strings"
	"sync"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"
)

// Store is the persistence contract for catalog reads.
type Store interface {
	UpsertAsset(a *Asset) error
	GetAsset(ns string, kind commonv1.AssetKind, name string) (*Asset, bool)
	ListAssets() []*Asset
	UpsertVersion(v *Version) error
	ListVersions(ns string, kind commonv1.AssetKind, name string) []*Version
	GetVersion(ns string, kind commonv1.AssetKind, name, version string) (*Version, bool)
}

// Asset is an in-memory catalog asset.
type Asset struct {
	Namespace      string
	Kind           commonv1.AssetKind
	Name           string
	DisplayName    string
	Summary        string
	IconDigest     string
	Tags           []string
	Categories     []string
	License        string
	Repository     string
	LatestVer      string
	LandingVisible bool
	CreatedAt      *timestamppb.Timestamp
	UpdatedAt      *timestamppb.Timestamp
}

// Version is an in-memory catalog version.
type Version struct {
	Namespace      string
	Kind           commonv1.AssetKind
	Name           string
	Version        string
	Status         commonv1.VersionStatus
	Manifest       []byte
	ManifestDigest *commonv1.Digest
	BundleDigest   *commonv1.Digest
	BundleSize     uint64
	PublishedAt    *timestamppb.Timestamp
	Readme         string
	Changelog      string
	Verified       bool
}

// Sentinel errors returned by MemStore.
var (
	ErrNotFound = errors.New("not found")
)

// MemStore is an in-memory Store.
type MemStore struct {
	mu       sync.RWMutex
	assets   map[string]*Asset
	versions map[string][]*Version
}

// NewMemStore returns an empty store.
func NewMemStore() *MemStore {
	return &MemStore{assets: map[string]*Asset{}, versions: map[string][]*Version{}}
}

func assetKey(ns string, kind commonv1.AssetKind, name string) string {
	return ns + "\x00" + kind.String() + "\x00" + name
}

func (m *MemStore) UpsertAsset(a *Asset) error {
	if a == nil || a.Namespace == "" || a.Name == "" {
		return errors.New("invalid asset")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.assets[assetKey(a.Namespace, a.Kind, a.Name)] = cloneAsset(a)
	return nil
}

func (m *MemStore) GetAsset(ns string, kind commonv1.AssetKind, name string) (*Asset, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	a, ok := m.assets[assetKey(ns, kind, name)]
	if !ok {
		return nil, false
	}
	return cloneAsset(a), true
}

func (m *MemStore) ListAssets() []*Asset {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Asset, 0, len(m.assets))
	for _, a := range m.assets {
		out = append(out, cloneAsset(a))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (m *MemStore) UpsertVersion(v *Version) error {
	if v == nil || v.Namespace == "" || v.Name == "" || v.Version == "" {
		return errors.New("invalid version")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := assetKey(v.Namespace, v.Kind, v.Name)
	list := m.versions[k]
	for i, existing := range list {
		if existing.Version == v.Version {
			list[i] = cloneVersion(v)
			m.versions[k] = list
			return nil
		}
	}
	m.versions[k] = append(list, cloneVersion(v))
	return nil
}

func (m *MemStore) ListVersions(ns string, kind commonv1.AssetKind, name string) []*Version {
	m.mu.RLock()
	defer m.mu.RUnlock()
	list := m.versions[assetKey(ns, kind, name)]
	out := make([]*Version, 0, len(list))
	for _, v := range list {
		out = append(out, cloneVersion(v))
	}
	return out
}

func (m *MemStore) GetVersion(ns string, kind commonv1.AssetKind, name, version string) (*Version, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, v := range m.versions[assetKey(ns, kind, name)] {
		if v.Version == version {
			return cloneVersion(v), true
		}
	}
	return nil, false
}

func cloneAsset(a *Asset) *Asset {
	if a == nil {
		return nil
	}
	cp := *a
	cp.Tags = append([]string(nil), a.Tags...)
	cp.Categories = append([]string(nil), a.Categories...)
	if a.CreatedAt != nil {
		cp.CreatedAt = proto.Clone(a.CreatedAt).(*timestamppb.Timestamp)
	}
	if a.UpdatedAt != nil {
		cp.UpdatedAt = proto.Clone(a.UpdatedAt).(*timestamppb.Timestamp)
	}
	return &cp
}

func cloneVersion(v *Version) *Version {
	if v == nil {
		return nil
	}
	var md, bd *commonv1.Digest
	if v.ManifestDigest != nil {
		md = proto.Clone(v.ManifestDigest).(*commonv1.Digest)
	}
	if v.BundleDigest != nil {
		bd = proto.Clone(v.BundleDigest).(*commonv1.Digest)
	}
	cp := &Version{
		Namespace:      v.Namespace,
		Kind:           v.Kind,
		Name:           v.Name,
		Version:        v.Version,
		Status:         v.Status,
		Manifest:       append([]byte(nil), v.Manifest...),
		ManifestDigest: md,
		BundleDigest:   bd,
		BundleSize:     v.BundleSize,
		Readme:         v.Readme,
		Changelog:      v.Changelog,
		Verified:       v.Verified,
	}
	if v.PublishedAt != nil {
		cp.PublishedAt = proto.Clone(v.PublishedAt).(*timestamppb.Timestamp)
	}
	return cp
}

// AssetRefKey builds a stable string key from an AssetRef (used by handlers).
func AssetRefKey(ref *commonv1.AssetRef) string {
	if ref == nil {
		return ""
	}
	return ref.Namespace + "\x00" + ref.Kind.String() + "\x00" + ref.Name
}

var (
	_ = proto.Clone // keep import available for future use
	_ = strings.TrimSpace
)
