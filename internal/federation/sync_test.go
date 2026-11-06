package federation

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"
	federationv1 "go.pilab.hu/agenthub/gen/go/agenthub/federation/v1"
)

// These tests drive collectSync with hand-signed pages instead of a
// network: they cover the parts of the walk a live hub cannot produce on
// demand — a chain break, wrong counts, a foreign generation — by signing
// exactly what a misbehaving hub would send.

func testHub(key ed25519.PrivateKey) HubInfo {
	pub := ed25519.PublicKey(append([]byte(nil), key[ed25519.SeedSize:]...))
	return HubInfo{
		ID:          testHubID,
		BaseURL:     "https://" + testHubID,
		Protocol:    protocolVersion,
		PublicKey:   pub,
		KeyID:       KeyID(pub),
		Fingerprint: Fingerprint(pub),
	}
}

// signSnapshot stamps a snapshot response with a valid hub signature over
// the document as it currently stands.
func signSnapshot(t *testing.T, key ed25519.PrivateKey, resp *federationv1.GetSnapshotResponse, at time.Time) {
	t.Helper()
	doc := wireSnapshot{
		SchemaVersion:  schemaVersion,
		Hub:            testHubID,
		Generation:     resp.GetGeneration(),
		CreatedAt:      at.UTC().Format(time.RFC3339Nano),
		Pages:          resp.GetPages(),
		EntryCount:     resp.GetEntryCount(),
		TombstoneCount: resp.GetTombstoneCount(),
		RootDigest:     digestText(resp.GetRootDigest()),
	}
	signInto(t, key, doc, at, func(sig *commonv1.Signature) { resp.Signature = sig })
}

// signPage stamps a page response with a valid hub signature over the
// document for the cursor the client sent.
func signPage(t *testing.T, key ed25519.PrivateKey, cursor string, resp *federationv1.SyncChangesResponse, at time.Time) {
	t.Helper()
	doc := wirePage{
		SchemaVersion: schemaVersion,
		Hub:           testHubID,
		Generation:    resp.GetGeneration(),
		Page:          resp.GetPage(),
		Cursor:        cursor,
		PrevDigest:    digestText(resp.GetPrevDigest()),
		NextCursor:    resp.GetNextCursor(),
		IssuedAt:      at.UTC().Format(time.RFC3339Nano),
		Entries:       make([]wireEntry, 0, len(resp.GetEntries())),
		Tombstones:    make([]wireTombstone, 0, len(resp.GetTombstones())),
	}
	for _, e := range resp.GetEntries() {
		doc.Entries = append(doc.Entries, wireEntryOf(e))
	}
	for _, tb := range resp.GetTombstones() {
		doc.Tombstones = append(doc.Tombstones, wireTombstoneOf(tb))
	}
	signInto(t, key, doc, at, func(sig *commonv1.Signature) { resp.Signature = sig })
}

// signInto canonicalizes doc, signs it with the hub key and hands the
// signature to set.
func signInto(t *testing.T, key ed25519.PrivateKey, doc any, at time.Time, set func(*commonv1.Signature)) {
	t.Helper()
	payload, err := canonicalJSON(doc)
	if err != nil {
		t.Fatal(err)
	}
	set(&commonv1.Signature{
		KeyId:     KeyID(pubOf(key)),
		Algorithm: signingAlgorithm,
		Value:     ed25519.Sign(key, payload),
		SignedAt:  timestamppb.New(at),
	})
}

func testEntry(namespace string, kind commonv1.AssetKind, name, version, status string) *federationv1.CatalogEntry {
	return &federationv1.CatalogEntry{
		Id:        "agenthub://" + testHubID + "/" + namespace + "/" + kindSlug(kind) + "/" + name,
		Kind:      kind,
		Namespace: namespace,
		Name:      name,
		Version:   version,
		OriginHub: testHubID,
		Status:    status,
		Size:      1024,
		Tags:      []string{"federated"},
	}
}

func testTombstone(namespace string, kind commonv1.AssetKind, name, version string) *federationv1.Tombstone {
	return &federationv1.Tombstone{
		Id:      "agenthub://" + testHubID + "/" + namespace + "/" + kindSlug(kind) + "/" + name,
		Version: version,
		Reason:  "yanked",
	}
}

