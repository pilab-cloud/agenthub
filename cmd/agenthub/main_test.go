package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	catalogv1 "go.pilab.hu/agenthub/gen/go/agenthub/catalog/v1"
	catalogv1connect "go.pilab.hu/agenthub/gen/go/agenthub/catalog/v1/catalogv1connect"
	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"

	"go.pilab.hu/agenthub/internal/cache"
	"go.pilab.hu/agenthub/internal/lockfile"
)

func TestRunInstallMissingLockfile(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	err := runInstall(context.Background(), &out, filepath.Join(t.TempDir(), lockfile.LockFileName), false, installOptions{})
	if err == nil {
		t.Fatal("expected error for missing lockfile")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("expected no output, got %q", out.String())
	}
}

func TestRunInstallOfflineCacheHit(t *testing.T) {
	t.Parallel()

	manifest := []byte(`{"name":"code-reviewer"}`)
	key := cache.Key(manifest)
	cacheDir := t.TempDir()
	c, err := cache.New(cacheDir)
	if err != nil {
		t.Fatalf("cache: %v", err)
	}
	if err := c.Put(key, manifest); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	path := writeLockfile(t, lockfile.LockedAsset{
		Ref:            "pilab/agent/code-reviewer@1.4.2",
		ManifestDigest: "sha256:" + key,
	})

	var out bytes.Buffer
	if err := runInstall(context.Background(), &out, path, false, installOptions{CacheDir: cacheDir}); err != nil {
		t.Fatalf("install: %v", err)
	}
	want := strings.Join([]string{
		"offline; using cache",
		"installing pilab/agent/code-reviewer@1.4.2",
		"cached manifest pilab/agent/code-reviewer@1.4.2",
		"summary: total=1 fetched=0 cached=1 missing=0",
		"",
	}, "\n")
	if got := out.String(); got != want {
		t.Fatalf("output:\n%s\nwant:\n%s", got, want)
	}
}

func TestRunInstallOfflineCacheMiss(t *testing.T) {
	t.Parallel()

	key := cache.Key([]byte("absent manifest"))
	path := writeLockfile(t, lockfile.LockedAsset{
		Ref:            "pilab/agent/code-reviewer@1.4.2",
		ManifestDigest: "sha256:" + key,
	})

	var out bytes.Buffer
	opts := installOptions{CacheDir: t.TempDir()}
	if err := runInstall(context.Background(), &out, path, false, opts); err != nil {
		t.Fatalf("install: %v", err)
	}
	want := strings.Join([]string{
		"offline; using cache",
		"installing pilab/agent/code-reviewer@1.4.2",
		"not cached pilab/agent/code-reviewer@1.4.2",
		"summary: total=1 fetched=0 cached=0 missing=1",
		"",
	}, "\n")
	if got := out.String(); got != want {
		t.Fatalf("output:\n%s\nwant:\n%s", got, want)
	}
}

func TestRunInstallOfflineWithoutManifestDigest(t *testing.T) {
	t.Parallel()

	path := writeLockfile(t, lockfile.LockedAsset{Ref: "pilab/tool/codegraph@1.0.3"})

	var out bytes.Buffer
	opts := installOptions{CacheDir: t.TempDir()}
	if err := runInstall(context.Background(), &out, path, false, opts); err != nil {
		t.Fatalf("install: %v", err)
	}
	want := strings.Join([]string{
		"offline; using cache",
		"installing pilab/tool/codegraph@1.0.3",
		"no manifest digest for pilab/tool/codegraph@1.0.3",
		"summary: total=1 fetched=0 cached=0 missing=1",
		"",
	}, "\n")
	if got := out.String(); got != want {
		t.Fatalf("output:\n%s\nwant:\n%s", got, want)
	}
}

func TestRunInstallFetchesManifest(t *testing.T) {
	t.Parallel()

	manifest := []byte(`{"name":"code-reviewer","version":"1.4.2"}`)
	key := cache.Key(manifest)
	stub := &stubCatalog{manifest: manifest}
	hub := newCatalogStub(t, stub)
	path := writeLockfile(t, lockfile.LockedAsset{
		Ref:            "pilab/agent/code-reviewer@1.4.2",
		ManifestDigest: "sha256:" + key,
	})

	cacheDir := t.TempDir()
	var out bytes.Buffer
	opts := installOptions{Hub: hub, CacheDir: cacheDir}
	if err := runInstall(context.Background(), &out, path, false, opts); err != nil {
		t.Fatalf("install: %v", err)
	}
	want := strings.Join([]string{
		"installing pilab/agent/code-reviewer@1.4.2",
		"fetched manifest pilab/agent/code-reviewer@1.4.2",
		"fetched file files/readme.md",
		"files: total=1 fetched=1 cached=0 missing=0",
		"summary: total=1 fetched=1 cached=0 missing=0",
		"",
	}, "\n")
	if got := out.String(); got != want {
		t.Fatalf("output:\n%s\nwant:\n%s", got, want)
	}

	c, err := cache.New(cacheDir)
	if err != nil {
		t.Fatalf("cache: %v", err)
	}
	if !c.Has(key) {
		t.Fatalf("manifest not cached under %s", key)
	}
	got, err := c.Get(key)
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	if !bytes.Equal(got, manifest) {
		t.Fatalf("cached manifest = %q, want %q", got, manifest)
	}
	// The blob is stored content-addressed under its declared digest.
	blobKey := cache.Key(defaultBlob)
	if !c.Has(blobKey) {
		t.Fatalf("blob not cached under %s", blobKey)
	}

	req := stub.request()
	digest := req.GetManifestDigest()
	if digest == nil || digest.GetValue() != key {
		t.Fatalf("GetBundle selector = %v, want manifest digest %s", digest, key)
	}
}

