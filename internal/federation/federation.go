// Package federation implements the client half of hub federation (M4.2):
// an UpstreamClient that speaks FederationService to a remote hub, verifies
// the Ed25519 signatures the hub puts on catalog snapshots and sync pages,
// and returns the catalog membership those signatures commit to. It also
// holds the local upstream configuration file (agenthub.federation.json)
// that `agenthub federate add` writes.
//
// Verification follows docs/architecture/federation.md §4 and §6: the hub
// key authenticates catalog membership, not asset authorship; a bootstrap is
// only accepted when the whole page walk chains back to the signed snapshot
// root digest; and the key is either pinned out of band (WithPinnedKey or
// `--fingerprint`) or trusted on first use.
//
// The signed-document rendering lives in verify.go as a peer-side copy of
// internal/api/federation.go: a federation peer re-derives the signed bytes
// independently instead of sharing the signer's code, and the interop tests
// pin the two implementations together.
package federation

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	federationv1 "go.pilab.hu/agenthub/gen/go/agenthub/federation/v1"
	federationv1connect "go.pilab.hu/agenthub/gen/go/agenthub/federation/v1/federationv1connect"
)

// Sentinel errors returned by the upstream client. The CLI maps the trust
// and integrity failures onto the verification exit code (5).
var (
	// ErrNoPublicKey is returned when a hub publishes no usable Ed25519 key.
	ErrNoPublicKey = errors.New("federation: no usable hub public key")
	// ErrKeyMismatch is returned when the key a hub advertises (or the
	// fingerprint derived from it) does not match the pinned one.
	ErrKeyMismatch = errors.New("federation: hub key does not match the pinned key")
	// ErrHubIDMismatch is returned when a hub identifies itself differently
	// from the pinned hub id: the hub id, not the URL, is the trust anchor
	// (federation.md §3.1).
	ErrHubIDMismatch = errors.New("federation: hub id does not match the pinned id")
	// ErrUnsupportedAlgorithm is returned for signing algorithms other than
	// ed25519.
	ErrUnsupportedAlgorithm = errors.New("federation: unsupported signing algorithm")
	// ErrProtocol is returned for a federation protocol major version this
	// client does not speak.
	ErrProtocol = errors.New("federation: unsupported federation protocol")
	// ErrSignature is returned when a snapshot or page signature does not
	// verify against the trusted hub key.
	ErrSignature = errors.New("federation: signature verification failed")
	// ErrDigest is returned when the prevDigest chain, the counts or the
	// recomputed root digest disagree with the signed snapshot.
	ErrDigest = errors.New("federation: digest chain verification failed")
	// ErrGenerationGap is returned when a page carries another catalog
	// generation than the snapshot the walk started from.
	ErrGenerationGap = errors.New("federation: catalog generation changed during sync")
	// ErrTooManyPages is returned when a walk does not terminate within
	// MaxPages pages.
	ErrTooManyPages = errors.New("federation: sync exceeded the page limit")
)

const (
	// DefaultTimeout bounds a single federation RPC, so an unreachable hub
	// fails the sync instead of hanging it.
	DefaultTimeout = 15 * time.Second
	// MaxPages caps one bootstrap walk, the federation.md §7.2 default: a
	// runaway upstream cannot starve the caller forever.
	MaxPages = 1000
	// protocolVersion is the federation protocol major version this client
	// speaks; a hub that reports anything else aborts the sync.
	protocolVersion = 1
	// signingAlgorithm is the only algorithm a hub key is accepted in.
	signingAlgorithm = "ed25519"
	// schemaVersion is stamped into every signed document.
	schemaVersion = 1
)

// HubInfo is the verified identity of a remote hub: what GetHubInfo
// returned, plus the identifiers derived from its key.
type HubInfo struct {
	// ID is the hub's stable identity; the trust anchor of federation.md §3.1.
	ID string
	// BaseURL is the base URL the hub advertises (it may change on
	// migration; the ID is what must not).
	BaseURL string
	// Protocol is the federation protocol major version the hub speaks.
	Protocol uint32
	// PublicKey is the Ed25519 key that signs snapshots and pages.
	PublicKey ed25519.PublicKey
	// KeyID is the signature key id the hub stamps on its signatures
	// ("ed25519:<16 hex chars>").
	KeyID string
	// Fingerprint is the sha256 of the public key ("sha256:<64 hex chars>"),
	// the value an operator pins out of band (federation.md §6.2).
	Fingerprint string
}

