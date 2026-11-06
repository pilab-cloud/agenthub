package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"
	federationv1 "go.pilab.hu/agenthub/gen/go/agenthub/federation/v1"
	"go.pilab.hu/agenthub/internal/catalog"
)

const (
	testHubID   = "hub.example"
	testBaseURL = "https://hub.example"
)

// newFederation returns a handler over a fresh in-memory catalog and a fixed
// test key, so signatures are stable within a test.
func newFederation(t *testing.T) (*Federation, *catalog.MemStore) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(pub) != ed25519.PublicKeySize {
		t.Fatalf("public key size: %d", len(pub))
	}
	store := catalog.NewMemStore()
	return NewFederationWithStore(testHubID, testBaseURL, store, key), store
}

// seedFederationCatalog publishes three assets in two namespaces:
//
//	pilab/agent/alpha  1.0.0 published, 1.1.0 yanked, 2.0.0-draft draft
//	pilab/tool/gamma   1.0.0 published
//	pirat/skill/beta   0.1.0 published, 0.2.0 deprecated
//
// The draft is never federated and the yank becomes a tombstone, so the
// canonical log holds five items: four entries and one tombstone.
func seedFederationCatalog(t *testing.T, store catalog.Store) {
	t.Helper()
	published := timestamppb.New(time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC))
	assets := []*catalog.Asset{
		{
			Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "alpha",
			DisplayName: "Alpha", Tags: []string{"go"}, LatestVer: "1.1.0",
			CreatedAt: published, UpdatedAt: published,
		},
		{
			Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_TOOL, Name: "gamma",
			DisplayName: "Gamma", Tags: []string{"cli"}, LatestVer: "1.0.0",
			CreatedAt: published, UpdatedAt: published,
		},
		{
			Namespace: "pirat", Kind: commonv1.AssetKind_ASSET_KIND_SKILL, Name: "beta",
			DisplayName: "Beta", Tags: []string{"review"}, LatestVer: "0.2.0",
			CreatedAt: published, UpdatedAt: published,
		},
	}
	for _, a := range assets {
		if err := store.UpsertAsset(a); err != nil {
			t.Fatal(err)
		}
	}
	versions := []*catalog.Version{
		{
			Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "alpha",
			Version: "1.0.0", Status: commonv1.VersionStatus_VERSION_STATUS_PUBLISHED,
			ManifestDigest: &commonv1.Digest{Algorithm: "sha256", Value: "aa"},
			BundleDigest:   &commonv1.Digest{Algorithm: "sha256", Value: "bb"},
			BundleSize:     10, PublishedAt: published,
		},
		{
			Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "alpha",
			Version: "1.1.0", Status: commonv1.VersionStatus_VERSION_STATUS_YANKED,
			BundleSize: 11, PublishedAt: published,
		},
		{
			Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "alpha",
			Version: "2.0.0-draft", Status: commonv1.VersionStatus_VERSION_STATUS_DRAFT,
			BundleSize: 12, PublishedAt: published,
		},
		{
			Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_TOOL, Name: "gamma",
			Version: "1.0.0", Status: commonv1.VersionStatus_VERSION_STATUS_PUBLISHED,
			BundleSize: 20, PublishedAt: published,
		},
		{
			Namespace: "pirat", Kind: commonv1.AssetKind_ASSET_KIND_SKILL, Name: "beta",
			Version: "0.1.0", Status: commonv1.VersionStatus_VERSION_STATUS_PUBLISHED,
			BundleSize: 30, PublishedAt: published,
		},
		{
			Namespace: "pirat", Kind: commonv1.AssetKind_ASSET_KIND_SKILL, Name: "beta",
			Version: "0.2.0", Status: commonv1.VersionStatus_VERSION_STATUS_DEPRECATED,
			BundleSize: 31, PublishedAt: published,
		},
	}
	for _, v := range versions {
		if err := store.UpsertVersion(v); err != nil {
			t.Fatal(err)
		}
	}
}

