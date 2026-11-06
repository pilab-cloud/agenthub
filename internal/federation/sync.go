package federation

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	federationv1 "go.pilab.hu/agenthub/gen/go/agenthub/federation/v1"
)

// Snapshot is a verified descriptor of one upstream catalog generation: the
// hub signature checked out, so generation, counts and root digest can be
// trusted as the target of a page walk.
type Snapshot struct {
	Hub            HubInfo
	Generation     uint64
	Pages          uint32
	EntryCount     uint64
	TombstoneCount uint64
	// RootDigest is "sha256:…" over the canonical change log the pages must
	// reproduce (federation.md §4.2).
	RootDigest string
}

// Page is one verified page of catalog changes.
type Page struct {
	Hub        HubInfo
	Generation uint64
	Page       uint32
	PrevDigest string
	NextCursor string
	PageDigest string
	// Entries and Tombstones are the verified contents of this page, in the
	// hub's canonical order.
	Entries    []*federationv1.CatalogEntry
	Tombstones []*federationv1.Tombstone
}

// SyncResult is one verified bootstrap of an upstream catalog: every
// signature checked against the trusted hub key, every page chained to the
// previous one, and the recomputed root digest equal to the snapshot's.
type SyncResult struct {
	Hub        HubInfo
	Generation uint64
	// RootDigest is the digest recomputed from the fetched pages; it equals
	// the signed snapshot root digest.
	RootDigest string
	// Pages is the number of SyncChanges pages this walk consumed.
	Pages int
	// Assets are the upstream versions the signatures commit to, in the
	// hub's canonical order. Catalog membership only: verifying a manifest
	// or bundle is a separate step (federation.md §6.1).
	Assets []*federationv1.CatalogEntry
	// Tombstones are the yanks the same walk delivered.
	Tombstones []*federationv1.Tombstone
}

