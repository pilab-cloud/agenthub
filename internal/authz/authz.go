// Package authz implements namespace RBAC for AgentHub (M3.2).
//
// Roles are hierarchical — owner > maintainer > publisher > reader — and are
// bound to a (hub, namespace, subject) triple. Hub-wide admins (callers
// carrying the admin scope) bypass namespace role checks. The default store
// is in-memory; PostgresRoleStore persists bindings across restarts (M3.6)
// and the Shadow SSO token validation landed in M3.3.
package authz

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// Role is a namespace RBAC role.
type Role string

// The namespace roles, ordered by privilege (lowest first). See the
// authorization matrix in docs/architecture/api.md §5.5.
const (
	RoleReader     Role = "reader"
	RolePublisher  Role = "publisher"
	RoleMaintainer Role = "maintainer"
	RoleOwner      Role = "owner"
)

// roleRanks maps each known role to its privilege rank; unknown roles rank 0.
var roleRanks = map[Role]int{
	RoleReader:     1,
	RolePublisher:  2,
	RoleMaintainer: 3,
	RoleOwner:      4,
}

// ScopeAdmin is the hub-wide admin scope (docs/architecture/api.md §5.5).
// Holders bypass namespace role checks.
const ScopeAdmin = "agenthub.admin"

// adminScopeShorthand is the bare spelling of ScopeAdmin accepted from the
// placeholder auth headers until Shadow SSO (M3.3) pins canonical scopes.
const adminScopeShorthand = "admin"

// Sentinel errors returned by the store and the Check helper.
var (
	ErrInvalidRole      = errors.New("authz: invalid role")
	ErrInvalidSubject   = errors.New("authz: invalid subject")
	ErrInvalidNamespace = errors.New("authz: invalid namespace")
	ErrBindingNotFound  = errors.New("authz: role binding not found")
	ErrUnauthenticated  = errors.New("authz: authentication required")
	ErrDenied           = errors.New("authz: permission denied")
)

// ParseRole normalizes s (trimmed, lower-case) into a Role.
func ParseRole(s string) (Role, error) {
	r := Role(strings.ToLower(strings.TrimSpace(s)))
	if _, ok := roleRanks[r]; !ok {
		return "", fmt.Errorf("%w: %q", ErrInvalidRole, s)
	}
	return r, nil
}

// Valid reports whether r is a known role.
func (r Role) Valid() bool {
	_, ok := roleRanks[r]
	return ok
}

// Rank returns the privilege rank of r; unknown roles rank 0.
func (r Role) Rank() int { return roleRanks[r] }

// AtLeast reports whether r is at least as privileged as min. Both roles must
// be known: an unknown role or requirement never satisfies the check.
func (r Role) AtLeast(min Role) bool {
	if !r.Valid() || !min.Valid() {
		return false
	}
	return r.Rank() >= min.Rank()
}

// Identity is the authenticated caller of a request.
type Identity struct {
	// Subject is the SSO subject id; empty for anonymous callers.
	Subject string
	// Scopes are the coarse OAuth scopes attached to the caller.
	Scopes []string
}

// Authenticated reports whether the caller is known: either a subject was
// presented or the caller holds the admin scope. Anonymous callers fail every
// check.
func (id Identity) Authenticated() bool {
	return id.Subject != "" || IsAdmin(id.Scopes)
}

// IsAdmin reports whether the caller holds the admin scope.
func (id Identity) IsAdmin() bool { return IsAdmin(id.Scopes) }

// IsAdmin reports whether scopes contain the admin scope (or its bare
// "admin" shorthand).
func IsAdmin(scopes []string) bool {
	for _, s := range scopes {
		if s == ScopeAdmin || s == adminScopeShorthand {
			return true
		}
	}
	return false
}

// ParseScopes splits a header value into scopes. Commas and whitespace are
// accepted separators and empty entries are dropped. The result is never nil,
// so callers can compare against an empty slice.
func ParseScopes(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || unicode.IsSpace(r)
	})
	out := make([]string, 0, len(fields))
	return append(out, fields...)
}

type identityCtxKey struct{}

// WithIdentity returns a context carrying id for handlers downstream of the
// auth interceptor.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityCtxKey{}, id)
}

// IdentityFromContext returns the identity stored by the auth interceptor.
// The zero Identity (anonymous, no scopes) is returned when ctx carries none.
func IdentityFromContext(ctx context.Context) Identity {
	if ctx == nil {
		return Identity{}
	}
	id, _ := ctx.Value(identityCtxKey{}).(Identity)
	return id
}