func TestRunInstallFetchesManifestByRef(t *testing.T) {
	t.Parallel()

	manifest := []byte(`{"name":"codegraph"}`)
	stub := &stubCatalog{manifest: manifest}
	hub := newCatalogStub(t, stub)
	// No manifest digest in the lockfile: install falls back to the ref selector.
	path := writeLockfile(t, lockfile.LockedAsset{Ref: "pilab/tool/codegraph@1.0.3"})

	cacheDir := t.TempDir()
	var out bytes.Buffer
	opts := installOptions{Hub: hub, CacheDir: cacheDir}
	if err := runInstall(context.Background(), &out, path, false, opts); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !strings.Contains(out.String(), "fetched manifest pilab/tool/codegraph@1.0.3") {
		t.Fatalf("output missing fetch line:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "summary: total=1 fetched=1 cached=0 missing=0") {
		t.Fatalf("output missing summary:\n%s", out.String())
	}

	c, err := cache.New(cacheDir)
	if err != nil {
		t.Fatalf("cache: %v", err)
	}
	if !c.Has(cache.Key(manifest)) {
		t.Fatal("manifest not cached by content digest")
	}
	ref := stub.request().GetRef()
	if ref == nil || ref.GetName() != "codegraph" {
		t.Fatalf("GetBundle selector = %v, want asset ref", ref)
	}
}

func TestRunInstallGetBundleUnimplementedFallsBackToCache(t *testing.T) {
	t.Parallel()

	stub := &stubCatalog{err: connect.NewError(connect.CodeUnimplemented, errors.New("not implemented"))}
	hub := newCatalogStub(t, stub)
	key := cache.Key([]byte("uncached manifest"))
	path := writeLockfile(t, lockfile.LockedAsset{
		Ref:            "pilab/agent/code-reviewer@1.4.2",
		ManifestDigest: "sha256:" + key,
	})

	var out bytes.Buffer
	opts := installOptions{Hub: hub, CacheDir: t.TempDir()}
	if err := runInstall(context.Background(), &out, path, false, opts); err != nil {
		t.Fatalf("install must stay resilient, got %v", err)
	}
	want := strings.Join([]string{
		"installing pilab/agent/code-reviewer@1.4.2",
		"offline; using cache",
		"not cached pilab/agent/code-reviewer@1.4.2",
		"summary: total=1 fetched=0 cached=0 missing=1",
		"",
	}, "\n")
	if got := out.String(); got != want {
		t.Fatalf("output:\n%s\nwant:\n%s", got, want)
	}
}

func TestRunInstallHubUnreachableFallsBackToCache(t *testing.T) {
	t.Parallel()

	key := cache.Key([]byte("uncached manifest"))
	path := writeLockfile(t, lockfile.LockedAsset{
		Ref:            "pilab/agent/code-reviewer@1.4.2",
		ManifestDigest: "sha256:" + key,
	})

	var out bytes.Buffer
	opts := installOptions{
		Hub:        "http://127.0.0.1:1",
		CacheDir:   t.TempDir(),
		HTTPClient: &http.Client{Timeout: 2 * time.Second},
	}
	if err := runInstall(context.Background(), &out, path, false, opts); err != nil {
		t.Fatalf("install must stay resilient, got %v", err)
	}
	if !strings.Contains(out.String(), "offline; using cache") {
		t.Fatalf("output missing offline line:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "summary: total=1 fetched=0 cached=0 missing=1") {
		t.Fatalf("output missing summary:\n%s", out.String())
	}
}

func TestRunInstallRejectsManifestDigestMismatch(t *testing.T) {
	t.Parallel()

	stub := &stubCatalog{manifest: []byte("tampered manifest")}
	hub := newCatalogStub(t, stub)
	path := writeLockfile(t, lockfile.LockedAsset{
		Ref:            "pilab/agent/code-reviewer@1.4.2",
		ManifestDigest: "sha256:" + cache.Key([]byte("expected manifest")),
	})

	var out bytes.Buffer
	opts := installOptions{Hub: hub, CacheDir: t.TempDir()}
	err := runInstall(context.Background(), &out, path, false, opts)
	if err == nil {
		t.Fatal("expected digest mismatch error")
	}
	if !errors.Is(err, cache.ErrDigestMismatch) {
		t.Fatalf("error = %v, want cache.ErrDigestMismatch", err)
	}
}

func TestRunInstallFrozenEmptyLockfile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), lockfile.LockFileName)
	if err := lockfile.WriteLocked(path, lockfile.LockedState{Assets: []lockfile.LockedAsset{}}); err != nil {
		t.Fatalf("write lockfile: %v", err)
	}

	var out bytes.Buffer
	err := runInstall(context.Background(), &out, path, true, installOptions{})
	if err == nil {
		t.Fatal("expected error for --frozen with empty lockfile")
	}
	if !strings.Contains(err.Error(), "--frozen") {
		t.Fatalf("expected --frozen error, got %v", err)
	}
	// Nothing installable offline is exit code 6, per cli-plugin.md §2.1.
	if code := exitCode(err); code != exitOffline {
		t.Fatalf("exit code = %d, want %d", code, exitOffline)
	}
}

