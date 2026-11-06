package lockfile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLockfileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	desiredPath := filepath.Join(dir, DesiredFileName)
	lockedPath := filepath.Join(dir, LockFileName)

	d := DesiredState{
		SchemaVersion: 1,
		Assets: []DesiredAsset{
			{Namespace: "pilab", Kind: "agent", Name: "code-reviewer", Version: "^1.4"},
		},
	}
	if err := WriteDesired(desiredPath, d); err != nil {
		t.Fatal(err)
	}
	gotD, err := ReadDesired(desiredPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotD.Assets) != 1 || gotD.Assets[0] != d.Assets[0] {
		t.Fatalf("desired mismatch: %+v", gotD)
	}

	l := LockedState{
		LockfileVersion: 1,
		GeneratedAt:     "2026-11-04T10:00:00Z",
		Resolver:        "agenthub/0.1.0",
		Assets: []LockedAsset{
			{Ref: "pilab/agent/code-reviewer@1.4.0", OriginHub: "agenthub.pilab.hu", ManifestDigest: "sha256:abc", BundleDigest: "sha256:def"},
		},
	}
	if err := WriteLocked(lockedPath, l); err != nil {
		t.Fatal(err)
	}
	gotL, err := ReadLocked(lockedPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotL.Assets) != 1 || gotL.Assets[0] != l.Assets[0] {
		t.Fatalf("locked mismatch: %+v", gotL)
	}

	// JSON uses indent.
	b, _ := os.ReadFile(lockedPath)
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["assets"]; !ok {
		t.Fatal("missing assets key")
	}
}

func TestMissingFilesEmpty(t *testing.T) {
	dir := t.TempDir()
	d, err := ReadDesired(filepath.Join(dir, "nope.json"))
	if err != nil || len(d.Assets) != 0 {
		t.Fatalf("err=%v assets=%d", err, len(d.Assets))
	}
	l, err := ReadLocked(filepath.Join(dir, "nope.lock.json"))
	if err != nil || len(l.Assets) != 0 {
		t.Fatalf("err=%v assets=%d", err, len(l.Assets))
	}
}

func TestAssetIdentity(t *testing.T) {
	d := DesiredAsset{Namespace: "pilab", Kind: "agent", Name: "code-reviewer", Version: "^1.4"}
	if got, want := d.Identity(), "pilab/agent/code-reviewer"; got != want {
		t.Fatalf("desired identity = %q, want %q", got, want)
	}
	l := LockedAsset{Ref: "pilab/agent/code-reviewer@1.4.2"}
	if got, want := l.Identity(), "pilab/agent/code-reviewer"; got != want {
		t.Fatalf("locked identity = %q, want %q", got, want)
	}
	if got, want := (LockedAsset{Ref: "pilab/tool/codegraph"}).Identity(), "pilab/tool/codegraph"; got != want {
		t.Fatalf("unversioned locked identity = %q, want %q", got, want)
	}
}

func TestPruneLocked(t *testing.T) {
	desired := DesiredState{SchemaVersion: 1, Assets: []DesiredAsset{
		{Namespace: "pilab", Kind: "agent", Name: "code-reviewer", Version: "^1.4"},
	}}
	locked := LockedState{
		LockfileVersion: 1,
		GeneratedAt:     "2026-11-04T10:00:00Z",
		Resolver:        "agenthub/0.1.0",
		Assets: []LockedAsset{
			{Ref: "pilab/agent/code-reviewer@1.4.2", OriginHub: "agenthub.pilab.hu", ManifestDigest: "sha256:abc"},
			{Ref: "pilab/tool/codegraph@1.0.3", OriginHub: "agenthub.pilab.hu"},
			{Ref: "pilab/skill/gone@2.0.0", OriginHub: "other.hub"},
		},
	}

	got := PruneLocked(desired, locked)
	if len(got.Assets) != 1 {
		t.Fatalf("assets = %d, want 1: %+v", len(got.Assets), got.Assets)
	}
	if got.Assets[0] != locked.Assets[0] {
		t.Fatalf("surviving pin changed: %+v", got.Assets[0])
	}
	if got.GeneratedAt != locked.GeneratedAt || got.Resolver != locked.Resolver {
		t.Fatalf("metadata not preserved: %+v", got)
	}
	// The input must stay untouched: PruneLocked works on a copy of the slices.
	if len(locked.Assets) != 3 {
		t.Fatalf("input lock mutated: %+v", locked.Assets)
	}

	// Nothing desired means an empty, non-nil asset list.
	got = PruneLocked(DesiredState{SchemaVersion: 1}, locked)
	if got.Assets == nil || len(got.Assets) != 0 {
		t.Fatalf("assets = %#v, want empty non-nil slice", got.Assets)
	}
}

