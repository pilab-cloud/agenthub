package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"
	federationv1 "go.pilab.hu/agenthub/gen/go/agenthub/federation/v1"
	federationv1connect "go.pilab.hu/agenthub/gen/go/agenthub/federation/v1/federationv1connect"

	"google.golang.org/protobuf/types/known/timestamppb"

	"go.pilab.hu/agenthub/internal/catalog"
)

// federationPageSize is the canonical page size of a catalog sync
// (docs/architecture/federation.md §4.2: a page holds at most 500 entries).
const federationPageSize = 500

// federationSchemaVersion is stamped into every signed document.
const federationSchemaVersion = 1

// Federation is the FederationService handler (M4.1): GetSnapshot serves a
// signed descriptor of one catalog generation, SyncChanges serves the
// ordered catalog changes since a cursor, and GetHubInfo publishes this
// hub's Ed25519 key so peers can verify both. M4.3 adds ExportBundle and
// ImportBundle, the byte-level half of a transfer to an air-gapped hub.
//
// # Signed documents
//
// Both RPCs sign deterministic JSON rendered from the fixed-layout structs
// below. canonicalJSON turns off HTML escaping and encoding/json writes
// struct fields in declaration order, so the bytes are stable.
//
// GetSnapshot signs
//
//	{"schemaVersion":1,"hub":…,"generation":…,"createdAt":…,"pages":…,
//	 "entryCount":…,"tombstoneCount":…,"rootDigest":"sha256:…"}
//
// SyncChanges signs the page without pageDigest and signature:
//
//	{"schemaVersion":1,"hub":…,"generation":…,"page":…,"cursor":…,
//	 "prevDigest":…,"nextCursor":…,"issuedAt":…,"entries":[…],
//	 "tombstones":[…]}
//
// Every field is recoverable by the peer: schemaVersion is the constant 1,
// hub comes from GetHubInfo, cursor is the cursor the caller sent, and
// createdAt/issuedAt are signature.signed_at rendered as RFC3339Nano in
// UTC (the response messages carry no separate timestamp field).
//
// # Digests
//
// rootDigest and pageDigest are the sha256 of the canonical change-log
// document {"hub":…,"items":[…]} covering the ordered items through that
// point, not the sha256 of the page document. That keeps the chain
// independent of the page size the peer asked for: the next page's
// prevDigest equals this page's pageDigest whatever limit was used, and a
// peer that paged to the end ends on pageDigest == snapshot.rootDigest.
// This is the one deliberate deviation from federation.md §4.2/§4.3, which
// defines pageDigest over the page document and rootDigest as the concat of
// fixed-size page digests.
//
// # Generation
//
// generation is a monotonic counter for catalog changes (federation.md §2)
// that advances when the digest of the *unfiltered* catalog changes, so
// every peer agrees on one counter regardless of its namespace selection.
// The counter lives in this handler and restarts with the process; a cursor
// issued before a restart then looks stale and the peer re-bootstraps,
// which is the safe direction. The root digest is content-derived and does
// not reset.
type Federation struct {
	federationv1connect.UnimplementedFederationServiceHandler
	hubID   string
	baseURL string
	store   catalog.Store
	// blobs holds content-addressed file bytes: ExportBundle reads it to
	// stream file content, ImportBundle writes what an import carries
	// (M4.3), and GetBundle will serve from it later.
	blobs catalog.BlobStore
	key   ed25519.PrivateKey
	keyID string

	// mu guards the generation counter: it advances only when the catalog
	// fingerprint changes, so concurrent readers of an unchanged catalog
	// observe the same generation.
	mu                 sync.Mutex
	generation         uint64
	catalogFingerprint [sha256.Size]byte
	hasFingerprint     bool
}

// NewFederation builds a Federation backed by an in-memory catalog and an
// ephemeral hub signing key.
func NewFederation(hubID, baseURL string) *Federation {
	return NewFederationWithStore(hubID, baseURL, catalog.NewMemStore(), nil)
}