func TestRunInstallFrozenWithAssets(t *testing.T) {
	t.Parallel()

	path := writeLockfile(t, lockfile.LockedAsset{Ref: "pilab/tool/codegraph@1.0.3"})

	var out bytes.Buffer
	opts := installOptions{CacheDir: t.TempDir()}
	err := runInstall(context.Background(), &out, path, true, opts)
	if err == nil {
		t.Fatal("expected --frozen to fail when nothing is installable offline")
	}
	if !errors.Is(err, errOfflineIncomplete) {
		t.Fatalf("error = %v, want errOfflineIncomplete", err)
	}
	if code := exitCode(err); code != exitOffline {
		t.Fatalf("exit code = %d, want %d", code, exitOffline)
	}
	want := strings.Join([]string{
		"offline; using cache",
		"installing pilab/tool/codegraph@1.0.3",
		"no manifest digest for pilab/tool/codegraph@1.0.3",
		"summary: total=1 fetched=0 cached=0 missing=1",
		"",
	}, "\n")
	if got := out.String(); got != want {
		t.Fatalf("output:\n%s\nwant:\n%s", got, want)
	}
}

func TestInstallCmdMissingLockfile(t *testing.T) {
	// Not parallel: changes the process working directory.
	t.Chdir(t.TempDir())

	root := newRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"install"})

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error when agenthub.lock.json is missing")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
}

func TestInstallCmdOfflineFromCache(t *testing.T) {
	// Not parallel: changes the process working directory.
	dir := t.TempDir()
	t.Chdir(dir)

	manifest := []byte(`{"name":"code-reviewer"}`)
	key := cache.Key(manifest)
	cacheDir := t.TempDir()
	c, err := cache.New(cacheDir)
	if err != nil {
		t.Fatalf("cache: %v", err)
	}
	if err := c.Put(key, manifest); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	if err := lockfile.WriteLocked(lockfile.LockFileName, lockfile.LockedState{
		Assets: []lockfile.LockedAsset{{
			Ref:            "pilab/agent/code-reviewer@1.4.2",
			ManifestDigest: "sha256:" + key,
		}},
	}); err != nil {
		t.Fatalf("write lockfile: %v", err)
	}

	root := newRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"install", "--frozen", "--offline", "--cache-dir", cacheDir})

	if err := root.Execute(); err != nil {
		t.Fatalf("install --frozen: %v", err)
	}
	want := strings.Join([]string{
		"offline; using cache",
		"installing pilab/agent/code-reviewer@1.4.2",
		"cached manifest pilab/agent/code-reviewer@1.4.2",
		"summary: total=1 fetched=0 cached=1 missing=0",
		"",
	}, "\n")
	if got := out.String(); got != want {
		t.Fatalf("output:\n%s\nwant:\n%s", got, want)
	}
	if _, err := os.Stat(filepath.Join(dir, lockfile.LockFileName)); err != nil {
		t.Fatalf("lockfile should not be modified: %v", err)
	}
}

func TestRunUpdateNoDesiredAssets(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	desiredPath := filepath.Join(dir, lockfile.DesiredFileName)
	lockPath := filepath.Join(dir, lockfile.LockFileName)

	var out bytes.Buffer
	// Offline would need a lockfile, but the empty desired state short-circuits.
	err := runUpdate(context.Background(), &out, desiredPath, lockPath, updateOptions{Offline: true})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	want := "no desired assets in agenthub.json; run `agenthub add <asset-ref>` first\n"
	if got := out.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("lockfile should not be created, stat err = %v", err)
	}
}

