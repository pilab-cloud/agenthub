// Package registry provides a Postgres-backed Store implementation.
package registry

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/uptrace/bun"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"
	"go.pilab.hu/agenthub/internal/db"
)

// PostgresNamespaceStore implements Store using bun.
type PostgresNamespaceStore struct {
	db    *bun.DB
	hubID string
}

// NewPostgresNamespaceStore constructs a Postgres-backed namespace store.
func NewPostgresNamespaceStore(db *bun.DB, hubID string) *PostgresNamespaceStore {
	if hubID == "" {
		hubID = "local"
	}
	return &PostgresNamespaceStore{db: db, hubID: hubID}
}

func (s *PostgresNamespaceStore) ensureHub(ctx context.Context) {
	_, _ = s.db.ExecContext(ctx, "INSERT INTO agenthub.hubs (id, base_url, protocol) VALUES (?, ?, 1) ON CONFLICT (id) DO NOTHING", s.hubID, "http://localhost")
}

func (s *PostgresNamespaceStore) Create(ns *commonv1.Namespace) (*commonv1.Namespace, error) {
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

	s.ensureHub(context.Background())
	model := db.Namespace{
		ID:          newUUID(),
		HubID:       s.hubID,
		Slug:        slug,
		DisplayName: ns.DisplayName,
		Description: ns.Description,
		Visibility:  ns.Visibility.String(),
		CreatedAt:   now,
	}
	_, err := s.db.NewInsert().Model(&model).Exec(context.Background())
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrDuplicate
		}
		return nil, err
	}
	cp := clone(ns)
	cp.Slug = slug
	return clone(cp), nil
}

func (s *PostgresNamespaceStore) Get(slug string) (*commonv1.Namespace, bool) {
	slug = strings.TrimSpace(slug)
	var m db.Namespace
	err := s.db.NewSelect().Model(&m).
		Where("hub_id = ? AND slug = ?", s.hubID, slug).
		Scan(context.Background())
	if err != nil {
		return nil, false
	}
	return namespaceToProto(m), true
}

func (s *PostgresNamespaceStore) List() []*commonv1.Namespace {
	var models []db.Namespace
	err := s.db.NewSelect().Model(&models).
		Where("hub_id = ?", s.hubID).
		Order("slug ASC").
		Scan(context.Background())
	if err != nil {
		return nil
	}
	out := make([]*commonv1.Namespace, 0, len(models))
	for _, m := range models {
		out = append(out, namespaceToProto(m))
	}
	return out
}

func namespaceToProto(m db.Namespace) *commonv1.Namespace {
	v, ok := commonv1.NamespaceVisibility_value[m.Visibility]
	if !ok {
		v = int32(commonv1.NamespaceVisibility_NAMESPACE_VISIBILITY_PUBLIC)
	}
	ns := &commonv1.Namespace{
		Slug:        m.Slug,
		DisplayName: m.DisplayName,
		Description: m.Description,
		Visibility:  commonv1.NamespaceVisibility(v),
	}
	if !m.CreatedAt.IsZero() {
		ns.CreatedAt = timestamppb.New(m.CreatedAt)
	}
	return clone(ns)
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique") || strings.Contains(msg, "duplicate key")
}

// NewUUID generates a random UUID string.
func NewUUID() string {
	return newUUIDImpl()
}

func newUUID() string { return newUUIDImpl() }

func newUUIDImpl() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
