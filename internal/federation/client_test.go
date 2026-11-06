package federation

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"
	federationv1 "go.pilab.hu/agenthub/gen/go/agenthub/federation/v1"
	federationv1connect "go.pilab.hu/agenthub/gen/go/agenthub/federation/v1/federationv1connect"

	"go.pilab.hu/agenthub/internal/api"
	"go.pilab.hu/agenthub/internal/catalog"
)

const testHubID = "hub.test"

// pubOf returns the public half of a hub key.
func pubOf(key ed25519.PrivateKey) ed25519.PublicKey {
	return ed25519.PublicKey(append([]byte(nil), key[ed25519.SeedSize:]...))
}

func newTestKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// seedCatalog publishes a small catalog whose canonical change log
// interleaves entries and tombstones across two namespaces:
//
//	pilab/agent/alpha 1.0.0 yanked      (tombstone)
//	pilab/agent/alpha 1.1.0 published   (entry)
//	pilab/tool/gamma  1.0.0 published   (entry)
//	pirat/skill/beta  0.1.0 published   (entry)
//	pirat/skill/beta  0.2.0 deprecated (entry)
//	pirat/skill/beta  0.3.0 yanked      (tombstone)
//
// The draft version is never federated, so the log holds six items: four
// entries and two tombstones, with a tombstone before and after entries of
// other assets — the merge in changeLog.appendPage has to restore that
// interleaving to re-derive the root digest.
func seedCatalog(t *testing.T, store catalog.Store) {
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
	versions := []*catalog.Version{
		{
			Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "alpha",
			Version: "1.0.0", Status: commonv1.VersionStatus_VERSION_STATUS_YANKED,
			BundleSize: 11, PublishedAt: published,
		},
		{
			Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "alpha",
			Version: "1.1.0", Status: commonv1.VersionStatus_VERSION_STATUS_PUBLISHED,
			ManifestDigest: &commonv1.Digest{Algorithm: "sha256", Value: strings.Repeat("a", 64)},
			BundleDigest:   &commonv1.Digest{Algorithm: "sha256", Value: strings.Repeat("b", 64)},
			BundleSize:     10, PublishedAt: published,
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
		{
			Namespace: "pirat", Kind: commonv1.AssetKind_ASSET_KIND_SKILL, Name: "beta",
			Version: "0.3.0", Status: commonv1.VersionStatus_VERSION_STATUS_YANKED,
			BundleSize: 32, PublishedAt: published,
		},
	}
	for _, a := range assets {
		if err := store.UpsertAsset(a); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range versions {
		if err := store.UpsertVersion(v); err != nil {
			t.Fatal(err)
		}
	}
}

// serveHandler mounts a federation handler on an httptest server.
func serveHandler(t *testing.T, h federationv1connect.FederationServiceHandler) *httptest.Server {
	t.Helper()
	path, handler := federationv1connect.NewFederationServiceHandler(h)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// newStubHub serves the real server-side Federation handler (M4.1) over
// httptest with a fixed key, so the client under test verifies documents
// produced by the actual signing implementation. seed == nil seeds the
// catalog described on seedCatalog.
func newStubHub(t *testing.T, key ed25519.PrivateKey, seed func(*catalog.MemStore)) *httptest.Server {
	t.Helper()
	store := catalog.NewMemStore()
	if seed == nil {
		seedCatalog(t, store)
	} else {
		seed(store)
	}
	return serveHandler(t, api.NewFederationWithStore(testHubID, "https://"+testHubID, store, key))
}

// tamperHub wraps the real handler and rewrites one response after it was
// signed: what a broken or malicious hub looks like to the client.
type tamperHub struct {
	federationv1connect.FederationServiceHandler
	hubInfo  func(*federationv1.HubInfo)
	snapshot func(*federationv1.GetSnapshotResponse)
}

func (h *tamperHub) GetHubInfo(ctx context.Context, req *connect.Request[federationv1.GetHubInfoRequest]) (*connect.Response[federationv1.GetHubInfoResponse], error) {
	resp, err := h.FederationServiceHandler.GetHubInfo(ctx, req)
	if err == nil && h.hubInfo != nil {
		h.hubInfo(resp.Msg.GetHub())
	}
	return resp, err
}

func (h *tamperHub) GetSnapshot(ctx context.Context, req *connect.Request[federationv1.GetSnapshotRequest]) (*connect.Response[federationv1.GetSnapshotResponse], error) {
	resp, err := h.FederationServiceHandler.GetSnapshot(ctx, req)
	if err == nil && h.snapshot != nil {
		h.snapshot(resp.Msg)
	}
	return resp, err
}

// foreignSigner serves one hub's identity and another hub's signed
// documents: the classic forged-signature case.
type foreignSigner struct {
	federationv1connect.FederationServiceHandler                                              // serves the documents
	info                                         federationv1connect.FederationServiceHandler // serves GetHubInfo
}

func (h *foreignSigner) GetHubInfo(ctx context.Context, req *connect.Request[federationv1.GetHubInfoRequest]) (*connect.Response[federationv1.GetHubInfoResponse], error) {
	return h.info.GetHubInfo(ctx, req)
}

func TestSyncBootstrapsAndVerifies(t *testing.T) {
	t.Parallel()

	key := newTestKey(t)
	srv := newStubHub(t, key, nil)
	client, err := NewUpstreamClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	res, err := client.Sync(context.Background(), nil)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Hub.ID != testHubID {
		t.Fatalf("hub id = %q, want %q", res.Hub.ID, testHubID)
	}
	if res.Generation < 1 {
		t.Fatalf("generation = %d, want >= 1", res.Generation)
	}
	if want := Fingerprint(pubOf(key)); res.Hub.Fingerprint != want {
		t.Fatalf("fingerprint = %q, want %q", res.Hub.Fingerprint, want)
	}
	if res.Pages != 1 {
		t.Fatalf("pages = %d, want 1", res.Pages)
	}
	if !strings.HasPrefix(res.RootDigest, "sha256:") {
		t.Fatalf("root digest = %q, want sha256:…", res.RootDigest)
	}

	// The walk returned the four published versions and the two yanks, in
	// the hub's canonical order.
	wantAssets := []string{
		"pilab/agent/alpha@1.1.0",
		"pilab/tool/gamma@1.0.0",
		"pirat/skill/beta@0.1.0",
		"pirat/skill/beta@0.2.0",
	}
	var gotAssets []string
	for _, a := range res.Assets {
		gotAssets = append(gotAssets, a.GetNamespace()+"/"+kindSlug(a.GetKind())+"/"+a.GetName()+"@"+a.GetVersion())
		if a.GetOriginHub() != testHubID {
			t.Fatalf("asset %s origin hub = %q, want %q", a.GetId(), a.GetOriginHub(), testHubID)
		}
	}
	if len(gotAssets) != len(wantAssets) {
		t.Fatalf("assets = %v, want %v", gotAssets, wantAssets)
	}
	for i := range wantAssets {
		if gotAssets[i] != wantAssets[i] {
			t.Fatalf("assets[%d] = %q, want %q (all: %v)", i, gotAssets[i], wantAssets[i], gotAssets)
		}
	}
	var gotTombs []string
	for _, tb := range res.Tombstones {
		gotTombs = append(gotTombs, tb.GetId()+"@"+tb.GetVersion())
	}
	wantTombs := []string{
		"agenthub://" + testHubID + "/pilab/agent/alpha@1.0.0",
		"agenthub://" + testHubID + "/pirat/skill/beta@0.3.0",
	}
	if len(gotTombs) != len(wantTombs) {
		t.Fatalf("tombstones = %v, want %v", gotTombs, wantTombs)
	}
	for i := range wantTombs {
		if gotTombs[i] != wantTombs[i] {
			t.Fatalf("tombstones[%d] = %q, want %q", i, gotTombs[i], wantTombs[i])
		}
	}
}

func TestSyncPagedWalk(t *testing.T) {
	t.Parallel()

	key := newTestKey(t)
	srv := newStubHub(t, key, nil)
	// Two entries per page: six log items walk over three pages, so the
	// prevDigest chain and the cross-page merge are exercised end to end.
	client, err := NewUpstreamClient(srv.URL, WithPageSize(2))
	if err != nil {
		t.Fatal(err)
	}

	res, err := client.Sync(context.Background(), nil)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Pages != 3 {
		t.Fatalf("pages = %d, want 3", res.Pages)
	}
	if len(res.Assets) != 4 || len(res.Tombstones) != 2 {
		t.Fatalf("got %d assets and %d tombstones, want 4 and 2",
			len(res.Assets), len(res.Tombstones))
	}
}

func TestSyncEmptyCatalog(t *testing.T) {
	t.Parallel()

	key := newTestKey(t)
	srv := newStubHub(t, key, func(*catalog.MemStore) {})
	client, err := NewUpstreamClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	res, err := client.Sync(context.Background(), nil)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(res.Assets) != 0 || len(res.Tombstones) != 0 || res.Pages != 1 {
		t.Fatalf("empty catalog: %d assets, %d tombstones, %d pages",
			len(res.Assets), len(res.Tombstones), res.Pages)
	}
	// The root digest of an empty log still has to be recomputed from the
	// pages, not copied from the snapshot.
	log := &changeLog{hubID: res.Hub.ID}
	empty, err := log.rootDigest()
	if err != nil {
		t.Fatal(err)
	}
	if res.RootDigest != empty {
		t.Fatalf("root digest = %q, want %q", res.RootDigest, empty)
	}
}

func TestSyncRejectsTamperedSnapshot(t *testing.T) {
	t.Parallel()

	key := newTestKey(t)
	store := catalog.NewMemStore()
	seedCatalog(t, store)
	real := api.NewFederationWithStore(testHubID, "https://"+testHubID, store, key)
	srv := serveHandler(t, &tamperHub{
		FederationServiceHandler: real,
		snapshot: func(resp *federationv1.GetSnapshotResponse) {
			resp.EntryCount++ // changes the signed document after signing
		},
	})
	client, err := NewUpstreamClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := client.Sync(context.Background(), nil); !strings.Contains(err.Error(), ErrSignature.Error()) {
		t.Fatalf("sync error = %v, want %v", err, ErrSignature)
	}
}

func TestSyncRejectsForeignSigner(t *testing.T) {
	t.Parallel()

	honestKey := newTestKey(t)
	foreignKey := newTestKey(t)
	honestStore := catalog.NewMemStore()
	seedCatalog(t, honestStore)
	foreignStore := catalog.NewMemStore()
	seedCatalog(t, foreignStore)

	honest := api.NewFederationWithStore(testHubID, "https://"+testHubID, honestStore, honestKey)
	foreign := api.NewFederationWithStore(testHubID, "https://"+testHubID, foreignStore, foreignKey)
	srv := serveHandler(t, &foreignSigner{FederationServiceHandler: foreign, info: honest})

	client, err := NewUpstreamClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Sync(context.Background(), nil); !strings.Contains(err.Error(), ErrSignature.Error()) {
		t.Fatalf("sync error = %v, want %v", err, ErrSignature)
	}
}

func TestPinnedTrustAnchors(t *testing.T) {
	t.Parallel()

	key := newTestKey(t)
	srv := newStubHub(t, key, nil)

	t.Run("key mismatch", func(t *testing.T) {
		_, other, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		client, err := NewUpstreamClient(srv.URL, WithPinnedKey(pubOf(other)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.HubInfo(context.Background()); !strings.Contains(err.Error(), ErrKeyMismatch.Error()) {
			t.Fatalf("hub info error = %v, want %v", err, ErrKeyMismatch)
		}
	})

	t.Run("hub id mismatch", func(t *testing.T) {
		client, err := NewUpstreamClient(srv.URL, WithPinnedHubID("other.test"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.HubInfo(context.Background()); !strings.Contains(err.Error(), ErrHubIDMismatch.Error()) {
			t.Fatalf("hub info error = %v, want %v", err, ErrHubIDMismatch)
		}
	})

	t.Run("matching pins", func(t *testing.T) {
		client, err := NewUpstreamClient(srv.URL,
			WithPinnedKey(pubOf(key)),
			WithPinnedHubID(testHubID))
		if err != nil {
			t.Fatal(err)
		}
		hub, err := client.HubInfo(context.Background())
		if err != nil {
			t.Fatalf("hub info: %v", err)
		}
		if hub.ID != testHubID {
			t.Fatalf("hub id = %q, want %q", hub.ID, testHubID)
		}
	})
}

func TestHubInfoRejectsBadSelfDescription(t *testing.T) {
	t.Parallel()

	key := newTestKey(t)
	store := catalog.NewMemStore()
	real := api.NewFederationWithStore(testHubID, "https://"+testHubID, store, key)

	cases := []struct {
		name   string
		mutate func(*federationv1.HubInfo)
		want   error
	}{
		{
			name:   "protocol",
			mutate: func(h *federationv1.HubInfo) { h.Protocol = 2 },
			want:   ErrProtocol,
		},
		{
			name:   "signing algorithm",
			mutate: func(h *federationv1.HubInfo) { h.SigningAlgorithm = "ecdsa" },
			want:   ErrUnsupportedAlgorithm,
		},
		{
			name:   "short key",
			mutate: func(h *federationv1.HubInfo) { h.PublicKey = make([]byte, 16) },
			want:   ErrNoPublicKey,
		},
		{
			name:   "missing key",
			mutate: func(h *federationv1.HubInfo) { h.PublicKey = nil },
			want:   ErrNoPublicKey,
		},
		{
			name:   "missing hub id",
			mutate: func(h *federationv1.HubInfo) { h.Id = "" },
			want:   errors.New("federation: hub info carries no hub id"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			srv := serveHandler(t, &tamperHub{
				FederationServiceHandler: real,
				hubInfo:                  tc.mutate,
			})
			client, err := NewUpstreamClient(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.HubInfo(context.Background())
			if err == nil {
				t.Fatal("hub info succeeded, want an error")
			}
			if !strings.Contains(err.Error(), tc.want.Error()) {
				t.Fatalf("hub info error = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestSnapshotAndSyncChangesManualWalk drives the public incremental API
// the way a sync worker with a stored cursor would: Snapshot once, then
// SyncChanges per page, checking the chain by hand.
func TestSnapshotAndSyncChangesManualWalk(t *testing.T) {
	t.Parallel()

	key := newTestKey(t)
	srv := newStubHub(t, key, nil)
	client, err := NewUpstreamClient(srv.URL, WithPageSize(2))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	snap, err := client.Snapshot(ctx, nil)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.EntryCount != 4 || snap.TombstoneCount != 2 {
		t.Fatalf("counts = %d/%d, want 4/2", snap.EntryCount, snap.TombstoneCount)
	}

	log := &changeLog{hubID: snap.Hub.ID}
	cursor := ""
	var prevDigest string
	pages := 0
	for {
		page, err := client.SyncChanges(ctx, cursor, nil, 2)
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		if page.Generation != snap.Generation {
			t.Fatalf("page generation = %d, snapshot %d", page.Generation, snap.Generation)
		}
		if page.PrevDigest != prevDigest {
			t.Fatalf("page %d prevDigest = %q, want %q", pages, page.PrevDigest, prevDigest)
		}
		if err := log.appendPage(&federationv1.SyncChangesResponse{
			Entries:    page.Entries,
			Tombstones: page.Tombstones,
		}); err != nil {
			t.Fatal(err)
		}
		pages++
		if page.NextCursor == "" {
			if page.PageDigest != snap.RootDigest {
				t.Fatalf("final pageDigest = %q, want %q", page.PageDigest, snap.RootDigest)
			}
			break
		}
		prevDigest = page.PageDigest
		cursor = page.NextCursor
	}
	if pages != 3 {
		t.Fatalf("pages = %d, want 3", pages)
	}
	if got := log.entryCount(); got != snap.EntryCount {
		t.Fatalf("entries = %d, snapshot commits to %d", got, snap.EntryCount)
	}
	if got := log.tombstoneCount(); got != snap.TombstoneCount {
		t.Fatalf("tombstones = %d, snapshot commits to %d", got, snap.TombstoneCount)
	}
	root, err := log.rootDigest()
	if err != nil {
		t.Fatal(err)
	}
	if root != snap.RootDigest {
		t.Fatalf("recomputed rootDigest = %q, want %q", root, snap.RootDigest)
	}
}

// TestUpstreamClientConcurrent exercises the cached hub identity and the
// server handler under -race with parallel syncs.
func TestUpstreamClientConcurrent(t *testing.T) {
	t.Parallel()

	key := newTestKey(t)
	srv := newStubHub(t, key, nil)
	client, err := NewUpstreamClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := client.Sync(context.Background(), nil)
			if err != nil {
				t.Errorf("sync: %v", err)
				return
			}
			if len(res.Assets) != 4 {
				t.Errorf("assets = %d, want 4", len(res.Assets))
			}
		}()
	}
	wg.Wait()
}

func TestNormalizeHubURL(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want string
		err  bool
	}{
		{in: "https://hub.example", want: "https://hub.example"},
		{in: "hub.example", want: "https://hub.example"},
		{in: "https://hub.example/", want: "https://hub.example"},
		{in: "  http://hub.example/base/  ", want: "http://hub.example/base"},
		{in: "", err: true},
		{in: "ftp://hub.example", err: true},
		{in: "https://", err: true},
	}
	for _, tc := range cases {
		got, err := normalizeHubURL(tc.in)
		if tc.err {
			if err == nil {
				t.Errorf("normalizeHubURL(%q) = %q, want an error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("normalizeHubURL(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("normalizeHubURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestKeyIDAndFingerprint(t *testing.T) {
	t.Parallel()

	key := newTestKey(t)
	pub := pubOf(key)
	if !strings.HasPrefix(KeyID(pub), "ed25519:") || len(KeyID(pub)) != len("ed25519:")+16 {
		t.Fatalf("key id = %q", KeyID(pub))
	}
	fp := Fingerprint(pub)
	if !strings.HasPrefix(fp, "sha256:") || len(fp) != len("sha256:")+64 {
		t.Fatalf("fingerprint = %q", fp)
	}
	normalized, err := ParseFingerprint(strings.TrimPrefix(fp, "sha256:"))
	if err != nil {
		t.Fatal(err)
	}
	if normalized != fp {
		t.Fatalf("normalized fingerprint = %q, want %q", normalized, fp)
	}
	if _, err := ParseFingerprint("sha256:nope"); err == nil {
		t.Fatal("ParseFingerprint accepted a non-digest")
	}
}
