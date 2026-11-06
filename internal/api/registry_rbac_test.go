package api

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"
	registryv1 "go.pilab.hu/agenthub/gen/go/agenthub/registry/v1"
	"go.pilab.hu/agenthub/internal/authz"
)

// newM36Registry builds a Registry on hub "hub1" with an existing namespace
// "ns" owned by alice, so every test starts from a known role layout.
func newM36Registry(t *testing.T) (*Registry, *authz.MemStore) {
	t.Helper()
	h, roles := newAuthzRegistry()
	if _, err := h.CreateNamespace(identCtx("alice"), connect.NewRequest(&registryv1.CreateNamespaceRequest{
		Slug: "ns",
	})); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	return h, roles
}

// createDraft returns the draft id of a draft created as author.
func createDraft(t *testing.T, h *Registry, ctx context.Context, name string) string {
	t.Helper()
	resp, err := h.CreateDraft(ctx, connect.NewRequest(&registryv1.CreateDraftRequest{
		Namespace: "ns",
		Kind:      commonv1.AssetKind_ASSET_KIND_AGENT,
		Name:      name,
		Manifest:  []byte("m"),
	}))
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	return resp.Msg.DraftId
}

func TestCreateDraftRequiresPublisher(t *testing.T) {
	h, roles := newM36Registry(t)

	denied := []struct {
		name string
		ctx  context.Context
		want connect.Code
	}{
		{"anonymous", context.Background(), connect.CodeUnauthenticated},
		{"unbound", identCtx("bob"), connect.CodePermissionDenied},
		{"reader", identCtx("reader"), connect.CodePermissionDenied},
	}
	if err := roles.Grant("hub1", "ns", "reader", authz.RoleReader); err != nil {
		t.Fatal(err)
	}
	for _, tc := range denied {
		t.Run("denied/"+tc.name, func(t *testing.T) {
			_, err := h.CreateDraft(tc.ctx, connect.NewRequest(&registryv1.CreateDraftRequest{
				Namespace: "ns",
				Kind:      commonv1.AssetKind_ASSET_KIND_AGENT,
				Name:      "x",
			}))
			if code := connect.CodeOf(err); code != tc.want {
				t.Fatalf("code: %v, want %v (err: %v)", code, tc.want, err)
			}
		})
	}

	allowed := []struct {
		name string
		ctx  context.Context
	}{
		{"publisher", identCtx("pub")},
		{"maintainer", identCtx("mnt")},
		{"owner", identCtx("alice")},
		{"hub admin", identCtx("", authz.ScopeAdmin)},
	}
	if err := roles.Grant("hub1", "ns", "pub", authz.RolePublisher); err != nil {
		t.Fatal(err)
	}
	if err := roles.Grant("hub1", "ns", "mnt", authz.RoleMaintainer); err != nil {
		t.Fatal(err)
	}
	for _, tc := range allowed {
		t.Run("allowed/"+tc.name, func(t *testing.T) {
			id := createDraft(t, h, tc.ctx, "draft-"+tc.name)
			// the author is recorded so later draft mutations can use the
			// author-or-maintainer rule (empty for a subject-less admin)
			d, ok := h.draftStore.Get(id)
			if !ok {
				t.Fatal("draft not found")
			}
			wantAuthor := authz.IdentityFromContext(tc.ctx).Subject
			if d.AuthorSub != wantAuthor {
				t.Fatalf("author: %q, want %q", d.AuthorSub, wantAuthor)
			}
		})
	}
}

func TestCreateDraftUnknownNamespace(t *testing.T) {
	h, _ := newM36Registry(t)
	// existence is checked before roles: unknown stays NotFound even for an
	// anonymous caller
	_, err := h.CreateDraft(context.Background(), connect.NewRequest(&registryv1.CreateDraftRequest{
		Namespace: "nope",
		Kind:      commonv1.AssetKind_ASSET_KIND_AGENT,
		Name:      "x",
	}))
	if code := connect.CodeOf(err); code != connect.CodeNotFound {
		t.Fatalf("code: %v, want %v (err: %v)", code, connect.CodeNotFound, err)
	}
}

