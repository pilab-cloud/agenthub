// Package catalog provides a Postgres-backed Store implementation.
package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/uptrace/bun"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"
	"go.pilab.hu/agenthub/internal/db"
	"go.pilab.hu/agenthub/internal/registry"
)

// PostgresCatalogStore implements Store using bun.
type PostgresCatalogStore struct {
	db    *bun.DB
	hubID string
}

// NewPostgresCatalogStore constructs a Postgres-backed catalog store.
func NewPostgresCatalogStore(db *bun.DB, hubID string) *PostgresCatalogStore {
	if hubID == "" {
		hubID = "local"
	}
	return &PostgresCatalogStore{db: db, hubID: hubID}
}

func (s *PostgresCatalogStore) namespaceID(ctx context.Context, slug string) (string, error) {
	slug = strings.TrimSpace(slug)
	var id string
	err := s.db.NewSelect().
		TableExpr("agenthub.namespaces").
		Column("id").
		Where("hub_id = ? AND slug = ?", s.hubID, slug).
		Scan(ctx, &id)
	if err != nil {
		return "", err
	}
	return id, nil
}

func (s *PostgresCatalogStore) UpsertAsset(a *Asset) error {
	if a == nil || a.Namespace == "" || a.Name == "" {
		return errors.New("invalid asset")
	}
	ctx := context.Background()
	nsID, err := s.namespaceID(ctx, a.Namespace)
	if err != nil {
		return err
	}
	// find existing id
	type idRow struct {
		ID string `bun:"id"`
	}
	var er idRow
	_ = s.db.NewSelect().TableExpr("agenthub.assets").Column("id").
		Where("namespace_id = ? AND kind = ? AND name = ?", nsID, a.Kind.String(), a.Name).
		Scan(ctx, &er)
	id := er.ID
	if id == "" {
		id = newUUIDFromRegistry()
	}

	now := time.Now().UTC()
	if a.CreatedAt == nil {
		a.CreatedAt = timestamppb.New(now)
	}
	if a.UpdatedAt == nil {
		a.UpdatedAt = timestamppb.New(now)
	}

	model := db.Asset{
		ID:             id,
		HubID:          s.hubID,
		NamespaceID:    nsID,
		Kind:           a.Kind.String(),
		Name:           a.Name,
		DisplayName:    a.DisplayName,
		Summary:        a.Summary,
		Description:    "", // not in catalog.Asset
		License:        a.License,
		Repository:     a.Repository,
		IconDigest:     a.IconDigest,
		Tags:           append([]string(nil), a.Tags...),
		Categories:     append([]string(nil), a.Categories...),
		LatestVersion:  a.LatestVer,
		LandingVisible: a.LandingVisible,
		CreatedAt:      a.CreatedAt.AsTime(),
		UpdatedAt:      a.UpdatedAt.AsTime(),
	}
	if model.Tags == nil {
		model.Tags = []string{}
	}
	if model.Categories == nil {
		model.Categories = []string{}
	}

	_, err = s.db.NewInsert().Model(&model).
		On("CONFLICT (namespace_id, kind, name) DO UPDATE").
		Set("display_name = EXCLUDED.display_name").
		Set("summary = EXCLUDED.summary").
		Set("icon_digest = EXCLUDED.icon_digest").
		Set("license = EXCLUDED.license").
		Set("repository = EXCLUDED.repository").
		Set("tags = EXCLUDED.tags").
		Set("categories = EXCLUDED.categories").
		Set("latest_version = EXCLUDED.latest_version").
		Set("landing_visible = EXCLUDED.landing_visible").
		Set("updated_at = EXCLUDED.updated_at").
		Exec(ctx)
	return err
}

func (s *PostgresCatalogStore) GetAsset(ns string, kind commonv1.AssetKind, name string) (*Asset, bool) {
	ctx := context.Background()
	nsID, err := s.namespaceID(ctx, ns)
	if err != nil {
		return nil, false
	}
	type row struct {
		db.Asset `bun:",embed"`
		Slug     string `bun:"slug"`
	}
	var r row
	err = s.db.NewSelect().
		TableExpr("agenthub.assets AS a").
		Join("JOIN agenthub.namespaces AS n ON n.id = a.namespace_id").
		ColumnExpr("a.*").
		Where("a.namespace_id = ? AND a.kind = ? AND a.name = ?", nsID, kind.String(), name).
		Scan(ctx, &r)
	if err != nil {
		return nil, false
	}
	return assetToCatalog(r.Asset, ns), true
}