// rootOf is the root digest of one change-log selection.
func rootOf(t *testing.T, items ...logItem) string {
	t.Helper()
	l := &changeLog{hubID: testHubID, items: items}
	digest, err := l.rootDigest()
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func entryItem(e *federationv1.CatalogEntry) logItem { return logItem{entry: e} }

func tombstoneItem(tb *federationv1.Tombstone) logItem { return logItem{tombstone: tb} }

func digestMsg(t *testing.T, text string) *commonv1.Digest {
	t.Helper()
	alg, value, ok := strings.Cut(text, ":")
	if !ok {
		t.Fatalf("digest %q has no algorithm", text)
	}
	return &commonv1.Digest{Algorithm: alg, Value: value}
}

// fixture is a four-item change log split over three pages:
//
//	[e1 pilab/agent/alpha] [t1 pilab/agent/beta] [e2 pilab/tool/gamma] [e3 pirat/skill/delta]
type fixture struct {
	key  ed25519.PrivateKey
	hub  HubInfo
	root string
	e1   *federationv1.CatalogEntry
	t1   *federationv1.Tombstone
	e2   *federationv1.CatalogEntry
	e3   *federationv1.CatalogEntry
	// prefixes[i] is the root digest of the log through page i (1-based).
	prefixes []string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	key := newTestKey(t)
	f := &fixture{
		key: key,
		hub: testHub(key),
		e1:  testEntry("pilab", commonv1.AssetKind_ASSET_KIND_AGENT, "alpha", "1.0.0", "published"),
		t1:  testTombstone("pilab", commonv1.AssetKind_ASSET_KIND_AGENT, "beta", "1.0.0"),
		e2:  testEntry("pilab", commonv1.AssetKind_ASSET_KIND_TOOL, "gamma", "1.0.0", "published"),
		e3:  testEntry("pirat", commonv1.AssetKind_ASSET_KIND_SKILL, "delta", "2.0.0", "deprecated"),
	}
	f.prefixes = []string{
		rootOf(t, entryItem(f.e1), tombstoneItem(f.t1)),
		rootOf(t, entryItem(f.e1), tombstoneItem(f.t1), entryItem(f.e2)),
		rootOf(t, entryItem(f.e1), tombstoneItem(f.t1), entryItem(f.e2), entryItem(f.e3)),
	}
	f.root = f.prefixes[2]
	return f
}

// snapshot is the descriptor this walk must reproduce: three entries, one
// tombstone, root over all four items.
func (f *fixture) snapshot(t *testing.T) *federationv1.GetSnapshotResponse {
	t.Helper()
	resp := &federationv1.GetSnapshotResponse{
		Generation:     7,
		Pages:          2,
		EntryCount:     3,
		TombstoneCount: 1,
		RootDigest:     digestMsg(t, f.root),
	}
	signSnapshot(t, f.key, resp, time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC))
	return resp
}

func (f *fixture) snap(t *testing.T) Snapshot {
	resp := f.snapshot(t)
	return Snapshot{
		Hub:            f.hub,
		Generation:     resp.GetGeneration(),
		Pages:          resp.GetPages(),
		EntryCount:     resp.GetEntryCount(),
		TombstoneCount: resp.GetTombstoneCount(),
		RootDigest:     digestText(resp.GetRootDigest()),
	}
}

// page1, page2, page3 are the three pages of the fixture walk, each signed
// for the cursor it is served under.
func (f *fixture) page1(t *testing.T, at time.Time) *federationv1.SyncChangesResponse {
	resp := &federationv1.SyncChangesResponse{
		Generation: 7,
		Page:       1,
		NextCursor: "c1",
		Entries:    []*federationv1.CatalogEntry{f.e1},
		Tombstones: []*federationv1.Tombstone{f.t1},
		PageDigest: digestMsg(t, f.prefixes[0]),
	}
	signPage(t, f.key, "", resp, at)
	return resp
}

func (f *fixture) page2(t *testing.T, at time.Time) *federationv1.SyncChangesResponse {
	resp := &federationv1.SyncChangesResponse{
		Generation: 7,
		Page:       2,
		PrevDigest: digestMsg(t, f.prefixes[0]),
		NextCursor: "c2",
		Entries:    []*federationv1.CatalogEntry{f.e2},
		PageDigest: digestMsg(t, f.prefixes[1]),
	}
	signPage(t, f.key, "c1", resp, at)
	return resp
}

func (f *fixture) page3(t *testing.T, at time.Time) *federationv1.SyncChangesResponse {
	resp := &federationv1.SyncChangesResponse{
		Generation: 7,
		Page:       3,
		PrevDigest: digestMsg(t, f.prefixes[1]),
		Entries:    []*federationv1.CatalogEntry{f.e3},
		PageDigest: digestMsg(t, f.root),
	}
	signPage(t, f.key, "c2", resp, at)
	return resp
}