func TestRunUpdateResolvesDesired(t *testing.T) {
	t.Parallel()

	stub := &stubCatalog{resolveResp: &catalogv1.ResolveResponse{Assets: []*catalogv1.ResolvedAsset{
		{
			Ref:            &commonv1.AssetRef{Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "code-reviewer", Version: "1.4.2"},
			ManifestDigest: &commonv1.Digest{Algorithm: "sha256", Value: "m1"},
			BundleDigest:   &commonv1.Digest{Algorithm: "sha256", Value: "b1"},
		},
		{
			Ref:       &commonv1.AssetRef{Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_TOOL, Name: "codegraph", Version: "1.0.3"},
			OriginHub: "mirror.example.com",
		},
	}}}
	hub := newCatalogStub(t, stub)
	desiredPath := writeDesired(t,
		lockfile.DesiredAsset{Namespace: "pilab", Kind: "agent", Name: "code-reviewer", Version: "^1.4"},
		lockfile.DesiredAsset{Namespace: "pilab", Kind: "tool", Name: "codegraph"},
	)
	lockPath := writeLockfile(t,
		lockfile.LockedAsset{Ref: "pilab/agent/code-reviewer@1.4.0", OriginHub: "agenthub.pilab.hu", ManifestDigest: "sha256:old"},
		lockfile.LockedAsset{Ref: "pilab/skill/stale@2.0.0", OriginHub: "agenthub.pilab.hu"},
	)

	var out bytes.Buffer
	if err := runUpdate(context.Background(), &out, desiredPath, lockPath, updateOptions{Hub: hub}); err != nil {
		t.Fatalf("update: %v", err)
	}
	want := strings.Join([]string{
		"updated pilab/agent/code-reviewer@1.4.2",
		"updated pilab/tool/codegraph@1.0.3",
		"lockfile updated: assets=2",
		"",
	}, "\n")
	if got := out.String(); got != want {
		t.Fatalf("output:\n%s\nwant:\n%s", got, want)
	}

	locked, err := lockfile.ReadLocked(lockPath)
	if err != nil {
		t.Fatalf("read lockfile: %v", err)
	}
	if len(locked.Assets) != 2 {
		t.Fatalf("locked assets = %+v, want the two desired assets", locked.Assets)
	}
	first := locked.Assets[0]
	if first.Ref != "pilab/agent/code-reviewer@1.4.2" ||
		first.OriginHub != hubHost(hub) ||
		first.ManifestDigest != "sha256:m1" ||
		first.BundleDigest != "sha256:b1" {
		t.Fatalf("first pin = %+v", first)
	}
	second := locked.Assets[1]
	if second.Ref != "pilab/tool/codegraph@1.0.3" ||
		second.OriginHub != "mirror.example.com" ||
		second.ManifestDigest != "" {
		t.Fatalf("second pin = %+v", second)
	}
	if locked.LockfileVersion != 1 || locked.GeneratedAt == "" || locked.Resolver == "" {
		t.Fatalf("lockfile metadata missing: %+v", locked)
	}

	// The request carries the desired list, the current pins and the update flag.
	req := stub.resolveRequest()
	if req == nil || !req.GetUpdate() {
		t.Fatalf("resolve request = %+v, want update=true", req)
	}
	if len(req.GetDesired()) != 2 || req.GetDesired()[0].GetRef().GetVersion() != "^1.4" {
		t.Fatalf("desired = %+v", req.GetDesired())
	}
	if len(req.GetLock()) != 2 {
		t.Fatalf("lock pins = %+v, want the current lockfile", req.GetLock())
	}
	pin := req.GetLock()[0]
	if pin.GetRef().GetName() != "code-reviewer" || pin.GetManifestDigest().GetValue() != "old" {
		t.Fatalf("first lock pin = %+v", pin)
	}
}

func TestRunUpdateOfflineKeepsPins(t *testing.T) {
	t.Parallel()

	desiredPath := writeDesired(t,
		lockfile.DesiredAsset{Namespace: "pilab", Kind: "agent", Name: "code-reviewer", Version: "^1.4"},
	)
	lockPath := filepath.Join(t.TempDir(), lockfile.LockFileName)
	if err := lockfile.WriteLocked(lockPath, lockfile.LockedState{
		GeneratedAt: "2026-11-04T10:00:00Z",
		Resolver:    "agenthub/0.1.0",
		Assets: []lockfile.LockedAsset{
			{Ref: "pilab/agent/code-reviewer@1.4.2", OriginHub: "agenthub.pilab.hu", ManifestDigest: "sha256:m1"},
			{Ref: "pilab/skill/stale@2.0.0", OriginHub: "agenthub.pilab.hu"},
		},
	}); err != nil {
		t.Fatalf("write lockfile: %v", err)
	}

	var out bytes.Buffer
	// No hub configured: offline must never need the network.
	if err := runUpdate(context.Background(), &out, desiredPath, lockPath, updateOptions{Offline: true}); err != nil {
		t.Fatalf("update --offline: %v", err)
	}
	want := strings.Join([]string{
		"offline; keeping existing lock pins",
		"lockfile updated: assets=1",
		"",
	}, "\n")
	if got := out.String(); got != want {
		t.Fatalf("output:\n%s\nwant:\n%s", got, want)
	}

	locked, err := lockfile.ReadLocked(lockPath)
	if err != nil {
		t.Fatalf("read lockfile: %v", err)
	}
	if len(locked.Assets) != 1 {
		t.Fatalf("locked assets = %+v, want the stale pin pruned", locked.Assets)
	}
	if locked.Assets[0].Ref != "pilab/agent/code-reviewer@1.4.2" || locked.Assets[0].ManifestDigest != "sha256:m1" {
		t.Fatalf("pin changed: %+v", locked.Assets[0])
	}
	if locked.GeneratedAt != "2026-11-04T10:00:00Z" || locked.Resolver != "agenthub/0.1.0" {
		t.Fatalf("metadata not preserved: %+v", locked)
	}
}