// verifySnapshotSignature re-renders the signed snapshot document from the
// response (createdAt comes from signature.signed_at) and checks it against
// the hub key.
func verifySnapshotSignature(t *testing.T, f *Federation, resp *federationv1.GetSnapshotResponse) {
	t.Helper()
	sig := resp.GetSignature()
	if sig == nil || sig.GetSignedAt() == nil {
		t.Fatal("snapshot carries no signature timestamp")
	}
	snapshot := &wireSnapshot{
		SchemaVersion:  federationSchemaVersion,
		Hub:            f.hubID,
		Generation:     resp.GetGeneration(),
		CreatedAt:      sig.GetSignedAt().AsTime().UTC().Format(time.RFC3339Nano),
		Pages:          resp.GetPages(),
		EntryCount:     resp.GetEntryCount(),
		TombstoneCount: resp.GetTombstoneCount(),
		RootDigest:     digestText(resp.GetRootDigest()),
	}
	payload, err := canonicalJSON(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(f.publicKey(), payload, sig.GetValue()) {
		t.Fatalf("snapshot signature does not verify over %s", payload)
	}
}

// verifyPageSignature re-renders the signed page document from the response
// (cursor is the cursor the caller sent, issuedAt comes from
// signature.signed_at) and checks it against the hub key.
func verifyPageSignature(t *testing.T, f *Federation, cursor string, resp *federationv1.SyncChangesResponse) {
	t.Helper()
	sig := resp.GetSignature()
	if sig == nil || sig.GetSignedAt() == nil {
		t.Fatal("page carries no signature timestamp")
	}
	page := &wirePage{
		SchemaVersion: federationSchemaVersion,
		Hub:           f.hubID,
		Generation:    resp.GetGeneration(),
		Page:          resp.GetPage(),
		Cursor:        cursor,
		PrevDigest:    digestText(resp.GetPrevDigest()),
		NextCursor:    resp.GetNextCursor(),
		IssuedAt:      sig.GetSignedAt().AsTime().UTC().Format(time.RFC3339Nano),
		Entries:       make([]wireEntry, 0, len(resp.GetEntries())),
		Tombstones:    make([]wireTombstone, 0, len(resp.GetTombstones())),
	}
	for _, e := range resp.GetEntries() {
		page.Entries = append(page.Entries, wireEntryOf(e))
	}
	for _, tb := range resp.GetTombstones() {
		page.Tombstones = append(page.Tombstones, wireTombstoneOf(tb))
	}
	payload, err := canonicalJSON(page)
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(f.publicKey(), payload, sig.GetValue()) {
		t.Fatalf("page signature does not verify over %s", payload)
	}
}

func TestFederationGetSnapshot(t *testing.T) {
	f, store := newFederation(t)
	seedFederationCatalog(t, store)

	resp, err := f.GetSnapshot(context.Background(), connect.NewRequest(&federationv1.GetSnapshotRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	got := resp.Msg
	if got.GetGeneration() < 1 {
		t.Fatalf("generation = %d, want >= 1", got.GetGeneration())
	}
	if got.GetEntryCount() != 4 || got.GetTombstoneCount() != 1 {
		t.Fatalf("counts = %d entries / %d tombstones, want 4 / 1",
			got.GetEntryCount(), got.GetTombstoneCount())
	}
	// five items in pages of 500 -> one page.
	if got.GetPages() != 1 {
		t.Fatalf("pages = %d, want 1", got.GetPages())
	}
	root := got.GetRootDigest()
	if root.GetAlgorithm() != "sha256" || len(root.GetValue()) != 64 {
		t.Fatalf("root digest = %v, want sha256 with 64 hex chars", root)
	}
	if _, err := hex.DecodeString(root.GetValue()); err != nil {
		t.Fatalf("root digest is not hex: %v", err)
	}
	sig := got.GetSignature()
	if sig.GetAlgorithm() != "ed25519" || len(sig.GetValue()) != ed25519.SignatureSize {
		t.Fatalf("signature = %v, want ed25519 of %d bytes", sig, ed25519.SignatureSize)
	}
	if !strings.HasPrefix(sig.GetKeyId(), "ed25519:") {
		t.Fatalf("key id = %q, want ed25519: prefix", sig.GetKeyId())
	}
	if sig.GetKeyId() != f.keyID {
		t.Fatalf("key id = %q, want %q", sig.GetKeyId(), f.keyID)
	}
	verifySnapshotSignature(t, f, got)
}

func TestFederationGetSnapshotGenerationAdvances(t *testing.T) {
	f, store := newFederation(t)
	seedFederationCatalog(t, store)
	ctx := context.Background()

	first, err := f.GetSnapshot(ctx, connect.NewRequest(&federationv1.GetSnapshotRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.GetSnapshot(ctx, connect.NewRequest(&federationv1.GetSnapshotRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if first.Msg.GetGeneration() != second.Msg.GetGeneration() {
		t.Fatalf("generation changed without a catalog change: %d -> %d",
			first.Msg.GetGeneration(), second.Msg.GetGeneration())
	}

	if err := store.UpsertVersion(&catalog.Version{
		Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_TOOL, Name: "gamma",
		Version: "1.1.0", Status: commonv1.VersionStatus_VERSION_STATUS_PUBLISHED,
		BundleSize: 21,
	}); err != nil {
		t.Fatal(err)
	}
	third, err := f.GetSnapshot(ctx, connect.NewRequest(&federationv1.GetSnapshotRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if third.Msg.GetGeneration() <= second.Msg.GetGeneration() {
		t.Fatalf("generation did not advance: %d -> %d",
			second.Msg.GetGeneration(), third.Msg.GetGeneration())
	}
	if third.Msg.GetEntryCount() != 5 {
		t.Fatalf("entry count = %d, want 5", third.Msg.GetEntryCount())
	}
}

func TestFederationGetSnapshotNamespaceFilter(t *testing.T) {
	f, store := newFederation(t)
	seedFederationCatalog(t, store)

	all, err := f.GetSnapshot(context.Background(), connect.NewRequest(&federationv1.GetSnapshotRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	filtered, err := f.GetSnapshot(context.Background(), connect.NewRequest(&federationv1.GetSnapshotRequest{
		Namespaces: []string{"pirat"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Msg.GetEntryCount() != 2 || filtered.Msg.GetTombstoneCount() != 0 {
		t.Fatalf("counts = %d / %d, want 2 / 0",
			filtered.Msg.GetEntryCount(), filtered.Msg.GetTombstoneCount())
	}
	if digestEqual(all.Msg.GetRootDigest(), filtered.Msg.GetRootDigest()) {
		t.Fatal("filtered root digest must differ from the unfiltered one")
	}
	// The generation is a property of the whole catalog, not of the filter.
	if all.Msg.GetGeneration() != filtered.Msg.GetGeneration() {
		t.Fatalf("generation = %d for the full catalog, %d filtered",
			all.Msg.GetGeneration(), filtered.Msg.GetGeneration())
	}
	verifySnapshotSignature(t, f, filtered.Msg)
}

func TestFederationSyncChangesPages(t *testing.T) {
	f, store := newFederation(t)
	seedFederationCatalog(t, store)
	ctx := context.Background()

	snapshot, err := f.GetSnapshot(ctx, connect.NewRequest(&federationv1.GetSnapshotRequest{}))
	if err != nil {
		t.Fatal(err)
	}

	// The canonical order: alpha@1.0.0, yanked alpha@1.1.0, gamma@1.0.0,
	// beta@0.1.0, beta@0.2.0. The draft version never appears.
	want := []string{
		"entry:alpha@1.0.0",
		"yanked:1.1.0",
		"entry:gamma@1.0.0",
		"entry:beta@0.1.0",
		"entry:beta@0.2.0",
	}

	var got []string
	var prev *commonv1.Digest
	cursor := ""
	pages := 0
	for {
		resp, err := f.SyncChanges(ctx, connect.NewRequest(&federationv1.SyncChangesRequest{
			Cursor: cursor,
			Limit:  2,
		}))
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		page := resp.Msg
		verifyPageSignature(t, f, cursor, page)

		if page.GetGeneration() != snapshot.Msg.GetGeneration() {
			t.Fatalf("page generation = %d, snapshot generation = %d",
				page.GetGeneration(), snapshot.Msg.GetGeneration())
		}
		if page.GetPage() != uint32(pages+1) {
			t.Fatalf("page number = %d, want %d", page.GetPage(), pages+1)
		}
		if pages == 0 {
			if page.GetPrevDigest() != nil {
				t.Fatalf("first page prev digest = %v, want none", page.GetPrevDigest())
			}
		} else if !digestEqual(prev, page.GetPrevDigest()) {
			t.Fatalf("page %d prev digest = %v, want previous pageDigest %v",
				pages, page.GetPrevDigest(), prev)
		}
		for _, e := range page.GetEntries() {
			got = append(got, "entry:"+e.GetName()+"@"+e.GetVersion())
		}
		for _, tb := range page.GetTombstones() {
			got = append(got, "yanked:"+tb.GetVersion())
		}
		prev = page.GetPageDigest()
		cursor = page.GetNextCursor()
		pages++
		if pages > 10 {
			t.Fatal("sync did not terminate")
		}
		if cursor == "" {
			break
		}
	}

	if pages != 3 {
		t.Fatalf("pages = %d, want 3 (5 items, limit 2)", pages)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d changes %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("change %d = %q, want %q", i, got[i], want[i])
		}
	}
	// A fully paged walk ends on the snapshot's root digest.
	if !digestEqual(prev, snapshot.Msg.GetRootDigest()) {
		t.Fatalf("final pageDigest = %v, want snapshot rootDigest %v",
			prev, snapshot.Msg.GetRootDigest())
	}
}

func TestFederationSyncChangesDefaultAndClampedLimit(t *testing.T) {
	f, store := newFederation(t)
	seedFederationCatalog(t, store)
	ctx := context.Background()

	for _, tc := range []struct {
		name  string
		limit uint32
	}{
		{name: "default", limit: 0},
		{name: "clamped", limit: 1000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := f.SyncChanges(ctx, connect.NewRequest(&federationv1.SyncChangesRequest{
				Limit: tc.limit,
			}))
			if err != nil {
				t.Fatal(err)
			}
			if len(resp.Msg.GetEntries()) != 4 || len(resp.Msg.GetTombstones()) != 1 {
				t.Fatalf("got %d entries / %d tombstones, want 4 / 1",
					len(resp.Msg.GetEntries()), len(resp.Msg.GetTombstones()))
			}
			if resp.Msg.GetNextCursor() != "" {
				t.Fatalf("next cursor = %q, want empty (everything fits one page)",
					resp.Msg.GetNextCursor())
			}
			if resp.Msg.GetPage() != 1 {
				t.Fatalf("page = %d, want 1", resp.Msg.GetPage())
			}
			verifyPageSignature(t, f, "", resp.Msg)
		})
	}
}

func TestFederationSyncChangesIncludeYanked(t *testing.T) {
	f, store := newFederation(t)
	seedFederationCatalog(t, store)
	ctx := context.Background()

	containsVersion := func(entries []*federationv1.CatalogEntry, version string) bool {
		for _, e := range entries {
			if e.GetVersion() == version {
				return true
			}
		}
		return false
	}

	without, err := f.SyncChanges(ctx, connect.NewRequest(&federationv1.SyncChangesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if containsVersion(without.Msg.GetEntries(), "1.1.0") {
		t.Fatal("yanked version must not be an entry by default")
	}
	if len(without.Msg.GetTombstones()) != 1 || without.Msg.GetTombstones()[0].GetVersion() != "1.1.0" {
		t.Fatalf("tombstones = %v, want alpha@1.1.0", without.Msg.GetTombstones())
	}

	with, err := f.SyncChanges(ctx, connect.NewRequest(&federationv1.SyncChangesRequest{
		IncludeYanked: true,
	}))
	if err != nil {
		t.Fatal(err)
	}
	yanked := ""
	for _, e := range with.Msg.GetEntries() {
		if e.GetVersion() == "1.1.0" {
			yanked = e.GetStatus()
		}
	}
	if yanked != "yanked" {
		t.Fatalf("include_yanked entry status = %q, want yanked", yanked)
	}
	if len(with.Msg.GetTombstones()) != 1 {
		t.Fatalf("tombstones = %d, want 1", len(with.Msg.GetTombstones()))
	}
	// The flag is presentation only: the root digest must not move.
	if !digestEqual(without.Msg.GetPageDigest(), with.Msg.GetPageDigest()) {
		t.Fatal("include_yanked changed the page digest")
	}
}

func TestFederationSyncChangesInvalidCursor(t *testing.T) {
	f, store := newFederation(t)
	seedFederationCatalog(t, store)
	ctx := context.Background()

	t.Run("not a cursor", func(t *testing.T) {
		_, err := f.SyncChanges(ctx, connect.NewRequest(&federationv1.SyncChangesRequest{
			Cursor: "not-a-cursor",
		}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("code = %v, want InvalidArgument (err %v)", connect.CodeOf(err), err)
		}
	})

	t.Run("tampered payload", func(t *testing.T) {
		first, err := f.SyncChanges(ctx, connect.NewRequest(&federationv1.SyncChangesRequest{Limit: 2}))
		if err != nil {
			t.Fatal(err)
		}
		payloadB64, sigB64, _ := strings.Cut(first.Msg.GetNextCursor(), ".")
		raw, err := base64.RawURLEncoding.DecodeString(payloadB64)
		if err != nil {
			t.Fatal(err)
		}
		raw[0] ^= 0xff
		tampered := base64.RawURLEncoding.EncodeToString(raw) + "." + sigB64

		_, err = f.SyncChanges(ctx, connect.NewRequest(&federationv1.SyncChangesRequest{
			Cursor: tampered,
		}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("code = %v, want InvalidArgument (err %v)", connect.CodeOf(err), err)
		}
	})

	t.Run("cursor signed by another hub", func(t *testing.T) {
		other, _ := newFederation(t)
		seedFederationCatalog(t, other.store)
		issued, err := other.encodeCursor(federationCursor{
			generation: 1,
			offset:     2,
			filter:     namespaceFingerprint(nil),
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.SyncChanges(ctx, connect.NewRequest(&federationv1.SyncChangesRequest{
			Cursor: issued,
		}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("code = %v, want InvalidArgument (err %v)", connect.CodeOf(err), err)
		}
	})
}

func TestFederationSyncChangesStaleGeneration(t *testing.T) {
	f, store := newFederation(t)
	seedFederationCatalog(t, store)
	ctx := context.Background()

	first, err := f.SyncChanges(ctx, connect.NewRequest(&federationv1.SyncChangesRequest{Limit: 2}))
	if err != nil {
		t.Fatal(err)
	}
	cursor := first.Msg.GetNextCursor()
	if cursor == "" {
		t.Fatal("expected a next cursor")
	}

	// The catalog moves under the walk: resuming would skip or duplicate
	// changes, so the peer has to re-bootstrap from GetSnapshot.
	if err := store.UpsertVersion(&catalog.Version{
		Namespace: "pirat", Kind: commonv1.AssetKind_ASSET_KIND_SKILL, Name: "beta",
		Version: "0.3.0", Status: commonv1.VersionStatus_VERSION_STATUS_PUBLISHED,
	}); err != nil {
		t.Fatal(err)
	}

	_, err = f.SyncChanges(ctx, connect.NewRequest(&federationv1.SyncChangesRequest{Cursor: cursor}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("code = %v, want FailedPrecondition (err %v)", connect.CodeOf(err), err)
	}
}

func TestFederationSyncChangesCursorFilterMismatch(t *testing.T) {
	f, store := newFederation(t)
	seedFederationCatalog(t, store)
	ctx := context.Background()

	first, err := f.SyncChanges(ctx, connect.NewRequest(&federationv1.SyncChangesRequest{Limit: 2}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.SyncChanges(ctx, connect.NewRequest(&federationv1.SyncChangesRequest{
		Cursor:     first.Msg.GetNextCursor(),
		Namespaces: []string{"pilab"},
	}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("code = %v, want FailedPrecondition (err %v)", connect.CodeOf(err), err)
	}
}

func TestFederationSyncChangesEmptyCatalog(t *testing.T) {
	f, _ := newFederation(t)
	ctx := context.Background()

	snapshot, err := f.GetSnapshot(ctx, connect.NewRequest(&federationv1.GetSnapshotRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Msg.GetEntryCount() != 0 || snapshot.Msg.GetTombstoneCount() != 0 ||
		snapshot.Msg.GetPages() != 0 || snapshot.Msg.GetGeneration() != 1 {
		t.Fatalf("snapshot of an empty catalog = %+v", snapshot.Msg)
	}
	raw, err := canonicalJSON(wireLog{Hub: f.hubID, Items: []wireItem{}})
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(raw)
	if hex.EncodeToString(want[:]) != snapshot.Msg.GetRootDigest().GetValue() {
		t.Fatalf("root digest = %s, want %x", snapshot.Msg.GetRootDigest().GetValue(), want)
	}
	verifySnapshotSignature(t, f, snapshot.Msg)

	page, err := f.SyncChanges(ctx, connect.NewRequest(&federationv1.SyncChangesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Msg.GetEntries()) != 0 || len(page.Msg.GetTombstones()) != 0 {
		t.Fatalf("got %d entries / %d tombstones, want none",
			len(page.Msg.GetEntries()), len(page.Msg.GetTombstones()))
	}
	if page.Msg.GetNextCursor() != "" || page.Msg.GetPrevDigest() != nil {
		t.Fatalf("next cursor = %q, prev digest = %v", page.Msg.GetNextCursor(), page.Msg.GetPrevDigest())
	}
	if !digestEqual(page.Msg.GetPageDigest(), snapshot.Msg.GetRootDigest()) {
		t.Fatal("empty page digest must equal the empty catalog root digest")
	}
	verifyPageSignature(t, f, "", page.Msg)
}

func TestFederationGetHubInfo(t *testing.T) {
	f, _ := newFederation(t)
	resp, err := f.GetHubInfo(context.Background(), connect.NewRequest(&federationv1.GetHubInfoRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	hub := resp.Msg.GetHub()
	if hub.GetId() != testHubID || hub.GetBaseUrl() != testBaseURL {
		t.Fatalf("hub info = %v, want %s / %s", hub, testHubID, testBaseURL)
	}
	if hub.GetSigningAlgorithm() != "ed25519" || hub.GetProtocol() != 1 {
		t.Fatalf("signing = %s protocol %d, want ed25519 / 1",
			hub.GetSigningAlgorithm(), hub.GetProtocol())
	}
	if len(hub.GetPublicKey()) != ed25519.PublicKeySize {
		t.Fatalf("public key size = %d, want %d", len(hub.GetPublicKey()), ed25519.PublicKeySize)
	}
	if string(hub.GetPublicKey()) != string(f.publicKey()) {
		t.Fatal("public key does not match the signing key")
	}
}

// TestFederationConcurrentSnapshots runs snapshots and syncs in parallel so
// go test -race covers the generation counter.
func TestFederationConcurrentSnapshots(t *testing.T) {
	f, store := newFederation(t)
	seedFederationCatalog(t, store)
	ctx := context.Background()

	const workers = 8
	var wg sync.WaitGroup
	generations := make([]uint64, workers)
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				resp, err := f.GetSnapshot(ctx, connect.NewRequest(&federationv1.GetSnapshotRequest{}))
				if err != nil {
					errs[worker] = err
					return
				}
				generations[worker] = resp.Msg.GetGeneration()
				if _, err := f.SyncChanges(ctx, connect.NewRequest(&federationv1.SyncChangesRequest{})); err != nil {
					errs[worker] = err
					return
				}
			}
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
	}
	for i, g := range generations {
		if g != generations[0] {
			t.Fatalf("worker %d saw generation %d, worker 0 saw %d (no catalog change in between)",
				i, g, generations[0])
		}
	}
	if generations[0] < 1 {
		t.Fatalf("generation = %d, want >= 1", generations[0])
	}
}
