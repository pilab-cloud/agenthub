// Package api provides ConnectRPC handler stubs. Real logic lands in M1+.
package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	catalogv1 "go.pilab.hu/agenthub/gen/go/agenthub/catalog/v1"
	catalogv1connect "go.pilab.hu/agenthub/gen/go/agenthub/catalog/v1/catalogv1connect"
	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"
	federationv1connect "go.pilab.hu/agenthub/gen/go/agenthub/federation/v1/federationv1connect"
	identityv1 "go.pilab.hu/agenthub/gen/go/agenthub/identity/v1"
	identityv1connect "go.pilab.hu/agenthub/gen/go/agenthub/identity/v1/identityv1connect"
	registryv1 "go.pilab.hu/agenthub/gen/go/agenthub/registry/v1"
	registryv1connect "go.pilab.hu/agenthub/gen/go/agenthub/registry/v1/registryv1connect"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"go.pilab.hu/agenthub/internal/audit"
	"go.pilab.hu/agenthub/internal/authz"
	"go.pilab.hu/agenthub/internal/catalog"
	"go.pilab.hu/agenthub/internal/registry"
)

// unimplemented returns a CodeUnimplemented error for an RPC.
func unimplemented(rpc string) error {
	return connect.NewError(connect.CodeUnimplemented, errors.New(rpc+" is not implemented yet (M1+)"))
}

// Catalog is the CatalogService handler.
type Catalog struct {
	catalogv1connect.UnimplementedCatalogServiceHandler
	store     catalog.Store
	blobs     catalog.BlobStore
	publicURL string
}

// NewCatalog builds a Catalog backed by an in-memory store.
func NewCatalog() *Catalog { return NewCatalogWithStore(catalog.NewMemStore()) }

// NewCatalogWithStore builds a Catalog backed by the given store.
func NewCatalogWithStore(s catalog.Store) *Catalog { return &Catalog{store: s} }

func (c *Catalog) SearchAssets(ctx context.Context, req *connect.Request[catalogv1.SearchAssetsRequest]) (*connect.Response[catalogv1.SearchAssetsResponse], error) {
	m := req.Msg
	q := strings.ToLower(strings.TrimSpace(m.Query))

	kinds := m.Kinds
	namespaces := m.Namespaces
	tags := m.Tags

	assets := c.store.ListAssets()
	filtered := make([]*catalog.Asset, 0, len(assets))
	for _, a := range assets {
		if q != "" {
			hay := strings.ToLower(a.DisplayName + " " + a.Summary)
			if !strings.Contains(hay, q) {
				continue
			}
		}
		if len(kinds) > 0 && !containsKind(kinds, a.Kind) {
			continue
		}
		if len(namespaces) > 0 && !containsString(namespaces, a.Namespace) {
			continue
		}
		if len(tags) > 0 && !containsAllTags(a.Tags, tags) {
			continue
		}
		filtered = append(filtered, a)
	}

	limit := 100
	if m.Page != nil && m.Page.GetLimit() > 0 {
		l := int(m.Page.GetLimit())
		if l > 100 {
			l = 100
		}
		limit = l
	}
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}

	out := make([]*catalogv1.AssetSummary, 0, len(filtered))
	for _, a := range filtered {
		out = append(out, assetSummary(a))
	}

	return connect.NewResponse(&catalogv1.SearchAssetsResponse{
		Assets: out,
		Page:   &commonv1.PageResponse{},
		Facets: map[string]uint32{},
	}), nil
}

func (c *Catalog) GetAsset(ctx context.Context, req *connect.Request[catalogv1.GetAssetRequest]) (*connect.Response[catalogv1.GetAssetResponse], error) {
	ref := req.Msg.Ref
	if ref == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("missing ref"))
	}
	a, ok := c.store.GetAsset(ref.Namespace, ref.Kind, ref.Name)
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, catalog.ErrNotFound)
	}
	resp := &catalogv1.GetAssetResponse{
		Asset:      assetSummary(a),
		Categories: append([]string(nil), a.Categories...),
		License:    a.License,
		Repository: a.Repository,
	}
	if a.CreatedAt != nil {
		resp.CreatedAt = proto.Clone(a.CreatedAt).(*timestamppb.Timestamp)
	}
	if a.UpdatedAt != nil {
		resp.UpdatedAt = proto.Clone(a.UpdatedAt).(*timestamppb.Timestamp)
	}
	return connect.NewResponse(resp), nil
}

func (c *Catalog) ListVersions(ctx context.Context, req *connect.Request[catalogv1.ListVersionsRequest]) (*connect.Response[catalogv1.ListVersionsResponse], error) {
	ref := req.Msg.Ref
	if ref == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("missing ref"))
	}
	if _, ok := c.store.GetAsset(ref.Namespace, ref.Kind, ref.Name); !ok {
		return nil, connect.NewError(connect.CodeNotFound, catalog.ErrNotFound)
	}
	versions := c.store.ListVersions(ref.Namespace, ref.Kind, ref.Name)

	limit := 100
	if req.Msg.Page != nil && req.Msg.Page.GetLimit() > 0 {
		l := int(req.Msg.Page.GetLimit())
		if l > 100 {
			l = 100
		}
		limit = l
	}
	if len(versions) > limit {
		versions = versions[:limit]
	}

	out := make([]*catalogv1.VersionSummary, 0, len(versions))
	for _, v := range versions {
		out = append(out, versionSummary(v))
	}
	return connect.NewResponse(&catalogv1.ListVersionsResponse{
		Versions: out,
		Page:     &commonv1.PageResponse{},
	}), nil
}

