// Package registry holds the in-memory draft store used by the web dashboard
// draft lifecycle (M1.5).
package registry

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"
)

// Sentinel errors for drafts.
var (
	ErrDraftNotFound = errors.New("draft not found")
)

// DraftFile is a file belonging to a web draft.
type DraftFile struct {
	Path    string
	Content []byte
}

// Draft is an in-memory web dashboard draft.
type Draft struct {
	DraftID   string
	Namespace string
	Kind      commonv1.AssetKind
	Name      string
	AuthorSub string
	Manifest  []byte
	Files     []DraftFile
	Status    string
	CreatedAt *timestamppb.Timestamp
	UpdatedAt *timestamppb.Timestamp
}

// DraftStore is the persistence contract for drafts.
type DraftStore interface {
	Create(d *Draft) (*Draft, error)
	CreateDraft(d *Draft) (*Draft, error)

	Get(id string) (*Draft, bool)
	GetDraft(id string) (*Draft, bool)

	Update(d *Draft) (*Draft, error)
	UpdateDraft(d *Draft) (*Draft, error)

	Delete(id string) error
	DeleteDraft(id string) error
	Discard(id string) error
	DiscardDraft(id string) error
	Remove(id string) error

	List() []*Draft
	ListDrafts() []*Draft
}

// MemDraftStore is an in-memory DraftStore.
type MemDraftStore struct {
	mu    sync.RWMutex
	items map[string]*Draft
}

// NewMemDraftStore returns an empty in-memory draft store.
func NewMemDraftStore() *MemDraftStore {
	return &MemDraftStore{items: map[string]*Draft{}}
}

func cloneDraftFile(f DraftFile) DraftFile {
	return DraftFile{Path: f.Path, Content: append([]byte(nil), f.Content...)}
}

func cloneDraft(d *Draft) *Draft {
	if d == nil {
		return nil
	}
	cp := &Draft{
		DraftID:   d.DraftID,
		Namespace: d.Namespace,
		Kind:      d.Kind,
		Name:      d.Name,
		AuthorSub: d.AuthorSub,
		Manifest:  append([]byte(nil), d.Manifest...),
		Status:    d.Status,
	}
	if d.Files != nil {
		cp.Files = make([]DraftFile, len(d.Files))
		for i, f := range d.Files {
			cp.Files[i] = cloneDraftFile(f)
		}
	}
	if d.CreatedAt != nil {
		cp.CreatedAt = proto.Clone(d.CreatedAt).(*timestamppb.Timestamp)
	}
	if d.UpdatedAt != nil {
		cp.UpdatedAt = proto.Clone(d.UpdatedAt).(*timestamppb.Timestamp)
	}
	return cp
}

func (m *MemDraftStore) Create(d *Draft) (*Draft, error) {
	if d == nil {
		return nil, errors.New("draft is nil")
	}
	if d.DraftID == "" {
		return nil, errors.New("draft id is required")
	}
	if d.Status == "" {
		d.Status = "draft"
	}
	now := time.Now().UTC()
	if d.CreatedAt == nil {
		d.CreatedAt = timestamppb.New(now)
	}
	if d.UpdatedAt == nil {
		d.UpdatedAt = timestamppb.New(now)
	}
	cp := cloneDraft(d)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.items[cp.DraftID]; ok {
		return nil, ErrDuplicate
	}
	m.items[cp.DraftID] = cp
	return cloneDraft(cp), nil
}

func (m *MemDraftStore) CreateDraft(d *Draft) (*Draft, error) { return m.Create(d) }

func (m *MemDraftStore) Get(id string) (*Draft, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d, ok := m.items[strings.TrimSpace(id)]
	if !ok || d == nil {
		return nil, false
	}
	return cloneDraft(d), true
}

func (m *MemDraftStore) GetDraft(id string) (*Draft, bool) { return m.Get(id) }

func (m *MemDraftStore) Update(d *Draft) (*Draft, error) {
	if d == nil {
		return nil, errors.New("draft is nil")
	}
	if strings.TrimSpace(d.DraftID) == "" {
		return nil, errors.New("draft id is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.items[strings.TrimSpace(d.DraftID)]
	if !ok || existing == nil {
		return nil, ErrDraftNotFound
	}
	cp := cloneDraft(d)
	if cp.Status == "" {
		cp.Status = existing.Status
	}
	if cp.CreatedAt == nil {
		cp.CreatedAt = cloneTimestamp(existing.CreatedAt)
	}
	cp.UpdatedAt = timestamppb.New(time.Now().UTC())
	m.items[cp.DraftID] = cp
	return cloneDraft(cp), nil
}

func (m *MemDraftStore) UpdateDraft(d *Draft) (*Draft, error) { return m.Update(d) }

func (m *MemDraftStore) delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	id = strings.TrimSpace(id)
	if _, ok := m.items[id]; !ok {
		return ErrDraftNotFound
	}
	delete(m.items, id)
	return nil
}

func (m *MemDraftStore) Delete(id string) error       { return m.delete(id) }
func (m *MemDraftStore) DeleteDraft(id string) error  { return m.delete(id) }
func (m *MemDraftStore) Discard(id string) error      { return m.delete(id) }
func (m *MemDraftStore) DiscardDraft(id string) error { return m.delete(id) }
func (m *MemDraftStore) Remove(id string) error       { return m.delete(id) }

func (m *MemDraftStore) List() []*Draft {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Draft, 0, len(m.items))
	for _, d := range m.items {
		out = append(out, cloneDraft(d))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DraftID < out[j].DraftID })
	return out
}

func (m *MemDraftStore) ListDrafts() []*Draft { return m.List() }

var _ DraftStore = (*MemDraftStore)(nil)
