package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	catalogv1 "go.pilab.hu/agenthub/gen/go/agenthub/catalog/v1"
	catalogv1connect "go.pilab.hu/agenthub/gen/go/agenthub/catalog/v1/catalogv1connect"

	"go.pilab.hu/agenthub/internal/cache"
	"go.pilab.hu/agenthub/internal/federation"
	"go.pilab.hu/agenthub/internal/lockfile"
	"go.pilab.hu/agenthub/internal/verify"
)

// defaultHTTPTimeout bounds hub calls and blob downloads of install and update.
const defaultHTTPTimeout = 30 * time.Second

var (
	// errVerification marks a digest, signature or policy failure (exit 5).
	errVerification = errors.New("verification failed")
	// errOfflineIncomplete marks an install that needed the network but could
	// not have it (exit 6).
	errOfflineIncomplete = errors.New("network unavailable and cache incomplete")
	// errResolution marks a desired asset the hub could not resolve (exit 4).
	errResolution = errors.New("resolution failed")
)

// exitCode maps an error to the CLI exit code of cli-plugin.md §2.1.
func exitCode(err error) int {
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, errVerification),
		errors.Is(err, cache.ErrDigestMismatch),
		errors.Is(err, verify.ErrInvalidSignature),
		errors.Is(err, federation.ErrKeyMismatch):
		return exitVerification
	case errors.Is(err, errOfflineIncomplete):
		return exitOffline
	case errors.Is(err, errResolution):
		return exitResolution
	default:
		return exitFailure
	}
}

// installOptions controls where install reads from and writes to. The zero
// value installs offline from the default cache.
type installOptions struct {
	Hub         string       // hub base URL; empty means offline
	CacheDir    string       // cache directory; empty means cache.DefaultDir()
	Offline     bool         // never touch the network
	HTTPClient  *http.Client // transport; nil means a client with a default timeout
	KeyResolver keyResolver  // namespace key source; nil reports signatures only
}

func newInstallCmd() *cobra.Command {
	var (
		frozen, offline        bool
		hubFlag, cacheDir, cfg string
	)
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the assets pinned in agenthub.lock.json into the cache",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts := installOptions{
				Hub:      resolveHub(cmd.Context(), hubFlag, cfg),
				CacheDir: cacheDir,
				Offline:  offline,
			}
			return runInstall(cmd.Context(), cmd.OutOrStdout(), lockfile.LockFileName, frozen, opts)
		},
	}
	cmd.Flags().BoolVar(&frozen, "frozen", false, "fail unless every pinned asset is fully installed")
	cmd.Flags().BoolVar(&offline, "offline", false, "never touch the network; use the cache only")
	cmd.Flags().StringVar(&hubFlag, "hub", "", "hub URL")
	cmd.Flags().StringVar(&cacheDir, "cache-dir", "", "cache directory (default: $AGENTHUB_CACHE_DIR or the user cache)")
	cmd.Flags().StringVar(&cfg, "config", "", "path to config file")
	return cmd
}

// runInstall installs every asset pinned in the lockfile at lockPath into the
// content-addressed cache. A hub that cannot serve a bundle downgrades the run
// to cache-only instead of failing it; with frozen set, anything not fully
// installed is an error.
func runInstall(ctx context.Context, out io.Writer, lockPath string, frozen bool, opts installOptions) error {
	exists, err := fileExists(lockPath)
	if err != nil {
		return err
	}
	if !exists {
		return stateNotFound(lockPath)
	}
	locked, err := lockfile.ReadLocked(lockPath)
	if err != nil {
		return err
	}
	if frozen && len(locked.Assets) == 0 {
		return fmt.Errorf("--frozen: lockfile %s has no assets: %w", lockPath, errOfflineIncomplete)
	}

	cacheDir := opts.CacheDir
	if cacheDir == "" {
		cacheDir = cache.DefaultDir()
	}
	c, err := cache.New(cacheDir)
	if err != nil {
		return fmt.Errorf("open cache: %w", err)
	}

	offline := opts.Offline || strings.TrimSpace(opts.Hub) == ""
	var client catalogv1connect.CatalogServiceClient
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	if offline {
		fmt.Fprintln(out, "offline; using cache")
	} else {
		client = catalogv1connect.NewCatalogServiceClient(httpClient, normalizeHubURL(opts.Hub))
	}

	var manifests, files fileStats
	cacheOnly := offline
	for _, asset := range locked.Assets {
		manifests.total++
		fmt.Fprintf(out, "installing %s\n", asset.Ref)

		if !cacheOnly {
			done, err := installFromHub(ctx, out, c, client, httpClient, opts, asset, &manifests, &files)
			if err != nil {
				return err
			}
			if done {
				continue
			}
			cacheOnly = true
			fmt.Fprintln(out, "offline; using cache")
		}
		if err := installFromCache(ctx, out, c, opts, asset, &manifests); err != nil {
			return err
		}
	}

	fmt.Fprintf(out, "summary: total=%d fetched=%d cached=%d missing=%d\n",
		manifests.total, manifests.fetched, manifests.cached, manifests.missing)

	if frozen {
		switch {
		case manifests.missing > 0 && cacheOnly:
			return fmt.Errorf("--frozen: %d of %d assets not installable: %w",
				manifests.missing, manifests.total, errOfflineIncomplete)
		case manifests.missing > 0 || files.missing > 0:
			return fmt.Errorf("--frozen: incomplete install: %d assets and %d files missing",
				manifests.missing, files.missing)
		}
	}
	return nil
}