// NewFederationWithStore builds a Federation over the given catalog store and
// hub key. A nil or wrong-sized key generates an ephemeral one, which is
// only acceptable for development: signatures stop verifying after a restart.
// The blob store defaults to an in-memory one (M4.3); use
// NewFederationWithBlobs to share one with another handler.
func NewFederationWithStore(hubID, baseURL string, store catalog.Store, key ed25519.PrivateKey) *Federation {
	return NewFederationWithBlobs(hubID, baseURL, store, key, nil)
}

// NewFederationWithBlobs builds a Federation over the given catalog store,
// blob store and hub key. A nil blob store gets a private in-memory one, so
// bundles imported over this handler stay reachable for ExportBundle and for
// the GetBundle RPC of a later slice.
func NewFederationWithBlobs(hubID, baseURL string, store catalog.Store, key ed25519.PrivateKey, blobs catalog.BlobStore) *Federation {
	if store == nil {
		store = catalog.NewMemStore()
	}
	if blobs == nil {
		blobs = catalog.NewMemBlobs()
	}
	if len(key) != ed25519.PrivateKeySize {
		_, generated, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			panic(fmt.Errorf("generate ephemeral hub key: %w", err))
		}
		key = generated
	}
	return &Federation{
		hubID:   hubID,
		baseURL: baseURL,
		store:   store,
		blobs:   blobs,
		key:     key,
		keyID:   hubKeyID(key),
	}
}

// hubKeyID derives a stable key identifier from the hub key: the first 8
// bytes of sha256(public key), hex encoded ("ed25519:9f2c…").
func hubKeyID(key ed25519.PrivateKey) string {
	sum := sha256.Sum256(key[ed25519.SeedSize:])
	return "ed25519:" + hex.EncodeToString(sum[:8])
}

// publicKey returns the Ed25519 public half of the hub key.
func (f *Federation) publicKey() ed25519.PublicKey {
	return ed25519.PublicKey(f.key[ed25519.SeedSize:])
}

// PublicKey returns a copy of this hub's Ed25519 public key, for the
// well-known document and for peers that pin the key out of band.
func (f *Federation) PublicKey() []byte {
	return append([]byte(nil), f.publicKey()...)
}

// GetHubInfo identifies this hub and publishes the key that signs snapshots
// and pages.
func (f *Federation) GetHubInfo(ctx context.Context, req *connect.Request[federationv1.GetHubInfoRequest]) (*connect.Response[federationv1.GetHubInfoResponse], error) {
	return connect.NewResponse(&federationv1.GetHubInfoResponse{
		Hub: &federationv1.HubInfo{
			Id:               f.hubID,
			BaseUrl:          f.baseURL,
			PublicKey:        f.PublicKey(),
			Protocol:         1,
			SigningAlgorithm: "ed25519",
		},
	}), nil
}

// GetSnapshot returns a signed descriptor of the catalog generation the
// caller is about to page through.
func (f *Federation) GetSnapshot(ctx context.Context, req *connect.Request[federationv1.GetSnapshotRequest]) (*connect.Response[federationv1.GetSnapshotResponse], error) {
	sel, err := f.selectCatalog(req.Msg.GetNamespaces())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	var entryCount, tombstoneCount uint64
	for _, it := range sel.items {
		if it.tombstone != nil {
			tombstoneCount++
			continue
		}
		entryCount++
	}
	// An empty catalog has no pages to fetch; ceil(len/pageSize) says 0.
	// A catalog would need 2^41 entries to overflow the uint32.
	pages := uint32((len(sel.items) + federationPageSize - 1) / federationPageSize) //nolint:gosec // G115: bounded by the catalog size, see comment

	root := sha256Digest(sel.root)
	issuedAt := time.Now().UTC()
	snapshot := &wireSnapshot{
		SchemaVersion:  federationSchemaVersion,
		Hub:            f.hubID,
		Generation:     sel.generation,
		CreatedAt:      issuedAt.Format(time.RFC3339Nano),
		Pages:          pages,
		EntryCount:     entryCount,
		TombstoneCount: tombstoneCount,
		RootDigest:     digestText(root),
	}
	payload, err := canonicalJSON(snapshot)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&federationv1.GetSnapshotResponse{
		Generation:     sel.generation,
		Pages:          pages,
		EntryCount:     entryCount,
		TombstoneCount: tombstoneCount,
		RootDigest:     root,
		Signature:      f.signature(payload, issuedAt),
	}), nil
}

