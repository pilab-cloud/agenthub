package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	catalogv1 "go.pilab.hu/agenthub/gen/go/agenthub/catalog/v1"

	"go.pilab.hu/agenthub/internal/cache"
	"go.pilab.hu/agenthub/internal/lockfile"
	"go.pilab.hu/agenthub/internal/verify"
)

// maxBlobBytes bounds one blob download: a body that reaches it is rejected as
// truncated instead of being written into the cache. When the hub declares a
// size, the read stops one byte past it so an oversized body is caught too.
const maxBlobBytes = 256 << 20 // 256 MiB

// errBlobUnavailable marks a blob download that failed for transport reasons
// (missing URL, refused connection, non-2xx status, truncated body). Install
// reports it and keeps going; it is never a hard error on its own.
var errBlobUnavailable = errors.New("blob download unavailable")

// keyResolver returns the Ed25519 public key of a namespace signing key.
// M2.4 ships no key distribution — the Registry has no RPC that lists public
// keys and /.well-known/agenthub/keys.json does not exist yet — so install runs
// without one and only reports signatures. See verifyManifestSignature.
type keyResolver func(ctx context.Context, namespace, keyID string) (ed25519.PublicKey, error)

// fileStats counts the bundle files of an install run.
type fileStats struct {
	total   int
	fetched int
	cached  int
	missing int
}

// add accumulates one asset's file counters into the run totals.
func (s *fileStats) add(other fileStats) {
	s.total += other.total
	s.fetched += other.fetched
	s.cached += other.cached
	s.missing += other.missing
}

// downloadBlobs fetches every blob GetBundle handed out for asset, verifies
// each body against its declared sha256 and stores it in the content-addressed
// cache under that digest. Blobs already present are re-read (which re-verifies
// their bytes) instead of downloaded. Transport failures and blobs without a
// usable sha256 are reported and counted as missing; bytes that do not hash to
// their declared digest are a hard error, never a silent install. The returned
// set holds the digest keys whose bytes were verified, downloaded or cached.
func downloadBlobs(ctx context.Context, out io.Writer, c *cache.Cache, httpClient *http.Client,
	asset lockfile.LockedAsset, blobs []*catalogv1.BlobLocation,
) (fileStats, map[string]struct{}, error) {
	stats := fileStats{}
	verified := make(map[string]struct{}, len(blobs))
	for _, blob := range blobs {
		if blob == nil {
			continue
		}
		stats.total++
		path := blob.GetPath()

		digest := digestString(blob.GetDigest())
		if strings.TrimSpace(digest) == "" {
			fmt.Fprintf(out, "file %s for %s: no digest\n", path, asset.Ref)
			stats.missing++
			continue
		}
		key, err := cache.KeyFromDigest(digest)
		if err != nil {
			fmt.Fprintf(out, "file %s for %s: %v\n", path, asset.Ref, err)
			stats.missing++
			continue
		}
		if _, err := c.Get(key); err == nil {
			stats.cached++
			verified[key] = struct{}{}
			fmt.Fprintf(out, "cached file %s\n", path)
			continue
		}

		body, err := fetchBlob(ctx, httpClient, blob)
		if err != nil {
			if errors.Is(err, errBlobUnavailable) {
				fmt.Fprintf(out, "file unavailable %s for %s: %v\n", path, asset.Ref, err)
				stats.missing++
				continue
			}
			return stats, verified, fmt.Errorf("download file %s for %s: %w", path, asset.Ref, err)
		}
		// Put re-hashes the body and refuses anything that does not hash to key.
		if err := c.Put(key, body); err != nil {
			return stats, verified, fmt.Errorf("verify file %s for %s: %w", path, asset.Ref, err)
		}
		stats.fetched++
		verified[key] = struct{}{}
		fmt.Fprintf(out, "fetched file %s\n", path)
	}
	return stats, verified, nil
}

// fetchBlob GETs one blob. Transport-level problems — bad URL, refused
// connection, non-2xx status, body cut short by the read limit — come back
// wrapped in errBlobUnavailable so the caller prints them and continues; every
// other error (the read limit itself) is a hard failure.
func fetchBlob(ctx context.Context, httpClient *http.Client, blob *catalogv1.BlobLocation) ([]byte, error) {
	rawURL := strings.TrimSpace(blob.GetUrl())
	if rawURL == "" {
		return nil, fmt.Errorf("%w: no url", errBlobUnavailable)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errBlobUnavailable, err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errBlobUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("%w: %s", errBlobUnavailable, resp.Status)
	}
	limit := int64(maxBlobBytes)
	if size := blob.GetSize(); size > 0 && int64(size) < limit {
		limit = int64(size) + 1
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, fmt.Errorf("%w: read: %w", errBlobUnavailable, err)
	}
	if int64(len(body)) == limit {
		return nil, fmt.Errorf("blob %s is over the %d byte read limit", blob.GetPath(), limit)
	}
	return body, nil
}