func (s *PostgresCatalogStore) ListAssets() []*Asset {
	ctx := context.Background()
	type row struct {
		db.Asset `bun:",embed"`
		Slug     string `bun:"slug"`
	}
	var rows []row
	err := s.db.NewSelect().
		TableExpr("agenthub.assets AS a").
		Join("JOIN agenthub.namespaces AS n ON n.id = a.namespace_id").
		ColumnExpr("a.*").
		ColumnExpr("n.slug").
		Where("n.hub_id = ?", s.hubID).
		Order("n.slug ASC").
		Order("a.name ASC").
		Scan(ctx, &rows)
	if err != nil {
		return nil
	}
	out := make([]*Asset, 0, len(rows))
	for _, r := range rows {
		out = append(out, assetToCatalog(r.Asset, r.Slug))
	}
	return out
}

func assetToCatalog(m db.Asset, nsSlug string) *Asset {
	kind := commonv1.AssetKind_ASSET_KIND_UNSPECIFIED
	if v, ok := commonv1.AssetKind_value[m.Kind]; ok {
		kind = commonv1.AssetKind(v)
	}
	a := &Asset{
		Namespace:      nsSlug,
		Kind:           kind,
		Name:           m.Name,
		DisplayName:    m.DisplayName,
		Summary:        m.Summary,
		IconDigest:     m.IconDigest,
		Tags:           append([]string(nil), m.Tags...),
		Categories:     append([]string(nil), m.Categories...),
		License:        m.License,
		Repository:     m.Repository,
		LatestVer:      m.LatestVersion,
		LandingVisible: m.LandingVisible,
	}
	if !m.CreatedAt.IsZero() {
		a.CreatedAt = timestamppb.New(m.CreatedAt)
	}
	if !m.UpdatedAt.IsZero() {
		a.UpdatedAt = timestamppb.New(m.UpdatedAt)
	}
	return a
}

func (s *PostgresCatalogStore) UpsertVersion(v *Version) error {
	if v == nil || v.Namespace == "" || v.Name == "" || v.Version == "" {
		return errors.New("invalid version")
	}
	ctx := context.Background()
	nsID, err := s.namespaceID(ctx, v.Namespace)
	if err != nil {
		return err
	}
	// resolve asset id
	type idRow struct {
		ID string `bun:"id"`
	}
	var ar idRow
	err = s.db.NewSelect().TableExpr("agenthub.assets").Column("id").
		Where("namespace_id = ? AND kind = ? AND name = ?", nsID, v.Kind.String(), v.Name).
		Scan(ctx, &ar)
	assetID := ar.ID

	if err != nil {
		return err
	}
	if assetID == "" {
		return errors.New("asset not found")
	}

	var existingID string
	_ = s.db.NewSelect().TableExpr("agenthub.asset_versions").Column("id").
		Where("asset_id = ? AND version = ?", assetID, v.Version).
		Scan(ctx, &existingID)
	id := existingID
	if id == "" {
		id = newUUIDFromRegistry()
	}

	manifest := v.Manifest
	if len(manifest) == 0 {
		manifest = []byte("{}")
	}
	md := ""
	if v.ManifestDigest != nil && v.ManifestDigest.Value != "" {
		md = v.ManifestDigest.Algorithm + ":" + v.ManifestDigest.Value
	}
	if md == "" {
		h := sha256.Sum256(manifest)
		md = "sha256:" + hex.EncodeToString(h[:])
	}
	bd := ""
	if v.BundleDigest != nil {
		bd = v.BundleDigest.Algorithm + ":" + v.BundleDigest.Value
	}
	status := v.Status.String()
	if status == "VERSION_STATUS_UNSPECIFIED" || status == "" {
		status = "VERSION_STATUS_PUBLISHED"
	}
	// normalize to db status text? Schema uses 'draft'/'published' maybe. In-memory uses proto enum. Let's store proto enum string for roundtrip.
	var publishedAt *time.Time
	if v.PublishedAt != nil {
		t := v.PublishedAt.AsTime()
		publishedAt = &t
	}

	model := db.AssetVersion{
		ID:             id,
		AssetID:        assetID,
		Version:        v.Version,
		Status:         status,
		Manifest:       json.RawMessage(manifest),
		ManifestDigest: md,
		BundleDigest:   bd,
		Readme:         v.Readme,
		Changelog:      v.Changelog,
		PublishedAt:    publishedAt,
	}
	if model.ManifestDigest == "" {
		model.ManifestDigest = ""
	}

	_, err = s.db.NewInsert().Model(&model).
		On("CONFLICT (asset_id, version) DO UPDATE").
		Set("status = EXCLUDED.status").
		Set("manifest = EXCLUDED.manifest").
		Set("manifest_digest = EXCLUDED.manifest_digest").
		Set("bundle_digest = EXCLUDED.bundle_digest").
		Set("readme = EXCLUDED.readme").
		Set("changelog = EXCLUDED.changelog").
		Set("published_at = EXCLUDED.published_at").
		Exec(ctx)
	return err
}