// KeyID derives the key identifier a hub stamps on its signatures: the
// first 8 bytes of sha256(publicKey), hex encoded ("ed25519:9f2c…").
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return "ed25519:" + hex.EncodeToString(sum[:8])
}

// Fingerprint returns the out-of-band pin of a hub key (federation.md §6.2):
// "sha256:" plus the hex-encoded sha256 of the Ed25519 public key.
func Fingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ParseFingerprint normalizes a pinned key fingerprint to "sha256:<hex>".
// Both the prefixed form and the bare 64-character hex digest are accepted.
func ParseFingerprint(s string) (string, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "sha256:")
	if len(s) != sha256.Size*2 {
		return "", fmt.Errorf("federation: fingerprint %q is not a sha256 digest of %d hex characters",
			s, sha256.Size*2)
	}
	decoded, err := hex.DecodeString(s)
	if err != nil || len(decoded) != sha256.Size {
		return "", fmt.Errorf("federation: fingerprint %q is not a sha256 digest of %d hex characters",
			s, sha256.Size*2)
	}
	return "sha256:" + strings.ToLower(s), nil
}

// ParsePublicKey decodes a textual Ed25519 public key: base64 in the four
// encodings Go ships, then hex, all of a 32-byte key. A hex key would be
// swallowed by base64 only when the decode yields the key size, so hex is
// tried after every base64 variant has failed the length check.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	trimmed := strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		if decoded, err := enc.DecodeString(trimmed); err == nil &&
			len(decoded) == ed25519.PublicKeySize {
			return ed25519.PublicKey(decoded), nil
		}
	}
	if decoded, err := hex.DecodeString(trimmed); err == nil && len(decoded) == ed25519.PublicKeySize {
		return ed25519.PublicKey(decoded), nil
	}
	return nil, fmt.Errorf("federation: %q is not a %d-byte ed25519 public key (base64 or hex)",
		s, ed25519.PublicKeySize)
}

// UpstreamClient is a federation client bound to one remote hub: it dials
// the hub, learns (or pins) its key and verifies every signed document the
// hub returns before handing it to the caller.
type UpstreamClient struct {
	baseURL    string
	httpClient *http.Client
	client     federationv1connect.FederationServiceClient

	// pins are the out-of-band trust anchors; a zero pin trusts whatever the
	// hub advertises on first use.
	pinnedKey   ed25519.PublicKey
	pinnedHubID string
	// pageSize is the `limit` sent with every SyncChanges request; 0 lets
	// the hub apply its default.
	pageSize uint32

	// mu guards the cached hub identity: concurrent syncs fetch it once.
	mu  sync.RWMutex
	hub *HubInfo
}

// Option configures an UpstreamClient.
type Option func(*UpstreamClient)

// WithHTTPClient sets the transport used for federation RPCs. The default
// is a client with DefaultTimeout.
func WithHTTPClient(h *http.Client) Option {
	return func(c *UpstreamClient) {
		if h != nil {
			c.httpClient = h
		}
	}
}

// WithPinnedKey pins the hub's Ed25519 public key out of band: a hub that
// advertises any other key is rejected with ErrKeyMismatch before a single
// catalog document is read. An empty key means "no pin" (trust on first use).
func WithPinnedKey(pub ed25519.PublicKey) Option {
	return func(c *UpstreamClient) {
		c.pinnedKey = append(ed25519.PublicKey(nil), pub...)
	}
}

// WithPinnedHubID pins the hub id: a hub that identifies itself with
// another id is rejected with ErrHubIDMismatch (federation.md §3.1: a
// redirect to a different hub id is rejected).
func WithPinnedHubID(id string) Option {
	return func(c *UpstreamClient) {
		c.pinnedHubID = strings.TrimSpace(id)
	}
}

// WithPageSize sets the `limit` Sync asks for on every page. The hub
// clamps it to the canonical 500 entries (federation.md §4.2); 0 lets the
// hub pick its default.
func WithPageSize(n uint32) Option {
	return func(c *UpstreamClient) {
		c.pageSize = n
	}
}