// bundleDigestToVerify returns the bundle digest install has to verify: the
// lockfile pin when there is one, otherwise what the hub reports. A hub that
// disagrees with the pin is a hard verification error.
func bundleDigestToVerify(asset lockfile.LockedAsset, resp *catalogv1.GetBundleResponse) (string, error) {
	pinned := strings.TrimSpace(asset.BundleDigest)
	reported := strings.TrimSpace(digestString(resp.GetBundleDigest()))
	switch {
	case pinned == "":
		return reported, nil
	case reported == "":
		return pinned, nil
	}
	pinnedKey, pinnedErr := cache.KeyFromDigest(pinned)
	reportedKey, reportedErr := cache.KeyFromDigest(reported)
	if pinnedErr == nil && reportedErr == nil && pinnedKey != reportedKey {
		return "", fmt.Errorf("%w: bundle digest for %s: lockfile pins %s, hub reports %s",
			errVerification, asset.Ref, pinned, reported)
	}
	return pinned, nil
}

// reportBundleDigest states how the bundle digest fared. Per
// docs/architecture/domain-model.md §3.2 the bundle digest is sha256 over the
// bundle payload (the tar.zst archive), so it is verified exactly when the
// payload is one of the served blobs — downloadBlobs has already hashed those
// bytes — and reported as unverifiable otherwise. It is deliberately not
// derived from the file digests: that is not what the hub pins.
func reportBundleDigest(out io.Writer, asset lockfile.LockedAsset, expected string, verified map[string]struct{}) {
	if strings.TrimSpace(expected) == "" {
		return
	}
	key, err := cache.KeyFromDigest(expected)
	if err != nil {
		fmt.Fprintf(out, "bundle digest not verified for %s: %v\n", asset.Ref, err)
		return
	}
	if _, ok := verified[key]; ok {
		fmt.Fprintf(out, "verified bundle digest %s\n", asset.Ref)
		return
	}
	fmt.Fprintf(out, "bundle digest not verified for %s: bundle payload not available\n", asset.Ref)
}

// verifyManifestSignature checks the publisher signatures a manifest carries.
// Per docs/architecture/domain-model.md §3.2 a signature covers the canonical
// bytes of the manifest with "signatures" and "manifestDigest" removed.
//
// M2.4 has no way to fetch a namespace public key, so without a resolver in
// installOptions the signature is reported and skipped: only a signature that
// is present, has a retrievable key and fails to verify is a hard error. An
// install never falls back to unsigned silently — the report line says so.
func verifyManifestSignature(ctx context.Context, out io.Writer, opts installOptions,
	asset lockfile.LockedAsset, manifest []byte,
) error {
	sigs, err := verify.ManifestSignatures(manifest)
	if err != nil {
		return fmt.Errorf("manifest for %s: %w", asset.Ref, err)
	}
	if len(sigs) == 0 {
		// Unsigned manifests install in M2.4; requireSignature policy is M3.
		return nil
	}
	if opts.KeyResolver == nil {
		fmt.Fprintf(out, "signature unverified for %s: no public key source\n", asset.Ref)
		return nil
	}

	namespace := namespaceOfRef(asset.Ref)
	var (
		haveKey bool
		lastErr error
	)
	// One valid signature is enough: a namespace may hold several active keys.
	for _, sig := range sigs {
		pub, err := opts.KeyResolver(ctx, namespace, sig.GetKeyId())
		if err != nil {
			lastErr = fmt.Errorf("resolve key %s: %w", sig.GetKeyId(), err)
			continue
		}
		haveKey = true
		if err := verify.CheckSignature(manifest, sig, pub); err != nil {
			lastErr = err
			continue
		}
		fmt.Fprintf(out, "verified signature %s\n", asset.Ref)
		return nil
	}
	if !haveKey {
		fmt.Fprintf(out, "signature unverified for %s: %v\n", asset.Ref, lastErr)
		return nil
	}
	return fmt.Errorf("signature for %s: %w", asset.Ref, lastErr)
}

// namespaceOfRef extracts the namespace from a lockfile ref; refs the CLI
// cannot parse resolve to an empty namespace, which a key resolver may reject.
func namespaceOfRef(ref string) string {
	parsed, err := parseAssetRef(ref)
	if err != nil {
		return ""
	}
	return parsed.GetNamespace()
}
