// Package lockfile defines the on-disk desired and resolved state files used by
// the agenthub CLI (agenthub.json and agenthub.lock.json).
package lockfile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DesiredAsset is one entry of the CLI's desired state (agenthub.json).
type DesiredAsset struct {
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Version   string `json:"version"` // version constraint
	Agent     string `json:"agent"`   // target harness/agent (e.g., "opencode", "claude", "copilot")
}

// Identity returns the versionless identity of the desired asset as
// "namespace/kind/name"; the version constraint is not part of it.
func (a DesiredAsset) Identity() string {
	return a.Namespace + "/" + a.Kind + "/" + a.Name
}

// LockedAsset is one resolved, pinned entry of agenthub.lock.json.
type LockedAsset struct {
	Ref            string `json:"ref"`
	OriginHub      string `json:"origin_hub"`
	ManifestDigest string `json:"manifest_digest"`
	BundleDigest   string `json:"bundle_digest"`
}

// Identity returns the versionless identity of the locked asset, parsed from
// its ref: "pilab/agent/code-reviewer@1.4.2" becomes
// "pilab/agent/code-reviewer".
func (a LockedAsset) Identity() string {
	return assetIdentity(a.Ref)
}

// DesiredState is the top-level document written to agenthub.json.
type DesiredState struct {
	SchemaVersion int            `json:"schemaVersion"`
	Registries    []string       `json:"registries,omitempty"`
	Assets        []DesiredAsset `json:"assets"`
	Policy        map[string]any `json:"policy,omitempty"`
}

// LockedState is the top-level document written to agenthub.lock.json.
type LockedState struct {
	LockfileVersion int           `json:"lockfileVersion"`
	GeneratedAt     string        `json:"generatedAt"`
	Resolver        string        `json:"resolver"`
	Assets          []LockedAsset `json:"assets"`
}

const (
	DesiredFileName = "agenthub.json"
	LockFileName    = "agenthub.lock.json"
)

// LoadDesired reads agenthub.json from path. Missing files yield an empty state.
func LoadDesired(path string) (DesiredState, error) {
	return ReadDesired(path)
}

// ReadDesired reads agenthub.json from path.
func ReadDesired(path string) (DesiredState, error) {
	var s DesiredState
	b, err := os.ReadFile(path) //nolint:gosec // path is caller-controlled state file
	if err != nil {
		if os.IsNotExist(err) {
			return DesiredState{SchemaVersion: 1, Assets: []DesiredAsset{}}, nil
		}
		return s, err
	}
	if len(b) == 0 {
		return DesiredState{SchemaVersion: 1, Assets: []DesiredAsset{}}, nil
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("read desired state %q: %w", path, err)
	}
	if s.SchemaVersion == 0 {
		s.SchemaVersion = 1
	}
	if s.Assets == nil {
		s.Assets = []DesiredAsset{}
	}
	return s, nil
}

// SaveDesired writes agenthub.json to path with indentation.
func SaveDesired(path string, s DesiredState) error {
	return WriteDesired(path, s)
}

// WriteDesired writes agenthub.json to path with indentation.
func WriteDesired(path string, s DesiredState) error {
	if s.SchemaVersion == 0 {
		s.SchemaVersion = 1
	}
	if s.Assets == nil {
		s.Assets = []DesiredAsset{}
	}
	return writeJSON(path, s)
}

// LoadLocked reads agenthub.lock.json from path.
func LoadLocked(path string) (LockedState, error) {
	return ReadLocked(path)
}

// ReadLocked reads agenthub.lock.json from path.
func ReadLocked(path string) (LockedState, error) {
	var s LockedState
	b, err := os.ReadFile(path) //nolint:gosec // path is caller-controlled state file
	if err != nil {
		if os.IsNotExist(err) {
			return LockedState{LockfileVersion: 1, Assets: []LockedAsset{}}, nil
		}
		return s, err
	}
	if len(b) == 0 {
		return LockedState{LockfileVersion: 1, Assets: []LockedAsset{}}, nil
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("read lockfile %q: %w", path, err)
	}
	if s.LockfileVersion == 0 {
		s.LockfileVersion = 1
	}
	if s.Assets == nil {
		s.Assets = []LockedAsset{}
	}
	return s, nil
}

// SaveLocked writes agenthub.lock.json to path with indentation.
func SaveLocked(path string, s LockedState) error {
	return WriteLocked(path, s)
}

// WriteLocked writes agenthub.lock.json to path with indentation.
func WriteLocked(path string, s LockedState) error {
	if s.LockfileVersion == 0 {
		s.LockfileVersion = 1
	}
	if s.Assets == nil {
		s.Assets = []LockedAsset{}
	}
	return writeJSON(path, s)
}

// ReadLockfile is an alias for ReadLocked.
func ReadLockfile(path string) (LockedState, error) { return ReadLocked(path) }

// WriteLockfile is an alias for WriteLocked.
func WriteLockfile(path string, s LockedState) error { return WriteLocked(path, s) }

// PruneLocked returns the locked state without the assets that are no longer
// desired. Surviving pins keep their order and metadata (origin hub, digests,
// generatedAt, resolver).
func PruneLocked(desired DesiredState, locked LockedState) LockedState {
	want := make(map[string]struct{}, len(desired.Assets))
	for _, a := range desired.Assets {
		want[a.Identity()] = struct{}{}
	}
	kept := make([]LockedAsset, 0, len(locked.Assets))
	for _, a := range locked.Assets {
		if _, ok := want[a.Identity()]; ok {
			kept = append(kept, a)
		}
	}
	locked.Assets = kept
	return locked
}

// RemoveAsset removes the desired asset matching ref from desired and
// recomputes locked by pruning the assets that are no longer desired. ref is
// "namespace/kind/name" or "namespace/kind/name@version"; the version (or
// version constraint) is ignored while matching. It reports whether a desired
// asset was removed; a ref without three slash-separated parts is an error.
func RemoveAsset(desired DesiredState, locked LockedState, ref string) (DesiredState, LockedState, bool, error) {
	id := assetIdentity(ref)
	parts := strings.Split(id, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return desired, locked, false, fmt.Errorf("asset ref must be namespace/kind/name")
	}
	kept := make([]DesiredAsset, 0, len(desired.Assets))
	found := false
	for _, a := range desired.Assets {
		if a.Identity() == id {
			found = true
			continue
		}
		kept = append(kept, a)
	}
	if !found {
		return desired, locked, false, nil
	}
	desired.Assets = kept
	return desired, PruneLocked(desired, locked), true, nil
}

// assetIdentity strips the "@version" suffix from an asset ref.
func assetIdentity(ref string) string {
	if i := strings.IndexByte(ref, '@'); i >= 0 {
		return ref[:i]
	}
	return ref
}

func writeJSON(path string, v any) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".lockfile-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
