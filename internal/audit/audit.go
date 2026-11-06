// Package audit provides the audit-log store used to record privileged
// operations (M3.1). The default store is in-memory; a Postgres-backed store
// (agenthub.audit_log) is available for deployments that persist audit events.
package audit

import (
	"errors"
	"strings"
	"sync"
	"time"
)

// Entry is one immutable audit-log record.
type Entry struct {
	At        time.Time
	Action    string
	Actor     string
	Namespace string
	Asset     string
	Version   string
	Detail    string
}

// Query filters and pages List results. Zero-value fields are ignored.
type Query struct {
	Namespace string
	Actor     string
	Action    string
	Limit     int
	Offset    int
}

// AuditStore records and reads audit-log entries.
type AuditStore interface {
	// Log appends one entry. detail is free-form; implementations store it as
	// JSON when it parses as JSON.
	Log(action, actor, namespace, asset, version, detail string) error
	// List returns matching entries, newest first.
	List(q Query) ([]Entry, error)
}

// ErrActionRequired is returned when Log is called without an action.
var ErrActionRequired = errors.New("audit: action is required")

const (
	// defaultMaxEntries caps the in-memory store; oldest entries are dropped.
	defaultMaxEntries = 4096
	// defaultListLimit is applied when Query.Limit is not set.
	defaultListLimit = 100
	// maxListLimit bounds a single List call.
	maxListLimit = 1000
)

// MemStore is an in-memory AuditStore. It keeps at most max entries and
// drops the oldest ones when the cap is reached. Safe for concurrent use.
type MemStore struct {
	mu      sync.RWMutex
	entries []Entry
	max     int
}

// NewMemStore returns an empty in-memory store with the default capacity.
func NewMemStore() *MemStore { return newMemStore(defaultMaxEntries) }

func newMemStore(max int) *MemStore {
	if max <= 0 {
		max = defaultMaxEntries
	}
	return &MemStore{entries: make([]Entry, 0, 8), max: max}
}

// Log records one entry.
func (m *MemStore) Log(action, actor, namespace, asset, version, detail string) error {
	if strings.TrimSpace(action) == "" {
		return ErrActionRequired
	}
	e := Entry{
		At:        time.Now().UTC(),
		Action:    action,
		Actor:     actor,
		Namespace: namespace,
		Asset:     asset,
		Version:   version,
		Detail:    detail,
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, e)
	if over := len(m.entries) - m.max; over > 0 {
		// Drop the oldest entries; copy handles the overlap.
		m.entries = append(m.entries[:0], m.entries[over:]...)
	}
	return nil
}

// List returns matching entries, newest first.
func (m *MemStore) List(q Query) ([]Entry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]Entry, 0, len(m.entries))
	for i := len(m.entries) - 1; i >= 0; i-- {
		e := m.entries[i]
		if q.Namespace != "" && e.Namespace != q.Namespace {
			continue
		}
		if q.Actor != "" && e.Actor != q.Actor {
			continue
		}
		if q.Action != "" && e.Action != q.Action {
			continue
		}
		out = append(out, e)
	}
	if q.Offset > 0 {
		if q.Offset >= len(out) {
			return []Entry{}, nil
		}
		out = out[q.Offset:]
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