func TestDraftMutationAuthorOrMaintainer(t *testing.T) {
	h, roles := newM36Registry(t)

	// bob creates the draft, so he is its author even though he only holds
	// the publisher role
	bobCtx := identCtx("bob")
	if err := roles.Grant("hub1", "ns", "bob", authz.RolePublisher); err != nil {
		t.Fatal(err)
	}
	createDraft(t, h, bobCtx, "author-draft")

	// carol is a publisher but not the author; dave is a maintainer
	if err := roles.Grant("hub1", "ns", "carol", authz.RolePublisher); err != nil {
		t.Fatal(err)
	}
	if err := roles.Grant("hub1", "ns", "dave", authz.RoleMaintainer); err != nil {
		t.Fatal(err)
	}

	mutations := []struct {
		name string
		call func(ctx context.Context, draftID string) error
	}{
		{"UpdateDraft", func(ctx context.Context, draftID string) error {
			_, err := h.UpdateDraft(ctx, connect.NewRequest(&registryv1.UpdateDraftRequest{
				DraftId:  draftID,
				Manifest: []byte("m2"),
			}))
			return err
		}},
		{"DiscardDraft", func(ctx context.Context, draftID string) error {
			_, err := h.DiscardDraft(ctx, connect.NewRequest(&registryv1.DiscardDraftRequest{DraftId: draftID}))
			return err
		}},
		{"DeleteDraft", func(ctx context.Context, draftID string) error {
			_, err := h.DeleteDraft(ctx, connect.NewRequest(&registryv1.DeleteDraftRequest{DraftId: draftID}))
			return err
		}},
	}

	// Each case runs against a fresh draft authored by bob (publisher), so a
	// successful mutation does not disturb the next assertion.
	cases := []struct {
		name    string
		ctx     context.Context
		wantErr bool
		want    connect.Code
	}{
		{"anonymous", context.Background(), true, connect.CodeUnauthenticated},
		{"unbound", identCtx("stranger"), true, connect.CodePermissionDenied},
		// a publisher who is not the author is denied
		{"non-author publisher", identCtx("carol"), true, connect.CodePermissionDenied},
		// the author passes regardless of namespace role
		{"author", bobCtx, false, connect.Code(0)},
		// a maintainer passes even when not the author
		{"maintainer", identCtx("dave"), false, connect.Code(0)},
		{"owner", identCtx("alice"), false, connect.Code(0)},
		{"hub admin", identCtx("", authz.ScopeAdmin), false, connect.Code(0)},
	}
	for _, m := range mutations {
		for _, tc := range cases {
			t.Run(m.name+"/"+tc.name, func(t *testing.T) {
				id := createDraft(t, h, bobCtx, "seed-"+m.name+"-"+tc.name)
				err := m.call(tc.ctx, id)
				if !tc.wantErr {
					if err != nil {
						t.Fatalf("err: %v, want success", err)
					}
					return
				}
				if code := connect.CodeOf(err); code != tc.want {
					t.Fatalf("code: %v, want %v (err: %v)", code, tc.want, err)
				}
			})
		}
	}

	// an unknown draft is NotFound before any role check
	_, err := h.UpdateDraft(context.Background(), connect.NewRequest(&registryv1.UpdateDraftRequest{DraftId: "missing"}))
	if code := connect.CodeOf(err); code != connect.CodeNotFound {
		t.Fatalf("unknown draft code: %v, want %v", code, connect.CodeNotFound)
	}
}

func TestPublishDraftRequiresPublisher(t *testing.T) {
	h, roles := newM36Registry(t)
	if err := roles.Grant("hub1", "ns", "bob", authz.RolePublisher); err != nil {
		t.Fatal(err)
	}
	if err := roles.Grant("hub1", "ns", "reader", authz.RoleReader); err != nil {
		t.Fatal(err)
	}

	// the author still needs the publisher role to publish: publishing is a
	// namespace capability, not an ownership one. Create the draft while bob
	// still holds publisher, then revoke.
	id := createDraft(t, h, identCtx("bob"), "unpublished")
	if err := roles.Revoke("hub1", "ns", "bob"); err != nil {
		t.Fatal(err)
	}
	_, err := h.PublishDraft(identCtx("bob"), connect.NewRequest(&registryv1.PublishDraftRequest{
		DraftId: id,
		Version: "1.0.0",
	}))
	if code := connect.CodeOf(err); code != connect.CodePermissionDenied {
		t.Fatalf("author without publisher code: %v, want %v (err: %v)", code, connect.CodePermissionDenied, err)
	}

	// re-grant publisher and run the matrix on fresh drafts
	if err := roles.Grant("hub1", "ns", "bob", authz.RolePublisher); err != nil {
		t.Fatal(err)
	}
	denied := []struct {
		name string
		ctx  context.Context
		want connect.Code
	}{
		{"anonymous", context.Background(), connect.CodeUnauthenticated},
		{"unbound", identCtx("stranger"), connect.CodePermissionDenied},
		{"reader", identCtx("reader"), connect.CodePermissionDenied},
	}
	for _, tc := range denied {
		t.Run("denied/"+tc.name, func(t *testing.T) {
			draftID := createDraft(t, h, identCtx("alice"), "pub-"+tc.name)
			_, err := h.PublishDraft(tc.ctx, connect.NewRequest(&registryv1.PublishDraftRequest{
				DraftId: draftID,
				Version: "1.0.0",
			}))
			if code := connect.CodeOf(err); code != tc.want {
				t.Fatalf("code: %v, want %v (err: %v)", code, tc.want, err)
			}
		})
	}

	allowed := []struct {
		name string
		ctx  context.Context
	}{
		{"publisher", identCtx("bob")},
		{"maintainer", identCtx("alice")},
		{"hub admin", identCtx("", authz.ScopeAdmin)},
	}
	for _, tc := range allowed {
		t.Run("allowed/"+tc.name, func(t *testing.T) {
			draftID := createDraft(t, h, identCtx("alice"), "ok-"+tc.name)
			if _, err := h.PublishDraft(tc.ctx, connect.NewRequest(&registryv1.PublishDraftRequest{
				DraftId: draftID,
				Version: "1.0." + tc.name,
			})); err != nil {
				t.Fatalf("publish: %v", err)
			}
		})
	}
}