func (c *Catalog) GetVersion(ctx context.Context, req *connect.Request[catalogv1.GetVersionRequest]) (*connect.Response[catalogv1.GetVersionResponse], error) {
	ref := req.Msg.Ref
	if ref == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("missing ref"))
	}
	version := ref.Version
	if version == "" {
		if a, ok := c.store.GetAsset(ref.Namespace, ref.Kind, ref.Name); ok {
			version = a.LatestVer
		}
	}
	v, ok := c.store.GetVersion(ref.Namespace, ref.Kind, ref.Name, version)
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, catalog.ErrNotFound)
	}
	resp := &catalogv1.GetVersionResponse{
		Version:   versionSummary(v),
		Manifest:  append([]byte(nil), v.Manifest...),
		Readme:    v.Readme,
		Changelog: v.Changelog,
	}
	return connect.NewResponse(resp), nil
}

func (c *Catalog) Resolve(ctx context.Context, req *connect.Request[catalogv1.ResolveRequest]) (*connect.Response[catalogv1.ResolveResponse], error) {
	m := req.Msg
	resp := &catalogv1.ResolveResponse{
		Assets:   []*catalogv1.ResolvedAsset{},
		Warnings: []*catalogv1.ResolutionWarning{},
	}
	for _, d := range m.Desired {
		if d == nil || d.Ref == nil {
			resp.Warnings = append(resp.Warnings, &catalogv1.ResolutionWarning{
				Code:    "invalid_ref",
				Message: "missing asset ref",
			})
			continue
		}
		ref := d.Ref
		a, ok := c.store.GetAsset(ref.Namespace, ref.Kind, ref.Name)
		if !ok {
			resp.Warnings = append(resp.Warnings, &catalogv1.ResolutionWarning{
				Code:    "not_found",
				Message: "asset not found",
				Ref:     proto.Clone(ref).(*commonv1.AssetRef),
			})
			continue
		}
		v, ok := c.pickVersion(a, ref.Version)
		if !ok {
			resp.Warnings = append(resp.Warnings, &catalogv1.ResolutionWarning{
				Code:    "not_found",
				Message: "version not found",
				Ref:     proto.Clone(ref).(*commonv1.AssetRef),
			})
			continue
		}
		ra := &catalogv1.ResolvedAsset{
			Ref: &commonv1.AssetRef{
				Hub:       ref.Hub,
				Namespace: a.Namespace,
				Kind:      a.Kind,
				Name:      a.Name,
				Version:   v.Version,
			},
			OriginHub:      "",
			ManifestDigest: cloneDigest(v.ManifestDigest),
			BundleDigest:   cloneDigest(v.BundleDigest),
			Signature:      nil,
		}
		resp.Assets = append(resp.Assets, ra)
	}
	return connect.NewResponse(resp), nil
}

func (c *Catalog) GetInstallGuide(ctx context.Context, req *connect.Request[catalogv1.GetInstallGuideRequest]) (*connect.Response[catalogv1.GetInstallGuideResponse], error) {
	return nil, unimplemented("GetInstallGuide")
}

func (c *Catalog) WatchCatalog(ctx context.Context, req *connect.Request[catalogv1.WatchCatalogRequest], stream *connect.ServerStream[catalogv1.WatchCatalogResponse]) error {
	return unimplemented("WatchCatalog")
}

// publishRecord is an in-memory publish session.
type publishRecord struct {
	id           string
	namespace    string
	kind         commonv1.AssetKind
	name         string
	version      string
	bundleDigest *commonv1.Digest
	files        []*commonv1.FileDescriptor
	expiresAt    time.Time
}

// Registry is the RegistryService handler.
type Registry struct {
	registryv1connect.UnimplementedRegistryServiceHandler
	regStore     registry.Store
	catStore     catalog.Store
	signingStore registry.SigningKeyStore
	draftStore   registry.DraftStore
	// roles holds namespace RBAC bindings (M3.2); hub scopes them so a
	// multi-hub deployment cannot collide.
	roles authz.Store
	hub   string
	// auditStore records privileged actions (M3.4); the server shares one
	// store with the rate limiter so ListAudit sees every entry.
	auditStore audit.AuditStore
	mu         sync.Mutex
	publishes  map[string]*publishRecord
	blobs      catalog.BlobStore
	publicURL  string
}

// NewRegistry builds a Registry backed by in-memory stores.
func NewRegistry() *Registry { return NewRegistryWithStore(registry.NewMemStore()) }

// NewRegistryWithStore builds a Registry backed by the given registry store
// (and a fresh in-memory catalog store) for backward compatibility.
func NewRegistryWithStore(s registry.Store) *Registry {
	return NewRegistryWithStores(s, catalog.NewMemStore())
}

// NewRegistryWithStores builds a Registry backed by the given stores.
func NewRegistryWithStores(regStore registry.Store, catStore catalog.Store) *Registry {
	return NewRegistryWithDraftStore(regStore, catStore, registry.NewMemDraftStore())
}

// NewRegistryWithDraftStore builds a Registry backed by the given stores and
// draft store.
func NewRegistryWithDraftStore(regStore registry.Store, catStore catalog.Store, draftStore registry.DraftStore) *Registry {
	if draftStore == nil {
		draftStore = registry.NewMemDraftStore()
	}
	return &Registry{
		regStore:     regStore,
		catStore:     catStore,
		signingStore: registry.NewMemSigningKeyStore(),
		draftStore:   draftStore,
		roles:        authz.NewMemStore(),
		auditStore:   audit.NewMemStore(),
		publishes:    map[string]*publishRecord{},
	}
}

// NewRegistryWithSigningStore builds a Registry backed by the given stores and
// signing key store.
func NewRegistryWithSigningStore(regStore registry.Store, catStore catalog.Store, signingStore registry.SigningKeyStore) *Registry {
	if signingStore == nil {
		signingStore = registry.NewMemSigningKeyStore()
	}
	return &Registry{
		regStore:     regStore,
		catStore:     catStore,
		signingStore: signingStore,
		draftStore:   registry.NewMemDraftStore(),
		roles:        authz.NewMemStore(),
		auditStore:   audit.NewMemStore(),
		publishes:    map[string]*publishRecord{},
	}
}