// SyncChanges returns one signed page of catalog changes after cursor.
//
// Errors: a cursor that is not a hub-signed cursor of this hub is
// CodeInvalidArgument; a cursor issued for another namespace selection or
// for an older generation is CodeFailedPrecondition, because resuming it
// would skip or duplicate changes — the peer has to re-bootstrap from
// GetSnapshot, exactly like the gap detection in federation.md §7.2.
func (f *Federation) SyncChanges(ctx context.Context, req *connect.Request[federationv1.SyncChangesRequest]) (*connect.Response[federationv1.SyncChangesResponse], error) {
	m := req.Msg
	limit := int(m.GetLimit())
	switch {
	case limit <= 0:
		limit = federationPageSize
	case limit > federationPageSize:
		limit = federationPageSize
	}

	sel, err := f.selectCatalog(m.GetNamespaces())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	filter := namespaceFingerprint(m.GetNamespaces())
	cursor := m.GetCursor()
	offset := 0
	if cursor != "" {
		c, err := f.decodeCursor(cursor)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		if c.filter != filter {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				errors.New("cursor was issued for a different namespace selection; restart the sync from GetSnapshot"))
		}
		if c.generation != sel.generation {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				errors.New("catalog generation changed since the cursor was issued; restart the sync from GetSnapshot"))
		}
		// A matching generation implies the same catalog, so the offset is
		// in range; clamp anyway rather than panic on a defensive path.
		switch {
		case c.offset >= uint64(len(sel.items)):
			offset = len(sel.items)
		default:
			offset = int(c.offset) //nolint:gosec // G115: bounded by len(sel.items) above
		}
	}

	end := offset + limit
	if end > len(sel.items) {
		end = len(sel.items)
	}
	pageItems := sel.items[offset:end]

	entries := make([]*federationv1.CatalogEntry, 0, len(pageItems))
	tombstones := make([]*federationv1.Tombstone, 0, len(pageItems))
	for _, it := range pageItems {
		if it.tombstone == nil {
			entries = append(entries, it.entry)
			continue
		}
		tombstones = append(tombstones, it.tombstone)
		// include_yanked repeats a yanked version in the entries list with
		// status "yanked". The canonical log still records it as a
		// tombstone only, so the root digest does not depend on this flag.
		if m.GetIncludeYanked() {
			entries = append(entries, it.entry)
		}
	}

	pageDigest, err := logDigest(f.hubID, sel.items[:end])
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var prevDigest *commonv1.Digest
	if offset > 0 {
		// The prefix before this page is exactly what the previous page
		// ended on, so prevDigest matches the pageDigest the peer stored.
		prefix, err := logDigest(f.hubID, sel.items[:offset])
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		prevDigest = sha256Digest(prefix)
	}

	nextCursor := ""
	if end < len(sel.items) {
		nextCursor, err = f.encodeCursor(federationCursor{
			generation: sel.generation,
			offset:     uint64(end),
			filter:     filter,
		})
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}

	// A page index overflows uint32 only past 2^32 pages of items, which
	// no catalog can hold.
	pageNo := uint32(offset/limit) + 1 //nolint:gosec // G115: bounded by the catalog size, see comment
	issuedAt := time.Now().UTC()
	page := &wirePage{
		SchemaVersion: federationSchemaVersion,
		Hub:           f.hubID,
		Generation:    sel.generation,
		Page:          pageNo,
		Cursor:        cursor,
		PrevDigest:    digestText(prevDigest),
		NextCursor:    nextCursor,
		IssuedAt:      issuedAt.Format(time.RFC3339Nano),
		Entries:       make([]wireEntry, 0, len(entries)),
		Tombstones:    make([]wireTombstone, 0, len(tombstones)),
	}
	for _, e := range entries {
		page.Entries = append(page.Entries, wireEntryOf(e))
	}
	for _, t := range tombstones {
		page.Tombstones = append(page.Tombstones, wireTombstoneOf(t))
	}
	payload, err := canonicalJSON(page)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&federationv1.SyncChangesResponse{
		Generation: sel.generation,
		Page:       pageNo,
		PrevDigest: prevDigest,
		NextCursor: nextCursor,
		Entries:    entries,
		Tombstones: tombstones,
		PageDigest: sha256Digest(pageDigest),
		Signature:  f.signature(payload, issuedAt),
	}), nil
}

