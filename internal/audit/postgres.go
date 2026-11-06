package audit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"go.pilab.hu/agenthub/internal/db"
)

// ErrNilDatabase is returned when the store was built without a connection.
var ErrNilDatabase = errors.New("audit: nil database")

// logTimeout bounds a single audit write so a slow database cannot stall a
// request path for long.
const logTimeout = 2 * time.Second

// PostgresStore implements AuditStore against agenthub.audit_log.
type PostgresStore struct {
	db      *bun.DB
	timeout time.Duration
}

// NewPostgresStore constructs a Postgres-backed audit store.
func NewPostgresStore(database *bun.DB) *PostgresStore {
	return &PostgresStore{db: database, timeout: logTimeout}
}

// Log records one entry. Errors are returned to the caller; the store never
// panics and never retries.
func (s *PostgresStore) Log(action, actor, namespace, asset, version, detail string) error {
	if s == nil || s.db == nil {
		return ErrNilDatabase
	}
	if strings.TrimSpace(action) == "" {
		return ErrActionRequired
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	model := &db.AuditLog{
		Action:    action,
		ActorSub:  actor,
		Namespace: namespace,
		Asset:     asset,
		Version:   version,
		Detail:    detailJSON(detail),
	}
	_, err := s.db.NewInsert().Model(model).Exec(ctx)
	return err
}

// List returns matching entries, newest first.
func (s *PostgresStore) List(q Query) ([]Entry, error) {
	if s == nil || s.db == nil {
		return nil, ErrNilDatabase
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	var models []db.AuditLog
	sel := s.db.NewSelect().Model(&models)
	if q.Namespace != "" {
		sel = sel.Where("namespace = ?", q.Namespace)
	}
	if q.Actor != "" {
		sel = sel.Where("actor_sub = ?", q.Actor)
	}
	if q.Action != "" {
		sel = sel.Where("action = ?", q.Action)
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}
	if err := sel.
		OrderExpr("at DESC, id DESC").
		Limit(int64(limit)).
		Offset(int64(offset)).
		Scan(ctx); err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(models))
	for _, m := range models {
		out = append(out, entryFromModel(m))
	}
	return out, nil
}

func entryFromModel(m db.AuditLog) Entry {
	return Entry{
		At:        m.At,
		Action:    m.Action,
		Actor:     m.ActorSub,
		Namespace: m.Namespace,
		Asset:     m.Asset,
		Version:   m.Version,
		Detail:    string(m.Detail),
	}
}

// detailJSON stores detail as-is when it already is valid JSON, otherwise it
// wraps it in a JSON string so the column stays valid jsonb.
func detailJSON(detail string) json.RawMessage {
	if detail == "" {
		return nil
	}
	if json.Valid([]byte(detail)) {
		return json.RawMessage(detail)
	}
	b, err := json.Marshal(detail)
	if err != nil {
		return nil
	}
	return json.RawMessage(b)
}