// NewRegistryWithRoles builds a Registry backed by the given stores and
// namespace role store, scoped to hub ("" is the default hub). This is the
// constructor the server uses; the other constructors default to an in-memory
// role store on the default hub.
func NewRegistryWithRoles(regStore registry.Store, catStore catalog.Store, roles authz.Store, hub string) *Registry {
	h := NewRegistryWithDraftStore(regStore, catStore, nil)
	if roles != nil {
		h.roles = roles
	}
	h.hub = hub
	return h
}

// NewRegistryWithAudit builds a Registry like NewRegistryWithRoles but backed
// by the given audit store (nil falls back to an in-memory store). The server
// passes the same store it feeds to the rate limiter (M3.4), so the audit
// entries written by the RPC handlers are the ones ListAudit returns.
func NewRegistryWithAudit(regStore registry.Store, catStore catalog.Store, roles authz.Store, hub string, audits audit.AuditStore) *Registry {
	h := NewRegistryWithRoles(regStore, catStore, roles, hub)
	if audits != nil {
		h.auditStore = audits
	}
	return h
}

// callerRole resolves the caller's role on namespace: hub admins report
// owner, unbound subjects report "".
func (r *Registry) callerRole(id authz.Identity, namespace string) string {
	if id.IsAdmin() {
		return string(authz.RoleOwner)
	}
	if id.Subject == "" {
		return ""
	}
	role, ok := r.roles.GetRole(r.hub, namespace, id.Subject)
	if !ok {
		return ""
	}
	return string(role)
}