// signature signs payload with the hub key and stamps it.
func (f *Federation) signature(payload []byte, signedAt time.Time) *commonv1.Signature {
	return &commonv1.Signature{
		KeyId:     f.keyID,
		Algorithm: "ed25519",
		Value:     ed25519.Sign(f.key, payload),
		SignedAt:  timestamppb.New(signedAt),
	}
}

// unimplementedIn reports an RPC that belongs to a later federation slice.
func unimplementedIn(rpc, milestone string) error {
	return connect.NewError(connect.CodeUnimplemented,
		errors.New(rpc+" is not implemented yet ("+milestone+")"))
}

// ListUpstreams is M4.2 (upstream management), not M4.1.
func (f *Federation) ListUpstreams(ctx context.Context, req *connect.Request[federationv1.ListUpstreamsRequest]) (*connect.Response[federationv1.ListUpstreamsResponse], error) {
	return nil, unimplementedIn("ListUpstreams", "M4.2")
}

// UpsertUpstream is M4.2 (upstream management), not M4.1.
func (f *Federation) UpsertUpstream(ctx context.Context, req *connect.Request[federationv1.UpsertUpstreamRequest]) (*connect.Response[federationv1.UpsertUpstreamResponse], error) {
	return nil, unimplementedIn("UpsertUpstream", "M4.2")
}

// DeleteUpstream is M4.2 (upstream management), not M4.1.
func (f *Federation) DeleteUpstream(ctx context.Context, req *connect.Request[federationv1.DeleteUpstreamRequest]) (*connect.Response[federationv1.DeleteUpstreamResponse], error) {
	return nil, unimplementedIn("DeleteUpstream", "M4.2")
}

// TriggerSync is M4.2 (the sync worker), not M4.1.
func (f *Federation) TriggerSync(ctx context.Context, req *connect.Request[federationv1.TriggerSyncRequest]) (*connect.Response[federationv1.TriggerSyncResponse], error) {
	return nil, unimplementedIn("TriggerSync", "M4.2")
}

// catalogSelection is one consistent view of the catalog: the ordered items
// of the requested namespaces, the root digest of that selection and the
// current catalog generation.
type catalogSelection struct {
	items      []changeItem
	root       [sha256.Size]byte
	generation uint64
}

// selectCatalog builds the change log for the requested namespaces and
// resolves the generation.
//
// The generation advances on the digest of the *unfiltered* catalog, so
// peers watching different namespaces still agree on one monotonic counter
// and a cursor stays valid as long as the catalog it walks is unchanged.
// The root digest covers the requested selection instead: it is what a peer
// re-computes from the pages it fetched with the same filter.
func (f *Federation) selectCatalog(namespaces []string) (catalogSelection, error) {
	full := f.buildLog()
	fullDigest, err := logDigest(f.hubID, full)
	if err != nil {
		return catalogSelection{}, err
	}
	generation := f.advanceGeneration(fullDigest)
	if len(namespaces) == 0 {
		return catalogSelection{items: full, root: fullDigest, generation: generation}, nil
	}

	items := filterLog(full, namespaces)
	root, err := logDigest(f.hubID, items)
	if err != nil {
		return catalogSelection{}, err
	}
	return catalogSelection{items: items, root: root, generation: generation}, nil
}

// advanceGeneration bumps the catalog generation when the catalog
// fingerprint changes. The counter only ever grows, so peers can rely on
// monotonic generations; it restarts at 1 with the process, which makes any
// older cursor look stale (the peer re-bootstraps).
func (f *Federation) advanceGeneration(fingerprint [sha256.Size]byte) uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.hasFingerprint || fingerprint != f.catalogFingerprint {
		f.generation++
		f.catalogFingerprint = fingerprint
		f.hasFingerprint = true
	}
	return f.generation
}

