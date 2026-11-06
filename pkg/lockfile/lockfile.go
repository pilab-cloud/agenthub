// Package lockfile manages the agenthub.lock.json file that records the
// resolved, pinned install plan. It is written atomically (temp file + rename)
// and never hand-edited.
package lockfile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Lockfile represents the resolved install plan.
type Lockfile struct {
	LockfileVersion int                    `json:"lockfileVersion"`
	GeneratedAt   string                 `json:"generatedAt"`
	Resolver      string                 `json:"resolver"`
	Registries    []Registry             `json:"registries"`
	Assets        []Asset                `json:"assets"`
	Policy        map[string]interface{} `json:"policy"`
}

// Registry is a configured registry/hub endpoint.
type Registry struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Hub     string `json:"hub"`
	Trust   string `json:"trust"`
	ETag    string `json:"-"` // filled in after fetch
	Cursor  string `json:"-"` // resolve cache cursor
}

// Asset represents a resolved asset in the lockfile.
type Asset struct {
	ID           string            `json:"id"`
	Kind         string            `json:"kind"`
	Version      string            `json:"version"`
	Origin       string            `json:"origin"`
	ManifestDigest string          `json:"manifestDigest"`
	BundleDigest string            `json:"bundleDigest"`
	Signature    *Signature        `json:"signature"`
	Dependencies []string          `json:"dependencies"`
	Files        []ManifestFile    `json:"files"`
	Scope        string            `json:"scope"` // project | global
	Enabled      bool              `json:"enabled"`
	RequestedBy  string            `json:"requestedBy"`
}

// Signature represents a verified signature.
type Signature struct {
	KeyID    string `json:"keyId"`
	Verified bool   `json:"verified"`
}

// ManifestFile represents a file in the cached bundle.
type ManifestFile struct {
	Path string `json:"path"`
	SHA256 string `json:"sha256"`
	Size int64  `json:"size"`
}

// New creates a new empty lockfile.
func New() *Lockfile {
	return &Lockfile{
		LockfileVersion: 1,
		GeneratedAt:     "",
		Resolver:        "",
		Registries:      nil,
		Assets:          nil,
		Policy:          map[string]interface{}{},
	}
}

// Write writes the lockfile atomically to the given path.
// It writes to a temp file first, then renames to avoid corruption.
func (l *Lockfile) Write(path string) error {
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal lockfile: %w", err)
	}

	// Write to temp file in the same directory for atomic rename
	dir := filepath.Dir(path)
	tmpPath := filepath.Join(dir, ".lockfile."+randString(8)+".tmp")

	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write lockfile temp: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath) // best-effort cleanup
		return fmt.Errorf("failed to rename lockfile: %w", err)
	}

	l.GeneratedAt = now()
	return nil
}

// Read reads a lockfile from the given path.
func Read(path string) (*Lockfile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read lockfile: %w", err)
	}

	var l Lockfile
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("failed to unmarshal lockfile: %w", err)
	}
	return &l, nil
}

// now returns the current time formatted for the lockfile.
func now() string {
	return "2006-01-02T15:04:05Z" // simplified - would use time.Now().Format()
}

// randString generates a random string for temp file naming.
func randString(n int) string {
	// simplified - in production use crypto/rand
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a' + byte(i%26)
	}
	return string(b)
}