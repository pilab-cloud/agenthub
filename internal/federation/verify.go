package federation

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"
	federationv1 "go.pilab.hu/agenthub/gen/go/agenthub/federation/v1"
)

// This file is the peer-side copy of the documents the hub signs: the same
// canonical JSON structs and rendering rules as internal/api/federation.go,
// re-derived here rather than imported so a federation peer does not share
// code with the signer. The interop tests in client_test.go run this
// verifier against the real server handler, which keeps the two in step.

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

// wireLog is the canonical form of a change-log selection; its sha256 is
// the selection's root digest.
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

// kindSlug spells an asset kind the way the wire format does:
// ASSET_KIND_AGENT → agent.
func kindSlug(kind commonv1.AssetKind) string {
	return strings.ToLower(strings.TrimPrefix(kind.String(), "ASSET_KIND_"))
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

// verifySnapshot checks the hub signature over the snapshot document and
// the shape of the root digest it commits to.
func verifySnapshot(hub HubInfo, resp *federationv1.GetSnapshotResponse) error {
	sig := resp.GetSignature()
	if sig == nil || sig.GetSignedAt() == nil {
		return fmt.Errorf("%w: snapshot carries no signature timestamp", ErrSignature)
	}
	doc := wireSnapshot{
		SchemaVersion:  schemaVersion,
		Hub:            hub.ID,
		Generation:     resp.GetGeneration(),
		CreatedAt:      signedAtText(sig),
		Pages:          resp.GetPages(),
		EntryCount:     resp.GetEntryCount(),
		TombstoneCount: resp.GetTombstoneCount(),
		RootDigest:     digestText(resp.GetRootDigest()),
	}
	payload, err := canonicalJSON(doc)
	if err != nil {
		return fmt.Errorf("%w: render snapshot document: %w", ErrSignature, err)
	}
	if err := checkSignature(sig, hub, payload); err != nil {
		return err
	}
	root := resp.GetRootDigest()
	if root.GetValue() == "" || (root.GetAlgorithm() != "" && root.GetAlgorithm() != "sha256") {
		return fmt.Errorf("%w: snapshot root digest is %q, want sha256",
			ErrDigest, digestText(root))
	}
	return nil
}

// verifyPage checks the hub signature over one page document. cursor is the
// cursor the client sent: the signature commits to it, so a page issued for
// another position of the walk fails here.
func verifyPage(hub HubInfo, cursor string, resp *federationv1.SyncChangesResponse) error {
	sig := resp.GetSignature()
	if sig == nil || sig.GetSignedAt() == nil {
		return fmt.Errorf("%w: page %d carries no signature timestamp", ErrSignature, resp.GetPage())
	}
	doc := wirePage{
		SchemaVersion: schemaVersion,
		Hub:           hub.ID,
		Generation:    resp.GetGeneration(),
		Page:          resp.GetPage(),
		Cursor:        cursor,
		PrevDigest:    digestText(resp.GetPrevDigest()),
		NextCursor:    resp.GetNextCursor(),
		IssuedAt:      signedAtText(sig),
		Entries:       make([]wireEntry, 0, len(resp.GetEntries())),
		Tombstones:    make([]wireTombstone, 0, len(resp.GetTombstones())),
	}
	for _, e := range resp.GetEntries() {
		doc.Entries = append(doc.Entries, wireEntryOf(e))
	}
	for _, t := range resp.GetTombstones() {
		doc.Tombstones = append(doc.Tombstones, wireTombstoneOf(t))
	}
	payload, err := canonicalJSON(doc)
	if err != nil {
		return fmt.Errorf("%w: render page document: %w", ErrSignature, err)
	}
	return checkSignature(sig, hub, payload)
}

// checkSignature verifies one hub signature over payload: the algorithm,
// the key id derived from the trusted hub key, and the Ed25519 signature
// itself.
func checkSignature(sig *commonv1.Signature, hub HubInfo, payload []byte) error {
	if sig.GetAlgorithm() != signingAlgorithm {
		return fmt.Errorf("%w: signature algorithm %q",
			ErrUnsupportedAlgorithm, sig.GetAlgorithm())
	}
	if sig.GetKeyId() != hub.KeyID {
		return fmt.Errorf("%w: signature key id %q, hub key is %q",
			ErrSignature, sig.GetKeyId(), hub.KeyID)
	}
	if len(sig.GetValue()) != ed25519.SignatureSize {
		return fmt.Errorf("%w: signature is %d bytes, want %d",
			ErrSignature, len(sig.GetValue()), ed25519.SignatureSize)
	}
	if !ed25519.Verify(hub.PublicKey, payload, sig.GetValue()) {
		return fmt.Errorf("%w: hub %q did not sign %q", ErrSignature, hub.ID, payload)
	}
	return nil
}

// signedAtText renders signature.signed_at as the timestamp inside the
// signed document (RFC3339Nano in UTC).
func signedAtText(sig *commonv1.Signature) string {
	return sig.GetSignedAt().AsTime().UTC().Format(time.RFC3339Nano)
}

// logItem is one position of the canonical change log: exactly one of the
// two is set, mirroring how the hub renders wireItem.
type logItem struct {
	entry     *federationv1.CatalogEntry
	tombstone *federationv1.Tombstone
}

// changeLog rebuilds the ordered change log a snapshot commits to from the
// verified pages, so the client can re-derive the root digest and the
// counts instead of taking the snapshot's word for them
// (federation.md §7.1 step 6).
type changeLog struct {
	hubID string
	items []logItem
}

// appendPage merges one verified page into the log. Within a page the hub
// keeps entries and tombstones as two sorted subsequences of the ordered
// change log, so a two-pointer merge restores the original interleaving;
// across pages the walk hands out contiguous slices of that same order, so
// pages can simply be appended.
func (l *changeLog) appendPage(page *federationv1.SyncChangesResponse) error {
	entries := page.GetEntries()
	tombs := page.GetTombstones()
	merged := make([]logItem, 0, len(entries)+len(tombs))
	i, j := 0, 0
	for i < len(entries) || j < len(tombs) {
		switch {
		case j >= len(tombs):
			merged = append(merged, logItem{entry: entries[i]})
			i++
		case i >= len(entries):
			merged = append(merged, logItem{tombstone: tombs[j]})
			j++
		default:
			entryKey, err := entrySortKey(entries[i])
			if err != nil {
				return err
			}
			tombKey, err := tombstoneSortKey(tombs[j])
			if err != nil {
				return err
			}
			switch {
			case entryKey < tombKey:
				merged = append(merged, logItem{entry: entries[i]})
				i++
			case entryKey > tombKey:
				merged = append(merged, logItem{tombstone: tombs[j]})
				j++
			default:
				return fmt.Errorf("%w: page holds an entry and a tombstone for %q",
					ErrDigest, entryKey)
			}
		}
	}
	l.items = append(l.items, merged...)
	return nil
}

// entryCount and tombstoneCount are the counts the snapshot signs.
func (l *changeLog) entryCount() uint64 {
	var n uint64
	for _, it := range l.items {
		if it.tombstone == nil {
			n++
		}
	}
	return n
}

func (l *changeLog) tombstoneCount() uint64 {
	var n uint64
	for _, it := range l.items {
		if it.tombstone != nil {
			n++
		}
	}
	return n
}

// assets and tombstones return the log contents in the hub's canonical
// order.
func (l *changeLog) assets() []*federationv1.CatalogEntry {
	out := make([]*federationv1.CatalogEntry, 0, l.entryCount())
	for _, it := range l.items {
		if it.tombstone == nil {
			out = append(out, it.entry)
		}
	}
	return out
}

func (l *changeLog) tombstones() []*federationv1.Tombstone {
	out := make([]*federationv1.Tombstone, 0, l.tombstoneCount())
	for _, it := range l.items {
		if it.tombstone != nil {
			out = append(out, it.tombstone)
		}
	}
	return out
}

// rootDigest recomputes the snapshot root digest over the accumulated log:
// sha256 of the canonical {"hub":…,"items":[…]} document.
func (l *changeLog) rootDigest() (string, error) {
	doc := wireLog{Hub: l.hubID, Items: make([]wireItem, 0, len(l.items))}
	for _, it := range l.items {
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
		return "", fmt.Errorf("federation: render change log: %w", err)
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// entrySortKey is the hub's ordering key of an entry:
// namespace\x00ASSET_KIND_X\x00name\x00version.
func entrySortKey(e *federationv1.CatalogEntry) (string, error) {
	if e.GetNamespace() == "" || e.GetName() == "" || e.GetVersion() == "" {
		return "", fmt.Errorf("%w: entry %q has no namespace, name or version",
			ErrDigest, e.GetId())
	}
	return strings.Join([]string{
		e.GetNamespace(), e.GetKind().String(), e.GetName(), e.GetVersion(),
	}, "\x00"), nil
}

// tombstoneSortKey derives the same ordering key from a tombstone, which
// carries only the asset URN and the version: the URN holds
// "agenthub://<hub>/<namespace>/<kind>/<name>", and the kind slug maps back
// to the enum spelling the hub orders by. An unknown kind fails closed: the
// client cannot place the tombstone in the log, so it cannot re-derive the
// root digest either.
func tombstoneSortKey(t *federationv1.Tombstone) (string, error) {
	namespace, kind, name, err := parseAssetURN(t.GetId())
	if err != nil {
		return "", err
	}
	enum, err := kindEnumName(kind)
	if err != nil {
		return "", err
	}
	if t.GetVersion() == "" {
		return "", fmt.Errorf("%w: tombstone %q has no version", ErrDigest, t.GetId())
	}
	return strings.Join([]string{namespace, enum, name, t.GetVersion()}, "\x00"), nil
}

// parseAssetURN splits "agenthub://<hub>/<namespace>/<kind>/<name>"; the
// name keeps any slashes it contains.
func parseAssetURN(id string) (namespace, kind, name string, err error) {
	rest, ok := strings.CutPrefix(id, "agenthub://")
	if !ok {
		return "", "", "", fmt.Errorf("%w: tombstone id %q is not an asset urn", ErrDigest, id)
	}
	_, rest, ok = strings.Cut(rest, "/") // the hub part: the signature already binds the hub
	if !ok {
		return "", "", "", fmt.Errorf("%w: tombstone id %q is not an asset urn", ErrDigest, id)
	}
	namespace, rest, ok = strings.Cut(rest, "/")
	if !ok {
		return "", "", "", fmt.Errorf("%w: tombstone id %q is not an asset urn", ErrDigest, id)
	}
	kind, name, ok = strings.Cut(rest, "/")
	if !ok || namespace == "" || kind == "" || name == "" {
		return "", "", "", fmt.Errorf("%w: tombstone id %q is not an asset urn", ErrDigest, id)
	}
	return namespace, kind, name, nil
}

// kindEnumName maps a wire kind slug back to the enum spelling the hub
// orders change-log items by ("agent" → "ASSET_KIND_AGENT"). An unknown
// slug fails closed: the client cannot place the tombstone in the log, so
// it cannot re-derive the root digest either.
func kindEnumName(slug string) (string, error) {
	switch slug {
	case "agent", "skill", "tool", "prompt", "pack", "unspecified":
		return "ASSET_KIND_" + strings.ToUpper(slug), nil
	default:
		return "", fmt.Errorf("%w: tombstone kind %q is unknown to this client", ErrDigest, slug)
	}
}