func TestRunUpdateOfflineReportsUnpinned(t *testing.T) {
	t.Parallel()

	desiredPath := writeDesired(t,
		lockfile.DesiredAsset{Namespace: "pilab", Kind: "agent", Name: "code-reviewer", Version: "^1.4"},
		lockfile.DesiredAsset{Namespace: "pilab", Kind: "tool", Name: "codegraph", Version: "1.0.3"},
	)
	lockPath := writeLockfile(t,
		lockfile.LockedAsset{Ref: "pilab/agent/code-reviewer@1.4.2"},
	)

	var out bytes.Buffer
	if err := runUpdate(context.Background(), &out, desiredPath, lockPath, updateOptions{Offline: true}); err != nil {
		t.Fatalf("update --offline: %v", err)
	}
	want := strings.Join([]string{
		"offline; keeping existing lock pins",
		"unpinned pilab/tool/codegraph",
		"lockfile updated: assets=1",
		"",
	}, "\n")
	if got := out.String(); got != want {
		t.Fatalf("output:\n%s\nwant:\n%s", got, want)
	}
}

func TestRunUpdateOfflineMissingLockfile(t *testing.T) {
	t.Parallel()

	desiredPath := writeDesired(t,
		lockfile.DesiredAsset{Namespace: "pilab", Kind: "agent", Name: "code-reviewer", Version: "^1.4"},
	)
	lockPath := filepath.Join(t.TempDir(), lockfile.LockFileName)

	var out bytes.Buffer
	err := runUpdate(context.Background(), &out, desiredPath, lockPath, updateOptions{Offline: true})
	if err == nil {
		t.Fatal("expected an error for a missing lockfile in offline mode")
	}
	if !strings.Contains(err.Error(), "--offline") || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v, want --offline not-found error", err)
	}
	if out.Len() != 0 {
		t.Fatalf("expected no output, got %q", out.String())
	}
}

func TestRunUpdateUnresolvedLeavesLockfileUntouched(t *testing.T) {
	t.Parallel()

	stub := &stubCatalog{resolveResp: &catalogv1.ResolveResponse{
		Warnings: []*catalogv1.ResolutionWarning{{
			Code:    "not_found",
			Message: "asset not found",
			Ref:     &commonv1.AssetRef{Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "code-reviewer"},
		}},
	}}
	hub := newCatalogStub(t, stub)
	desiredPath := writeDesired(t,
		lockfile.DesiredAsset{Namespace: "pilab", Kind: "agent", Name: "code-reviewer", Version: "^1.4"},
	)
	lockPath := writeLockfile(t,
		lockfile.LockedAsset{Ref: "pilab/agent/code-reviewer@1.4.0", ManifestDigest: "sha256:old"},
	)
	before, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read lockfile: %v", err)
	}

	var out bytes.Buffer
	err = runUpdate(context.Background(), &out, desiredPath, lockPath, updateOptions{Hub: hub})
	if err == nil {
		t.Fatal("expected an error for an unresolvable desired asset")
	}
	if !strings.Contains(err.Error(), "cannot resolve pilab/agent/code-reviewer") {
		t.Fatalf("error = %v, want cannot-resolve error", err)
	}
	want := "warning: not_found: asset not found (pilab/agent/code-reviewer)\n"
	if got := out.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	after, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read lockfile: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("lockfile modified on failure:\nbefore: %s\nafter: %s", before, after)
	}
}

func TestRunUpdateUnknownDesiredKind(t *testing.T) {
	t.Parallel()

	desiredPath := writeDesired(t,
		lockfile.DesiredAsset{Namespace: "pilab", Kind: "widget", Name: "gizmo"},
	)
	var out bytes.Buffer
	err := runUpdate(context.Background(), &out, desiredPath, filepath.Join(t.TempDir(), lockfile.LockFileName),
		updateOptions{Offline: true})
	if err == nil {
		t.Fatal("expected an error for an unknown desired kind")
	}
	if !strings.Contains(err.Error(), `unknown asset kind "widget"`) {
		t.Fatalf("error = %v, want unknown-kind error", err)
	}
}

