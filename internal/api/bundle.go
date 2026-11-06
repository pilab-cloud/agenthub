package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"connectrpc.com/connect"
	"github.com/Masterminds/semver/v3"

	catalogv1 "go.pilab.hu/agenthub/gen/go/agenthub/catalog/v1"
	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"
	registryv1 "go.pilab.hu/agenthub/gen/go/agenthub/registry/v1"
	"go.pilab.hu/agenthub/internal/catalog"
)

// BlobPath is the URL path blobs are served and uploaded under.
const BlobPath = "/blobs/"

func blobURL(publicURL, sha string) string {
	return strings.TrimSuffix(publicURL, "/") + BlobPath + sha
}

// WithBlobs makes BeginPublish hand out upload URLs under publicURL.
func (r *Registry) WithBlobs(blobs catalog.BlobStore, publicURL string) *Registry {
	r.blobs, r.publicURL = blobs, publicURL
	return r
}

// WithBlobs makes GetBundle serve file blobs from publicURL.
func (c *Catalog) WithBlobs(blobs catalog.BlobStore, publicURL string) *Catalog {
	c.blobs, c.publicURL = blobs, publicURL
	return c
}

func (r *Registry) uploadTargets(files []*commonv1.FileDescriptor) []*registryv1.UploadTarget {
	targets := []*registryv1.UploadTarget{}
	if r.blobs == nil {
		return targets
	}
	for _, f := range files {
		sha := strings.TrimPrefix(strings.ToLower(f.GetSha256()), "sha256:")
		if sha == "" {
			continue
		}
		if _, have := r.blobs.Get(&commonv1.Digest{Algorithm: "sha256", Value: sha}); have {
			continue
		}
		targets = append(targets, &registryv1.UploadTarget{Path: f.GetPath(), Url: blobURL(r.publicURL, sha)})
	}
	return targets
}

// manifestFileEntry is the part of a manifest file entry GetBundle needs.
type manifestFileEntry struct {
	Path   string `json:"path"`
	Sha256 string `json:"sha256"`
	Size   uint64 `json:"size"`
}

func (c *Catalog) findVersion(m *catalogv1.GetBundleRequest) (*catalog.Version, bool) {
	if ref := m.GetRef(); ref != nil {
		return c.store.GetVersion(ref.GetNamespace(), ref.GetKind(), ref.GetName(), ref.GetVersion())
	}
	want := m.GetManifestDigest()
	if want == nil {
		return nil, false
	}
	for _, a := range c.store.ListAssets() {
		for _, v := range c.store.ListVersions(a.Namespace, a.Kind, a.Name) {
			if digestEqual(v.ManifestDigest, want) {
				return v, true
			}
		}
	}
	return nil, false
}

// GetBundle returns a version's manifest and download URLs for its files.
func (c *Catalog) GetBundle(_ context.Context, req *connect.Request[catalogv1.GetBundleRequest]) (*connect.Response[catalogv1.GetBundleResponse], error) {
	v, ok := c.findVersion(req.Msg)
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("bundle not found"))
	}
	var doc struct {
		Files []manifestFileEntry `json:"files"`
	}
	_ = json.Unmarshal(v.Manifest, &doc)

	resp := &catalogv1.GetBundleResponse{
		Manifest:       append([]byte(nil), v.Manifest...),
		ManifestDigest: cloneDigest(v.ManifestDigest),
		BundleDigest:   cloneDigest(v.BundleDigest),
	}
	for _, f := range doc.Files {
		sha := strings.TrimPrefix(strings.ToLower(f.Sha256), "sha256:")
		if sha == "" {
			continue
		}
		resp.Files = append(resp.Files, &catalogv1.BlobLocation{
			Path:   f.Path,
			Digest: &commonv1.Digest{Algorithm: "sha256", Value: sha},
			Url:    blobURL(c.publicURL, sha),
			Size:   f.Size,
		})
	}
	return connect.NewResponse(resp), nil
}

// pickVersion resolves a requested version to a published version of a. An
// exact version wins; an empty request means the latest; anything else is a
// semver constraint ("*", "^2", ">=1.2 <2") and picks the highest match.
// Yanked versions are never picked by a constraint.
func (c *Catalog) pickVersion(a *catalog.Asset, want string) (*catalog.Version, bool) {
	ns, kind, name := a.Namespace, a.Kind, a.Name
	want = strings.TrimSpace(want)
	if want == "" {
		return c.store.GetVersion(ns, kind, name, a.LatestVer)
	}
	if v, ok := c.store.GetVersion(ns, kind, name, want); ok {
		return v, true
	}
	cons, err := semver.NewConstraint(want)
	if err != nil {
		return nil, false
	}
	var best *catalog.Version
	var bestSV *semver.Version
	for _, v := range c.store.ListVersions(ns, kind, name) {
		if v.Status == commonv1.VersionStatus_VERSION_STATUS_YANKED {
			continue
		}
		sv, err := semver.NewVersion(v.Version)
		if err != nil || !cons.Check(sv) {
			continue
		}
		if bestSV == nil || sv.GreaterThan(bestSV) {
			best, bestSV = v, sv
		}
	}
	return best, best != nil
}
