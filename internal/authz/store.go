package authz

import (
	"fmt"
	"slices"
	"strings"
	"sync"
)

// Binding ties one subject to a role on one hub namespace.
type Binding struct {
	Hub       string
	Namespace string
	Subject   string
	Role      Role
}

// Store is the namespace RBAC store.
//
// GetRole and ListRoles are the read paths used by handlers. Grant and Revoke
// are admin operations: callers must verify the actor is a hub admin (see
// IsAdmin) or the namespace owner before invoking them.
type Store interface {
	// GetRole returns the role of subject on hub/namespace, ok=false when no
	// binding exists.
	GetRole(hub, namespace, subject string) (Role, bool)
	// ListRoles returns the bindings of hub/namespace ordered by subject.
	ListRoles(hub, namespace string) []Binding
	// Grant assigns role to subject, replacing an existing binding.
	Grant(hub, namespace, subject string, role Role) error
	// Revoke removes the binding of subject, ErrBindingNotFound when absent.
	Revoke(hub, namespace, subject string) error
}

// MemStore is the default in-memory Store (M3.2). Bindings are lost on
// restart; deployments with a database use PostgresRoleStore (M3.6).
type MemStore struct {
	mu       sync.RWMutex
	bindings map[string]Binding
}

// NewMemStore returns an empty in-memory role store.
func NewMemStore() *MemStore {
	return &MemStore{bindings: map[string]Binding{}}
}

func bindingKey(hub, namespace, subject string) string {
	return hub + "\x00" + namespace + "\x00" + subject
}

// GetRole implements Store.
func (s *MemStore) GetRole(hub, namespace, subject string) (Role, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.bindings[bindingKey(hub, namespace, subject)]
	if !ok {
		return "", false
	}
	return b.Role, true
}

// ListRoles implements Store.
func (s *MemStore) ListRoles(hub, namespace string) []Binding {
	s.mu.RLock()
	defer s.mu.RUnlock()
	prefix := hub + "\x00" + namespace + "\x00"
	out := make([]Binding, 0, len(s.bindings))
	for _, b := range s.bindings {
		if strings.HasPrefix(bindingKey(b.Hub, b.Namespace, b.Subject), prefix) {
			out = append(out, b)
		}
	}
	slices.SortFunc(out, func(a, b Binding) int {
		return strings.Compare(a.Subject, b.Subject)
	})
	return out
}

// Grant implements Store.
func (s *MemStore) Grant(hub, namespace, subject string, role Role) error {
	if subject == "" {
		return ErrInvalidSubject
	}
	if namespace == "" {
		return ErrInvalidNamespace
	}
	if !role.Valid() {
		return fmt.Errorf("%w: %q", ErrInvalidRole, role)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bindings[bindingKey(hub, namespace, subject)] = Binding{
		Hub:       hub,
		Namespace: namespace,
		Subject:   subject,
		Role:      role,
	}
	return nil
}

// Revoke implements Store.
func (s *MemStore) Revoke(hub, namespace, subject string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := bindingKey(hub, namespace, subject)
	if _, ok := s.bindings[key]; !ok {
		return fmt.Errorf("%w: %q", ErrBindingNotFound, subject)
	}
	delete(s.bindings, key)
	return nil
}

// Check reports whether id satisfies min on hub/namespace.
//
// Anonymous callers get ErrUnauthenticated; hub admins bypass the namespace
// role check; everyone else needs a binding of at least min — a missing
// binding is a denial, never a fallback.
func Check(store Store, hub, namespace string, id Identity, min Role) error {
	if !id.Authenticated() {
		return ErrUnauthenticated
	}
	if id.IsAdmin() {
		return nil
	}
	role, ok := store.GetRole(hub, namespace, id.Subject)
	if !ok || !role.AtLeast(min) {
		return fmt.Errorf("%w: %s on namespace %q requires role %s", ErrDenied, id.Subject, namespace, min)
	}
	return nil
}