// authzError maps authz sentinel errors onto Connect error codes.
func authzError(err error) error {
	switch {
	case errors.Is(err, authz.ErrUnauthenticated):
		return connect.NewError(connect.CodeUnauthenticated, err)
	case errors.Is(err, authz.ErrDenied):
		return connect.NewError(connect.CodePermissionDenied, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}

// requireRole authorizes the caller for min on namespace and maps the authz
// result onto Connect codes (nil when allowed). Namespace existence is the
// caller's business: handlers check it first so an unknown namespace stays
// NotFound even for an unbound caller, matching RegisterSigningKey and
// ListAudit (M3.6).
func (r *Registry) requireRole(ctx context.Context, namespace string, min authz.Role) error {
	if err := authz.Check(r.roles, r.hub, namespace, authz.IdentityFromContext(ctx), min); err != nil {
		return authzError(err)
	}
	return nil
}

// requireDraftRole authorizes a draft mutation: the draft's author always
// passes (the draft was created under the publisher rule), everyone else
// needs min on the draft's namespace — maintainer+ for edits and discards
// (M3.6). Hub admins bypass the namespace role check and anonymous callers
// stay unauthenticated via authz.Check.
func (r *Registry) requireDraftRole(ctx context.Context, d *registry.Draft, min authz.Role) error {
	id := authz.IdentityFromContext(ctx)
	if id.Subject != "" && id.Subject == d.AuthorSub {
		return nil
	}
	if err := authz.Check(r.roles, r.hub, d.Namespace, id, min); err != nil {
		return authzError(err)
	}
	return nil
}

// refNamespace returns the trimmed namespace of ref, or CodeInvalidArgument
// when the request carries no usable reference (M3.6).
func refNamespace(ref *commonv1.AssetRef) (string, error) {
	if ref == nil || strings.TrimSpace(ref.Namespace) == "" {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("ref is required"))
	}
	return strings.TrimSpace(ref.Namespace), nil
}

// assetNamespace extracts the namespace from an asset id of the form
// "<namespace>/<kind>/<name>" — the canonical asset identifier of the web
// routes (`/a/:ns/:kind/:name`). Anything else is CodeInvalidArgument (M3.6).
func assetNamespace(assetID string) (string, error) {
	parts := strings.Split(strings.TrimSpace(assetID), "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", connect.NewError(connect.CodeInvalidArgument,
			errors.New(`asset_id must be "<namespace>/<kind>/<name>"`))
	}
	return parts[0], nil
}

// The audited registry actions (M3.4). The action is the RPC name, so the
// audit trail maps one-to-one onto the calls listed in the M3.4 slice scope.
const (
	auditActionCreateNamespace    = "CreateNamespace"
	auditActionBeginPublish       = "BeginPublish"
	auditActionCommitPublish      = "CommitPublish"
	auditActionCreateDraft        = "CreateDraft"
	auditActionPublishDraft       = "PublishDraft"
	auditActionRegisterSigningKey = "RegisterSigningKey"
	auditActionRevokeSigningKey   = "RevokeSigningKey"
)

// auditDetail renders a JSON detail blob for one audit entry. Rendering can
// only fail on an unencodable value, which the callers never pass; failures
// degrade to an empty detail rather than an error.
func auditDetail(fields map[string]any) string {
	if len(fields) == 0 {
		return ""
	}
	b, err := json.Marshal(fields)
	if err != nil {
		return ""
	}
	return string(b)
}

// auditEvent records one audit entry for a privileged RPC. The actor is the
// caller's subject from the request context (M3.4); audit write failures never
// fail the request, matching the rate-limit middleware (M3.1).
func (r *Registry) auditEvent(ctx context.Context, action, namespace, asset, version, detail string) {
	if r.auditStore == nil {
		return
	}
	_ = r.auditStore.Log(action, authz.IdentityFromContext(ctx).Subject, namespace, asset, version, detail)
}

func (r *Registry) CreateNamespace(ctx context.Context, req *connect.Request[registryv1.CreateNamespaceRequest]) (*connect.Response[registryv1.CreateNamespaceResponse], error) {
	m := req.Msg
	id := authz.IdentityFromContext(ctx)
	// M3.2: creating a namespace needs the admin scope or an authenticated
	// subject; the creator becomes the owner of the namespace it creates.
	if !id.Authenticated() {
		return nil, connect.NewError(connect.CodeUnauthenticated, authz.ErrUnauthenticated)
	}
	ns := &commonv1.Namespace{
		Slug:        m.Slug,
		DisplayName: m.DisplayName,
		Description: m.Description,
		Visibility:  m.Visibility,
	}
	created, err := r.regStore.Create(ns)
	if err != nil {
		switch {
		case errors.Is(err, registry.ErrInvalidSlug):
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		case errors.Is(err, registry.ErrDuplicate):
			return nil, connect.NewError(connect.CodeAlreadyExists, err)
		default:
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	if id.Subject != "" {
		if err := r.roles.Grant(r.hub, created.Slug, id.Subject, authz.RoleOwner); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	r.auditEvent(ctx, auditActionCreateNamespace, created.Slug, "", "", auditDetail(map[string]any{
		"visibility": created.Visibility.String(),
	}))
	return connect.NewResponse(&registryv1.CreateNamespaceResponse{Namespace: created}), nil
}

func (r *Registry) GetNamespace(ctx context.Context, req *connect.Request[registryv1.GetNamespaceRequest]) (*connect.Response[registryv1.GetNamespaceResponse], error) {
	ns, ok := r.regStore.Get(req.Msg.Slug)
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, registry.ErrNotFound)
	}
	role := r.callerRole(authz.IdentityFromContext(ctx), req.Msg.Slug)
	return connect.NewResponse(&registryv1.GetNamespaceResponse{Namespace: ns, CallerRole: role}), nil
}

func (r *Registry) ListNamespaces(ctx context.Context, req *connect.Request[registryv1.ListNamespacesRequest]) (*connect.Response[registryv1.ListNamespacesResponse], error) {
	id := authz.IdentityFromContext(ctx)
	items := r.regStore.List()
	out := make([]*registryv1.NamespaceWithRole, 0, len(items))
	for _, ns := range items {
		out = append(out, &registryv1.NamespaceWithRole{
			Namespace:  ns,
			CallerRole: r.callerRole(id, ns.Slug),
		})
	}
	return connect.NewResponse(&registryv1.ListNamespacesResponse{
		Namespaces: out,
		Page:       &commonv1.PageResponse{},
	}), nil
}

func (r *Registry) BeginPublish(ctx context.Context, req *connect.Request[registryv1.BeginPublishRequest]) (*connect.Response[registryv1.BeginPublishResponse], error) {
	m := req.Msg
	if _, ok := r.regStore.Get(m.Namespace); !ok {
		return nil, connect.NewError(connect.CodeNotFound, registry.ErrNotFound)
	}
	if err := r.requireRole(ctx, m.Namespace, authz.RolePublisher); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	id, err := newPublishID()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	rec := &publishRecord{
		id:           id,
		namespace:    m.Namespace,
		kind:         m.Kind,
		name:         m.Name,
		version:      m.Version,
		bundleDigest: cloneDigest(m.BundleDigest),
		files:        cloneFiles(m.Files),
		expiresAt:    now.Add(15 * time.Minute),
	}

	r.mu.Lock()
	r.publishes[id] = rec
	r.mu.Unlock()

	r.auditEvent(ctx, auditActionBeginPublish, m.Namespace, m.Name, m.Version, auditDetail(map[string]any{
		"publish_id": id,
	}))

	return connect.NewResponse(&registryv1.BeginPublishResponse{
		PublishId: id,
		Uploads:   r.uploadTargets(rec.files),
		ExpiresAt: timestamppb.New(rec.expiresAt),
	}), nil
}

func (r *Registry) CommitPublish(ctx context.Context, req *connect.Request[registryv1.CommitPublishRequest]) (*connect.Response[registryv1.CommitPublishResponse], error) {
	m := req.Msg

	r.mu.Lock()
	rec, ok := r.publishes[m.PublishId]
	if ok && time.Now().UTC().After(rec.expiresAt) {
		delete(r.publishes, m.PublishId)
		ok = false
	}
	r.mu.Unlock()
	if !ok || rec == nil {
		return nil, connect.NewError(connect.CodeNotFound, registry.ErrNotFound)
	}
	if err := r.requireRole(ctx, rec.namespace, authz.RolePublisher); err != nil {
		return nil, err
	}

	md := m.ManifestDigest
	if md == nil || md.Algorithm != "sha256" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("manifest digest must use sha256"))
	}
	h := sha256.Sum256(m.Manifest)
	computed := hex.EncodeToString(h[:])
	if computed != md.Value {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("manifest digest mismatch"))
	}

	// idempotency
	if existing, ok := r.catStore.GetVersion(rec.namespace, rec.kind, rec.name, rec.version); ok {
		if digestEqual(existing.ManifestDigest, md) {
			// idempotent re-commit: still an audited publish.commit
			r.auditEvent(ctx, auditActionCommitPublish, rec.namespace, rec.name, rec.version, auditDetail(map[string]any{
				"publish_id": m.PublishId,
				"idempotent": true,
			}))
			ra := &catalogv1.ResolvedAsset{
				Ref:            &commonv1.AssetRef{Hub: "", Namespace: rec.namespace, Kind: rec.kind, Name: rec.name, Version: rec.version},
				OriginHub:      "",
				ManifestDigest: cloneDigest(md),
				BundleDigest:   cloneDigest(rec.bundleDigest),
				Signature:      cloneSignature(m.Signature),
			}
			return connect.NewResponse(&registryv1.CommitPublishResponse{Asset: ra}), nil
		}
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("version already exists with different manifest"))
	}

	now := timestamppb.Now()
	meta := parseManifestMeta(m.Manifest)
	displayName := meta.DisplayName
	if displayName == "" {
		displayName = rec.name
	}
	if err := r.catStore.UpsertAsset(&catalog.Asset{
		Namespace:      rec.namespace,
		Kind:           rec.kind,
		Name:           rec.name,
		DisplayName:    displayName,
		Summary:        meta.Summary,
		LatestVer:      rec.version,
		LandingVisible: true,
		CreatedAt:      now,
		UpdatedAt:      now,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := r.catStore.UpsertVersion(&catalog.Version{
		Namespace:      rec.namespace,
		Kind:           rec.kind,
		Name:           rec.name,
		Version:        rec.version,
		Status:         commonv1.VersionStatus_VERSION_STATUS_PUBLISHED,
		Manifest:       append([]byte(nil), m.Manifest...),
		ManifestDigest: cloneDigest(md),
		BundleDigest:   cloneDigest(rec.bundleDigest),
		PublishedAt:    now,
		Readme:         m.Readme,
		Changelog:      m.Changelog,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	ra := &catalogv1.ResolvedAsset{
		Ref:            &commonv1.AssetRef{Hub: "", Namespace: rec.namespace, Kind: rec.kind, Name: rec.name, Version: rec.version},
		OriginHub:      "",
		ManifestDigest: cloneDigest(md),
		BundleDigest:   cloneDigest(rec.bundleDigest),
		Signature:      cloneSignature(m.Signature),
	}
	r.auditEvent(ctx, auditActionCommitPublish, rec.namespace, rec.name, rec.version, auditDetail(map[string]any{
		"publish_id": m.PublishId,
	}))
	return connect.NewResponse(&registryv1.CommitPublishResponse{Asset: ra}), nil
}

func (r *Registry) CreateDraft(ctx context.Context, req *connect.Request[registryv1.CreateDraftRequest]) (*connect.Response[registryv1.CreateDraftResponse], error) {
	m := req.Msg
	if _, ok := r.regStore.Get(m.Namespace); !ok {
		return nil, connect.NewError(connect.CodeNotFound, registry.ErrNotFound)
	}
	// M3.6: creating a draft needs the publisher role on the namespace
	// (api.md §5.5: "Create/edit a draft → publisher+").
	if err := r.requireRole(ctx, m.Namespace, authz.RolePublisher); err != nil {
		return nil, err
	}
	id, err := newPublishID()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	now := timestamppb.Now()
	files := make([]registry.DraftFile, 0, len(m.Files))
	for _, f := range m.Files {
		if f == nil {
			continue
		}
		files = append(files, registry.DraftFile{Path: f.Path, Content: append([]byte(nil), f.Content...)})
	}
	draft := &registry.Draft{
		DraftID:   id,
		Namespace: m.Namespace,
		Kind:      m.Kind,
		Name:      m.Name,
		// the author is the caller: draft mutations allow the author even
		// after their namespace role is revoked (M3.6)
		AuthorSub: authz.IdentityFromContext(ctx).Subject,
		Manifest:  append([]byte(nil), m.Manifest...),
		Files:     files,
		Status:    "draft",
		CreatedAt: now,
		UpdatedAt: now,
	}
	created, err := r.draftStore.Create(draft)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	r.auditEvent(ctx, auditActionCreateDraft, m.Namespace, m.Name, "", auditDetail(map[string]any{
		"draft_id": created.DraftID,
	}))
	return connect.NewResponse(&registryv1.CreateDraftResponse{DraftId: id}), nil
}

func (r *Registry) UpdateDraft(ctx context.Context, req *connect.Request[registryv1.UpdateDraftRequest]) (*connect.Response[registryv1.UpdateDraftResponse], error) {
	m := req.Msg
	existing, ok := r.draftStore.Get(m.DraftId)
	if !ok || existing == nil {
		return nil, connect.NewError(connect.CodeNotFound, registry.ErrDraftNotFound)
	}
	// M3.6: the author or a maintainer+ may edit a draft (api.md §5.5:
	// "Create/edit a draft → publisher+"; the author always passes).
	if err := r.requireDraftRole(ctx, existing, authz.RoleMaintainer); err != nil {
		return nil, err
	}
	if m.Manifest != nil {
		existing.Manifest = append([]byte(nil), m.Manifest...)
	}
	if m.Files != nil {
		files := make([]registry.DraftFile, 0, len(m.Files))
		for _, f := range m.Files {
			if f == nil {
				continue
			}
			files = append(files, registry.DraftFile{Path: f.Path, Content: append([]byte(nil), f.Content...)})
		}
		existing.Files = files
	}
	if _, err := r.draftStore.Update(existing); err != nil {
		if errors.Is(err, registry.ErrDraftNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&registryv1.UpdateDraftResponse{}), nil
}

// draftForAuthz loads a draft by id for an authorization decision, mapping a
// missing draft onto CodeNotFound (M3.6). Draft existence is checked before
// the role, matching the namespace-existence-first order of the other RPCs.
func (r *Registry) draftForAuthz(draftID string) (*registry.Draft, error) {
	d, ok := r.draftStore.Get(draftID)
	if !ok || d == nil {
		return nil, connect.NewError(connect.CodeNotFound, registry.ErrDraftNotFound)
	}
	return d, nil
}

func (r *Registry) DiscardDraft(ctx context.Context, req *connect.Request[registryv1.DiscardDraftRequest]) (*connect.Response[registryv1.DiscardDraftResponse], error) {
	draft, err := r.draftForAuthz(req.Msg.DraftId)
	if err != nil {
		return nil, err
	}
	// M3.6: the author or a maintainer+ may discard a draft.
	if err := r.requireDraftRole(ctx, draft, authz.RoleMaintainer); err != nil {
		return nil, err
	}
	if err := r.draftStore.Discard(req.Msg.DraftId); err != nil {
		if errors.Is(err, registry.ErrDraftNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&registryv1.DiscardDraftResponse{}), nil
}

func (r *Registry) DeleteDraft(ctx context.Context, req *connect.Request[registryv1.DeleteDraftRequest]) (*connect.Response[registryv1.DeleteDraftResponse], error) {
	draft, err := r.draftForAuthz(req.Msg.DraftId)
	if err != nil {
		return nil, err
	}
	// M3.6: the author or a maintainer+ may delete a draft.
	if err := r.requireDraftRole(ctx, draft, authz.RoleMaintainer); err != nil {
		return nil, err
	}
	if err := r.draftStore.Delete(req.Msg.DraftId); err != nil {
		if errors.Is(err, registry.ErrDraftNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&registryv1.DeleteDraftResponse{}), nil
}

func (r *Registry) PublishDraft(ctx context.Context, req *connect.Request[registryv1.PublishDraftRequest]) (*connect.Response[registryv1.PublishDraftResponse], error) {
	m := req.Msg
	draft, ok := r.draftStore.Get(m.DraftId)
	if !ok || draft == nil {
		return nil, connect.NewError(connect.CodeNotFound, registry.ErrDraftNotFound)
	}
	if strings.TrimSpace(m.Version) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("version is required"))
	}
	if _, ok := r.regStore.Get(draft.Namespace); !ok {
		return nil, connect.NewError(connect.CodeNotFound, registry.ErrNotFound)
	}
	// M3.6: publishing a draft needs the publisher role on its namespace
	// (api.md §5.5: "Publish a draft → publisher+"). An author without the
	// publisher role is denied: publishing is a namespace capability, not an
	// ownership one.
	if err := r.requireRole(ctx, draft.Namespace, authz.RolePublisher); err != nil {
		return nil, err
	}

	now := timestamppb.Now()
	if err := r.catStore.UpsertAsset(&catalog.Asset{
		Namespace:   draft.Namespace,
		Kind:        draft.Kind,
		Name:        draft.Name,
		DisplayName: draft.Name,
		Summary:     "",
		LatestVer:   m.Version,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	h := sha256.Sum256(draft.Manifest)
	md := hex.EncodeToString(h[:])
	if err := r.catStore.UpsertVersion(&catalog.Version{
		Namespace:      draft.Namespace,
		Kind:           draft.Kind,
		Name:           draft.Name,
		Version:        m.Version,
		Status:         commonv1.VersionStatus_VERSION_STATUS_PUBLISHED,
		Manifest:       append([]byte(nil), draft.Manifest...),
		ManifestDigest: &commonv1.Digest{Algorithm: "sha256", Value: md},
		PublishedAt:    now,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	_ = r.draftStore.Delete(m.DraftId)

	r.auditEvent(ctx, auditActionPublishDraft, draft.Namespace, draft.Name, m.Version, auditDetail(map[string]any{
		"draft_id": m.DraftId,
	}))

	ra := &catalogv1.ResolvedAsset{
		Ref:            &commonv1.AssetRef{Hub: "", Namespace: draft.Namespace, Kind: draft.Kind, Name: draft.Name, Version: m.Version},
		OriginHub:      "",
		ManifestDigest: &commonv1.Digest{Algorithm: "sha256", Value: md},
	}
	return connect.NewResponse(&registryv1.PublishDraftResponse{Asset: ra}), nil
}

func (r *Registry) SetLandingVisibility(ctx context.Context, req *connect.Request[registryv1.SetLandingVisibilityRequest]) (*connect.Response[registryv1.SetLandingVisibilityResponse], error) {
	// M3.6: hiding/showing a landing card needs the maintainer role on the
	// asset's namespace (api.md §5.5: "Hide/show landing card → maintainer+").
	// asset_id is the canonical "<namespace>/<kind>/<name>" identifier.
	ns, err := assetNamespace(req.Msg.AssetId)
	if err != nil {
		return nil, err
	}
	if _, ok := r.regStore.Get(ns); !ok {
		return nil, connect.NewError(connect.CodeNotFound, registry.ErrNotFound)
	}
	if err := r.requireRole(ctx, ns, authz.RoleMaintainer); err != nil {
		return nil, err
	}
	return nil, unimplemented("SetLandingVisibility")
}

func (r *Registry) ValidateBundle(ctx context.Context, req *connect.Request[registryv1.ValidateBundleRequest]) (*connect.Response[registryv1.ValidateBundleResponse], error) {
	// M3.6: validating a bundle for a namespace needs the publisher role
	// (api.md §5.5 ties bundle work to publish).
	ns := strings.TrimSpace(req.Msg.Namespace)
	if ns == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("namespace is required"))
	}
	if _, ok := r.regStore.Get(ns); !ok {
		return nil, connect.NewError(connect.CodeNotFound, registry.ErrNotFound)
	}
	if err := r.requireRole(ctx, ns, authz.RolePublisher); err != nil {
		return nil, err
	}
	return nil, unimplemented("ValidateBundle")
}

func (r *Registry) YankVersion(ctx context.Context, req *connect.Request[registryv1.YankVersionRequest]) (*connect.Response[registryv1.YankVersionResponse], error) {
	// M3.6: yanking a version needs the maintainer role (api.md §5.5:
	// "Yank/deprecate → maintainer+").
	ns, err := refNamespace(req.Msg.Ref)
	if err != nil {
		return nil, err
	}
	if _, ok := r.regStore.Get(ns); !ok {
		return nil, connect.NewError(connect.CodeNotFound, registry.ErrNotFound)
	}
	if err := r.requireRole(ctx, ns, authz.RoleMaintainer); err != nil {
		return nil, err
	}
	return nil, unimplemented("YankVersion")
}

func (r *Registry) UnyankVersion(ctx context.Context, req *connect.Request[registryv1.UnyankVersionRequest]) (*connect.Response[registryv1.UnyankVersionResponse], error) {
	// M3.6: unyanking mirrors yanking: maintainer+ on the namespace.
	ns, err := refNamespace(req.Msg.Ref)
	if err != nil {
		return nil, err
	}
	if _, ok := r.regStore.Get(ns); !ok {
		return nil, connect.NewError(connect.CodeNotFound, registry.ErrNotFound)
	}
	if err := r.requireRole(ctx, ns, authz.RoleMaintainer); err != nil {
		return nil, err
	}
	return nil, unimplemented("UnyankVersion")
}

func (r *Registry) DeprecateVersion(ctx context.Context, req *connect.Request[registryv1.DeprecateVersionRequest]) (*connect.Response[registryv1.DeprecateVersionResponse], error) {
	// M3.6: deprecating a version needs the maintainer role (api.md §5.5:
	// "Yank/deprecate → maintainer+").
	ns, err := refNamespace(req.Msg.Ref)
	if err != nil {
		return nil, err
	}
	if _, ok := r.regStore.Get(ns); !ok {
		return nil, connect.NewError(connect.CodeNotFound, registry.ErrNotFound)
	}
	if err := r.requireRole(ctx, ns, authz.RoleMaintainer); err != nil {
		return nil, err
	}
	return nil, unimplemented("DeprecateVersion")
}

func (r *Registry) RegisterSigningKey(ctx context.Context, req *connect.Request[registryv1.RegisterSigningKeyRequest]) (*connect.Response[registryv1.RegisterSigningKeyResponse], error) {
	m := req.Msg
	if _, ok := r.regStore.Get(m.Namespace); !ok {
		return nil, connect.NewError(connect.CodeNotFound, registry.ErrNotFound)
	}
	// M3.2: registering a signing key needs the publisher role on the
	// namespace (hub admins bypass the role check).
	if err := r.requireRole(ctx, m.Namespace, authz.RolePublisher); err != nil {
		return nil, err
	}
	if strings.TrimSpace(m.Kid) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("kid is required"))
	}
	key := &registry.SigningKey{
		Namespace: m.Namespace,
		Kid:       m.Kid,
		Algorithm: m.Algorithm,
		Custody:   m.Custody,
		PublicKey: m.PublicKey,
		KmsKeyRef: m.KmsKeyRef,
	}
	if m.ValidFrom != nil {
		key.ValidFrom = proto.Clone(m.ValidFrom).(*timestamppb.Timestamp)
	}
	created, err := r.signingStore.Register(key)
	if err != nil {
		switch {
		case errors.Is(err, registry.ErrSigningKeyDuplicate):
			return nil, connect.NewError(connect.CodeAlreadyExists, err)
		default:
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}
	r.auditEvent(ctx, auditActionRegisterSigningKey, m.Namespace, "", "", auditDetail(map[string]any{
		"kid": created.Kid,
	}))
	return connect.NewResponse(&registryv1.RegisterSigningKeyResponse{Key: signingKeyToProto(created)}), nil
}

func (r *Registry) RevokeSigningKey(ctx context.Context, req *connect.Request[registryv1.RevokeSigningKeyRequest]) (*connect.Response[registryv1.RevokeSigningKeyResponse], error) {
	m := req.Msg
	if _, ok := r.regStore.Get(m.Namespace); !ok {
		return nil, connect.NewError(connect.CodeNotFound, registry.ErrNotFound)
	}
	// M3.6: revoking a signing key needs the owner role (api.md §5.5:
	// "Register/revoke signing keys → owner").
	if err := r.requireRole(ctx, m.Namespace, authz.RoleOwner); err != nil {
		return nil, err
	}
	if strings.TrimSpace(m.Kid) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("kid is required"))
	}
	revoked, err := r.signingStore.Revoke(m.Namespace, m.Kid)
	_ = revoked
	if err != nil {
		switch {
		case errors.Is(err, registry.ErrSigningKeyNotFound):
			return nil, connect.NewError(connect.CodeNotFound, err)
		default:
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	r.auditEvent(ctx, auditActionRevokeSigningKey, m.Namespace, "", "", auditDetail(map[string]any{
		"kid": m.Kid,
	}))
	return connect.NewResponse(&registryv1.RevokeSigningKeyResponse{}), nil
}

func signingKeyToProto(k *registry.SigningKey) *registryv1.SigningKey {
	if k == nil {
		return nil
	}
	out := &registryv1.SigningKey{
		Namespace: k.Namespace,
		Kid:       k.Kid,
		Algorithm: k.Algorithm,
		Custody:   k.Custody,
		PublicKey: k.PublicKey,
		KmsKeyRef: k.KmsKeyRef,
	}
	if k.CreatedAt != nil {
		out.CreatedAt = proto.Clone(k.CreatedAt).(*timestamppb.Timestamp)
	}
	if k.ValidFrom != nil {
		out.ValidFrom = proto.Clone(k.ValidFrom).(*timestamppb.Timestamp)
	}
	if k.RevokedAt != nil {
		out.RevokedAt = proto.Clone(k.RevokedAt).(*timestamppb.Timestamp)
	}
	return out
}

// auditPageLimit caps one ListAudit page: common.v1.PageRequest documents a
// server-side cap of 100.
const auditPageLimit = 100

// ListAudit returns the namespace's audit trail, newest first. The caller
// needs the maintainer role on the namespace (hub admins bypass), the
// namespace filter is mandatory and the page cursor is the decimal offset of
// the next entry (M3.4).
func (r *Registry) ListAudit(ctx context.Context, req *connect.Request[registryv1.ListAuditRequest]) (*connect.Response[registryv1.ListAuditResponse], error) {
	m := req.Msg
	ns := strings.TrimSpace(m.Namespace)
	if ns == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("namespace is required"))
	}
	if _, ok := r.regStore.Get(ns); !ok {
		return nil, connect.NewError(connect.CodeNotFound, registry.ErrNotFound)
	}
	if err := r.requireRole(ctx, ns, authz.RoleMaintainer); err != nil {
		return nil, err
	}

	offset := 0
	if cursor := strings.TrimSpace(m.Page.GetCursor()); cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 0 {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid page cursor"))
		}
		offset = n
	}
	limit := int(m.Page.GetLimit())
	if limit <= 0 || limit > auditPageLimit {
		limit = auditPageLimit
	}

	var entries []audit.Entry
	if r.auditStore != nil {
		var err error
		entries, err = r.auditStore.List(audit.Query{Namespace: ns, Limit: limit, Offset: offset})
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}

	events := make([]*registryv1.AuditEvent, 0, len(entries))
	for _, e := range entries {
		events = append(events, auditEntryToProto(e))
	}
	resp := &registryv1.ListAuditResponse{
		Events: events,
		Page:   &commonv1.PageResponse{},
	}
	if len(entries) == limit {
		// A full page usually means more entries follow; the next call may
		// still come back empty, which is acceptable for offset paging.
		resp.Page.NextCursor = strconv.Itoa(offset + len(entries))
	}
	return connect.NewResponse(resp), nil
}

// auditEntryToProto maps one store entry onto the wire message. The store
// only records the actor subject, so actor_email and ip stay empty; the
// namespace is the request's filter, not a field of AuditEvent.
func auditEntryToProto(e audit.Entry) *registryv1.AuditEvent {
	out := &registryv1.AuditEvent{
		ActorSub:   e.Actor,
		Action:     e.Action,
		Asset:      e.Asset,
		Version:    e.Version,
		DetailJson: e.Detail,
	}
	if !e.At.IsZero() {
		out.At = timestamppb.New(e.At)
	}
	return out
}

// Identity is the IdentityService handler stub.
type Identity struct {
	identityv1connect.UnimplementedIdentityServiceHandler
}

func NewIdentity() *Identity { return &Identity{} }
func (i *Identity) WhoAmI(ctx context.Context, req *connect.Request[identityv1.WhoAmIRequest]) (*connect.Response[identityv1.WhoAmIResponse], error) {
	return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("not authenticated"))
}

func (i *Identity) ListMyNamespaces(ctx context.Context, req *connect.Request[identityv1.ListMyNamespacesRequest]) (*connect.Response[identityv1.ListMyNamespacesResponse], error) {
	return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("not authenticated"))
}