// installFromHub fetches one asset through GetBundle. It reports false, with no
// error, when the hub cannot serve the bundle (unreachable, unimplemented...)
// so the caller can fall back to the cache. Verification failures are errors.
func installFromHub(ctx context.Context, out io.Writer, c *cache.Cache, client catalogv1connect.CatalogServiceClient,
	httpClient *http.Client, opts installOptions, asset lockfile.LockedAsset, manifests, files *fileStats,
) (bool, error) {
	req := &catalogv1.GetBundleRequest{}
	if d := parseDigest(asset.ManifestDigest); d != nil {
		req.Selector = &catalogv1.GetBundleRequest_ManifestDigest{ManifestDigest: d}
	} else {
		ref, err := parseAssetRef(asset.Ref)
		if err != nil {
			return false, err
		}
		req.Selector = &catalogv1.GetBundleRequest_Ref{Ref: ref}
	}
	resp, err := client.GetBundle(ctx, connect.NewRequest(req))
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, nil
	}
	manifest := resp.Msg.GetManifest()

	key := cache.Key(manifest)
	if strings.TrimSpace(asset.ManifestDigest) != "" {
		if key, err = cache.KeyFromDigest(asset.ManifestDigest); err != nil {
			return false, fmt.Errorf("manifest digest for %s: %w", asset.Ref, err)
		}
	}
	// Put re-hashes the manifest and refuses anything that does not hash to key.
	if err := c.Put(key, manifest); err != nil {
		return false, fmt.Errorf("cache manifest for %s: %w", asset.Ref, err)
	}
	fmt.Fprintf(out, "fetched manifest %s\n", asset.Ref)
	manifests.fetched++

	if err := verifyManifestSignature(ctx, out, opts, asset, manifest); err != nil {
		return false, err
	}
	expected, err := bundleDigestToVerify(asset, resp.Msg)
	if err != nil {
		return false, err
	}
	stats, verified, err := downloadBlobs(ctx, out, c, httpClient, asset, resp.Msg.GetFiles())
	if err != nil {
		return false, err
	}
	if stats.total > 0 {
		fmt.Fprintf(out, "files: total=%d fetched=%d cached=%d missing=%d\n",
			stats.total, stats.fetched, stats.cached, stats.missing)
	}
	files.add(stats)
	reportBundleDigest(out, asset, expected, verified)
	return true, nil
}

// installFromCache checks one asset against the cache only.
func installFromCache(ctx context.Context, out io.Writer, c *cache.Cache, opts installOptions,
	asset lockfile.LockedAsset, manifests *fileStats,
) error {
	if strings.TrimSpace(asset.ManifestDigest) == "" {
		fmt.Fprintf(out, "no manifest digest for %s\n", asset.Ref)
		manifests.missing++
		return nil
	}
	key, err := cache.KeyFromDigest(asset.ManifestDigest)
	if err != nil {
		return fmt.Errorf("manifest digest for %s: %w", asset.Ref, err)
	}
	manifest, err := c.Get(key)
	if err != nil {
		if errors.Is(err, cache.ErrNotFound) {
			fmt.Fprintf(out, "not cached %s\n", asset.Ref)
			manifests.missing++
			return nil
		}
		return fmt.Errorf("cache manifest for %s: %w", asset.Ref, err)
	}
	if err := verifyManifestSignature(ctx, out, opts, asset, manifest); err != nil {
		return err
	}
	fmt.Fprintf(out, "cached manifest %s\n", asset.Ref)
	manifests.cached++
	return nil
}
