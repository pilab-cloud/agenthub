package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"
)

// defaultHub is the public hub used when neither --hub nor AGENTHUB_HUB is set.
const defaultHub = "https://agenthub.pilab.hu"

// envHub overrides the default hub.
const envHub = "AGENTHUB_HUB"

// Exit codes, per docs/architecture/cli-plugin.md §2.1.
const (
	exitOK           = 0
	exitFailure      = 1
	exitUsage        = 2
	exitAuth         = 3
	exitResolution   = 4
	exitVerification = 5
	exitOffline      = 6
	exitDrift        = 7
	exitDenied       = 8
)

// assetKinds maps the CLI spelling of a kind (singular or plural) to its enum.
var assetKinds = map[string]commonv1.AssetKind{
	"agent":   commonv1.AssetKind_ASSET_KIND_AGENT,
	"skill":   commonv1.AssetKind_ASSET_KIND_SKILL,
	"tool":    commonv1.AssetKind_ASSET_KIND_TOOL,
	"prompt":  commonv1.AssetKind_ASSET_KIND_PROMPT,
	"pack":    commonv1.AssetKind_ASSET_KIND_PACK,
	"agents":  commonv1.AssetKind_ASSET_KIND_AGENT,
	"skills":  commonv1.AssetKind_ASSET_KIND_SKILL,
	"tools":   commonv1.AssetKind_ASSET_KIND_TOOL,
	"prompts": commonv1.AssetKind_ASSET_KIND_PROMPT,
	"packs":   commonv1.AssetKind_ASSET_KIND_PACK,
}

// parseKind converts a kind name (singular or plural) to its enum.
func parseKind(s string) (commonv1.AssetKind, error) {
	if k, ok := assetKinds[strings.ToLower(strings.TrimSpace(s))]; ok {
		return k, nil
	}
	return commonv1.AssetKind_ASSET_KIND_UNSPECIFIED, fmt.Errorf("unknown asset kind %q", s)
}

// kindString is the canonical singular spelling of k used in refs.
func kindString(k commonv1.AssetKind) string {
	switch k {
	case commonv1.AssetKind_ASSET_KIND_AGENT:
		return "agent"
	case commonv1.AssetKind_ASSET_KIND_SKILL:
		return "skill"
	case commonv1.AssetKind_ASSET_KIND_TOOL:
		return "tool"
	case commonv1.AssetKind_ASSET_KIND_PROMPT:
		return "prompt"
	case commonv1.AssetKind_ASSET_KIND_PACK:
		return "pack"
	default:
		return "unknown"
	}
}

// parseAssetRef parses "namespace/kind/name[@version]".
func parseAssetRef(s string) (*commonv1.AssetRef, error) {
	s = strings.TrimSpace(s)
	id, version, _ := strings.Cut(s, "@")
	parts := strings.Split(id, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return nil, fmt.Errorf("asset ref %q must be namespace/kind/name[@version]", s)
	}
	kind, err := parseKind(parts[1])
	if err != nil {
		return nil, err
	}
	return &commonv1.AssetRef{
		Namespace: parts[0],
		Kind:      kind,
		Name:      parts[2],
		Version:   version,
	}, nil
}

// assetRefString is the inverse of parseAssetRef.
func assetRefString(ref *commonv1.AssetRef) string {
	if ref == nil {
		return ""
	}
	s := ref.GetNamespace() + "/" + kindString(ref.GetKind()) + "/" + ref.GetName()
	if v := ref.GetVersion(); v != "" {
		s += "@" + v
	}
	return s
}

// parseDigest converts "algo:value" (or a bare value) into a Digest; an empty
// string yields nil.
func parseDigest(s string) *commonv1.Digest {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if algo, value, ok := strings.Cut(s, ":"); ok {
		return &commonv1.Digest{Algorithm: algo, Value: value}
	}
	return &commonv1.Digest{Value: s}
}

// digestString is the inverse of parseDigest.
func digestString(d *commonv1.Digest) string {
	if d == nil || d.GetValue() == "" {
		return ""
	}
	if d.GetAlgorithm() == "" {
		return d.GetValue()
	}
	return d.GetAlgorithm() + ":" + d.GetValue()
}

// normalizeHubURL turns a hub flag value into a base URL: a bare host gets an
// https scheme and trailing slashes are dropped.
func normalizeHubURL(hub string) string {
	hub = strings.TrimRight(strings.TrimSpace(hub), "/")
	if hub != "" && !strings.Contains(hub, "://") {
		hub = "https://" + hub
	}
	return hub
}

// hubHost returns the host[:port] of a hub URL; a value that does not parse as
// a URL is returned unchanged.
func hubHost(hub string) string {
	u, err := url.Parse(normalizeHubURL(hub))
	if err != nil || u.Host == "" {
		return strings.TrimSpace(hub)
	}
	return u.Host
}

// resolveHub picks the hub: the --hub flag, then $AGENTHUB_HUB, then the public
// hub.
func resolveHub(_ context.Context, flag, _ string) string {
	if v := strings.TrimSpace(flag); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv(envHub)); v != "" {
		return v
	}
	return defaultHub
}

// stateNotFound reports a missing state file.
func stateNotFound(path string) error {
	return fmt.Errorf("%s not found", path)
}

// fileExists reports whether path exists; any stat error other than
// not-exist is returned.
func fileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, err
	}
}