func TestRunRemove(t *testing.T) {
	t.Parallel()

	// Both the canonical form (with a version to ignore) and a kind alias.
	for _, arg := range []string{"pilab/agent/code-reviewer@1.4.2", "pilab/agents/code-reviewer"} {
		t.Run(arg, func(t *testing.T) {
			desiredPath := writeDesired(t,
				lockfile.DesiredAsset{Namespace: "pilab", Kind: "agent", Name: "code-reviewer", Version: "^1.4"},
				lockfile.DesiredAsset{Namespace: "pilab", Kind: "tool", Name: "codegraph", Version: "1.0.3"},
			)
			lockPath := writeLockfile(t,
				lockfile.LockedAsset{Ref: "pilab/agent/code-reviewer@1.4.2", ManifestDigest: "sha256:m1"},
				lockfile.LockedAsset{Ref: "pilab/tool/codegraph@1.0.3", ManifestDigest: "sha256:m2"},
			)

			var out bytes.Buffer
			if err := runRemove(&out, desiredPath, lockPath, arg); err != nil {
				t.Fatalf("remove: %v", err)
			}
			want := strings.Join([]string{
				"removed pilab/agent/code-reviewer",
				"lockfile updated: assets=1",
				"",
			}, "\n")
			if got := out.String(); got != want {
				t.Fatalf("output:\n%s\nwant:\n%s", got, want)
			}

			desired, err := lockfile.ReadDesired(desiredPath)
			if err != nil {
				t.Fatalf("read desired state: %v", err)
			}
			if len(desired.Assets) != 1 || desired.Assets[0].Name != "codegraph" {
				t.Fatalf("desired assets = %+v, want only codegraph", desired.Assets)
			}
			locked, err := lockfile.ReadLocked(lockPath)
			if err != nil {
				t.Fatalf("read lockfile: %v", err)
			}
			if len(locked.Assets) != 1 || locked.Assets[0].Ref != "pilab/tool/codegraph@1.0.3" {
				t.Fatalf("locked assets = %+v, want only the codegraph pin", locked.Assets)
			}
		})
	}
}

func TestRunRemoveNotFound(t *testing.T) {
	t.Parallel()

	desiredPath := writeDesired(t,
		lockfile.DesiredAsset{Namespace: "pilab", Kind: "tool", Name: "codegraph", Version: "1.0.3"},
	)
	lockPath := writeLockfile(t, lockfile.LockedAsset{Ref: "pilab/tool/codegraph@1.0.3"})

	var out bytes.Buffer
	err := runRemove(&out, desiredPath, lockPath, "pilab/agent/code-reviewer")
	if err == nil {
		t.Fatal("expected an error for an unknown asset")
	}
	if !strings.Contains(err.Error(), "not found") || !strings.Contains(err.Error(), "pilab/agent/code-reviewer") {
		t.Fatalf("error = %v, want not-found error naming the asset", err)
	}
	if out.Len() != 0 {
		t.Fatalf("expected no output, got %q", out.String())
	}
	desired, readErr := lockfile.ReadDesired(desiredPath)
	if readErr != nil || len(desired.Assets) != 1 {
		t.Fatalf("desired state changed: err=%v assets=%+v", readErr, desired.Assets)
	}
}

func TestRunRemoveWithoutLockfile(t *testing.T) {
	t.Parallel()

	desiredPath := writeDesired(t,
		lockfile.DesiredAsset{Namespace: "pilab", Kind: "agent", Name: "code-reviewer", Version: "^1.4"},
	)
	lockPath := filepath.Join(t.TempDir(), lockfile.LockFileName)

	var out bytes.Buffer
	if err := runRemove(&out, desiredPath, lockPath, "pilab/agent/code-reviewer"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	want := "removed pilab/agent/code-reviewer\n"
	if got := out.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	desired, err := lockfile.ReadDesired(desiredPath)
	if err != nil || len(desired.Assets) != 0 {
		t.Fatalf("desired assets not emptied: err=%v assets=%+v", err, desired.Assets)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("lockfile should not be created, stat err = %v", err)
	}
}

func TestRemoveCmd(t *testing.T) {
	// Not parallel: changes the process working directory.
	t.Chdir(t.TempDir())

	if err := lockfile.WriteDesired(lockfile.DesiredFileName, lockfile.DesiredState{
		SchemaVersion: 1,
		Assets: []lockfile.DesiredAsset{
			{Namespace: "pilab", Kind: "agent", Name: "code-reviewer", Version: "^1.4"},
			{Namespace: "pilab", Kind: "tool", Name: "codegraph", Version: "1.0.3"},
		},
	}); err != nil {
		t.Fatalf("write desired state: %v", err)
	}
	if err := lockfile.WriteLocked(lockfile.LockFileName, lockfile.LockedState{
		Assets: []lockfile.LockedAsset{
			{Ref: "pilab/agent/code-reviewer@1.4.2"},
			{Ref: "pilab/tool/codegraph@1.0.3"},
		},
	}); err != nil {
		t.Fatalf("write lockfile: %v", err)
	}

	root := newRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"remove", "pilab/agent/code-reviewer"})

	if err := root.Execute(); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !strings.Contains(out.String(), "removed pilab/agent/code-reviewer") {
		t.Fatalf("output missing removal line:\n%s", out.String())
	}
	desired, err := lockfile.ReadDesired(lockfile.DesiredFileName)
	if err != nil || len(desired.Assets) != 1 || desired.Assets[0].Name != "codegraph" {
		t.Fatalf("desired state: err=%v assets=%+v", err, desired.Assets)
	}
	locked, err := lockfile.ReadLocked(lockfile.LockFileName)
	if err != nil || len(locked.Assets) != 1 || locked.Assets[0].Ref != "pilab/tool/codegraph@1.0.3" {
		t.Fatalf("lockfile: err=%v assets=%+v", err, locked.Assets)
	}
}