func TestRemoveAssetRecomputes(t *testing.T) {
	desired := DesiredState{SchemaVersion: 1, Assets: []DesiredAsset{
		{Namespace: "pilab", Kind: "agent", Name: "code-reviewer", Version: "^1.4"},
		{Namespace: "pilab", Kind: "tool", Name: "codegraph", Version: "1.0.3"},
	}}
	locked := LockedState{
		LockfileVersion: 1,
		GeneratedAt:     "2026-11-04T10:00:00Z",
		Resolver:        "agenthub/0.1.0",
		Assets: []LockedAsset{
			{Ref: "pilab/agent/code-reviewer@1.4.2", OriginHub: "agenthub.pilab.hu"},
			{Ref: "pilab/tool/codegraph@1.0.3", OriginHub: "agenthub.pilab.hu"},
			{Ref: "pilab/skill/stale@2.0.0", OriginHub: "agenthub.pilab.hu"},
		},
	}

	// The version suffix on the argument is ignored while matching.
	gotD, gotL, removed, err := RemoveAsset(desired, locked, "pilab/agent/code-reviewer@1.4.2")
	if err != nil {
		t.Fatalf("RemoveAsset: %v", err)
	}
	if !removed {
		t.Fatal("expected the desired asset to be removed")
	}
	if len(gotD.Assets) != 1 || gotD.Assets[0].Name != "codegraph" {
		t.Fatalf("desired assets = %+v, want only codegraph", gotD.Assets)
	}
	// The lockfile is recomputed: the removed asset and every stale pin are gone.
	if len(gotL.Assets) != 1 || gotL.Assets[0].Ref != "pilab/tool/codegraph@1.0.3" {
		t.Fatalf("locked assets = %+v, want only the codegraph pin", gotL.Assets)
	}
	if gotL.GeneratedAt != locked.GeneratedAt || gotL.Resolver != locked.Resolver {
		t.Fatalf("metadata not preserved: %+v", gotL)
	}
	// Inputs stay untouched.
	if len(desired.Assets) != 2 || len(locked.Assets) != 3 {
		t.Fatalf("inputs mutated: desired=%+v locked=%+v", desired.Assets, locked.Assets)
	}
}

func TestRemoveAssetNotFound(t *testing.T) {
	desired := DesiredState{SchemaVersion: 1, Assets: []DesiredAsset{
		{Namespace: "pilab", Kind: "tool", Name: "codegraph", Version: "1.0.3"},
	}}
	locked := LockedState{LockfileVersion: 1, Assets: []LockedAsset{
		{Ref: "pilab/tool/codegraph@1.0.3"},
	}}

	gotD, gotL, removed, err := RemoveAsset(desired, locked, "pilab/agent/code-reviewer")
	if err != nil {
		t.Fatalf("RemoveAsset: %v", err)
	}
	if removed {
		t.Fatal("expected no removal")
	}
	if len(gotD.Assets) != 1 || len(gotL.Assets) != 1 {
		t.Fatalf("state changed on no-op: desired=%+v locked=%+v", gotD.Assets, gotL.Assets)
	}
}

func TestRemoveAssetInvalidRef(t *testing.T) {
	for _, ref := range []string{"", "code-reviewer", "pilab/agent", "pilab/agent/", "/agent/code-reviewer"} {
		_, _, _, err := RemoveAsset(DesiredState{}, LockedState{}, ref)
		if err == nil {
			t.Fatalf("ref %q: expected an error", ref)
		}
	}
}