// scriptedFetch serves pages by cursor and records the cursors it was
// asked for.
type scriptedFetch struct {
	pages map[string]*federationv1.SyncChangesResponse
	asked []string
}

func (s *scriptedFetch) fetch(_ context.Context, cursor string) (*federationv1.SyncChangesResponse, error) {
	s.asked = append(s.asked, cursor)
	resp, ok := s.pages[cursor]
	if !ok {
		return nil, fmt.Errorf("federation: no scripted page for cursor %q", cursor)
	}
	return resp, nil
}

func TestCollectSyncVerifiesFullWalk(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	at := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	fetch := &scriptedFetch{pages: map[string]*federationv1.SyncChangesResponse{
		"":   f.page1(t, at),
		"c1": f.page2(t, at),
		"c2": f.page3(t, at),
	}}

	res, err := collectSync(context.Background(), f.hub, f.snap(t), fetch.fetch)
	if err != nil {
		t.Fatalf("collectSync: %v", err)
	}
	if want := []string{"", "c1", "c2"}; len(fetch.asked) != 3 || fetch.asked[0] != "" || fetch.asked[1] != "c1" || fetch.asked[2] != "c2" {
		t.Fatalf("asked for cursors %v, want %v", fetch.asked, want)
	}
	if res.Pages != 3 || res.Generation != 7 || res.RootDigest != f.root {
		t.Fatalf("pages=%d generation=%d root=%q, want 3/7/%q",
			res.Pages, res.Generation, res.RootDigest, f.root)
	}
	if len(res.Assets) != 3 || res.Assets[0] != f.e1 || res.Assets[1] != f.e2 || res.Assets[2] != f.e3 {
		t.Fatalf("assets = %d entries in walk order, want e1,e2,e3", len(res.Assets))
	}
	if len(res.Tombstones) != 1 || res.Tombstones[0] != f.t1 {
		t.Fatalf("tombstones = %d, want the single yank", len(res.Tombstones))
	}
}