func TestUpdateCmdOffline(t *testing.T) {
	// Not parallel: changes the process working directory.
	t.Chdir(t.TempDir())

	if err := lockfile.WriteDesired(lockfile.DesiredFileName, lockfile.DesiredState{
		SchemaVersion: 1,
		Assets: []lockfile.DesiredAsset{
			{Namespace: "pilab", Kind: "agent", Name: "code-reviewer", Version: "^1.4"},
		},
	}); err != nil {
		t.Fatalf("write desired state: %v", err)
	}
	if err := lockfile.WriteLocked(lockfile.LockFileName, lockfile.LockedState{
		Assets: []lockfile.LockedAsset{
			{Ref: "pilab/agent/code-reviewer@1.4.2", ManifestDigest: "sha256:m1"},
			{Ref: "pilab/skill/stale@2.0.0"},
		},
	}); err != nil {
		t.Fatalf("write lockfile: %v", err)
	}

	root := newRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"update", "--offline"})

	if err := root.Execute(); err != nil {
		t.Fatalf("update --offline: %v", err)
	}
	if !strings.Contains(out.String(), "offline; keeping existing lock pins") {
		t.Fatalf("output missing offline line:\n%s", out.String())
	}
	locked, err := lockfile.ReadLocked(lockfile.LockFileName)
	if err != nil {
		t.Fatalf("read lockfile: %v", err)
	}
	if len(locked.Assets) != 1 || locked.Assets[0].ManifestDigest != "sha256:m1" {
		t.Fatalf("lockfile = %+v, want the pin kept and the stale entry pruned", locked.Assets)
	}
}

func TestUpdateCmdFromHub(t *testing.T) {
	// Not parallel: changes the process working directory.
	t.Chdir(t.TempDir())

	stub := &stubCatalog{resolveResp: &catalogv1.ResolveResponse{Assets: []*catalogv1.ResolvedAsset{{
		Ref:            &commonv1.AssetRef{Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "code-reviewer", Version: "1.4.2"},
		ManifestDigest: &commonv1.Digest{Algorithm: "sha256", Value: "m1"},
	}}}}
	hub := newCatalogStub(t, stub)
	if err := lockfile.WriteDesired(lockfile.DesiredFileName, lockfile.DesiredState{
		SchemaVersion: 1,
		Assets: []lockfile.DesiredAsset{
			{Namespace: "pilab", Kind: "agent", Name: "code-reviewer", Version: "^1.4"},
		},
	}); err != nil {
		t.Fatalf("write desired state: %v", err)
	}

	root := newRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"update", "--hub", hub})

	if err := root.Execute(); err != nil {
		t.Fatalf("update: %v", err)
	}
	if !strings.Contains(out.String(), "updated pilab/agent/code-reviewer@1.4.2") {
		t.Fatalf("output missing update line:\n%s", out.String())
	}
	locked, err := lockfile.ReadLocked(lockfile.LockFileName)
	if err != nil {
		t.Fatalf("read lockfile: %v", err)
	}
	if len(locked.Assets) != 1 ||
		locked.Assets[0].Ref != "pilab/agent/code-reviewer@1.4.2" ||
		locked.Assets[0].ManifestDigest != "sha256:m1" {
		t.Fatalf("lockfile = %+v, want the resolved pin", locked.Assets)
	}
}

func TestParseDigestRoundTrip(t *testing.T) {
	t.Parallel()

	for _, s := range []string{"sha256:abc123", "sha512:def456", "abc123"} {
		if got := digestString(parseDigest(s)); got != s {
			t.Fatalf("round trip of %q = %q", s, got)
		}
	}
	if d := parseDigest(""); d != nil {
		t.Fatalf("empty digest = %+v, want nil", d)
	}
}

// writeLockfile writes a single-asset lockfile into a temp dir and returns
// its path.
func writeLockfile(t *testing.T, assets ...lockfile.LockedAsset) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), lockfile.LockFileName)
	if err := lockfile.WriteLocked(path, lockfile.LockedState{Assets: assets}); err != nil {
		t.Fatalf("write lockfile: %v", err)
	}
	return path
}