// NewUpstreamClient builds a client for the given hub base URL. A URL
// without a scheme is dialed over https; anything that is not an http(s)
// URL with a host is rejected.
func NewUpstreamClient(hubURL string, opts ...Option) (*UpstreamClient, error) {
	base, err := normalizeHubURL(hubURL)
	if err != nil {
		return nil, err
	}
	c := &UpstreamClient{
		baseURL:    base,
		httpClient: &http.Client{Timeout: DefaultTimeout},
	}
	for _, opt := range opts {
		opt(c)
	}
	if len(c.pinnedKey) != 0 && len(c.pinnedKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: pinned key is %d bytes, want %d",
			ErrNoPublicKey, len(c.pinnedKey), ed25519.PublicKeySize)
	}
	if c.httpClient == nil {
		c.httpClient = &http.Client{Timeout: DefaultTimeout}
	}
	c.client = federationv1connect.NewFederationServiceClient(c.httpClient, base)
	return c, nil
}

// BaseURL returns the normalized hub URL this client dials.
func (c *UpstreamClient) BaseURL() string {
	return c.baseURL
}

// HubInfo returns the verified hub identity, fetching it from the hub on
// first use and reusing the cached value afterwards.
func (c *UpstreamClient) HubInfo(ctx context.Context) (HubInfo, error) {
	c.mu.RLock()
	cached := c.hub
	c.mu.RUnlock()
	if cached != nil {
		return *cached, nil
	}
	hub, err := c.fetchHubInfo(ctx)
	if err != nil {
		return HubInfo{}, err
	}
	c.mu.Lock()
	c.hub = &hub
	c.mu.Unlock()
	return hub, nil
}

// fetchHubInfo calls GetHubInfo and validates the hub's self-description
// against the pinned trust anchors: protocol, signing algorithm, key size,
// pinned key and pinned hub id. Everything downstream (snapshot and page
// verification) is keyed off the identity returned here.
func (c *UpstreamClient) fetchHubInfo(ctx context.Context) (HubInfo, error) {
	resp, err := c.client.GetHubInfo(ctx, connect.NewRequest(&federationv1.GetHubInfoRequest{}))
	if err != nil {
		return HubInfo{}, fmt.Errorf("federation: get hub info: %w", err)
	}
	info := resp.Msg.GetHub()
	if info.GetId() == "" {
		return HubInfo{}, errors.New("federation: hub info carries no hub id")
	}
	if info.GetProtocol() != protocolVersion {
		return HubInfo{}, fmt.Errorf("%w: hub %q speaks protocol %d, this client speaks %d",
			ErrProtocol, info.GetId(), info.GetProtocol(), protocolVersion)
	}
	if info.GetSigningAlgorithm() != signingAlgorithm {
		return HubInfo{}, fmt.Errorf("%w: hub %q signs with %q",
			ErrUnsupportedAlgorithm, info.GetId(), info.GetSigningAlgorithm())
	}
	raw := info.GetPublicKey()
	if len(raw) != ed25519.PublicKeySize {
		return HubInfo{}, fmt.Errorf("%w: hub %q published a %d-byte key, want %d",
			ErrNoPublicKey, info.GetId(), len(raw), ed25519.PublicKeySize)
	}
	pub := ed25519.PublicKey(append([]byte(nil), raw...))
	if len(c.pinnedKey) != 0 && !pub.Equal(c.pinnedKey) {
		return HubInfo{}, fmt.Errorf("%w: hub %q advertises %s, pinned %s",
			ErrKeyMismatch, info.GetId(), Fingerprint(pub), Fingerprint(c.pinnedKey))
	}
	if c.pinnedHubID != "" && info.GetId() != c.pinnedHubID {
		return HubInfo{}, fmt.Errorf("%w: hub %q, pinned %q",
			ErrHubIDMismatch, info.GetId(), c.pinnedHubID)
	}
	return HubInfo{
		ID:          info.GetId(),
		BaseURL:     info.GetBaseUrl(),
		Protocol:    info.GetProtocol(),
		PublicKey:   pub,
		KeyID:       KeyID(pub),
		Fingerprint: Fingerprint(pub),
	}, nil
}

// normalizeHubURL adds a scheme when missing, drops trailing slashes and
// rejects anything that is not an http(s) URL with a host.
func normalizeHubURL(hub string) (string, error) {
	hub = strings.TrimSpace(hub)
	if hub == "" {
		return "", errors.New("federation: empty hub url")
	}
	if !strings.Contains(hub, "://") {
		hub = "https://" + hub
	}
	hub = strings.TrimRight(hub, "/")
	u, err := url.Parse(hub)
	if err != nil {
		return "", fmt.Errorf("federation: invalid hub url %q: %w", hub, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("federation: hub url %q must use http or https", hub)
	}
	if u.Host == "" {
		return "", fmt.Errorf("federation: hub url %q has no host", hub)
	}
	return hub, nil
}