func TestRevokeSigningKeyRequiresOwner(t *testing.T) {
	h, roles := newM36Registry(t)
	for _, b := range []struct {
		subject string
		role    authz.Role
	}{
		{"reader", authz.RoleReader},
		{"publisher", authz.RolePublisher},
		{"maintainer", authz.RoleMaintainer},
	} {
		if err := roles.Grant("hub1", "ns", b.subject, b.role); err != nil {
			t.Fatal(err)
		}
	}

	denied := []struct {
		name string
		ctx  context.Context
		want connect.Code
	}{
		{"anonymous", context.Background(), connect.CodeUnauthenticated},
		{"unbound", identCtx("stranger"), connect.CodePermissionDenied},
		{"reader", identCtx("reader"), connect.CodePermissionDenied},
		{"publisher", identCtx("publisher"), connect.CodePermissionDenied},
		// maintainer is below owner: revoking keys is an owner-only action
		{"maintainer", identCtx("maintainer"), connect.CodePermissionDenied},
	}
	for _, tc := range denied {
		t.Run("denied/"+tc.name, func(t *testing.T) {
			_, err := h.RevokeSigningKey(tc.ctx, connect.NewRequest(&registryv1.RevokeSigningKeyRequest{
				Namespace: "ns",
				Kid:       "k-" + tc.name,
			}))
			if code := connect.CodeOf(err); code != tc.want {
				t.Fatalf("code: %v, want %v (err: %v)", code, tc.want, err)
			}
		})
	}

	// a maintainer may register a key (publisher+) but not revoke it; only
	// the owner and hub admins revoke
	if err := registerKey(t, h, identCtx("maintainer"), "ns", "k-owner"); err != nil {
		t.Fatalf("maintainer register: %v", err)
	}
	allowed := []struct {
		name string
		ctx  context.Context
	}{
		{"owner", identCtx("alice")},
		{"hub admin", identCtx("", authz.ScopeAdmin)},
	}
	for _, tc := range allowed {
		t.Run("allowed/"+tc.name, func(t *testing.T) {
			if _, err := h.RevokeSigningKey(tc.ctx, connect.NewRequest(&registryv1.RevokeSigningKeyRequest{
				Namespace: "ns",
				Kid:       "k-owner",
			})); err != nil {
				t.Fatalf("revoke: %v", err)
			}
		})
	}
}

