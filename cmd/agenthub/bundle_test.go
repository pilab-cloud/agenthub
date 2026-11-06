package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	catalogv1 "go.pilab.hu/agenthub/gen/go/agenthub/catalog/v1"
	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"

	"go.pilab.hu/agenthub/internal/cache"
	"go.pilab.hu/agenthub/internal/lockfile"
	"go.pilab.hu/agenthub/internal/verify"
)

func TestRunInstallDownloadsAndVerifiesBlobs(t *testing.T) {
	t.Parallel()

	manifest := []byte(`{"name":"code-reviewer","version":"1.4.2"}`)
	prompt := []byte("You are a code reviewer.")
	guide := []byte("# Install guide")
	stub := &stubCatalog{
		manifest: manifest,
		blobs: map[string][]byte{
			"prompt.md":          prompt,
			"guides/opencode.md": guide,
		},
		files: []*catalogv1.BlobLocation{
			{
				Path:   "prompt.md",
				Digest: &commonv1.Digest{Algorithm: "sha256", Value: cache.Key(prompt)},
				Size:   uint64(len(prompt)),
			},
			{
				Path:   "guides/opencode.md",
				Digest: &commonv1.Digest{Algorithm: "sha256", Value: cache.Key(guide)},
			},
		},
	}
	hub := newCatalogStub(t, stub)
	path := writeLockfile(t, lockfile.LockedAsset{
		Ref:            "pilab/agent/code-reviewer@1.4.2",
		ManifestDigest: "sha256:" + cache.Key(manifest),
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
		"fetched file prompt.md",
		"fetched file guides/opencode.md",
		"files: total=2 fetched=2 cached=0 missing=0",
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
	for path, wantBody := range map[string][]byte{"prompt.md": prompt, "guides/opencode.md": guide} {
		key := cache.Key(wantBody)
		body, err := c.Get(key)
		if err != nil {
			t.Fatalf("read cached blob %s: %v", path, err)
		}
		if !bytes.Equal(body, wantBody) {
			t.Fatalf("cached blob %s = %q, want %q", path, body, wantBody)
		}
	}
}

func TestRunInstallServesBlobsFromCache(t *testing.T) {
	t.Parallel()

	manifest := []byte(`{"name":"code-reviewer"}`)
	stub := &stubCatalog{manifest: manifest}
	hub := newCatalogStub(t, stub)
	cacheDir := t.TempDir()
	c, err := cache.New(cacheDir)
	if err != nil {
		t.Fatalf("cache: %v", err)
	}
	// The blob is already there; only the manifest has to be fetched.
	if err := c.Put(cache.Key(defaultBlob), defaultBlob); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	path := writeLockfile(t, lockfile.LockedAsset{
		Ref:            "pilab/agent/code-reviewer@1.4.2",
		ManifestDigest: "sha256:" + cache.Key(manifest),
	})

	var out bytes.Buffer
	opts := installOptions{Hub: hub, CacheDir: cacheDir}
	if err := runInstall(context.Background(), &out, path, false, opts); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !strings.Contains(out.String(), "cached file files/readme.md") {
		t.Fatalf("output missing cached file line:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "files: total=1 fetched=0 cached=1 missing=0") {
		t.Fatalf("output missing files summary:\n%s", out.String())
	}
}

func TestRunInstallRejectsBlobDigestMismatch(t *testing.T) {
	t.Parallel()

	manifest := []byte(`{"name":"code-reviewer"}`)
	// The blob body does not hash to the digest the hub declared for it.
	stub := &stubCatalog{manifest: manifest, blob: []byte("tampered blob")}
	hub := newCatalogStub(t, stub)
	path := writeLockfile(t, lockfile.LockedAsset{
		Ref:            "pilab/agent/code-reviewer@1.4.2",
		ManifestDigest: "sha256:" + cache.Key(manifest),
	})

	var out bytes.Buffer
	opts := installOptions{Hub: hub, CacheDir: t.TempDir()}
	err := runInstall(context.Background(), &out, path, false, opts)
	if err == nil {
		t.Fatal("expected blob digest mismatch error")
	}
	if !errors.Is(err, cache.ErrDigestMismatch) {
		t.Fatalf("error = %v, want cache.ErrDigestMismatch", err)
	}
	if code := exitCode(err); code != exitVerification {
		t.Fatalf("exit code = %d, want %d", code, exitVerification)
	}
}

func TestRunInstallReportsUnavailableBlob(t *testing.T) {
	t.Parallel()

	manifest := []byte(`{"name":"code-reviewer"}`)
	stub := &stubCatalog{manifest: manifest, blobStatus: http.StatusBadGateway}
	hub := newCatalogStub(t, stub)
	path := writeLockfile(t, lockfile.LockedAsset{
		Ref:            "pilab/agent/code-reviewer@1.4.2",
		ManifestDigest: "sha256:" + cache.Key(manifest),
	})

	var out bytes.Buffer
	opts := installOptions{Hub: hub, CacheDir: t.TempDir()}
	if err := runInstall(context.Background(), &out, path, false, opts); err != nil {
		t.Fatalf("a blob the hub cannot serve must not fail the run: %v", err)
	}
	if !strings.Contains(out.String(), "file unavailable files/readme.md for pilab/agent/code-reviewer@1.4.2") {
		t.Fatalf("output missing unavailable file line:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "files: total=1 fetched=0 cached=0 missing=1") {
		t.Fatalf("output missing files summary:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "summary: total=1 fetched=1 cached=0 missing=0") {
		t.Fatalf("output missing summary:\n%s", out.String())
	}
}

func TestRunInstallFrozenFailsOnIncompleteBundle(t *testing.T) {
	t.Parallel()

	manifest := []byte(`{"name":"code-reviewer"}`)
	stub := &stubCatalog{manifest: manifest, blobStatus: http.StatusNotFound}
	hub := newCatalogStub(t, stub)
	path := writeLockfile(t, lockfile.LockedAsset{
		Ref:            "pilab/agent/code-reviewer@1.4.2",
		ManifestDigest: "sha256:" + cache.Key(manifest),
	})

	var out bytes.Buffer
	opts := installOptions{Hub: hub, CacheDir: t.TempDir()}
	err := runInstall(context.Background(), &out, path, true, opts)
	if err == nil {
		t.Fatal("expected --frozen to fail on an incomplete bundle")
	}
	if !strings.Contains(err.Error(), "incomplete install") {
		t.Fatalf("error = %v, want incomplete install", err)
	}
	// The hub itself answered, so the network was available: generic failure.
	if code := exitCode(err); code != exitFailure {
		t.Fatalf("exit code = %d, want %d", code, exitFailure)
	}
}

func TestRunInstallVerifiesBundleDigest(t *testing.T) {
	t.Parallel()

	manifest := []byte(`{"name":"code-reviewer"}`)
	// The default blob is the bundle payload: its digest is the bundle digest.
	stub := &stubCatalog{manifest: manifest}
	hub := newCatalogStub(t, stub)
	path := writeLockfile(t, lockfile.LockedAsset{
		Ref:            "pilab/agent/code-reviewer@1.4.2",
		ManifestDigest: "sha256:" + cache.Key(manifest),
		BundleDigest:   "sha256:" + cache.Key(defaultBlob),
	})

	var out bytes.Buffer
	opts := installOptions{Hub: hub, CacheDir: t.TempDir()}
	if err := runInstall(context.Background(), &out, path, false, opts); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !strings.Contains(out.String(), "verified bundle digest pilab/agent/code-reviewer@1.4.2") {
		t.Fatalf("output missing bundle digest line:\n%s", out.String())
	}
}

func TestRunInstallReportsUnverifiableBundleDigest(t *testing.T) {
	t.Parallel()

	manifest := []byte(`{"name":"code-reviewer"}`)
	stub := &stubCatalog{manifest: manifest}
	hub := newCatalogStub(t, stub)
	path := writeLockfile(t, lockfile.LockedAsset{
		Ref:            "pilab/agent/code-reviewer@1.4.2",
		ManifestDigest: "sha256:" + cache.Key(manifest),
		// Pinned digest of a payload the hub does not serve: per domain-model
		// §3.2 it cannot be recomputed from the file digests, so it is reported.
		BundleDigest: "sha256:" + cache.Key([]byte("tar.zst payload")),
	})

	var out bytes.Buffer
	opts := installOptions{Hub: hub, CacheDir: t.TempDir()}
	if err := runInstall(context.Background(), &out, path, false, opts); err != nil {
		t.Fatalf("install: %v", err)
	}
	want := "bundle digest not verified for pilab/agent/code-reviewer@1.4.2: bundle payload not available"
	if !strings.Contains(out.String(), want) {
		t.Fatalf("output missing %q:\n%s", want, out.String())
	}
}

func TestRunInstallRejectsConflictingBundleDigest(t *testing.T) {
	t.Parallel()

	manifest := []byte(`{"name":"code-reviewer"}`)
	stub := &stubCatalog{
		manifest:     manifest,
		bundleDigest: &commonv1.Digest{Algorithm: "sha256", Value: cache.Key(defaultBlob)},
	}
	hub := newCatalogStub(t, stub)
	path := writeLockfile(t, lockfile.LockedAsset{
		Ref:            "pilab/agent/code-reviewer@1.4.2",
		ManifestDigest: "sha256:" + cache.Key(manifest),
		BundleDigest:   "sha256:" + cache.Key([]byte("something else")),
	})

	var out bytes.Buffer
	opts := installOptions{Hub: hub, CacheDir: t.TempDir()}
	err := runInstall(context.Background(), &out, path, false, opts)
	if err == nil {
		t.Fatal("expected a bundle digest conflict to fail the install")
	}
	if !errors.Is(err, errVerification) {
		t.Fatalf("error = %v, want errVerification", err)
	}
	if code := exitCode(err); code != exitVerification {
		t.Fatalf("exit code = %d, want %d", code, exitVerification)
	}
}

func TestRunInstallVerifiesManifestSignature(t *testing.T) {
	t.Parallel()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	manifest := signedManifest(t, priv, "ed25519:9f2c", map[string]any{
		"name":    "code-reviewer",
		"version": "1.4.2",
	})
	stub := &stubCatalog{manifest: manifest}
	hub := newCatalogStub(t, stub)
	path := writeLockfile(t, lockfile.LockedAsset{
		Ref:            "pilab/agent/code-reviewer@1.4.2",
		ManifestDigest: "sha256:" + cache.Key(manifest),
	})

	var out bytes.Buffer
	opts := installOptions{
		Hub:      hub,
		CacheDir: t.TempDir(),
		KeyResolver: func(_ context.Context, namespace, keyID string) (ed25519.PublicKey, error) {
			if namespace != "pilab" {
				t.Errorf("namespace = %q, want pilab", namespace)
			}
			if keyID != "ed25519:9f2c" {
				t.Errorf("key id = %q, want ed25519:9f2c", keyID)
			}
			return pub, nil
		},
	}
	if err := runInstall(context.Background(), &out, path, false, opts); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !strings.Contains(out.String(), "verified signature pilab/agent/code-reviewer@1.4.2") {
		t.Fatalf("output missing signature line:\n%s", out.String())
	}
}

func TestRunInstallRejectsInvalidManifestSignature(t *testing.T) {
	t.Parallel()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	wrongPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	manifest := signedManifest(t, priv, "ed25519:9f2c", map[string]any{"name": "code-reviewer"})
	stub := &stubCatalog{manifest: manifest}
	hub := newCatalogStub(t, stub)
	path := writeLockfile(t, lockfile.LockedAsset{
		Ref:            "pilab/agent/code-reviewer@1.4.2",
		ManifestDigest: "sha256:" + cache.Key(manifest),
	})

	var out bytes.Buffer
	opts := installOptions{
		Hub:         hub,
		CacheDir:    t.TempDir(),
		KeyResolver: func(context.Context, string, string) (ed25519.PublicKey, error) { return wrongPub, nil },
	}
	err = runInstall(context.Background(), &out, path, false, opts)
	if err == nil {
		t.Fatal("expected an invalid signature to fail the install")
	}
	if !errors.Is(err, verify.ErrInvalidSignature) {
		t.Fatalf("error = %v, want verify.ErrInvalidSignature", err)
	}
	if code := exitCode(err); code != exitVerification {
		t.Fatalf("exit code = %d, want %d", code, exitVerification)
	}
}

func TestRunInstallReportsSignatureWithoutKeySource(t *testing.T) {
	t.Parallel()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	manifest := signedManifest(t, priv, "ed25519:9f2c", map[string]any{"name": "code-reviewer"})
	stub := &stubCatalog{manifest: manifest}
	hub := newCatalogStub(t, stub)
	path := writeLockfile(t, lockfile.LockedAsset{
		Ref:            "pilab/agent/code-reviewer@1.4.2",
		ManifestDigest: "sha256:" + cache.Key(manifest),
	})

	// M2.4 default: no key distribution, so the signature is reported, not
	// verified — and never a silent pass.
	var out bytes.Buffer
	opts := installOptions{Hub: hub, CacheDir: t.TempDir()}
	if err := runInstall(context.Background(), &out, path, false, opts); err != nil {
		t.Fatalf("install: %v", err)
	}
	want := "signature unverified for pilab/agent/code-reviewer@1.4.2: no public key source"
	if !strings.Contains(out.String(), want) {
		t.Fatalf("output missing %q:\n%s", want, out.String())
	}

	// The same holds when the signed manifest comes from the cache.
	out.Reset()
	opts.Offline = true
	opts.Hub = ""
	if err := runInstall(context.Background(), &out, path, false, opts); err != nil {
		t.Fatalf("offline install: %v", err)
	}
	if !strings.Contains(out.String(), want) || !strings.Contains(out.String(), "cached manifest") {
		t.Fatalf("output missing cached signature report:\n%s", out.String())
	}
}

func TestRunInstallReportsSignatureKeyLookupFailure(t *testing.T) {
	t.Parallel()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	manifest := signedManifest(t, priv, "ed25519:9f2c", map[string]any{"name": "code-reviewer"})
	stub := &stubCatalog{manifest: manifest}
	hub := newCatalogStub(t, stub)
	path := writeLockfile(t, lockfile.LockedAsset{
		Ref:            "pilab/agent/code-reviewer@1.4.2",
		ManifestDigest: "sha256:" + cache.Key(manifest),
	})

	var out bytes.Buffer
	opts := installOptions{
		Hub:      hub,
		CacheDir: t.TempDir(),
		KeyResolver: func(context.Context, string, string) (ed25519.PublicKey, error) {
			return nil, errors.New("keys endpoint not implemented")
		},
	}
	if err := runInstall(context.Background(), &out, path, false, opts); err != nil {
		t.Fatalf("a key we cannot fetch must not fail the install: %v", err)
	}
	if !strings.Contains(out.String(), "signature unverified for pilab/agent/code-reviewer@1.4.2") {
		t.Fatalf("output missing signature report:\n%s", out.String())
	}
}

func TestExitCode(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{name: "nil", err: nil, want: 0},
		{name: "generic", err: errors.New("boom"), want: exitFailure},
		{name: "verification", err: errVerification, want: exitVerification},
		{name: "manifest digest", err: fmt.Errorf("cache manifest: %w", cache.ErrDigestMismatch), want: exitVerification},
		{name: "signature", err: fmt.Errorf("signature for x: %w", verify.ErrInvalidSignature), want: exitVerification},
		{name: "offline", err: fmt.Errorf("--frozen: %w", errOfflineIncomplete), want: exitOffline},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := exitCode(tc.err); got != tc.want {
				t.Fatalf("exitCode(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

// signedManifest returns a manifest carrying one Ed25519 signature over the
// canonical unsigned form of payload, the way `agenthub pack` writes it.
func signedManifest(t *testing.T, priv ed25519.PrivateKey, keyID string, payload map[string]any) []byte {
	t.Helper()
	unsigned, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	canonical, err := verify.CanonicalManifest(unsigned)
	if err != nil {
		t.Fatalf("canonical manifest: %v", err)
	}
	doc := make(map[string]any, len(payload)+1)
	for k, v := range payload {
		doc[k] = v
	}
	doc["signatures"] = []map[string]any{{
		"keyId":     keyID,
		"algorithm": "ed25519",
		"value":     base64.StdEncoding.EncodeToString(ed25519.Sign(priv, canonical)),
		"signedAt":  "2026-11-04T10:00:00Z",
	}}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal signed manifest: %v", err)
	}
	return out
}