// Snapshot fetches and verifies the signed snapshot descriptor of the
// upstream catalog, optionally restricted to namespaces.
func (c *UpstreamClient) Snapshot(ctx context.Context, namespaces []string) (Snapshot, error) {
	hub, err := c.HubInfo(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	return c.snapshot(ctx, hub, namespaces)
}

func (c *UpstreamClient) snapshot(ctx context.Context, hub HubInfo, namespaces []string) (Snapshot, error) {
	resp, err := c.client.GetSnapshot(ctx, connect.NewRequest(&federationv1.GetSnapshotRequest{
		Namespaces: namespaces,
	}))
	if err != nil {
		return Snapshot{}, fmt.Errorf("federation: get snapshot: %w", err)
	}
	if err := verifySnapshot(hub, resp.Msg); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{
		Hub:            hub,
		Generation:     resp.Msg.GetGeneration(),
		Pages:          resp.Msg.GetPages(),
		EntryCount:     resp.Msg.GetEntryCount(),
		TombstoneCount: resp.Msg.GetTombstoneCount(),
		RootDigest:     digestText(resp.Msg.GetRootDigest()),
	}, nil
}

// SyncChanges fetches and verifies one page of catalog changes after
// cursor. An empty cursor asks for the first page, which is what a
// bootstrap starts with. The page signature commits to cursor, so a page
// issued for another position of the walk is rejected here.
func (c *UpstreamClient) SyncChanges(ctx context.Context, cursor string, namespaces []string, limit uint32) (Page, error) {
	hub, err := c.HubInfo(ctx)
	if err != nil {
		return Page{}, err
	}
	resp, err := c.rawSyncChanges(ctx, cursor, namespaces, limit)
	if err != nil {
		return Page{}, err
	}
	if err := verifyPage(hub, cursor, resp); err != nil {
		return Page{}, err
	}
	return pageOf(hub, resp), nil
}

func (c *UpstreamClient) rawSyncChanges(ctx context.Context, cursor string, namespaces []string, limit uint32) (*federationv1.SyncChangesResponse, error) {
	resp, err := c.client.SyncChanges(ctx, connect.NewRequest(&federationv1.SyncChangesRequest{
		Cursor:     cursor,
		Limit:      limit,
		Namespaces: namespaces,
	}))
	if err != nil {
		return nil, fmt.Errorf("federation: sync changes: %w", err)
	}
	return resp.Msg, nil
}

func pageOf(hub HubInfo, resp *federationv1.SyncChangesResponse) Page {
	return Page{
		Hub:        hub,
		Generation: resp.GetGeneration(),
		Page:       resp.GetPage(),
		PrevDigest: digestText(resp.GetPrevDigest()),
		NextCursor: resp.GetNextCursor(),
		PageDigest: digestText(resp.GetPageDigest()),
		Entries:    resp.GetEntries(),
		Tombstones: resp.GetTombstones(),
	}
}

// Sync bootstraps the upstream catalog: GetHubInfo, then GetSnapshot, then
// every SyncChanges page until nextCursor is empty. Each document is
// verified against the trusted hub key and the walk as a whole must chain
// back to the signed snapshot root digest, so the returned assets are the
// catalog membership the hub committed to (federation.md §7.1).
//
// It does not store anything locally: the caller decides what to do with
// the verified assets.
func (c *UpstreamClient) Sync(ctx context.Context, namespaces []string) (*SyncResult, error) {
	hub, err := c.HubInfo(ctx)
	if err != nil {
		return nil, err
	}
	snap, err := c.snapshot(ctx, hub, namespaces)
	if err != nil {
		return nil, err
	}
	return collectSync(ctx, hub, snap, func(ctx context.Context, cursor string) (*federationv1.SyncChangesResponse, error) {
		return c.rawSyncChanges(ctx, cursor, namespaces, c.pageSize)
	})
}

// pageFetcher returns the page positioned at cursor. collectSync verifies
// what it gets, so a fetcher only has to transport bytes.
type pageFetcher func(ctx context.Context, cursor string) (*federationv1.SyncChangesResponse, error)

// collectSync walks the pages of one snapshot and verifies the walk:
// every page signature, one consistent generation, the prevDigest chain,
// and finally the snapshot's counts and root digest recomputed from the
// fetched entries. Any disagreement aborts the walk; nothing is returned
// half-verified.
func collectSync(ctx context.Context, hub HubInfo, snap Snapshot, fetch pageFetcher) (*SyncResult, error) {
	log := &changeLog{hubID: hub.ID}
	cursor := ""
	var prev *federationv1.SyncChangesResponse
	fetched := 0
	for {
		if fetched >= MaxPages {
			return nil, fmt.Errorf("%w: more than %d pages for generation %d",
				ErrTooManyPages, MaxPages, snap.Generation)
		}
		resp, err := fetch(ctx, cursor)
		if err != nil {
			return nil, err
		}
		if err := verifyPage(hub, cursor, resp); err != nil {
			return nil, err
		}
		if resp.GetGeneration() != snap.Generation {
			return nil, fmt.Errorf("%w: snapshot generation %d, page %d carries generation %d",
				ErrGenerationGap, snap.Generation, resp.GetPage(), resp.GetGeneration())
		}
		wantPrev := ""
		if prev != nil {
			wantPrev = digestText(prev.GetPageDigest())
		}
		if got := digestText(resp.GetPrevDigest()); got != wantPrev {
			return nil, fmt.Errorf("%w: page %d prevDigest %q, want %q",
				ErrDigest, resp.GetPage(), got, wantPrev)
		}
		if err := log.appendPage(resp); err != nil {
			return nil, err
		}
		fetched++

		next := resp.GetNextCursor()
		if next == "" {
			// Last page: its pageDigest must land on the snapshot root.
			if got := digestText(resp.GetPageDigest()); got != snap.RootDigest {
				return nil, fmt.Errorf("%w: final pageDigest %q, snapshot rootDigest %q",
					ErrDigest, got, snap.RootDigest)
			}
			break
		}
		if next == cursor {
			return nil, fmt.Errorf("%w: cursor did not advance past %q", ErrDigest, cursor)
		}
		cursor = next
		prev = resp
	}

	if got := log.entryCount(); got != snap.EntryCount {
		return nil, fmt.Errorf("%w: fetched %d entries, snapshot commits to %d",
			ErrDigest, got, snap.EntryCount)
	}
	if got := log.tombstoneCount(); got != snap.TombstoneCount {
		return nil, fmt.Errorf("%w: fetched %d tombstones, snapshot commits to %d",
			ErrDigest, got, snap.TombstoneCount)
	}
	root, err := log.rootDigest()
	if err != nil {
		return nil, err
	}
	if root != snap.RootDigest {
		return nil, fmt.Errorf("%w: recomputed rootDigest %q, snapshot rootDigest %q",
			ErrDigest, root, snap.RootDigest)
	}
	return &SyncResult{
		Hub:        hub,
		Generation: snap.Generation,
		RootDigest: root,
		Pages:      fetched,
		Assets:     log.assets(),
		Tombstones: log.tombstones(),
	}, nil
}