// TestRemainingRPCsEnforceRoles covers the RPCs whose business logic is still
// unimplemented: the role check runs first, so an unauthorized caller gets
// Unauthenticated/PermissionDenied instead of Unimplemented (deny by
// default), and an authorized caller still sees Unimplemented.
func TestRemainingRPCsEnforceRoles(t *testing.T) {
	h, roles := newM36Registry(t)
	for _, b := range []struct {
		subject string
		role    authz.Role
	}{
		{"reader", authz.RoleReader},
		{"publisher", authz.RolePublisher},
		{"maintainer", authz.RoleMaintainer},
	} {
		if err := roles.Grant("hub1", "ns", b.subject, b.role); err != nil {
			t.Fatal(err)
		}
	}

	ref := &commonv1.AssetRef{Namespace: "ns", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "foo", Version: "1.0.0"}
	rpcs := []struct {
		name string
		// min is the role an authorized caller must hold
		min  authz.Role
		call func(ctx context.Context) error
	}{
		{"SetLandingVisibility", authz.RoleMaintainer, func(ctx context.Context) error {
			_, err := h.SetLandingVisibility(ctx, connect.NewRequest(&registryv1.SetLandingVisibilityRequest{
				AssetId: "ns/agent/foo",
			}))
			return err
		}},
		{"ValidateBundle", authz.RolePublisher, func(ctx context.Context) error {
			_, err := h.ValidateBundle(ctx, connect.NewRequest(&registryv1.ValidateBundleRequest{Namespace: "ns"}))
			return err
		}},
		{"YankVersion", authz.RoleMaintainer, func(ctx context.Context) error {
			_, err := h.YankVersion(ctx, connect.NewRequest(&registryv1.YankVersionRequest{Ref: ref}))
			return err
		}},
		{"UnyankVersion", authz.RoleMaintainer, func(ctx context.Context) error {
			_, err := h.UnyankVersion(ctx, connect.NewRequest(&registryv1.UnyankVersionRequest{Ref: ref}))
			return err
		}},
		{"DeprecateVersion", authz.RoleMaintainer, func(ctx context.Context) error {
			_, err := h.DeprecateVersion(ctx, connect.NewRequest(&registryv1.DeprecateVersionRequest{Ref: ref}))
			return err
		}},
	}

	identities := []struct {
		name string
		ctx  context.Context
		role authz.Role
		anon bool
	}{
		{"anonymous", context.Background(), "", true},
		{"unbound", identCtx("stranger"), "", false},
		{"reader", identCtx("reader"), authz.RoleReader, false},
		{"publisher", identCtx("publisher"), authz.RolePublisher, false},
		{"maintainer", identCtx("maintainer"), authz.RoleMaintainer, false},
		{"owner", identCtx("alice"), authz.RoleOwner, false},
		{"hub admin", identCtx("", authz.ScopeAdmin), authz.RoleOwner, false},
	}

	for _, rpc := range rpcs {
		for _, id := range identities {
			t.Run(rpc.name+"/"+id.name, func(t *testing.T) {
				err := rpc.call(id.ctx)
				switch {
				case id.anon:
					if code := connect.CodeOf(err); code != connect.CodeUnauthenticated {
						t.Fatalf("anonymous code: %v, want %v (err: %v)", code, connect.CodeUnauthenticated, err)
					}
				case id.role == "":
					if code := connect.CodeOf(err); code != connect.CodePermissionDenied {
						t.Fatalf("unbound code: %v, want %v (err: %v)", code, connect.CodePermissionDenied, err)
					}
				case id.role.AtLeast(rpc.min):
					if code := connect.CodeOf(err); code != connect.CodeUnimplemented {
						t.Fatalf("authorized code: %v, want %v (err: %v)", code, connect.CodeUnimplemented, err)
					}
				default:
					if code := connect.CodeOf(err); code != connect.CodePermissionDenied {
						t.Fatalf("below-min code: %v, want %v (err: %v)", code, connect.CodePermissionDenied, err)
					}
				}
			})
		}
	}
}

func TestRemainingRPCsValidationBeforeRoles(t *testing.T) {
	h, _ := newM36Registry(t)
	// an authorized caller sees the argument/existence errors, not
	// Unimplemented
	owner := identCtx("alice")

	wantCode := func(err error, want connect.Code, msg string) {
		t.Helper()
		if code := connect.CodeOf(err); code != want {
			t.Fatalf("%s code: %v, want %v (err: %v)", msg, code, want, err)
		}
	}

	_, err := h.ValidateBundle(owner, connect.NewRequest(&registryv1.ValidateBundleRequest{}))
	wantCode(err, connect.CodeInvalidArgument, "empty namespace")
	_, err = h.ValidateBundle(owner, connect.NewRequest(&registryv1.ValidateBundleRequest{Namespace: "nope"}))
	wantCode(err, connect.CodeNotFound, "unknown namespace")

	_, err = h.YankVersion(owner, connect.NewRequest(&registryv1.YankVersionRequest{}))
	wantCode(err, connect.CodeInvalidArgument, "nil ref")
	_, err = h.YankVersion(owner, connect.NewRequest(&registryv1.YankVersionRequest{
		Ref: &commonv1.AssetRef{Namespace: "nope", Name: "foo"},
	}))
	wantCode(err, connect.CodeNotFound, "yank unknown namespace")

	_, err = h.SetLandingVisibility(owner, connect.NewRequest(&registryv1.SetLandingVisibilityRequest{
		AssetId: "not-an-asset-id",
	}))
	wantCode(err, connect.CodeInvalidArgument, "bad asset id")
	_, err = h.SetLandingVisibility(owner, connect.NewRequest(&registryv1.SetLandingVisibilityRequest{
		AssetId: "nope/agent/foo",
	}))
	wantCode(err, connect.CodeNotFound, "landing unknown namespace")

	// an unknown namespace stays NotFound even for an anonymous caller: the
	// existence check runs before the role check
	_, err = h.YankVersion(context.Background(), connect.NewRequest(&registryv1.YankVersionRequest{
		Ref: &commonv1.AssetRef{Namespace: "nope", Name: "foo"},
	}))
	wantCode(err, connect.CodeNotFound, "anonymous unknown namespace")
}