func TestCollectSyncRejectsBadWalks(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		build func(t *testing.T, f *fixture) (*federationv1.GetSnapshotResponse, *scriptedFetch)
		want  error
	}{
		{
			// The page was signed for another cursor: its signature does
			// not cover the position the client asked from.
			name: "page signed for another cursor",
			build: func(t *testing.T, f *fixture) (*federationv1.GetSnapshotResponse, *scriptedFetch) {
				at := time.Now()
				wrong := f.page2(t, at)
				signPage(t, f.key, "somewhere-else", wrong, at)
				return f.snapshot(t), &scriptedFetch{pages: map[string]*federationv1.SyncChangesResponse{
					"":   f.page1(t, at),
					"c1": wrong,
				}}
			},
			want: ErrSignature,
		},
		{
			// A validly signed page whose prevDigest does not continue the
			// chain: a gap the client must not walk over.
			name: "broken prevDigest chain",
			build: func(t *testing.T, f *fixture) (*federationv1.GetSnapshotResponse, *scriptedFetch) {
				at := time.Now()
				broken := f.page2(t, at)
				broken.PrevDigest = digestMsg(t, f.root) // not where page 1 ended
				signPage(t, f.key, "c1", broken, at)
				return f.snapshot(t), &scriptedFetch{pages: map[string]*federationv1.SyncChangesResponse{
					"":   f.page1(t, at),
					"c1": broken,
				}}
			},
			want: ErrDigest,
		},
		{
			name: "generation gap",
			build: func(t *testing.T, f *fixture) (*federationv1.GetSnapshotResponse, *scriptedFetch) {
				at := time.Now()
				gap := f.page1(t, at)
				gap.Generation = f.snap(t).Generation + 1
				signPage(t, f.key, "", gap, at)
				return f.snapshot(t), &scriptedFetch{pages: map[string]*federationv1.SyncChangesResponse{
					"": gap,
				}}
			},
			want: ErrGenerationGap,
		},
		{
			name: "snapshot counts do not match the pages",
			build: func(t *testing.T, f *fixture) (*federationv1.GetSnapshotResponse, *scriptedFetch) {
				at := time.Now()
				liar := f.snapshot(t)
				liar.EntryCount = 99
				signSnapshot(t, f.key, liar, at)
				return liar, &scriptedFetch{pages: map[string]*federationv1.SyncChangesResponse{
					"":   f.page1(t, at),
					"c1": f.page2(t, at),
					"c2": f.page3(t, at),
				}}
			},
			want: ErrDigest,
		},
		{
			name: "final pageDigest misses the snapshot root",
			build: func(t *testing.T, f *fixture) (*federationv1.GetSnapshotResponse, *scriptedFetch) {
				at := time.Now()
				liar := f.snapshot(t)
				liar.RootDigest = digestMsg(t, f.prefixes[1])
				signSnapshot(t, f.key, liar, at)
				return liar, &scriptedFetch{pages: map[string]*federationv1.SyncChangesResponse{
					"":   f.page1(t, at),
					"c1": f.page2(t, at),
					"c2": f.page3(t, at),
				}}
			},
			want: ErrDigest,
		},
		{
			// An entry and a tombstone for the same version: the log the
			// client rebuilds would not match the hub's, so the walk fails
			// closed instead of guessing an order.
			name: "entry and tombstone for the same version",
			build: func(t *testing.T, f *fixture) (*federationv1.GetSnapshotResponse, *scriptedFetch) {
				at := time.Now()
				dup := &federationv1.SyncChangesResponse{
					Generation: 7,
					Page:       1,
					Entries:    []*federationv1.CatalogEntry{f.e1},
					Tombstones: []*federationv1.Tombstone{
						testTombstone("pilab", commonv1.AssetKind_ASSET_KIND_AGENT, "alpha", "1.0.0"),
					},
					PageDigest: digestMsg(t, f.prefixes[2]),
				}
				signPage(t, f.key, "", dup, at)
				snap := f.snapshot(t)
				snap.EntryCount = 1
				snap.TombstoneCount = 1
				signSnapshot(t, f.key, snap, at)
				return snap, &scriptedFetch{pages: map[string]*federationv1.SyncChangesResponse{
					"": dup,
				}}
			},
			want: ErrDigest,
		},
		{
			name: "cursor does not advance",
			build: func(t *testing.T, f *fixture) (*federationv1.GetSnapshotResponse, *scriptedFetch) {
				at := time.Now()
				stuck := f.page2(t, at)
				stuck.NextCursor = "c1" // the cursor it was served under
				signPage(t, f.key, "c1", stuck, at)
				return f.snapshot(t), &scriptedFetch{pages: map[string]*federationv1.SyncChangesResponse{
					"":   f.page1(t, at),
					"c1": stuck,
				}}
			},
			want: ErrDigest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)
			snapResp, fetch := tc.build(t, f)
			snap := Snapshot{
				Hub:            f.hub,
				Generation:     snapResp.GetGeneration(),
				Pages:          snapResp.GetPages(),
				EntryCount:     snapResp.GetEntryCount(),
				TombstoneCount: snapResp.GetTombstoneCount(),
				RootDigest:     digestText(snapResp.GetRootDigest()),
			}
			_, err := collectSync(context.Background(), f.hub, snap, fetch.fetch)
			if err == nil {
				t.Fatal("collectSync succeeded, want an error")
			}
			if !strings.Contains(err.Error(), tc.want.Error()) {
				t.Fatalf("collectSync error = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestCollectSyncBoundsPages scripts an endless upstream: every page is
// well-formed and signed, but nextCursor never runs out.
func TestCollectSyncBoundsPages(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	at := time.Now()
	pages := 0
	fetch := func(_ context.Context, cursor string) (*federationv1.SyncChangesResponse, error) {
		pages++
		resp := &federationv1.SyncChangesResponse{
			Generation: 7,
			Page:       uint32(pages),
			NextCursor: fmt.Sprintf("c%d", pages),
			PageDigest: digestMsg(t, f.prefixes[0]),
		}
		if pages > 1 {
			resp.PrevDigest = digestMsg(t, f.prefixes[0])
		}
		signPage(t, f.key, cursor, resp, at)
		return resp, nil
	}

	_, err := collectSync(context.Background(), f.hub, f.snap(t), fetch)
	if err == nil {
		t.Fatal("collectSync succeeded on an endless walk")
	}
	if !strings.Contains(err.Error(), ErrTooManyPages.Error()) {
		t.Fatalf("collectSync error = %v, want %v", err, ErrTooManyPages)
	}
	if pages != MaxPages {
		t.Fatalf("fetched %d pages, want %d", pages, MaxPages)
	}
}
