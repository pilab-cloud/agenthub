package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	catalogv1 "go.pilab.hu/agenthub/gen/go/agenthub/catalog/v1"
	catalogv1connect "go.pilab.hu/agenthub/gen/go/agenthub/catalog/v1/catalogv1connect"
	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"

	"go.pilab.hu/agenthub/internal/lockfile"
	"go.pilab.hu/agenthub/internal/version"
)

// updateOptions controls how update resolves the desired state.
type updateOptions struct {
	Hub        string       // hub base URL
	Offline    bool         // keep the existing pins; never touch the network
	HTTPClient *http.Client // transport; nil means a client with a default timeout
}

func newUpdateCmd() *cobra.Command {
	var (
		offline      bool
		hubFlag, cfg string
	)
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Re-resolve agenthub.json and rewrite agenthub.lock.json",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUpdate(cmd.Context(), cmd.OutOrStdout(), lockfile.DesiredFileName, lockfile.LockFileName,
				updateOptions{Hub: resolveHub(cmd.Context(), hubFlag, cfg), Offline: offline})
		},
	}
	cmd.Flags().BoolVar(&offline, "offline", false, "keep existing lock pins; never touch the network")
	cmd.Flags().StringVar(&hubFlag, "hub", "", "hub URL")
	cmd.Flags().StringVar(&cfg, "config", "", "path to config file")
	return cmd
}

// desiredRef converts a desired entry to a catalog ref; the version field
// carries the constraint.
func desiredRef(a lockfile.DesiredAsset) (*commonv1.AssetRef, error) {
	kind, err := parseKind(a.Kind)
	if err != nil {
		return nil, err
	}
	return &commonv1.AssetRef{Namespace: a.Namespace, Kind: kind, Name: a.Name, Version: a.Version}, nil
}

// runUpdate resolves the desired state and rewrites the lockfile. The lockfile
// is only written when every desired asset resolved.
func runUpdate(ctx context.Context, out io.Writer, desiredPath, lockPath string, opts updateOptions) error {
	desired, err := lockfile.ReadDesired(desiredPath)
	if err != nil {
		return err
	}
	if len(desired.Assets) == 0 {
		fmt.Fprintf(out, "no desired assets in %s; run `agenthub add <asset-ref>` first\n", lockfile.DesiredFileName)
		return nil
	}
	refs := make([]*commonv1.AssetRef, 0, len(desired.Assets))
	for _, a := range desired.Assets {
		ref, err := desiredRef(a)
		if err != nil {
			return err
		}
		refs = append(refs, ref)
	}

	if opts.Offline {
		return updateOffline(out, desired, lockPath)
	}

	locked, err := lockfile.ReadLocked(lockPath)
	if err != nil {
		return err
	}
	req := &catalogv1.ResolveRequest{Update: true}
	for _, ref := range refs {
		req.Desired = append(req.Desired, &catalogv1.DesiredAsset{Ref: ref})
	}
	for _, pin := range locked.Assets {
		ref, err := parseAssetRef(pin.Ref)
		if err != nil {
			return fmt.Errorf("lockfile pin %q: %w", pin.Ref, err)
		}
		req.Lock = append(req.Lock, &catalogv1.LockedAsset{
			Ref:            ref,
			OriginHub:      pin.OriginHub,
			ManifestDigest: parseDigest(pin.ManifestDigest),
			BundleDigest:   parseDigest(pin.BundleDigest),
		})
	}

	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	client := catalogv1connect.NewCatalogServiceClient(httpClient, normalizeHubURL(opts.Hub))
	resp, err := client.Resolve(ctx, connect.NewRequest(req))
	if err != nil {
		return fmt.Errorf("resolve: %w", err)
	}

	for _, w := range resp.Msg.GetWarnings() {
		line := fmt.Sprintf("warning: %s: %s", w.GetCode(), w.GetMessage())
		if w.GetRef() != nil {
			line += fmt.Sprintf(" (%s/%s/%s)", w.GetRef().GetNamespace(), kindString(w.GetRef().GetKind()), w.GetRef().GetName())
		}
		fmt.Fprintln(out, line)
	}

	resolved := make(map[string]struct{}, len(resp.Msg.GetAssets()))
	next := make([]lockfile.LockedAsset, 0, len(resp.Msg.GetAssets()))
	origin := hubHost(opts.Hub)
	for _, ra := range resp.Msg.GetAssets() {
		ref := assetRefString(ra.GetRef())
		resolved[identityOf(ra.GetRef())] = struct{}{}
		pin := lockfile.LockedAsset{
			Ref:            ref,
			OriginHub:      ra.GetOriginHub(),
			ManifestDigest: digestString(ra.GetManifestDigest()),
			BundleDigest:   digestString(ra.GetBundleDigest()),
		}
		if pin.OriginHub == "" {
			pin.OriginHub = origin
		}
		next = append(next, pin)
	}
	var unresolved []string
	for _, ref := range refs {
		if _, ok := resolved[identityOf(ref)]; !ok {
			unresolved = append(unresolved, identityOf(ref))
		}
	}
	if len(unresolved) > 0 {
		return fmt.Errorf("%w: cannot resolve %s", errResolution, strings.Join(unresolved, ", "))
	}

	for _, pin := range next {
		fmt.Fprintf(out, "updated %s\n", pin.Ref)
	}
	locked.Assets = next
	locked.LockfileVersion = 1
	locked.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	locked.Resolver = "agenthub/" + version.Version
	if err := lockfile.WriteLocked(lockPath, locked); err != nil {
		return err
	}
	fmt.Fprintf(out, "lockfile updated: assets=%d\n", len(next))
	return nil
}