// writeDesired writes the desired state into a temp dir and returns its path.
func writeDesired(t *testing.T, assets ...lockfile.DesiredAsset) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), lockfile.DesiredFileName)
	if err := lockfile.WriteDesired(path, lockfile.DesiredState{SchemaVersion: 1, Assets: assets}); err != nil {
		t.Fatalf("write desired state: %v", err)
	}
	return path
}

// stubCatalog is a CatalogService that serves GetBundle from a fixed manifest
// (or fails with a fixed error) for install tests, and Resolve from a fixed
// response for update tests. Blob URLs point at the stub's own HTTP server so
// install tests exercise real downloads without touching the network.
type stubCatalog struct {
	catalogv1connect.UnimplementedCatalogServiceHandler

	mu          sync.Mutex
	manifest    []byte
	err         error
	gotReq      *catalogv1.GetBundleRequest
	resolveResp *catalogv1.ResolveResponse
	resolveErr  error
	gotResolve  *catalogv1.ResolveRequest

	// blob serving: content for /blobs/<path> from blobs (or blob, or the
	// default "blob"), a non-zero status answers instead of the content, and an
	// empty URL in a file spec is filled in from the stub's own base URL.
	blob         []byte
	blobs        map[string][]byte
	blobStatus   int
	bundleDigest *commonv1.Digest
	files        []*catalogv1.BlobLocation
	baseURL      string
}

func (s *stubCatalog) GetBundle(_ context.Context, req *connect.Request[catalogv1.GetBundleRequest]) (*connect.Response[catalogv1.GetBundleResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gotReq = req.Msg
	if s.err != nil {
		return nil, s.err
	}
	return connect.NewResponse(&catalogv1.GetBundleResponse{
		Manifest:       s.manifest,
		ManifestDigest: &commonv1.Digest{Algorithm: "sha256", Value: cache.Key(s.manifest)},
		BundleDigest:   s.bundleDigest,
		Files:          s.blobLocations(),
	}), nil
}

// blobLocations returns the response's file list with blob URLs resolved
// against the stub server. Callers hold s.mu.
func (s *stubCatalog) blobLocations() []*catalogv1.BlobLocation {
	specs := s.files
	if specs == nil {
		specs = []*catalogv1.BlobLocation{{
			Path:   "files/readme.md",
			Digest: &commonv1.Digest{Algorithm: "sha256", Value: cache.Key(defaultBlob)},
		}}
	}
	out := make([]*catalogv1.BlobLocation, 0, len(specs))
	for _, f := range specs {
		u := strings.TrimSpace(f.GetUrl())
		if u == "" {
			u = s.baseURL + "/blobs/" + f.GetPath()
		}
		out = append(out, &catalogv1.BlobLocation{
			Path:   f.GetPath(),
			Digest: f.GetDigest(),
			Size:   f.GetSize(),
			Url:    u,
		})
	}
	return out
}

// blobPayload returns what the stub's /blobs/ handler serves for path.
func (s *stubCatalog) blobPayload(path string) ([]byte, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if body, ok := s.blobs[path]; ok {
		return body, s.blobStatus
	}
	if s.blob != nil {
		return s.blob, s.blobStatus
	}
	return defaultBlob, s.blobStatus
}

// setBaseURL publishes the stub server's origin to the GetBundle handler.
func (s *stubCatalog) setBaseURL(u string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.baseURL = u
}

// defaultBlob is the content behind the stub's default blob URL.
var defaultBlob = []byte("blob")

func (s *stubCatalog) Resolve(_ context.Context, req *connect.Request[catalogv1.ResolveRequest]) (*connect.Response[catalogv1.ResolveResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gotResolve = req.Msg
	if s.resolveErr != nil {
		return nil, s.resolveErr
	}
	if s.resolveResp == nil {
		return connect.NewResponse(&catalogv1.ResolveResponse{}), nil
	}
	return connect.NewResponse(s.resolveResp), nil
}

func (s *stubCatalog) request() *catalogv1.GetBundleRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gotReq
}

// resolveRequest returns the last Resolve request the stub received.
func (s *stubCatalog) resolveRequest() *catalogv1.ResolveRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gotResolve
}

// newCatalogStub serves st over HTTP (plus a /blobs/ endpoint for the
// pre-signed blob URLs GetBundle hands out) and returns the hub base URL.
func newCatalogStub(t *testing.T, st *stubCatalog) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/blobs/", func(w http.ResponseWriter, r *http.Request) {
		body, status := st.blobPayload(strings.TrimPrefix(r.URL.Path, "/blobs/"))
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		_, _ = w.Write(body)
	})
	_, connectHandler := catalogv1connect.NewCatalogServiceHandler(st)
	mux.Handle("/", connectHandler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	st.setBaseURL(srv.URL)
	return srv.URL
}
