package federation

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FederationFileName is the local upstream configuration file the CLI
// records federated hubs in (agenthub.federation.json, next to
// agenthub.json and agenthub.lock.json).
const FederationFileName = "agenthub.federation.json"

// Sync modes an upstream may be recorded with (federation.md §5).
const (
	ModeReference = "reference"
	ModeMirror    = "mirror"
	ModeProxy     = "proxy"
)

// Trust levels of a recorded upstream (federation.md §6.2).
const (
	// TrustTOFU marks a hub whose key was accepted on first use; the
	// fingerprint still has to be verified out of band.
	TrustTOFU = "tofu"
	// TrustPinned marks a hub whose key was pinned with --fingerprint.
	TrustPinned = "pinned"
)

// Upstream is one recorded federated hub in agenthub.federation.json.
type Upstream struct {
	// Name is the local alias (`federate add --name`, default: hub host).
	Name string `json:"name"`
	// HubID is the upstream's stable identity, the trust anchor.
	HubID string `json:"hub_id"`
	// BaseURL is the URL the upstream was added through.
	BaseURL string `json:"base_url"`
	// Mode is reference, mirror or proxy; M4.2 records it, a sync worker
	// that honours it lands later.
	Mode string `json:"mode"`
	// Trust is "tofu" or "pinned".
	Trust string `json:"trust"`
	// Fingerprint is the pinned hub key fingerprint ("sha256:…").
	Fingerprint string `json:"fingerprint"`
	// PublicKey is the hub's Ed25519 public key, base64 encoded, so a
	// later sync can verify without re-learning the key over the network.
	PublicKey string `json:"public_key"`
	// AddedAt is when the upstream was first recorded (RFC3339, UTC); it is
	// preserved when the same URL is recorded again.
	AddedAt string `json:"added_at"`
}

// Config is the top-level document written to agenthub.federation.json.
type Config struct {
	SchemaVersion int        `json:"schemaVersion"`
	Upstreams     []Upstream `json:"upstreams"`
}

// ReadConfig reads the upstream configuration at path. A missing or empty
// file yields an empty configuration; a schema version this build does not
// understand is an error rather than a silent downgrade.
func ReadConfig(path string) (Config, error) {
	var cfg Config
	b, err := os.ReadFile(path) //nolint:gosec // path is caller-controlled state file
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{SchemaVersion: 1, Upstreams: []Upstream{}}, nil
		}
		return cfg, fmt.Errorf("federation: read %q: %w", path, err)
	}
	if len(b) == 0 {
		return Config{SchemaVersion: 1, Upstreams: []Upstream{}}, nil
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("federation: parse %q: %w", path, err)
	}
	if cfg.SchemaVersion == 0 {
		cfg.SchemaVersion = 1
	}
	if cfg.SchemaVersion > 1 {
		return cfg, fmt.Errorf("federation: %q has schema version %d, this build speaks 1",
			path, cfg.SchemaVersion)
	}
	if cfg.Upstreams == nil {
		cfg.Upstreams = []Upstream{}
	}
	return cfg, nil
}

// WriteConfig writes the upstream configuration to path with indentation,
// atomically (temp file + rename), like the other CLI state files.
func WriteConfig(path string, cfg Config) error {
	cfg.SchemaVersion = 1
	if cfg.Upstreams == nil {
		cfg.Upstreams = []Upstream{}
	}
	return writeJSON(path, cfg)
}

// Upsert records u, replacing the upstream recorded under the same base URL
// (keeping its original added_at) and reporting whether it replaced one.
// Names are local aliases and must not collide with another upstream's.
func (c *Config) Upsert(u Upstream) (bool, error) {
	if err := u.validate(); err != nil {
		return false, err
	}
	u.BaseURL = strings.TrimRight(strings.TrimSpace(u.BaseURL), "/")
	for i, existing := range c.Upstreams {
		if !strings.EqualFold(strings.TrimRight(existing.BaseURL, "/"), u.BaseURL) {
			continue
		}
		if existing.AddedAt != "" {
			u.AddedAt = existing.AddedAt
		}
		c.Upstreams[i] = u
		return true, nil
	}
	for _, existing := range c.Upstreams {
		if existing.Name == u.Name {
			return false, fmt.Errorf("federation: upstream name %q already records %q",
				u.Name, existing.BaseURL)
		}
	}
	if c.Upstreams == nil {
		c.Upstreams = []Upstream{}
	}
	c.Upstreams = append(c.Upstreams, u)
	return false, nil
}

// validate rejects a record that could not have come from `federate add`.
func (u Upstream) validate() error {
	switch {
	case strings.TrimSpace(u.Name) == "":
		return errors.New("federation: upstream name is required")
	case strings.TrimSpace(u.HubID) == "":
		return errors.New("federation: upstream hub id is required")
	case strings.TrimSpace(u.BaseURL) == "":
		return errors.New("federation: upstream base url is required")
	}
	if _, err := ValidMode(u.Mode); err != nil {
		return err
	}
	switch u.Trust {
	case TrustTOFU, TrustPinned:
	default:
		return fmt.Errorf("federation: upstream trust %q is not %s or %s",
			u.Trust, TrustTOFU, TrustPinned)
	}
	if _, err := ParseFingerprint(u.Fingerprint); err != nil {
		return err
	}
	if strings.TrimSpace(u.PublicKey) != "" {
		if _, err := ParsePublicKey(u.PublicKey); err != nil {
			return err
		}
	}
	return nil
}

// ValidMode normalizes a sync mode and rejects unknown ones: a typo must
// not be recorded as an operational mode.
func ValidMode(mode string) (string, error) {
	switch normalized := strings.ToLower(strings.TrimSpace(mode)); normalized {
	case ModeReference, ModeMirror, ModeProxy:
		return normalized, nil
	default:
		return "", fmt.Errorf("federation: mode %q is not reference, mirror or proxy", mode)
	}
}

func writeJSON(path string, v any) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("federation: create %q: %w", dir, err)
		}
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".federation-*")
	if err != nil {
		return fmt.Errorf("federation: temp file for %q: %w", path, err)
	}
	tmp := f.Name()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("federation: encode %q: %w", path, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("federation: write %q: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("federation: replace %q: %w", path, err)
	}
	return nil
}

// EncodePublicKey renders an Ed25519 public key the way it is stored in
// agenthub.federation.json.
func EncodePublicKey(pub []byte) string {
	return base64.StdEncoding.EncodeToString(pub)
}