// changeItem is one position of the canonical change log.
type changeItem struct {
	ns  string
	key string // total order: namespace\x00kind\x00name\x00version
	// entry projects the version onto the wire format. It is non-nil for
	// every item, yanked versions included (status "yanked"), so
	// include_yanked can repeat them in the entries list.
	entry *federationv1.CatalogEntry
	// tombstone is set only for yanked versions: the canonical log records
	// a yank as a tombstone, never as an entry.
	tombstone *federationv1.Tombstone
}

// buildLog renders the whole catalog as the canonical change log: one item
// per federated version ordered by (namespace, kind, name, version). Draft
// versions are unpublished and never federated; yanked versions become
// tombstones. The result is a per-call value, so the contained messages can
// be handed to a response without cloning.
func (f *Federation) buildLog() []changeItem {
	assets := f.store.ListAssets()
	items := make([]changeItem, 0, len(assets))
	for _, a := range assets {
		for _, v := range f.store.ListVersions(a.Namespace, a.Kind, a.Name) {
			if v.Status == commonv1.VersionStatus_VERSION_STATUS_DRAFT {
				continue
			}
			entry := f.entryFor(a, v)
			it := changeItem{
				ns:    a.Namespace,
				key:   strings.Join([]string{a.Namespace, a.Kind.String(), a.Name, v.Version}, "\x00"),
				entry: entry,
			}
			if v.Status == commonv1.VersionStatus_VERSION_STATUS_YANKED {
				it.tombstone = &federationv1.Tombstone{
					Id:      entry.GetId(),
					Version: v.Version,
					Reason:  "yanked",
					// The catalog store does not record when a version was
					// yanked yet, so "at" stays unset.
				}
			}
			items = append(items, it)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].key < items[j].key })
	return items
}

// filterLog keeps the items of the requested namespaces. An empty selection
// means "everything".
func filterLog(items []changeItem, namespaces []string) []changeItem {
	if len(namespaces) == 0 {
		return items
	}
	allow := make(map[string]struct{}, len(namespaces))
	for _, ns := range namespaces {
		allow[ns] = struct{}{}
	}
	out := make([]changeItem, 0, len(items))
	for _, it := range items {
		if _, ok := allow[it.ns]; ok {
			out = append(out, it)
		}
	}
	return out
}

// entryFor projects one catalog version onto the federation wire message.
// origin_version_id, publisher_key_id and risk_class stay empty: the catalog
// store carries no per-version id, publisher keys live in the namespace key
// list and risk classification is a later federation slice.
func (f *Federation) entryFor(a *catalog.Asset, v *catalog.Version) *federationv1.CatalogEntry {
	return &federationv1.CatalogEntry{
		Id:             assetURN(f.hubID, a.Namespace, a.Kind, a.Name),
		Kind:           a.Kind,
		Namespace:      a.Namespace,
		Name:           a.Name,
		Version:        v.Version,
		OriginHub:      f.hubID,
		ManifestDigest: cloneDigest(v.ManifestDigest),
		BundleDigest:   cloneDigest(v.BundleDigest),
		PublishedAt:    cloneTimestamp(v.PublishedAt),
		Status:         versionStatus(v.Status),
		Size:           v.BundleSize,
		Tags:           append([]string{}, a.Tags...),
	}
}

// assetURN is the stable identity of an asset on this hub
// (docs/architecture/domain-model.md §8.1).
func assetURN(hubID, namespace string, kind commonv1.AssetKind, name string) string {
	return "agenthub://" + hubID + "/" + namespace + "/" + kindSlug(kind) + "/" + name
}

// kindSlug spells an asset kind the way the wire format does:
// ASSET_KIND_AGENT → agent.
func kindSlug(kind commonv1.AssetKind) string {
	return strings.ToLower(strings.TrimPrefix(kind.String(), "ASSET_KIND_"))
}