// updateOffline keeps the existing pins of every still-desired asset and
// prunes the rest.
func updateOffline(out io.Writer, desired lockfile.DesiredState, lockPath string) error {
	exists, err := fileExists(lockPath)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("update --offline: %w", stateNotFound(lockPath))
	}
	locked, err := lockfile.ReadLocked(lockPath)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "offline; keeping existing lock pins")
	pinned := make(map[string]struct{}, len(locked.Assets))
	for _, a := range locked.Assets {
		pinned[a.Identity()] = struct{}{}
	}
	for _, a := range desired.Assets {
		if _, ok := pinned[a.Identity()]; !ok {
			fmt.Fprintf(out, "unpinned %s\n", a.Identity())
		}
	}
	locked = lockfile.PruneLocked(desired, locked)
	if err := lockfile.WriteLocked(lockPath, locked); err != nil {
		return err
	}
	fmt.Fprintf(out, "lockfile updated: assets=%d\n", len(locked.Assets))
	return nil
}

// identityOf is the versionless "namespace/kind/name" of ref.
func identityOf(ref *commonv1.AssetRef) string {
	return ref.GetNamespace() + "/" + kindString(ref.GetKind()) + "/" + ref.GetName()
}

func newRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <asset-ref>",
		Short: "Remove an asset from agenthub.json and prune the lockfile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRemove(cmd.OutOrStdout(), lockfile.DesiredFileName, lockfile.LockFileName, args[0])
		},
	}
}

// runRemove drops arg (a ref; any version is ignored) from the desired state
// and prunes the lockfile when there is one. Cached blobs are left alone.
func runRemove(out io.Writer, desiredPath, lockPath, arg string) error {
	ref, err := parseAssetRef(arg)
	if err != nil {
		return err
	}
	id := identityOf(ref)

	desired, err := lockfile.ReadDesired(desiredPath)
	if err != nil {
		return err
	}
	hasLock, err := fileExists(lockPath)
	if err != nil {
		return err
	}
	locked, err := lockfile.ReadLocked(lockPath)
	if err != nil {
		return err
	}
	desired, locked, found, err := lockfile.RemoveAsset(desired, locked, id)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("asset %s not found in %s", id, desiredPath)
	}
	if err := lockfile.WriteDesired(desiredPath, desired); err != nil {
		return err
	}
	fmt.Fprintf(out, "removed %s\n", id)
	if !hasLock {
		return nil
	}
	if err := lockfile.WriteLocked(lockPath, locked); err != nil {
		return err
	}
	fmt.Fprintf(out, "lockfile updated: assets=%d\n", len(locked.Assets))
	return nil
}