func (s *PostgresCatalogStore) ListVersions(ns string, kind commonv1.AssetKind, name string) []*Version {
	ctx := context.Background()
	nsID, err := s.namespaceID(ctx, ns)
	if err != nil {
		return nil
	}
	type idRow2 struct {
		ID string `bun:"id"`
	}
	var ar2 idRow2
	err = s.db.NewSelect().TableExpr("agenthub.assets").Column("id").
		Where("namespace_id = ? AND kind = ? AND name = ?", nsID, kind.String(), name).
		Scan(ctx, &ar2)
	assetID := ar2.ID
	if err != nil || assetID == "" {
		return nil
	}
	var models []db.AssetVersion
	err = s.db.NewSelect().Model(&models).
		Where("asset_id = ?", assetID).
		Order("published_at DESC").
		Scan(ctx)
	if err != nil {
		return nil
	}
	out := make([]*Version, 0, len(models))
	for _, m := range models {
		out = append(out, versionToCatalog(m, ns, kind, name))
	}
	return out
}

func (s *PostgresCatalogStore) GetVersion(ns string, kind commonv1.AssetKind, name, version string) (*Version, bool) {
	ctx := context.Background()
	nsID, err := s.namespaceID(ctx, ns)
	if err != nil {
		return nil, false
	}
	type idRow3 struct {
		ID string `bun:"id"`
	}
	var ar3 idRow3
	err = s.db.NewSelect().TableExpr("agenthub.assets").Column("id").
		Where("namespace_id = ? AND kind = ? AND name = ?", nsID, kind.String(), name).
		Scan(ctx, &ar3)
	assetID := ar3.ID
	if err != nil || assetID == "" {
		return nil, false
	}
	var m db.AssetVersion
	err = s.db.NewSelect().Model(&m).
		Where("asset_id = ? AND version = ?", assetID, version).
		Scan(ctx)
	if err != nil {
		return nil, false
	}
	return versionToCatalog(m, ns, kind, name), true
}

func versionToCatalog(m db.AssetVersion, ns string, kind commonv1.AssetKind, name string) *Version {
	v := &Version{
		Namespace:  ns,
		Kind:       kind,
		Name:       name,
		Version:    m.Version,
		Manifest:   append([]byte(nil), m.Manifest...),
		BundleSize: 0,
		Readme:     m.Readme,
		Changelog:  m.Changelog,
	}
	// status parse
	if sv, ok := commonv1.VersionStatus_value[m.Status]; ok {
		v.Status = commonv1.VersionStatus(sv)
	} else {
		v.Status = commonv1.VersionStatus_VERSION_STATUS_PUBLISHED
	}
	// digest parse
	v.ManifestDigest = parseDigest(m.ManifestDigest)
	v.BundleDigest = parseDigest(m.BundleDigest)
	if m.PublishedAt != nil {
		v.PublishedAt = timestamppb.New(*m.PublishedAt)
	}
	return v
}

func parseDigest(s string) *commonv1.Digest {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if i := strings.Index(s, ":"); i >= 0 {
		val := s[i+1:]
		if val == "" {
			return nil
		}
		return &commonv1.Digest{Algorithm: s[:i], Value: val}
	}
	return &commonv1.Digest{Algorithm: "sha256", Value: s}
}

func newUUIDFromRegistry() string {
	return registry.NewUUID()
}