// versionStatus spells a version status the way the wire format does:
// VERSION_STATUS_PUBLISHED → published.
func versionStatus(status commonv1.VersionStatus) string {
	return strings.ToLower(strings.TrimPrefix(status.String(), "VERSION_STATUS_"))
}

// cloneTimestamp copies a timestamp into a fresh message.
func cloneTimestamp(ts *timestamppb.Timestamp) *timestamppb.Timestamp {
	if ts == nil {
		return nil
	}
	return &timestamppb.Timestamp{Seconds: ts.GetSeconds(), Nanos: ts.GetNanos()}
}

// namespaceFingerprint binds a cursor to the namespace selection it was
// issued for, so a walk cannot resume under a different filter.
func namespaceFingerprint(namespaces []string) string {
	sorted := append([]string(nil), namespaces...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\x00")))
	return hex.EncodeToString(sum[:])
}

// federationCursor is the position of a sync walk. It is signed with the
// hub key so a peer cannot move its own offsets.
type federationCursor struct {
	generation uint64
	offset     uint64
	filter     string
}

type wireCursor struct {
	Generation uint64 `json:"g"`
	Offset     uint64 `json:"o"`
	Filter     string `json:"f"`
}

// encodeCursor renders the cursor as "<base64url payload>.<base64url sig>".
func (f *Federation) encodeCursor(c federationCursor) (string, error) {
	payload, err := canonicalJSON(wireCursor{
		Generation: c.generation,
		Offset:     c.offset,
		Filter:     c.filter,
	})
	if err != nil {
		return "", err
	}
	sig := ed25519.Sign(f.key, payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(sig), nil
}

// decodeCursor verifies the hub signature before reading the payload: a
// tampered cursor is a bad request, not a server error.
func (f *Federation) decodeCursor(raw string) (federationCursor, error) {
	var zero federationCursor
	payloadB64, sigB64, ok := strings.Cut(raw, ".")
	if !ok {
		return zero, errors.New("cursor is not in <payload>.<signature> form")
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return zero, fmt.Errorf("decode cursor payload: %w", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return zero, fmt.Errorf("decode cursor signature: %w", err)
	}
	if len(sig) != ed25519.SignatureSize {
		return zero, fmt.Errorf("cursor signature is %d bytes, want %d", len(sig), ed25519.SignatureSize)
	}
	if !ed25519.Verify(f.publicKey(), payload, sig) {
		return zero, errors.New("cursor signature does not verify")
	}
	var w wireCursor
	if err := json.Unmarshal(payload, &w); err != nil {
		return zero, fmt.Errorf("decode cursor body: %w", err)
	}
	return federationCursor{generation: w.Generation, offset: w.Offset, filter: w.Filter}, nil
}

// wireSnapshot is the signed body of GetSnapshotResponse. createdAt is
// signature.signed_at rendered as RFC3339Nano in UTC.
type wireSnapshot struct {
	SchemaVersion  int    `json:"schemaVersion"`
	Hub            string `json:"hub"`
	Generation     uint64 `json:"generation"`
	CreatedAt      string `json:"createdAt"`
	Pages          uint32 `json:"pages"`
	EntryCount     uint64 `json:"entryCount"`
	TombstoneCount uint64 `json:"tombstoneCount"`
	RootDigest     string `json:"rootDigest"`
}

// wirePage is the signed body of SyncChangesResponse; pageDigest and
// signature are added afterwards. cursor is the cursor the caller sent
// ("" for the first page), issuedAt is signature.signed_at rendered as
// RFC3339Nano in UTC.
type wirePage struct {
	SchemaVersion int             `json:"schemaVersion"`
	Hub           string          `json:"hub"`
	Generation    uint64          `json:"generation"`
	Page          uint32          `json:"page"`
	Cursor        string          `json:"cursor"`
	PrevDigest    string          `json:"prevDigest"`
	NextCursor    string          `json:"nextCursor"`
	IssuedAt      string          `json:"issuedAt"`
	Entries       []wireEntry     `json:"entries"`
	Tombstones    []wireTombstone `json:"tombstones"`
}

// wireEntry is CatalogEntry with primitive fields only, so the signed JSON
// does not depend on protobuf internals. Field names follow
// federation.md §4.1.
type wireEntry struct {
	ID              string   `json:"id"`
	Kind            string   `json:"kind"`
	Namespace       string   `json:"namespace"`
	Name            string   `json:"name"`
	Version         string   `json:"version"`
	OriginHub       string   `json:"originHub"`
	OriginVersionID string   `json:"originVersionId"`
	ManifestDigest  string   `json:"manifestDigest"`
	BundleDigest    string   `json:"bundleDigest"`
	PublishedAt     string   `json:"publishedAt"`
	Status          string   `json:"status"`
	PublisherKeyID  string   `json:"publisherKeyId"`
	RiskClass       string   `json:"riskClass"`
	Size            uint64   `json:"size"`
	Tags            []string `json:"tags"`
}

// wireTombstone is Tombstone with primitive fields only.
type wireTombstone struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Reason  string `json:"reason"`
	At      string `json:"at,omitempty"`
}

func wireEntryOf(e *federationv1.CatalogEntry) wireEntry {
	return wireEntry{
		ID:              e.GetId(),
		Kind:            kindSlug(e.GetKind()),
		Namespace:       e.GetNamespace(),
		Name:            e.GetName(),
		Version:         e.GetVersion(),
		OriginHub:       e.GetOriginHub(),
		OriginVersionID: e.GetOriginVersionId(),
		ManifestDigest:  digestText(e.GetManifestDigest()),
		BundleDigest:    digestText(e.GetBundleDigest()),
		PublishedAt:     timestampText(e.GetPublishedAt()),
		Status:          e.GetStatus(),
		PublisherKeyID:  e.GetPublisherKeyId(),
		RiskClass:       e.GetRiskClass(),
		Size:            e.GetSize(),
		Tags:            append([]string{}, e.GetTags()...),
	}
}

func wireTombstoneOf(t *federationv1.Tombstone) wireTombstone {
	return wireTombstone{
		ID:      t.GetId(),
		Version: t.GetVersion(),
		Reason:  t.GetReason(),
		At:      timestampText(t.GetAt()),
	}
}

// wireLog is the canonical form of a change-log selection; its sha256 is the
// selection's root digest.
type wireLog struct {
	Hub   string     `json:"hub"`
	Items []wireItem `json:"items"`
}

// wireItem discriminates the two kinds of change: a version peers may
// install, or a yank they must apply.
type wireItem struct {
	Type      string         `json:"type"` // "entry" | "tombstone"
	Entry     *wireEntry     `json:"entry,omitempty"`
	Tombstone *wireTombstone `json:"tombstone,omitempty"`
}

// logDigest commits to the ordered change log of one selection:
// sha256 over the canonical {"hub":…,"items":[…]} document.
func logDigest(hubID string, items []changeItem) ([sha256.Size]byte, error) {
	doc := wireLog{Hub: hubID, Items: make([]wireItem, 0, len(items))}
	for _, it := range items {
		var wi wireItem
		if it.tombstone != nil {
			tombstone := wireTombstoneOf(it.tombstone)
			wi = wireItem{Type: "tombstone", Tombstone: &tombstone}
		} else {
			entry := wireEntryOf(it.entry)
			wi = wireItem{Type: "entry", Entry: &entry}
		}
		doc.Items = append(doc.Items, wi)
	}
	raw, err := canonicalJSON(doc)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(raw), nil
}

// canonicalJSON renders v as deterministic JSON: struct fields keep their
// declaration order, HTML escaping is off (so <, > and & stay literal) and
// the trailing newline json.Encoder appends is dropped.
func canonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// sha256Digest wraps a sum as the wire Digest message.
func sha256Digest(sum [sha256.Size]byte) *commonv1.Digest {
	return &commonv1.Digest{Algorithm: "sha256", Value: hex.EncodeToString(sum[:])}
}

// digestText renders a digest as "algorithm:value" ("" when unset), the
// form used inside signed documents.
func digestText(d *commonv1.Digest) string {
	if d == nil || d.GetValue() == "" {
		return ""
	}
	alg := d.GetAlgorithm()
	if alg == "" {
		alg = "sha256"
	}
	return alg + ":" + d.GetValue()
}

// timestampText renders a timestamp as RFC3339Nano in UTC ("" when unset).
func timestampText(ts *timestamppb.Timestamp) string {
	if ts == nil {
		return ""
	}
	return ts.AsTime().UTC().Format(time.RFC3339Nano)
}

// LoadHubKey reads the hub's Ed25519 signing key.
//
// An empty path generates an ephemeral key, which keeps development and
// tests working without provisioning but loses the key on restart, forcing
// every peer to re-bootstrap. Production sets signing.hub_key_path (config
// validation requires it when federation.enabled is set).
//
// Accepted formats: a PKCS#8 PEM block ("PRIVATE KEY", as written by
// `openssl genpkey -algorithm ed25519`), or base64 (standard or URL-safe,
// padded or not) or hex of a 32-byte seed or of a full 64-byte private key.
func LoadHubKey(path string) (ed25519.PrivateKey, error) {
	if strings.TrimSpace(path) == "" {
		_, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("generate ephemeral hub key: %w", err)
		}
		return key, nil
	}
	raw, err := os.ReadFile(path) //nolint:gosec // path is the operator-configured signing.hub_key_path
	if err != nil {
		return nil, fmt.Errorf("read hub key %q: %w", path, err)
	}
	key, err := parseHubKey(raw)
	if err != nil {
		return nil, fmt.Errorf("parse hub key %q: %w", path, err)
	}
	return key, nil
}

func parseHubKey(raw []byte) (ed25519.PrivateKey, error) {
	trimmed := bytes.TrimSpace(raw)
	if block, _ := pem.Decode(trimmed); block != nil {
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse PKCS#8: %w", err)
		}
		key, ok := parsed.(ed25519.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("PKCS#8 key is %T, want ed25519", parsed)
		}
		return key, nil
	}
	if decoded, ok := decodeKeyText(trimmed); ok {
		return keyFromBytes(decoded)
	}
	// Binary keys are tried against the raw file content first: a random
	// seed or private key may itself start or end with a whitespace byte,
	// which trimming would silently turn into a 31- or 63-byte key. Text
	// encodings already went through decodeKeyText above, so a file that is
	// text falls through to the trimmed bytes below.
	if key, err := keyFromBytes(raw); err == nil {
		return key, nil
	}
	return keyFromBytes(trimmed)
}

