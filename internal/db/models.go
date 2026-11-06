// Package db defines bun-backed persistence models for AgentHub.
package db

import (
	"encoding/json"
	"time"

	"github.com/uptrace/bun"
)

// Namespace is the bun model for agenthub.namespaces.
type Namespace struct {
	bun.BaseModel `bun:"table:agenthub.namespaces"`

	ID          string    `bun:"id,pk"`
	HubID       string    `bun:"hub_id,notnull"`
	Slug        string    `bun:"slug,notnull"`
	DisplayName string    `bun:"display_name,notnull"`
	Description string    `bun:"description"`
	Visibility  string    `bun:"visibility,notnull"`
	CreatedAt   time.Time `bun:"created_at,notnull,default:now()"`
}

// Asset is the bun model for agenthub.assets.
type Asset struct {
	bun.BaseModel `bun:"table:agenthub.assets"`

	ID             string    `bun:"id,pk"`
	HubID          string    `bun:"hub_id,notnull"`
	NamespaceID    string    `bun:"namespace_id,notnull"`
	Kind           string    `bun:"kind,notnull"`
	Name           string    `bun:"name,notnull"`
	DisplayName    string    `bun:"display_name"`
	Summary        string    `bun:"summary,notnull"`
	Description    string    `bun:"description"`
	License        string    `bun:"license"`
	Homepage       string    `bun:"homepage"`
	Repository     string    `bun:"repository"`
	IconDigest     string    `bun:"icon_digest"`
	Tags           []string  `bun:"tags,array"`
	Categories     []string  `bun:"categories,array"`
	LatestVersion  string    `bun:"latest_version"`
	LandingVisible bool      `bun:"landing_visible,notnull,default:true"`
	CreatedAt      time.Time `bun:"created_at,notnull,default:now()"`
	UpdatedAt      time.Time `bun:"updated_at,notnull,default:now()"`
}

// AssetVersion is the bun model for agenthub.asset_versions.
type AssetVersion struct {
	bun.BaseModel `bun:"table:agenthub.asset_versions"`

	ID              string          `bun:"id,pk"`
	AssetID         string          `bun:"asset_id,notnull"`
	Version         string          `bun:"version,notnull"`
	Status          string          `bun:"status,notnull,default:draft"`
	Manifest        json.RawMessage `bun:"manifest,type:jsonb,notnull"`
	ManifestDigest  string          `bun:"manifest_digest,notnull"`
	BundleDigest    string          `bun:"bundle_digest,notnull"`
	OriginHubID     string          `bun:"origin_hub_id"`
	OriginVersionID string          `bun:"origin_version_id,nullzero"`
	PublishedBy     string          `bun:"published_by"`
	PublishedAt     *time.Time      `bun:"published_at"`
	YankedAt        *time.Time      `bun:"yanked_at"`
	DeprecationNote string          `bun:"deprecation_note"`
	Readme          string          `bun:"readme"`
	Changelog       string          `bun:"changelog"`
}

// NamespaceRole is the bun model for agenthub.namespace_roles (M3.6).
type NamespaceRole struct {
	bun.BaseModel `bun:"table:agenthub.namespace_roles"`

	HubID     string    `bun:"hub_id,pk"`
	Namespace string    `bun:"namespace,pk"`
	Subject   string    `bun:"subject,pk"`
	Role      string    `bun:"role,notnull"`
	GrantedAt time.Time `bun:"granted_at,notnull,default:now()"`
	UpdatedAt time.Time `bun:"updated_at,notnull,default:now()"`
}

// AuditLog is the bun model for agenthub.audit_log.
type AuditLog struct {
	bun.BaseModel `bun:"table:agenthub.audit_log"`

	ID         int64           `bun:"id,pk,nullzero"`
	At         time.Time       `bun:"at,notnull,default:now()"`
	ActorSub   string          `bun:"actor_sub"`
	ActorEmail string          `bun:"actor_email"`
	Action     string          `bun:"action,notnull"`
	Namespace  string          `bun:"namespace"`
	Asset      string          `bun:"asset"`
	Version    string          `bun:"version"`
	Detail     json.RawMessage `bun:"detail,type:jsonb"`
	IP         *string         `bun:"ip,type:inet"`
}