func (i *Identity) GetTokenInfo(ctx context.Context, req *connect.Request[identityv1.GetTokenInfoRequest]) (*connect.Response[identityv1.GetTokenInfoResponse], error) {
	return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("not authenticated"))
}

var (
	_ catalogv1connect.CatalogServiceHandler       = (*Catalog)(nil)
	_ registryv1connect.RegistryServiceHandler     = (*Registry)(nil)
	_ federationv1connect.FederationServiceHandler = (*Federation)(nil)
	_ identityv1connect.IdentityServiceHandler     = (*Identity)(nil)
)

func assetSummary(a *catalog.Asset) *catalogv1.AssetSummary {
	if a == nil {
		return nil
	}
	return &catalogv1.AssetSummary{
		Ref:           &commonv1.AssetRef{Namespace: a.Namespace, Kind: a.Kind, Name: a.Name},
		DisplayName:   a.DisplayName,
		Summary:       a.Summary,
		Tags:          append([]string(nil), a.Tags...),
		LatestVersion: a.LatestVer,
	}
}

func versionSummary(v *catalog.Version) *catalogv1.VersionSummary {
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
	vs := &catalogv1.VersionSummary{
		Ref:            &commonv1.AssetRef{Namespace: v.Namespace, Kind: v.Kind, Name: v.Name, Version: v.Version},
		Status:         v.Status,
		ManifestDigest: md,
		BundleDigest:   bd,
		BundleSize:     v.BundleSize,
		Verified:       v.Verified,
	}
	if v.PublishedAt != nil {
		vs.PublishedAt = proto.Clone(v.PublishedAt).(*timestamppb.Timestamp)
	}
	return vs
}

func containsKind(kinds []commonv1.AssetKind, k commonv1.AssetKind) bool {
	for _, x := range kinds {
		if x == k {
			return true
		}
	}
	return false
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func containsAllTags(assetTags, wanted []string) bool {
	for _, w := range wanted {
		if !containsString(assetTags, w) {
			return false
		}
	}
	return true
}

func newPublishID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func cloneDigest(d *commonv1.Digest) *commonv1.Digest {
	if d == nil {
		return nil
	}
	return proto.Clone(d).(*commonv1.Digest)
}

func cloneSignature(s *commonv1.Signature) *commonv1.Signature {
	if s == nil {
		return nil
	}
	return proto.Clone(s).(*commonv1.Signature)
}

func cloneFiles(files []*commonv1.FileDescriptor) []*commonv1.FileDescriptor {
	out := make([]*commonv1.FileDescriptor, 0, len(files))
	for _, f := range files {
		if f == nil {
			continue
		}
		out = append(out, proto.Clone(f).(*commonv1.FileDescriptor))
	}
	return out
}

func digestEqual(a, b *commonv1.Digest) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Algorithm == b.Algorithm && a.Value == b.Value
}