// decodeKeyText decodes a text-encoded key: base64 in the four encodings Go
// ships, then hex. A decode only counts when it yields a key-sized result,
// otherwise a hex key would be swallowed by base64 (hex characters are a
// subset of the base64 alphabet).
func decodeKeyText(trimmed []byte) ([]byte, bool) {
	s := string(trimmed)
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		if decoded, err := enc.DecodeString(s); err == nil && isKeyLength(len(decoded)) {
			return decoded, true
		}
	}
	if decoded, err := hex.DecodeString(s); err == nil && isKeyLength(len(decoded)) {
		return decoded, true
	}
	return nil, false
}

func isKeyLength(n int) bool {
	return n == ed25519.SeedSize || n == ed25519.PrivateKeySize
}

// keyFromBytes turns a seed or a full private key into a usable key. A
// 64-byte input must be self-consistent (seed and public half), otherwise
// the published public key would not match the signatures.
func keyFromBytes(b []byte) (ed25519.PrivateKey, error) {
	switch len(b) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(b), nil
	case ed25519.PrivateKeySize:
		key := ed25519.PrivateKey(append([]byte(nil), b...))
		if !bytes.Equal(ed25519.NewKeyFromSeed(key[:ed25519.SeedSize]), key) {
			return nil, errors.New("private key does not match its seed")
		}
		return key, nil
	default:
		return nil, fmt.Errorf("want a %d-byte seed or a %d-byte private key, got %d bytes",
			ed25519.SeedSize, ed25519.PrivateKeySize, len(b))
	}
}
