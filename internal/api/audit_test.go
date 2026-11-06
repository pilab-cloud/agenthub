package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"

	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"
	registryv1 "go.pilab.hu/agenthub/gen/go/agenthub/registry/v1"
	"go.pilab.hu/agenthub/internal/audit"
	"go.pilab.hu/agenthub/internal/authz"
	"go.pilab.hu/agenthub/internal/catalog"
	"go.pilab.hu/agenthub/internal/registry"
)

// newAuditRegistry builds a Registry with exposed role and audit stores bound
// to hub "hub1" so tests can seed bindings and read entries directly.
func newAuditRegistry() (*Registry, *audit.MemStore, *authz.MemStore) {
	roles := authz.NewMemStore()
	audits := audit.NewMemStore()
	h := NewRegistryWithAudit(registry.NewMemStore(), catalog.NewMemStore(), roles, "hub1", audits)
	return h, audits, roles
}

// failingAuditStore simulates an audit backend outage: every write and read
// fails.
type failingAuditStore struct{}

func (failingAuditStore) Log(_, _, _, _, _, _ string) error { return errors.New("audit down") }

func (failingAuditStore) List(audit.Query) ([]audit.Entry, error) {
	return nil, errors.New("audit down")
}

// oldestFirst reverses the store's newest-first ordering.
func oldestFirst(entries []audit.Entry) []audit.Entry {
	out := make([]audit.Entry, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		out = append(out, entries[i])
	}
	return out
}

