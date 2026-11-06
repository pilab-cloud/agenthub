package authz

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"go.pilab.hu/agenthub/internal/db"
)

// ErrNilDatabase is returned when the store was built without a connection.
var ErrNilDatabase = errors.New("authz: nil database")

// roleTimeout bounds a single role-store operation so a slow database cannot
// stall a request path for long (same policy as the audit store, M3.1).
const roleTimeout = 2 * time.Second

// PostgresRoleStore implements Store against agenthub.namespace_roles (M3.6),
// so role bindings survive a restart. Every method mirrors MemStore: same
// validation, same ErrBindingNotFound on a missing revoke, same
// subject-ordered listing.
type PostgresRoleStore struct {
	db      *bun.DB
	timeout time.Duration
}

// NewPostgresRoleStore constructs a Postgres-backed role store.
func NewPostgresRoleStore(database *bun.DB) *PostgresRoleStore {
	return &PostgresRoleStore{db: database, timeout: roleTimeout}
}

// opCtx returns a bounded context for one store operation.
func (s *PostgresRoleStore) opCtx() (context.Context, context.CancelFunc, error) {
	if s == nil || s.db == nil {
		return nil, nil, ErrNilDatabase
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	return ctx, cancel, nil
}

// GetRole implements Store. A lookup failure (including a nil database) is
// reported as "no binding": the caller treats it as a denial, never as a
// fallback (deny by default).
func (s *PostgresRoleStore) GetRole(hub, namespace, subject string) (Role, bool) {
	ctx, cancel, err := s.opCtx()
	if err != nil {
		return "", false
	}
	defer cancel()
	var m db.NamespaceRole
	err = s.db.NewSelect().Model(&m).
		Where("hub_id = ? AND namespace = ? AND subject = ?", hub, namespace, subject).
		Scan(ctx)
	if err != nil {
		return "", false
	}
	return Role(m.Role), true
}

// ListRoles implements Store, ordered by subject like MemStore.
func (s *PostgresRoleStore) ListRoles(hub, namespace string) []Binding {
	ctx, cancel, err := s.opCtx()
	if err != nil {
		return nil
	}
	defer cancel()
	var models []db.NamespaceRole
	if err := s.db.NewSelect().Model(&models).
		Where("hub_id = ? AND namespace = ?", hub, namespace).
		OrderExpr("subject ASC").
		Scan(ctx); err != nil {
		return nil
	}
	out := make([]Binding, 0, len(models))
	for _, m := range models {
		out = append(out, Binding{
			Hub:       m.HubID,
			Namespace: m.Namespace,
			Subject:   m.Subject,
			Role:      Role(m.Role),
		})
	}
	return out
}

// Grant implements Store, upserting the binding (an existing binding for the
// same subject is replaced, matching MemStore).
func (s *PostgresRoleStore) Grant(hub, namespace, subject string, role Role) error {
	if subject == "" {
		return ErrInvalidSubject
	}
	if namespace == "" {
		return ErrInvalidNamespace
	}
	if !role.Valid() {
		return fmt.Errorf("%w: %q", ErrInvalidRole, role)
	}
	ctx, cancel, err := s.opCtx()
	if err != nil {
		return err
	}
	defer cancel()
	model := &db.NamespaceRole{
		HubID:     hub,
		Namespace: namespace,
		Subject:   subject,
		Role:      string(role),
	}
	_, err = s.db.NewInsert().Model(model).
		On("CONFLICT (hub_id, namespace, subject) DO UPDATE").
		Set("role = EXCLUDED.role").
		Set("updated_at = now()").
		Exec(ctx)
	return err
}

// Revoke implements Store, returning ErrBindingNotFound when no binding
// exists.
func (s *PostgresRoleStore) Revoke(hub, namespace, subject string) error {
	ctx, cancel, err := s.opCtx()
	if err != nil {
		return err
	}
	defer cancel()
	res, err := s.db.NewDelete().Model((*db.NamespaceRole)(nil)).
		Where("hub_id = ? AND namespace = ? AND subject = ?", hub, namespace, subject).
		Exec(ctx)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: %q", ErrBindingNotFound, subject)
	}
	return nil
}

var _ Store = (*PostgresRoleStore)(nil)