// addOptions controls `agenthub add`.
type addOptions struct {
	Agent     string // target harness recorded with the asset
	NoInstall bool   // skip installing into the cache
	Update    updateOptions
	Install   installOptions
}

func newAddCmd() *cobra.Command {
	var (
		opts         addOptions
		hubFlag, cfg string
	)
	cmd := &cobra.Command{
		Use:   "add <asset-ref>",
		Short: "Add an asset to agenthub.json, resolve it and install it",
		Long: strings.TrimSpace(`
Adds namespace/kind/name[@constraint] to agenthub.json (replacing any entry for
the same asset), resolves it into agenthub.lock.json and installs it into the
cache. Without a constraint any version is accepted. If resolution fails,
agenthub.json is left as it was.`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			hub := resolveHub(cmd.Context(), hubFlag, cfg)
			opts.Update.Hub, opts.Install.Hub = hub, hub
			opts.Install.Offline = opts.Update.Offline
			return runAdd(cmd.Context(), cmd.OutOrStdout(), lockfile.DesiredFileName, lockfile.LockFileName, args[0], opts)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&opts.NoInstall, "no-install", false, "record and resolve only; do not install into the cache")
	f.BoolVar(&opts.Update.Offline, "offline", false, "never touch the network")
	f.StringVar(&opts.Agent, "agent", "", "target harness/agent (e.g. opencode, claude, copilot)")
	f.StringVar(&opts.Install.CacheDir, "cache-dir", "", "cache directory (default: $AGENTHUB_CACHE_DIR or the user cache)")
	f.StringVar(&hubFlag, "hub", "", "hub URL")
	f.StringVar(&cfg, "config", "", "path to config file")
	return cmd
}

// runAdd upserts ref into the desired state, re-resolves, and installs. A failed
// resolution restores the previous desired state.
func runAdd(ctx context.Context, out io.Writer, desiredPath, lockPath, refArg string, opts addOptions) error {
	ref, err := parseAssetRef(refArg)
	if err != nil {
		return err
	}
	version := ref.GetVersion()
	if version == "" {
		version = "*"
	}
	entry := lockfile.DesiredAsset{
		Namespace: ref.GetNamespace(),
		Kind:      kindString(ref.GetKind()),
		Name:      ref.GetName(),
		Version:   version,
		Agent:     opts.Agent,
	}

	hadFile, err := fileExists(desiredPath)
	if err != nil {
		return err
	}
	prev, err := lockfile.ReadDesired(desiredPath)
	if err != nil {
		return err
	}
	next := prev
	next.Assets = make([]lockfile.DesiredAsset, 0, len(prev.Assets)+1)
	replaced := false
	for _, a := range prev.Assets {
		if a.Identity() == entry.Identity() {
			next.Assets = append(next.Assets, entry)
			replaced = true
			continue
		}
		next.Assets = append(next.Assets, a)
	}
	if !replaced {
		next.Assets = append(next.Assets, entry)
	}
	if err := lockfile.WriteDesired(desiredPath, next); err != nil {
		return err
	}
	rollback := func() {
		if hadFile {
			_ = lockfile.WriteDesired(desiredPath, prev)
		} else {
			_ = os.Remove(desiredPath)
		}
	}

	verb := "added"
	if replaced {
		verb = "updated"
	}
	if opts.Update.Offline {
		// Offline cannot resolve a new pin, so only a re-add of a pinned asset works.
		if exists, _ := fileExists(lockPath); !exists {
			rollback()
			return fmt.Errorf("add --offline: %w", stateNotFound(lockPath))
		}
	}
	if err := runUpdate(ctx, out, desiredPath, lockPath, opts.Update); err != nil {
		rollback()
		return err
	}
	fmt.Fprintf(out, "%s %s@%s in %s\n", verb, entry.Identity(), version, desiredPath)
	if opts.NoInstall {
		return nil
	}
	return runInstall(ctx, out, lockPath, false, opts.Install)
}