func TestRegistryLogsPrivilegedActions(t *testing.T) {
	h, audits, _ := newAuditRegistry()
	ctx := identCtx("alice")

	if _, err := h.CreateNamespace(ctx, connect.NewRequest(&registryv1.CreateNamespaceRequest{Slug: "ns"})); err != nil {
		t.Fatal(err)
	}

	begin, err := h.BeginPublish(ctx, connect.NewRequest(&registryv1.BeginPublishRequest{
		Namespace: "ns",
		Kind:      commonv1.AssetKind_ASSET_KIND_AGENT,
		Name:      "foo",
		Version:   "1.0.0",
	}))
	if err != nil {
		t.Fatal(err)
	}
	manifest := []byte("manifest-bytes")
	sum := sha256.Sum256(manifest)
	if _, err := h.CommitPublish(ctx, connect.NewRequest(&registryv1.CommitPublishRequest{
		PublishId:      begin.Msg.PublishId,
		Manifest:       manifest,
		ManifestDigest: &commonv1.Digest{Algorithm: "sha256", Value: hex.EncodeToString(sum[:])},
	})); err != nil {
		t.Fatal(err)
	}

	cr, err := h.CreateDraft(ctx, connect.NewRequest(&registryv1.CreateDraftRequest{
		Namespace: "ns",
		Kind:      commonv1.AssetKind_ASSET_KIND_AGENT,
		Name:      "drafty",
		Manifest:  []byte("m"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.PublishDraft(ctx, connect.NewRequest(&registryv1.PublishDraftRequest{
		DraftId: cr.Msg.DraftId,
		Version: "2.0.0",
	})); err != nil {
		t.Fatal(err)
	}

	if _, err := h.RegisterSigningKey(ctx, connect.NewRequest(&registryv1.RegisterSigningKeyRequest{
		Namespace: "ns",
		Kid:       "k1",
		PublicKey: "pk",
	})); err != nil {
		t.Fatal(err)
	}
	if _, err := h.RevokeSigningKey(ctx, connect.NewRequest(&registryv1.RevokeSigningKeyRequest{
		Namespace: "ns",
		Kid:       "k1",
	})); err != nil {
		t.Fatal(err)
	}

	entries, err := audits.List(audit.Query{Namespace: "ns"})
	if err != nil {
		t.Fatal(err)
	}
	got := oldestFirst(entries)
	want := []string{
		auditActionCreateNamespace,
		auditActionBeginPublish,
		auditActionCommitPublish,
		auditActionCreateDraft,
		auditActionPublishDraft,
		auditActionRegisterSigningKey,
		auditActionRevokeSigningKey,
	}
	if len(got) != len(want) {
		t.Fatalf("entries: %d, want %d (%+v)", len(got), len(want), got)
	}
	for i, action := range want {
		if got[i].Action != action {
			t.Errorf("entry %d action: %q, want %q", i, got[i].Action, action)
		}
		if got[i].Actor != "alice" {
			t.Errorf("entry %d actor: %q, want alice (%s)", i, got[i].Actor, got[i].Action)
		}
		if got[i].Namespace != "ns" {
			t.Errorf("entry %d namespace: %q, want ns (%s)", i, got[i].Namespace, got[i].Action)
		}
		if got[i].At.IsZero() {
			t.Errorf("entry %d has no timestamp (%s)", i, got[i].Action)
		}
	}

	// asset/version land on the publish and draft entries
	if got[1].Asset != "foo" || got[1].Version != "1.0.0" {
		t.Errorf("BeginPublish target: %s@%s, want foo@1.0.0", got[1].Asset, got[1].Version)
	}
	if got[2].Asset != "foo" || got[2].Version != "1.0.0" {
		t.Errorf("CommitPublish target: %s@%s, want foo@1.0.0", got[2].Asset, got[2].Version)
	}
	if got[4].Asset != "drafty" || got[4].Version != "2.0.0" {
		t.Errorf("PublishDraft target: %s@%s, want drafty@2.0.0", got[4].Asset, got[4].Version)
	}
	// details carry the identifiers needed to correlate the entry
	if !strings.Contains(got[1].Detail, begin.Msg.PublishId) {
		t.Errorf("BeginPublish detail: %q, want publish id %q", got[1].Detail, begin.Msg.PublishId)
	}
	if !strings.Contains(got[5].Detail, `"kid":"k1"`) {
		t.Errorf("RegisterSigningKey detail: %q, want kid k1", got[5].Detail)
	}
	if !strings.Contains(got[6].Detail, `"kid":"k1"`) {
		t.Errorf("RevokeSigningKey detail: %q, want kid k1", got[6].Detail)
	}
}

func TestRegistryDoesNotLogFailedActions(t *testing.T) {
	h, audits, _ := newAuditRegistry()
	ctx := identCtx("alice")

	if _, err := h.CreateNamespace(ctx, connect.NewRequest(&registryv1.CreateNamespaceRequest{Slug: "ns"})); err != nil {
		t.Fatal(err)
	}
	// unknown namespace: BeginPublish fails and must not be audited
	if _, err := h.BeginPublish(ctx, connect.NewRequest(&registryv1.BeginPublishRequest{
		Namespace: "nope",
		Kind:      commonv1.AssetKind_ASSET_KIND_AGENT,
		Name:      "foo",
		Version:   "1.0.0",
	})); err == nil {
		t.Fatal("expected error")
	}
	// valid begin, then a digest mismatch: CommitPublish fails unaudited
	begin, err := h.BeginPublish(ctx, connect.NewRequest(&registryv1.BeginPublishRequest{
		Namespace: "ns",
		Kind:      commonv1.AssetKind_ASSET_KIND_AGENT,
		Name:      "foo",
		Version:   "1.0.0",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.CommitPublish(ctx, connect.NewRequest(&registryv1.CommitPublishRequest{
		PublishId:      begin.Msg.PublishId,
		Manifest:       []byte("manifest"),
		ManifestDigest: &commonv1.Digest{Algorithm: "sha256", Value: "deadbeef"},
	})); err == nil {
		t.Fatal("expected error")
	}

	entries, err := audits.List(audit.Query{})
	if err != nil {
		t.Fatal(err)
	}
	got := oldestFirst(entries)
	want := []string{auditActionCreateNamespace, auditActionBeginPublish}
	if len(got) != len(want) {
		t.Fatalf("entries: %+v, want only %v", got, want)
	}
	for i, action := range want {
		if got[i].Action != action {
			t.Fatalf("entry %d: %q, want %q", i, got[i].Action, action)
		}
	}
}

func TestListAuditAuthorization(t *testing.T) {
	h, _, roles := newAuditRegistry()
	if _, err := h.CreateNamespace(identCtx("alice"), connect.NewRequest(&registryv1.CreateNamespaceRequest{Slug: "ns"})); err != nil {
		t.Fatal(err)
	}
	for _, b := range []struct {
		subject string
		role    authz.Role
	}{
		{"reader", authz.RoleReader},
		{"publisher", authz.RolePublisher},
		{"maintainer", authz.RoleMaintainer},
		{"owner", authz.RoleOwner},
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
		{"unbound", identCtx("bob"), connect.CodePermissionDenied},
		{"reader", identCtx("reader"), connect.CodePermissionDenied},
		{"publisher", identCtx("publisher"), connect.CodePermissionDenied},
	}
	for _, tc := range denied {
		t.Run("denied/"+tc.name, func(t *testing.T) {
			_, err := h.ListAudit(tc.ctx, connect.NewRequest(&registryv1.ListAuditRequest{Namespace: "ns"}))
			if code := connect.CodeOf(err); code != tc.want {
				t.Fatalf("code: %v, want %v (err: %v)", code, tc.want, err)
			}
		})
	}

	allowed := []struct {
		name string
		ctx  context.Context
	}{
		{"maintainer", identCtx("maintainer")},
		{"owner", identCtx("owner")},
		{"creator", identCtx("alice")},
		{"hub admin", identCtx("", authz.ScopeAdmin)},
	}
	for _, tc := range allowed {
		t.Run("allowed/"+tc.name, func(t *testing.T) {
			resp, err := h.ListAudit(tc.ctx, connect.NewRequest(&registryv1.ListAuditRequest{Namespace: "ns"}))
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if resp.Msg == nil {
				t.Fatal("missing response")
			}
		})
	}
}

func TestListAuditValidation(t *testing.T) {
	h, _, _ := newAuditRegistry()
	ctx := identCtx("", authz.ScopeAdmin)

	// namespace is mandatory
	_, err := h.ListAudit(ctx, connect.NewRequest(&registryv1.ListAuditRequest{}))
	if code := connect.CodeOf(err); code != connect.CodeInvalidArgument {
		t.Fatalf("empty namespace code: %v, want %v", code, connect.CodeInvalidArgument)
	}

	// unknown namespace stays NotFound even for a hub admin
	_, err = h.ListAudit(ctx, connect.NewRequest(&registryv1.ListAuditRequest{Namespace: "nope"}))
	if code := connect.CodeOf(err); code != connect.CodeNotFound {
		t.Fatalf("unknown namespace code: %v, want %v", code, connect.CodeNotFound)
	}

	if _, err := h.CreateNamespace(identCtx("alice"), connect.NewRequest(&registryv1.CreateNamespaceRequest{Slug: "ns"})); err != nil {
		t.Fatal(err)
	}
	for _, cursor := range []string{"abc", "-1", "1.5"} {
		_, err := h.ListAudit(ctx, connect.NewRequest(&registryv1.ListAuditRequest{
			Namespace: "ns",
			Page:      &commonv1.PageRequest{Cursor: cursor},
		}))
		if code := connect.CodeOf(err); code != connect.CodeInvalidArgument {
			t.Fatalf("cursor %q code: %v, want %v", cursor, code, connect.CodeInvalidArgument)
		}
	}
}

func TestListAuditFilterAndPaging(t *testing.T) {
	h, audits, roles := newAuditRegistry()
	// seed the namespace directly: creating it through the RPC would add its
	// own audit entry and shift every page boundary
	if _, err := h.regStore.Create(&commonv1.Namespace{Slug: "ns"}); err != nil {
		t.Fatal(err)
	}
	if err := roles.Grant("hub1", "ns", "mia", authz.RoleMaintainer); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := audits.Log("NsAction", "alice", "ns", "", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	// an entry of another namespace must never leak into ns's log
	if err := audits.Log("OtherAction", "alice", "other", "", "", ""); err != nil {
		t.Fatal(err)
	}

	ctx := identCtx("mia")
	page1, err := h.ListAudit(ctx, connect.NewRequest(&registryv1.ListAuditRequest{
		Namespace: "ns",
		Page:      &commonv1.PageRequest{Limit: 2},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(page1.Msg.Events) != 2 {
		t.Fatalf("page1 events: %d, want 2", len(page1.Msg.Events))
	}
	if page1.Msg.Page.NextCursor != "2" {
		t.Fatalf("page1 next cursor: %q, want 2", page1.Msg.Page.NextCursor)
	}

	page2, err := h.ListAudit(ctx, connect.NewRequest(&registryv1.ListAuditRequest{
		Namespace: "ns",
		Page:      &commonv1.PageRequest{Limit: 2, Cursor: page1.Msg.Page.NextCursor},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(page2.Msg.Events) != 2 {
		t.Fatalf("page2 events: %d, want 2", len(page2.Msg.Events))
	}
	if page2.Msg.Page.NextCursor != "4" {
		t.Fatalf("page2 next cursor: %q, want 4", page2.Msg.Page.NextCursor)
	}

	page3, err := h.ListAudit(ctx, connect.NewRequest(&registryv1.ListAuditRequest{
		Namespace: "ns",
		Page:      &commonv1.PageRequest{Limit: 2, Cursor: page2.Msg.Page.NextCursor},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(page3.Msg.Events) != 1 {
		t.Fatalf("page3 events: %d, want 1", len(page3.Msg.Events))
	}
	if page3.Msg.Page.NextCursor != "" {
		t.Fatalf("page3 next cursor: %q, want empty", page3.Msg.Page.NextCursor)
	}

	// every event carries the actor and the namespace filter applied
	for _, page := range []*registryv1.ListAuditResponse{page1.Msg, page2.Msg, page3.Msg} {
		for _, ev := range page.Events {
			if ev.Action != "NsAction" {
				t.Fatalf("leaked event from another namespace: %+v", ev)
			}
			if ev.ActorSub != "alice" {
				t.Fatalf("actor: %q, want alice", ev.ActorSub)
			}
			if ev.At == nil {
				t.Fatal("missing timestamp")
			}
		}
	}
}

func TestListAuditLimitClamped(t *testing.T) {
	h, audits, roles := newAuditRegistry()
	if _, err := h.regStore.Create(&commonv1.Namespace{Slug: "ns"}); err != nil {
		t.Fatal(err)
	}
	if err := roles.Grant("hub1", "ns", "mia", authz.RoleMaintainer); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < auditPageLimit+1; i++ {
		if err := audits.Log("NsAction", "alice", "ns", "", "", ""); err != nil {
			t.Fatal(err)
		}
	}

	resp, err := h.ListAudit(identCtx("mia"), connect.NewRequest(&registryv1.ListAuditRequest{
		Namespace: "ns",
		Page:      &commonv1.PageRequest{Limit: auditPageLimit + 1},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Msg.Events) != auditPageLimit {
		t.Fatalf("events: %d, want the %d cap", len(resp.Msg.Events), auditPageLimit)
	}
	if resp.Msg.Page.NextCursor != "100" {
		t.Fatalf("next cursor: %q, want 100", resp.Msg.Page.NextCursor)
	}
}

func TestAuditWriteFailureDoesNotFailRequest(t *testing.T) {
	h := NewRegistryWithAudit(registry.NewMemStore(), catalog.NewMemStore(), authz.NewMemStore(), "hub1", failingAuditStore{})
	ctx := identCtx("alice")

	if _, err := h.CreateNamespace(ctx, connect.NewRequest(&registryv1.CreateNamespaceRequest{Slug: "ns"})); err != nil {
		t.Fatalf("create with broken audit store: %v", err)
	}
	if _, err := h.RegisterSigningKey(ctx, connect.NewRequest(&registryv1.RegisterSigningKeyRequest{
		Namespace: "ns",
		Kid:       "k1",
		PublicKey: "pk",
	})); err != nil {
		t.Fatalf("register with broken audit store: %v", err)
	}

	// a failing read is surfaced, not silently swallowed
	_, err := h.ListAudit(identCtx("", authz.ScopeAdmin), connect.NewRequest(&registryv1.ListAuditRequest{Namespace: "ns"}))
	if code := connect.CodeOf(err); code != connect.CodeInternal {
		t.Fatalf("list code: %v, want %v (err: %v)", code, connect.CodeInternal, err)
	}
}
