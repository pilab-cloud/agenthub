// Package api provides ConnectRPC handler stubs. Real logic lands in M1+.
package api

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	catalogv1 "go.pilab.hu/agenthub/gen/go/agenthub/catalog/v1"
	catalogv1connect "go.pilab.hu/agenthub/gen/go/agenthub/catalog/v1/catalogv1connect"
	federationv1 "go.pilab.hu/agenthub/gen/go/agenthub/federation/v1"
	federationv1connect "go.pilab.hu/agenthub/gen/go/agenthub/federation/v1/federationv1connect"
	identityv1 "go.pilab.hu/agenthub/gen/go/agenthub/identity/v1"
	identityv1connect "go.pilab.hu/agenthub/gen/go/agenthub/identity/v1/identityv1connect"
	registryv1 "go.pilab.hu/agenthub/gen/go/agenthub/registry/v1"
	registryv1connect "go.pilab.hu/agenthub/gen/go/agenthub/registry/v1/registryv1connect"
)

// unimplemented returns a CodeUnimplemented error for an RPC.
func unimplemented(rpc string) error {
	return connect.NewError(connect.CodeUnimplemented, errors.New(rpc+" is not implemented yet (M1+)"))
}

// Catalog is the CatalogService handler stub.
type Catalog struct {
	catalogv1connect.UnimplementedCatalogServiceHandler
}

func NewCatalog() *Catalog { return &Catalog{} }
func (c *Catalog) SearchAssets(ctx context.Context, req *connect.Request[catalogv1.SearchAssetsRequest]) (*connect.Response[catalogv1.SearchAssetsResponse], error) {
	return nil, unimplemented("SearchAssets")
}

func (c *Catalog) GetAsset(ctx context.Context, req *connect.Request[catalogv1.GetAssetRequest]) (*connect.Response[catalogv1.GetAssetResponse], error) {
	return nil, unimplemented("GetAsset")
}

func (c *Catalog) ListVersions(ctx context.Context, req *connect.Request[catalogv1.ListVersionsRequest]) (*connect.Response[catalogv1.ListVersionsResponse], error) {
	return nil, unimplemented("ListVersions")
}

func (c *Catalog) GetVersion(ctx context.Context, req *connect.Request[catalogv1.GetVersionRequest]) (*connect.Response[catalogv1.GetVersionResponse], error) {
	return nil, unimplemented("GetVersion")
}

func (c *Catalog) Resolve(ctx context.Context, req *connect.Request[catalogv1.ResolveRequest]) (*connect.Response[catalogv1.ResolveResponse], error) {
	return nil, unimplemented("Resolve")
}

func (c *Catalog) GetBundle(ctx context.Context, req *connect.Request[catalogv1.GetBundleRequest]) (*connect.Response[catalogv1.GetBundleResponse], error) {
	return nil, unimplemented("GetBundle")
}

func (c *Catalog) GetInstallGuide(ctx context.Context, req *connect.Request[catalogv1.GetInstallGuideRequest]) (*connect.Response[catalogv1.GetInstallGuideResponse], error) {
	return nil, unimplemented("GetInstallGuide")
}

func (c *Catalog) WatchCatalog(ctx context.Context, req *connect.Request[catalogv1.WatchCatalogRequest], stream *connect.ServerStream[catalogv1.WatchCatalogResponse]) error {
	return unimplemented("WatchCatalog")
}

// Registry is the RegistryService handler stub.
type Registry struct {
	registryv1connect.UnimplementedRegistryServiceHandler
}

func NewRegistry() *Registry { return &Registry{} }
func (r *Registry) CreateNamespace(ctx context.Context, req *connect.Request[registryv1.CreateNamespaceRequest]) (*connect.Response[registryv1.CreateNamespaceResponse], error) {
	return nil, unimplemented("CreateNamespace")
}

func (r *Registry) GetNamespace(ctx context.Context, req *connect.Request[registryv1.GetNamespaceRequest]) (*connect.Response[registryv1.GetNamespaceResponse], error) {
	return nil, unimplemented("GetNamespace")
}

func (r *Registry) ListNamespaces(ctx context.Context, req *connect.Request[registryv1.ListNamespacesRequest]) (*connect.Response[registryv1.ListNamespacesResponse], error) {
	return nil, unimplemented("ListNamespaces")
}

func (r *Registry) BeginPublish(ctx context.Context, req *connect.Request[registryv1.BeginPublishRequest]) (*connect.Response[registryv1.BeginPublishResponse], error) {
	return nil, unimplemented("BeginPublish")
}

func (r *Registry) CommitPublish(ctx context.Context, req *connect.Request[registryv1.CommitPublishRequest]) (*connect.Response[registryv1.CommitPublishResponse], error) {
	return nil, unimplemented("CommitPublish")
}

func (r *Registry) CreateDraft(ctx context.Context, req *connect.Request[registryv1.CreateDraftRequest]) (*connect.Response[registryv1.CreateDraftResponse], error) {
	return nil, unimplemented("CreateDraft")
}

func (r *Registry) UpdateDraft(ctx context.Context, req *connect.Request[registryv1.UpdateDraftRequest]) (*connect.Response[registryv1.UpdateDraftResponse], error) {
	return nil, unimplemented("UpdateDraft")
}

func (r *Registry) DiscardDraft(ctx context.Context, req *connect.Request[registryv1.DiscardDraftRequest]) (*connect.Response[registryv1.DiscardDraftResponse], error) {
	return nil, unimplemented("DiscardDraft")
}

