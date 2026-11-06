// Package ports defines the interface contracts for AgentHub's dependencies.
// These interfaces allow the core domain logic to be tested and swapped
// without depending on concrete implementations (databases, external services, etc.).
package ports

import (
	"go.pilab.hu/agenthub/internal/domain"
)

// Repository is the interface for asset metadata storage.
// Implementations can use PostgreSQL, in-memory stores, or other backends.
type Repository interface {
	// SaveAsset persists an asset version metadata.
	SaveAsset(*domain.Asset) error
	// GetAsset retrieves an asset by namespace and kind.
	GetAsset(namespace string, kind domain.AssetKind) (*domain.Asset, error)
	// ListAssets lists assets in a namespace.
	ListAssets(namespace string, kind domain.AssetKind) ([]*domain.Asset, error)
	// GetVersion retrieves a specific version of an asset.
	GetVersion(namespace string, kind domain.AssetKind, version string) (*domain.Asset, error)
	// DeleteVersion marks a version as yanked or deleted.
	DeleteVersion(namespace string, kind domain.AssetKind, version string) error
}

// Cache is the interface for content-addressed cache operations.
type Cache interface {
	// GetBlob retrieves a blob by its SHA-256 digest.
	GetBlob(digest string) ([]byte, error)
	// StoreBlob stores a blob and returns its digest.
	StoreBlob(data []byte) (string, error)
	// GetManifest retrieves a manifest JSON by its digest.
	GetManifest(digest string) (string, error)
	// StoreManifest stores a manifest and returns its digest.
	StoreManifest(many []byte) (string, error)
	// GetBundle retrieves a bundle tarball by its digest.
	GetBundle(digest string) ([]byte, error)
	// StoreBundle stores a bundle tarball and returns its digest.
	StoreBundle(data []byte) (string, error)
}

// SigningService is the interface for Ed25519 signature verification.
type SigningService interface {
	// VerifyManifestSignature verifies that a manifest was signed by an active namespace key.
	VerifyManifestSignature(manifest *domain.Manifest) error
	// SignManifest signs a manifest with the namespace key.
	SignManifest(manifest *domain.Manifest) (*domain.Signature, error)
	// GetPublicKey returns the public key for a namespace.
	GetPublicKey(namespace string) ([]byte, error)
}

// HubInfo holds information about an AgentHub deployment.
type HubInfo struct {
	Hub          string
	BaseURL      string
	ProtocolVersion int
	PublicKey    string
	SigningKeys  []domain.Signature
}

// InstallGuideRenderer renders install guides for different harnesses.
type InstallGuideRenderer interface {
	// Render renders an install guide for the given harness and scope.
	Render(asset *domain.Asset, harness, scope string) (string, error)
	// Validate validates the guide content.
	Validate(body string) error
}

// ModelLister lists available models for model policy resolution.
type ModelLister interface {
	// List returns the available models.
	List() ([]string, error)
}

// RateLimiter limits the rate of operations per principal.
type RateLimiter interface {
	// Allow checks if the operation is allowed based on the rate limit.
	Allow(principal string) bool
	// Record records that an operation occurred.
	Record(principal string)
}

// Telemetry is the interface for observability operations.
type Telemetry interface {
	// RecordEvent records a domain event for tracing.
	RecordEvent(event string, attributes map[string]string)
	// ObserveValue observes a numeric value with a label.
	ObserveValue(value float64, labels map[string]string)
}