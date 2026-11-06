// Package manifest provides manifest parsing, validation, and canonicalization
// for AgentHub asset bundles and published manifests.
package manifest

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"go.pilab.hu/agenthub/internal/domain"
)

// Manifest represents the canonical signed manifest of a published version.
type Manifest struct {
	SchemaVersion int                    `json:"schemaVersion"`
	ID            string                 `json:"id"`
	Kind          domain.AssetKind       `json:"kind"`
	Name          string                 `json:"name"`
	Namespace     string                 `json:"namespace"`
	Version       string                 `json:"version"`
	DisplayName   string                 `json:"displayName"`
	Summary       string                 `json:"summary"`
	Description   string                 `json:"description"`
	License       string                 `json:"license"`
	Homepage      string                 `json:"homepage"`
	Repository    string                 `json:"repository"`
	Icon          string                 `json:"icon"`
	Tags          []string               `json:"tags"`
	Categories    []string               `json:"categories"`
	Compatibility domain.Compatibility   `json:"compatibility"`
	Permissions   []domain.PermissionRule `json:"permissions"`
	Spec          domain.Spec            `json:"spec"`
	Files         []domain.ManifestFile  `json:"files"`
	Dependencies  []domain.Dependency    `json:"dependencies"`
	Guides        map[string]domain.Guide `json:"guides"`
	Publisher     domain.Publisher       `json:"publisher"`
	ManifestDigest string                `json:"manifestDigest"` // sha256 of JCS-reduced JSON
	BundleDigest  string                 `json:"bundleDigest"`   // sha256 of tar.zst payload
	Signatures    []domain.Signature     `json:"signatures"`
}

// ManifestFile represents a single file in the bundle with its hash.
type ManifestFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Mode   int    `json:"mode"`
}

// Signature represents an Ed25519 signature over the manifest.
type Signature struct {
	KeyID    string `json:"keyId"`
	Algorithm string `json:"algorithm"`
	Value    string `json:"value"` // base64-encoded
	SignedAt string `json:"signedAt"`
}

// Publisher represents the publishing namespace key info.
type Publisher struct {
	Namespace string `json:"namespace"`
	KeyID     string `json:"keyId"`
}

// Canonicalize returns the manifest serialized with RFC 8785 JCS,
// removing signatures and manifestDigest before hashing.
func (m *Manifest) Canonicalize() (string, error) {
	// Build a copy without transient fields
	cpy := *m
	cpy.ManifestDigest = ""
	cpy.Signatures = nil

	// Sort slices for deterministic ordering
	sortStrings(cpy.Tags)
	sortStrings(cpy.Categories)
	sortManifestFiles(cpy.Files)
	sortManifestDependencies(cpy.Dependencies)

	data, err := json.Marshal(cpy)
	if err != nil {
		return "", fmt.Errorf("failed to marshal manifest: %w", err)
	}
	return string(data), nil
}

// sortStrings sorts a string slice in place.
func sortStrings(s []string) {
	for i := 0; i < len(s); i++ {
		for j := i + 1; j < len(s); j++ {
			if s[i] > s[j] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
}

// sortManifestFiles sorts ManifestFile slices by path.
func sortManifestFiles(files []domain.ManifestFile) {
	for i := 0; i < len(files); i++ {
		for j := i + 1; j < len(files); j++ {
			if files[i].Path > files[j].Path {
				files[i], files[j] = files[j], files[i]
			}
		}
	}
}

// sortManifestDependencies sorts Dependency slices by ID.
func sortManifestDependencies(deps []domain.Dependency) {
	for i := 0; i < len(deps); i++ {
		for j := i + 1; j < len(deps); j++ {
			if deps[i].ID > deps[j].ID {
				deps[i], deps[j] = deps[j], deps[i]
			}
		}
	}
}

// Digest computes the SHA-256 digest of the canonicalized manifest.
func (m *Manifest) Digest() (string, error) {
	canonical, err := m.Canonicalize()
	if err != nil {
		return "", err
	}
	h := sha256.Sum256([]byte(canonical))
	return fmt.Sprintf("sha256:%x", h), nil
}

// ValidateSchema checks that the manifest has all required fields.
func (m *Manifest) ValidateSchema() error {
	if m.SchemaVersion != 1 {
		return fmt.Errorf("unknown schema version: %d", m.SchemaVersion)
	}
	if m.Kind == "" {
		return fmt.Errorf("kind is required")
	}
	if m.Name == "" {
		return fmt.Errorf("name is required")
	}
	if m.Namespace == "" {
		return fmt.Errorf("namespace is required")
	}
	if m.Version == "" {
		return fmt.Errorf("version is required")
	}
	if m.Summary == "" {
		return fmt.Errorf("summary is required")
	}
	if m.License == "" {
		return fmt.Errorf("license is required for public assets")
	}
	if m.Compatibility.Harnesses == nil || len(m.Compatibility.Harnesses) == 0 {
		return fmt.Errorf("at least one harness is required in compatibility")
	}
	return nil
}

// AssetRef returns a canonical asset reference string.
func (m *Manifest) AssetRef() string {
	return fmt.Sprintf("agenthub://%s/%s/%s@%s", m.Kind, m.Namespace, m.Name, m.Version)
}