func (r *Registry) DeleteDraft(ctx context.Context, req *connect.Request[registryv1.DeleteDraftRequest]) (*connect.Response[registryv1.DeleteDraftResponse], error) {
	return nil, unimplemented("DeleteDraft")
}

func (r *Registry) PublishDraft(ctx context.Context, req *connect.Request[registryv1.PublishDraftRequest]) (*connect.Response[registryv1.PublishDraftResponse], error) {
	return nil, unimplemented("PublishDraft")
}

func (r *Registry) SetLandingVisibility(ctx context.Context, req *connect.Request[registryv1.SetLandingVisibilityRequest]) (*connect.Response[registryv1.SetLandingVisibilityResponse], error) {
	return nil, unimplemented("SetLandingVisibility")
}

func (r *Registry) ValidateBundle(ctx context.Context, req *connect.Request[registryv1.ValidateBundleRequest]) (*connect.Response[registryv1.ValidateBundleResponse], error) {
	return nil, unimplemented("ValidateBundle")
}

func (r *Registry) YankVersion(ctx context.Context, req *connect.Request[registryv1.YankVersionRequest]) (*connect.Response[registryv1.YankVersionResponse], error) {
	return nil, unimplemented("YankVersion")
}

func (r *Registry) UnyankVersion(ctx context.Context, req *connect.Request[registryv1.UnyankVersionRequest]) (*connect.Response[registryv1.UnyankVersionResponse], error) {
	return nil, unimplemented("UnyankVersion")
}

func (r *Registry) DeprecateVersion(ctx context.Context, req *connect.Request[registryv1.DeprecateVersionRequest]) (*connect.Response[registryv1.DeprecateVersionResponse], error) {
	return nil, unimplemented("DeprecateVersion")
}

func (r *Registry) RegisterSigningKey(ctx context.Context, req *connect.Request[registryv1.RegisterSigningKeyRequest]) (*connect.Response[registryv1.RegisterSigningKeyResponse], error) {
	return nil, unimplemented("RegisterSigningKey")
}

func (r *Registry) RevokeSigningKey(ctx context.Context, req *connect.Request[registryv1.RevokeSigningKeyRequest]) (*connect.Response[registryv1.RevokeSigningKeyResponse], error) {
	return nil, unimplemented("RevokeSigningKey")
}

func (r *Registry) ListAudit(ctx context.Context, req *connect.Request[registryv1.ListAuditRequest]) (*connect.Response[registryv1.ListAuditResponse], error) {
	return nil, unimplemented("ListAudit")
}

// Federation is the FederationService handler stub.
type Federation struct {
	federationv1connect.UnimplementedFederationServiceHandler
	hubID   string
	baseURL string
}

func NewFederation(hubID, baseURL string) *Federation {
	return &Federation{hubID: hubID, baseURL: baseURL}
}

func (f *Federation) GetHubInfo(ctx context.Context, req *connect.Request[federationv1.GetHubInfoRequest]) (*connect.Response[federationv1.GetHubInfoResponse], error) {
	return connect.NewResponse(&federationv1.GetHubInfoResponse{
		Hub: &federationv1.HubInfo{
			Id:               f.hubID,
			BaseUrl:          f.baseURL,
			Protocol:         1,
			SigningAlgorithm: "ed25519",
		},
	}), nil
}

func (f *Federation) GetSnapshot(ctx context.Context, req *connect.Request[federationv1.GetSnapshotRequest]) (*connect.Response[federationv1.GetSnapshotResponse], error) {
	return nil, unimplemented("GetSnapshot")
}

func (f *Federation) SyncChanges(ctx context.Context, req *connect.Request[federationv1.SyncChangesRequest]) (*connect.Response[federationv1.SyncChangesResponse], error) {
	return nil, unimplemented("SyncChanges")
}

func (f *Federation) ListUpstreams(ctx context.Context, req *connect.Request[federationv1.ListUpstreamsRequest]) (*connect.Response[federationv1.ListUpstreamsResponse], error) {
	return nil, unimplemented("ListUpstreams")
}

func (f *Federation) UpsertUpstream(ctx context.Context, req *connect.Request[federationv1.UpsertUpstreamRequest]) (*connect.Response[federationv1.UpsertUpstreamResponse], error) {
	return nil, unimplemented("UpsertUpstream")
}

func (f *Federation) DeleteUpstream(ctx context.Context, req *connect.Request[federationv1.DeleteUpstreamRequest]) (*connect.Response[federationv1.DeleteUpstreamResponse], error) {
	return nil, unimplemented("DeleteUpstream")
}

func (f *Federation) TriggerSync(ctx context.Context, req *connect.Request[federationv1.TriggerSyncRequest]) (*connect.Response[federationv1.TriggerSyncResponse], error) {
	return nil, unimplemented("TriggerSync")
}

func (f *Federation) ExportBundle(ctx context.Context, req *connect.Request[federationv1.ExportBundleRequest], stream *connect.ServerStream[federationv1.ExportBundleResponse]) error {
	return unimplemented("ExportBundle")
}

func (f *Federation) ImportBundle(ctx context.Context, stream *connect.ClientStream[federationv1.ImportBundleRequest]) (*connect.Response[federationv1.ImportBundleResponse], error) {
	return nil, unimplemented("ImportBundle")
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
