# Federation

Federation lets multiple AgentHub deployments exchange catalog metadata and
content without losing provenance. A lockfile always records the **origin hub**
and the **origin digests**, no matter which hub served the bytes.

This document covers:

- hub identity and the `.well-known` document
- signed catalog pages and snapshots
- the three operating modes: reference, mirror, proxy
- trust bootstrap, key rotation, and revocation
- the sync algorithm and failure handling
- name resolution across hubs
- offline export/import

Federation is **phase M4**. Nothing in M0–M3 depends on it, and a single hub is
fully functional without it.

---

## 1. Goals and non-goals

### Goals

| ID | Goal |
| --- | --- |
| F1 | A consumer can install an asset from an upstream hub through a local hub |
| F2 | Mirrors cannot alter content without breaking a lockfile |
| F3 | Origin, version, manifest digest, and bundle digest survive every hop |
| F4 | A compromised upstream cannot forge assets for another namespace |
| F5 | An air-gapped hub can be populated from a signed export |
| F6 | Local search stays fast and available when an upstream is down |
| F7 | Private namespaces are never leaked to an upstream |

### Non-goals

- Consensus or multi-writer replication. Each asset has exactly one origin.
- Real-time push. Sync is pull-based with optional webhooks as a trigger.
- Cross-hub namespace ownership transfer. That is an explicit admin operation.
- Re-signing upstream content. Mirrors preserve origin signatures untouched.
- A global namespace. Names are only unique within `(hub, namespace, kind, name)`.

---

## 2. Concepts

| Term | Meaning |
| --- | --- |
| **Hub ID** | Stable DNS name, e.g. `agenthub.pilab.hu`. Never changes, even if the base URL does |
| **Hub key** | Ed25519 key that signs catalog pages, snapshots, and exports |
| **Publisher key** | Namespace-scoped Ed25519 key that signs asset manifests |
| **Origin** | The hub where a version was first published |
| **Upstream** | A hub this hub pulls from |
| **Downstream** | A hub that pulls from this hub |
| **Reference mode** | Metadata is aggregated; downloads go to the origin |
| **Mirror mode** | Metadata and blobs are copied locally; origin is preserved |
| **Proxy mode** | Requests are forwarded on demand; minimal caching |
| **Catalog page** | A signed, ordered batch of entries and tombstones |
| **Snapshot** | A signed descriptor of the full catalog state at a generation |
| **Generation** | Monotonic counter for catalog changes |
| **Tombstone** | A signed record that a version was yanked or removed |

---

## 3. Hub identity

### 3.1 `/.well-known/agenthub.json`

Every hub serves a public identity document:

```json
{
  "schemaVersion": 1,
  "hub": "agenthub.pilab.hu",
  "baseUrl": "https://agenthub.pilab.hu",
  "displayName": "PiLab AgentHub",
  "protocol": { "min": 1, "max": 1 },
  "generation": 4821,
  "keys": [
    {
      "kid": "ed25519:hub-2026-01",
      "algorithm": "ed25519",
      "publicKey": "base64…",
      "validFrom": "2026-01-01T00:00:00Z",
      "validUntil": null,
      "revokedAt": null
    }
  ],
  "endpoints": {
    "connect": "/",
    "catalog": "/agenthub.catalog.v1.CatalogService/",
    "federation": "/agenthub.federation.v1.FederationService/",
    "blobs": "/v1/blobs/",
    "bundles": "/v1/bundles/",
    "skills": "/opencode/skills/"
  },
  "features": ["reference", "mirror", "proxy", "export", "webhooks"],
  "publisherKeys": "/.well-known/agenthub/keys.json",
  "limits": {
    "maxPageBytes": 4194304,
    "maxBundleBytes": 67108864,
    "maxSyncConcurrency": 4
  }
}
```

Rules:

- The document is cacheable for 5 minutes; `generation` lets clients detect
  changes without refetching everything.
- `hub` must match the configured upstream ID exactly. A redirect to a different
  hub ID is rejected.
- `baseUrl` may change (migration), but the `hub` ID is the trust anchor and the
  key in every URN.
- Unknown fields are ignored; unknown `protocol` major versions abort the sync.
- The document itself is unsigned. Its integrity comes from TLS plus the
  out-of-band key fingerprint recorded when the upstream was added. The key list
  is signed implicitly by the catalog pages the keys verify.

### 3.2 Hub key rotation

A new hub key must be signed by an old key:

```json
{
  "kid": "ed25519:hub-2027-01",
  "algorithm": "ed25519",
  "publicKey": "base64…",
  "validFrom": "2027-01-01T00:00:00Z",
  "rotationProof": {
    "previousKid": "ed25519:hub-2026-01",
    "algorithm": "ed25519",
    "value": "base64…"
  }
}
```

Verification:

1. The new key is listed in the well-known document.
2. `rotationProof` verifies with the previous key.
3. The previous key is still trusted (not revoked) at `validFrom`.
4. A downstream that pinned a fingerprint accepts a rotation only if the chain
   reaches a pinned key. Otherwise it alarms and requires an admin decision.

### 3.3 Revocation

- Revoked hub keys remain listed with `revokedAt`.
- Pages signed at or after `revokedAt` are rejected.
- A revoked hub key triggers a **freeze**: the upstream stops syncing, keeps
  serving already-verified content, and raises an alert.
- Publisher key revocation is scoped to a namespace. Versions signed before
  revocation remain installable but are flagged in the store and in
  `agenthub info`; versions signed after revocation are rejected.
- A signed revocation list is served at `/.well-known/agenthub/revocations.json`
  so mirrors can propagate revocations without waiting for a catalog page.

---

## 4. Signed catalogs

### 4.1 Entry

```json
{
  "id": "agenthub://agenthub.pilab.hu/pilab/agent/code-reviewer",
  "kind": "agent",
  "namespace": "pilab",
  "name": "code-reviewer",
  "version": "1.4.2",
  "originHub": "agenthub.pilab.hu",
  "originVersionId": "0193f2c4-…",
  "manifestDigest": "sha256:…",
  "bundleDigest": "sha256:…",
  "publishedAt": "2026-10-30T12:00:00Z",
  "status": "published",
  "publisherKeyId": "ed25519:9f2c…",
  "summary": "Independent reviewer for pull requests and branches",
  "riskClass": "read-only",
  "size": 184320,
  "tags": ["review", "git"],
  "compatibility": { "harnesses": ["opencode"], "opencode": ">=2.2.0 <3.0.0" }
}
```

Notes:

- `originVersionId` makes idempotent upserts possible even if a version string is
  reused after a delete (which is not allowed, but mirrors must be defensive).
- `status` is `published`, `deprecated`, or `yanked`.
- Entry content is a projection of the manifest. The authoritative manifest is
  fetched separately and verified against `manifestDigest`.

### 4.2 Page

```json
{
  "schemaVersion": 1,
  "hub": "agenthub.pilab.hu",
  "generation": 4821,
  "page": 17,
  "prevDigest": "sha256:…",
  "cursor": "opaque-cursor",
  "nextCursor": "opaque-cursor",
  "issuedAt": "2026-11-04T10:00:00Z",
  "entries": [],
  "tombstones": [],
  "pageDigest": "sha256:…",
  "signature": {
    "kid": "ed25519:hub-2026-01",
    "algorithm": "ed25519",
    "value": "base64…"
  }
}
```

Canonicalization:

- The signature covers the page with `signature` and `pageDigest` removed,
  serialized with RFC 8785 JCS.
- `pageDigest = sha256(JCS(page without signature and pageDigest))`.
- `prevDigest` is the previous page's `pageDigest`, forming a chain within a
  generation.
- `cursor` is the position *before* this page; `nextCursor` is the position
  *after* it. Cursors are opaque, signed by the hub, and expire after 7 days.
- A page is at most 4 MiB and 500 entries.

### 4.3 Snapshot

A snapshot describes a complete generation:

```json
{
  "schemaVersion": 1,
  "hub": "agenthub.pilab.hu",
  "generation": 4821,
  "createdAt": "2026-11-04T09:00:00Z",
  "pages": 34,
  "entryCount": 16872,
  "tombstoneCount": 91,
  "rootDigest": "sha256:…",
  "signature": { "kid": "ed25519:hub-2026-01", "algorithm": "ed25519", "value": "base64…" }
}
```

`rootDigest` is the digest of the ordered list of page digests:

```text
rootDigest = sha256(concat(pageDigest[0] || pageDigest[1] || … || pageDigest[n-1]))
```

Verification of a bootstrap:

1. Verify the snapshot signature.
2. Fetch all pages, verify each page signature and `prevDigest` chain.
3. Recompute `rootDigest` and compare.
4. Recompute `entryCount` and `tombstoneCount` and compare.
5. Only then apply the data and store the cursor.

If any step fails, the bootstrap is aborted and the previous generation remains
active. There is no partial import.

### 4.4 Tombstones

```json
{
  "id": "agenthub://agenthub.pilab.hu/pilab/agent/legacy-reviewer",
  "version": "0.9.0",
  "reason": "yanked",
  "at": "2026-10-01T00:00:00Z"
}
```

- `yanked`: hide from search and new resolutions; keep bundles for the retention
  window (default 180 days).
- `deleted`: drafts only; published versions are never hard-deleted.
- Tombstones are retained indefinitely (they are tiny).
- A mirror never deletes a local blob while any local lockfile or retention
  window references it. Tombstones stop *new* resolutions, not existing pins.

---

## 5. Federation modes

Each upstream is configured with exactly one mode.

| Mode | Metadata | Blobs | Offline install | Storage cost | Use case |
| --- | --- | --- | --- | --- | --- |
| `reference` | Aggregated locally, verified | Redirect to origin | No | Low | Public catalog aggregation |
| `mirror` | Copied locally, verified | Copied locally, verified | Yes | High | Air-gapped or latency-sensitive |
| `proxy` | Cached briefly, not authoritative | Streamed or redirected | No | Very low | A thin front for a trusted public hub |

### 5.1 Reference mode

- Sync stores catalog entries with `originHub` and origin digests.
- Search includes upstream results, labeled `via <upstream>`.
- `GetBundle` returns a redirect (HTTP 302) or a pre-signed origin URL, never a
  local copy.
- The lockfile records `origin` and, additionally, `servedBy` for diagnostics.
- If the upstream is down, new installs of its assets fail; existing caches keep
  working.
- The local hub never claims ownership. Store pages render "Published by
  `agenthub.pilab.hu` · mirrored by `hub.internal.example`".

### 5.2 Mirror mode

- Metadata and blobs are copied and verified before being served.
- The origin URN and origin signature are preserved. The local hub is a CDN, not
  a publisher.
- Local download URLs may be used, but the lockfile still pins `origin` and the
  origin digests.
- A mirror may serve a subset: namespace allowlist, kind allowlist, or a maximum
  size.
- Mirroring is asynchronous. A version appears in local search only after its
  metadata is verified; it becomes downloadable after its bundle and blobs are
  verified.
- `mirroredAt`, `verifiedAt`, and `originFetchedAt` are tracked per version.

### 5.3 Proxy mode

- Search and resolve requests are forwarded to the upstream on demand.
- Responses are cached for a short TTL (default 60 seconds).
- Blobs are either redirected or streamed through the proxy with range support.
- No cursor, no long-term metadata, no offline guarantee.
- Proxy mode is for a trusted upstream where the local hub is only a convenience
  endpoint. It should not be used for air-gapped deployments.

### 5.4 Mode selection

```yaml
upstreams:
  - hub: agenthub.pilab.hu
    base_url: https://agenthub.pilab.hu
    mode: reference
    namespaces: ["pilab", "pirat"]
    trust:
      require_signature: true
      fingerprints:
        - "sha256:9f2c…"          # hub key fingerprint, out-of-band
    sync:
      interval: 15m
      on_demand: true

  - hub: hub.vendor.example
    base_url: https://hub.vendor.example
    mode: mirror
    namespaces: ["vendor"]
    trust:
      require_signature: true
      fingerprints: ["sha256:ab12…"]
    sync:
      interval: 1h
      max_bytes: 5368709120       # stop mirroring above 5 GiB
```

Rules:

- `namespaces` is an allowlist. Empty means "all public namespaces", which is
  only allowed in reference mode.
- Private namespaces are never federated; the API rejects sync requests for them
  unless the upstream token is explicitly scoped to that namespace.
- A hub cannot mirror itself (loop detection by `hub` ID).

---

## 6. Trust model

### 6.1 Trust anchors

| Level | Signed by | Verified with | Failure |
| --- | --- | --- | --- |
| Transport | TLS | System roots / mTLS | Abort |
| Hub document | TLS + pinned fingerprint | Out-of-band fingerprint | Abort on mismatch |
| Catalog page | Hub key | Hub key from well-known + pin | Abort page |
| Manifest | Publisher key | Publisher key from signed key list | Reject asset |
| Bundle | SHA-256 | Manifest | Reject asset |
| Files | SHA-256 | Manifest | Reject asset |

The hub key authenticates **catalog membership**, not asset authorship. A
compromised hub key can reorder or omit entries (a denial of service) but cannot
forge a manifest signed by a namespace publisher.

### 6.2 Bootstrap

```bash
agenthub remote add pilab https://agenthub.pilab.hu \
  --mode reference \
  --fingerprint sha256:9f2c…
```

- The fingerprint is the SHA-256 of the hub's Ed25519 public key.
- If no fingerprint is supplied, the CLI prints it and requires confirmation
  (TOFU). TOFU is recorded with `trust: tofu` and a warning in `agenthub status`.
- Changing the fingerprint later requires an explicit `agenthub remote trust`
  command; a sync never silently accepts a new key.

### 6.3 Trust evaluation

For each entry:

1. The page signature is valid and the key is trusted.
2. The manifest digest matches the entry.
3. The manifest signature verifies with a publisher key that was valid for the
   namespace at `signedAt`.
4. The publisher key is in the signed key list for that namespace and is not
   revoked at `signedAt`.
5. The bundle digest and file hashes match.
6. The asset's namespace is allowed by the upstream policy.

Any failure rejects that entry and records an audit event. It does not abort the
whole page unless the page signature itself is invalid.

### 6.4 Compromise playbook

| Compromised | Effect | Response |
| --- | --- | --- |
| Upstream transport | MITM | TLS failure; no sync |
| Upstream hub key | Forged catalog membership | Freeze upstream; re-bootstrap with a new fingerprint |
| Publisher key | Forged versions in one namespace | Revoke key; yank affected versions; notify lockfile owners |
| Mirror storage | Tampered blobs | Digest verification on read; re-fetch from origin |
| Downstream hub | Tampered local serving | Origin signatures still verify; client rejects |

Clients always verify origin signatures, even when the serving hub is trusted.
This is the core invariant.

---

## 7. Sync algorithm

### 7.1 Bootstrap (first sync or reset)

```text
1. GET /.well-known/agenthub.json
2. Verify hub ID, protocol, TLS, and key fingerprint.
3. GET /v1/federation/snapshot?namespaces=...
4. Verify snapshot signature.
5. Stream pages in order:
   a. Verify page signature.
   b. Verify prevDigest chain.
   c. Buffer entries in a batch (default 500).
   d. Upsert assets/versions/dependencies in one transaction per batch.
6. Recompute rootDigest and counts; compare with the snapshot.
7. Mark generation active; store cursor; set lastSyncAt.
8. In mirror mode, enqueue blob fetches for new versions.
```

The current generation remains active until step 6 succeeds. A failed bootstrap
does not partially replace a working catalog.

### 7.2 Incremental sync

```text
1. POST SyncChanges {cursor, limit, namespaces, includeYanked}
2. Verify page signature, generation >= stored generation, prevDigest == stored cursor digest.
3. Upsert entries and tombstones in a transaction.
4. Advance cursor and generation in the same transaction.
5. Repeat while nextCursor is non-empty and the page is full.
```

Properties:

- **At-least-once.** A crash after commit but before acknowledging re-delivers a
  page; upserts are idempotent by `(originHub, originVersionId)`.
- **Monotonic.** A page with `generation < stored` is ignored (replay).
- **Gap detection.** `generation` increasing by more than one, or a `prevDigest`
  mismatch, forces a snapshot re-bootstrap.
- **Bounded.** A sync run processes at most `maxPages` (default 1000) to avoid
  starving request traffic.

### 7.3 Blob mirroring

```text
1. Select versions where mirroredAt IS NULL and status != 'yanked'.
2. GET the origin bundle by digest (or a pre-signed URL).
3. Verify bundleDigest, every file hash, and the manifest signature.
4. Store blobs content-addressed; write a local bundle copy.
5. Set mirroredAt, verifiedAt, originFetchedAt.
```

- Concurrency is bounded per upstream and globally.
- A failed blob fetch retries with exponential backoff (1m, 5m, 30m, 2h, 6h)
  and then parks the version with `mirrorStatus: failed`.
- Mirroring never blocks metadata sync. Search can show a version before its
  blobs are local.

### 7.4 Tombstones and retention

- A tombstone stops new resolutions immediately.
- Existing lockfiles continue to resolve for the retention window.
- After retention, downloads return `410 Gone` with the origin URL and the
  tombstone timestamp.
- Mirrors keep the blob until no local lockfile references it and the retention
  window has expired.

### 7.5 Scheduling

- A periodic job per upstream (`interval`).
- Optional webhook: the upstream POSTs a signed `catalog.changed` event; the
  local hub triggers an incremental sync.
- Manual `TriggerSync` (admin) and `agenthub remote sync`.
- A sync lock per upstream prevents concurrent runs across replicas (Postgres
  advisory lock).

### 7.6 Failure handling

| Failure | Behavior |
| --- | --- |
| DNS/TLS error | Retry with backoff; keep serving local data |
| Well-known key mismatch | Freeze upstream; alert; no data applied |
| Snapshot signature invalid | Abort bootstrap; keep current generation |
| Page signature invalid | Abort run; keep last good cursor |
| Gap in generation | Force bootstrap on next run |
| Manifest signature invalid | Reject entry; continue page; alert |
| Blob digest mismatch | Reject blob; mark version failed; retry |
| Storage full | Stop mirroring; alert; metadata sync continues |

Every failure is recorded in `upstream_sync_runs` with a reason and a correlation
ID. The store shows a banner when an upstream has not synced within 2× its
interval.

---

## 8. Name resolution

### 8.1 Full URN

```text
agenthub://agenthub.pilab.hu/pilab/agent/code-reviewer@1.4.2
```

Always unambiguous. The hub component is the origin, not the serving hub.

### 8.2 Shorthand

```text
pilab/agent/code-reviewer@^1.4
```

Resolution order:

1. Explicit `--registry`.
2. Local hub.
3. Configured upstreams, in configuration order.
4. If more than one hub provides a match, fail with a disambiguation error that
   lists each origin and its trust status.

The resolver never prefers a mirror just because it is faster. Provenance wins
over latency.

### 8.3 Collisions

- Two hubs may publish the same `(namespace, kind, name)`.
- The store displays both with their origin hub.
- The lockfile pins the full URN, so the ambiguity is resolved once.
- A local namespace cannot "claim" an upstream namespace. If `pilab` exists
  locally and upstream, both are visible; publishing locally does not shadow the
  upstream.
- Optional `alias` mapping for private deployments:

```yaml
aliases:
  pilab: "agenthub.pilab.hu/pilab"
```

Aliases affect shorthand parsing only; they never change stored identities.

### 8.4 Search result labeling

```json
{
  "ref": "agenthub://agenthub.pilab.hu/pilab/agent/code-reviewer",
  "origin": "agenthub.pilab.hu",
  "servedBy": "hub.internal.example",
  "mode": "mirror",
  "verified": true
}
```

The UI must show `servedBy` when it differs from `origin`.

---

## 9. Offline export and import

### 9.1 Export format

```text
agenthub-export-2026-11-04.tar.zst
├── export.json
├── snapshot.json
├── pages/
│   ├── 000000.json
│   └── ...
├── manifests/sha256/<digest>.json
├── bundles/sha256/<bundleDigest>.tar.zst
└── keys/
    ├── hub.json
    └── publishers.json
```

`export.json`:

```json
{
  "schemaVersion": 1,
  "hub": "agenthub.pilab.hu",
  "baseUrl": "https://agenthub.pilab.hu",
  "createdAt": "2026-11-04T10:00:00Z",
  "generation": 4821,
  "selection": { "namespaces": ["pilab"], "kinds": ["agent", "skill", "pack"] },
  "counts": { "assets": 42, "versions": 57, "bundles": 57 },
  "snapshotDigest": "sha256:…",
  "signature": { "kid": "ed25519:hub-2026-01", "algorithm": "ed25519", "value": "base64…" }
}
```

- The export contains public keys only. Private keys never leave the origin.
- Manifests keep their publisher signatures. The export signature covers the
  archive manifest (file list + digests), not each bundle again.
- Bundles are byte-identical to the originals; content addressing is preserved.

### 9.2 Import

```bash
agenthub import agenthub-export-2026-11-04.tar.zst \
  --trust-key ./hub-2026-01.pub \
  --mode mirror \
  --namespace pilab
```

Import steps:

1. Verify the archive signature against a trusted hub key.
2. Verify `export.json` against the included snapshot and page digests.
3. Verify every manifest signature and bundle digest.
4. Insert assets as **mirrored** records with `originHub` set to the exporting
   hub. The importing hub does not become the publisher.
5. Store blobs if `--mode mirror`; otherwise store metadata only.
6. Record an audit event with the archive digest and the trust key.

Import is idempotent by `(originHub, originVersionId)`. A second import of the
same archive is a no-op.

### 9.3 Air-gapped workflow

```text
connected side                          air-gapped side
──────────────                          ────────────────
agenthub export --namespace pilab
        │
        ├── USB / transfer ──────────▶ agenthub import --trust-key …
                                        │
                                        ├─ store serves mirrored assets
                                        └─ clients install with origin pins
```

The air-gapped hub can serve clients, but it cannot publish to the origin unless
a one-way transfer carries a signed publish request (out of scope for v1).

### 9.4 Selective and incremental export

- `--namespace`, `--kind`, `--since <generation>` narrow the selection.
- `--since` produces a delta export containing changed entries and tombstones.
- Delta imports require the base generation to be present; otherwise a full
  export is required.
- Exports are immutable files; the filename includes the generation for
  traceability.

---

## 10. Security

### 10.1 SSRF and upstream validation

- Upstream URLs must be `https` (an explicit `--insecure-http` is required for
  local development).
- Resolve DNS once, validate the IP, and connect to that IP with the original
  `Host` header to prevent DNS rebinding.
- Reject loopback, link-local, multicast, and private ranges unless the address
  is explicitly allowlisted (needed for on-prem hubs).
- Limit redirects (max 3) and re-validate every hop.
- Enforce `maxPageBytes`, `maxBundleBytes`, and a timeout per request.

### 10.2 Replay and truncation

- `generation` must be monotonic per upstream.
- `issuedAt` must be within ±10 minutes of local time; a larger skew is a warning
  and disables on-demand sync until resolved.
- Page chains and snapshot root digests detect truncation and reordering within a
  generation.
- A full snapshot comparison runs weekly to detect a malicious upstream that
  silently omits entries (omission is otherwise indistinguishable from a
  deletion).

### 10.3 Denial of service

- Per-upstream concurrency and bandwidth limits.
- Circuit breaker after 5 consecutive failures; half-open probe every 5 minutes.
- Sync jobs run at low priority and yield to request traffic.
- A single upstream cannot consume more than `max_bytes` of mirror storage.
- Page parsing rejects duplicate keys and oversized JSON.

### 10.4 Privacy

- Private namespaces are excluded from the public catalog and from sync by
  default.
- A federation token can be scoped to specific namespaces; the upstream enforces
  it on every page and blob request.
- Export includes only the selected namespaces; the selection is recorded in
  `export.json` and in the audit log.
- Search results never reveal the existence of a private namespace the caller
  cannot read.

### 10.5 Audit

Federation audit events:

```text
upstream.added
upstream.updated
upstream.key.rotated
upstream.key.revoked
upstream.sync.started
upstream.sync.succeeded
upstream.sync.failed
upstream.frozen
export.created
import.completed
import.rejected
```

Each event records the upstream hub, generation, cursor, actor, and correlation
ID. Sync events are retained for 90 days; trust events are retained indefinitely.

---

## 11. Storage schema additions

```sql
CREATE TABLE upstream_sync_runs (
  id             uuid PRIMARY KEY,
  upstream_id    uuid NOT NULL REFERENCES upstreams(id),
  started_at     timestamptz NOT NULL DEFAULT now(),
  finished_at    timestamptz,
  mode           text NOT NULL,             -- bootstrap | incremental | blobs | export
  from_generation bigint,
  to_generation  bigint,
  pages          int NOT NULL DEFAULT 0,
  entries        int NOT NULL DEFAULT 0,
  tombstones     int NOT NULL DEFAULT 0,
  bytes          bigint NOT NULL DEFAULT 0,
  status         text NOT NULL,             -- running | succeeded | failed | frozen
  error_code     text,
  error_detail   text,
  correlation_id text
);

CREATE TABLE upstream_assets (
  upstream_id       uuid NOT NULL REFERENCES upstreams(id),
  origin_version_id text NOT NULL,
  asset_id          uuid REFERENCES assets(id),
  version_id        uuid REFERENCES asset_versions(id),
  origin_hub        text NOT NULL,
  manifest_digest   text NOT NULL,
  bundle_digest     text NOT NULL,
  status            text NOT NULL,
  mirrored_at       timestamptz,
  verified_at       timestamptz,
  last_error        text,
  PRIMARY KEY (upstream_id, origin_version_id)
);

CREATE TABLE upstream_cursors (
  upstream_id   uuid PRIMARY KEY REFERENCES upstreams(id),
  generation    bigint NOT NULL,
  cursor        text,
  cursor_digest text,
  updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE trust_keys (
  id            uuid PRIMARY KEY,
  hub_id        text NOT NULL,
  kid           text NOT NULL,
  public_key    text NOT NULL,
  fingerprint   text NOT NULL,
  trust         text NOT NULL,              -- pinned | tofu | rotation
  valid_from    timestamptz NOT NULL,
  revoked_at    timestamptz,
  UNIQUE (hub_id, kid)
);
```

`assets` and `asset_versions` already carry `origin_hub_id` and
`origin_version_id`; mirrored rows set both and are never treated as locally
published.

---

## 12. API additions

```proto
service FederationService {
  rpc GetHubInfo(GetHubInfoRequest) returns (GetHubInfoResponse);
  rpc GetSnapshot(GetSnapshotRequest) returns (GetSnapshotResponse);
  rpc SyncChanges(SyncChangesRequest) returns (SyncChangesResponse);
  rpc ListUpstreams(ListUpstreamsRequest) returns (ListUpstreamsResponse);
  rpc UpsertUpstream(UpsertUpstreamRequest) returns (UpsertUpstreamResponse);
  rpc DeleteUpstream(DeleteUpstreamRequest) returns (DeleteUpstreamResponse);
  rpc TriggerSync(TriggerSyncRequest) returns (TriggerSyncResponse);
  rpc ExportBundle(ExportBundleRequest) returns (stream ExportChunk);
  rpc ImportBundle(stream ImportChunk) returns (ImportBundleResponse);
}
```

HTTP:

| Method | Path | Auth |
| --- | --- | --- |
| GET | `/.well-known/agenthub.json` | Public |
| GET | `/.well-known/agenthub/keys.json` | Public |
| GET | `/.well-known/agenthub/revocations.json` | Public |
| GET | `/v1/federation/snapshot` | Federation token |
| GET | `/v1/federation/pages/{n}` | Federation token |
| POST | `/v1/federation/webhook` | HMAC + hub signature |

`SyncChanges` requires a token with `agenthub.federation` and a namespace scope
that covers the request. Public namespaces may be readable anonymously *only* if
the deployment explicitly enables `federation.publicRead`.

---

## 13. Observability

Metrics:

```text
agenthub_federation_upstream_up{hub,mode}                  gauge
agenthub_federation_sync_duration_seconds{hub,mode,result} histogram
agenthub_federation_sync_entries_total{hub,mode}           counter
agenthub_federation_sync_lag_seconds{hub}                  gauge
agenthub_federation_verify_failures_total{hub,reason}      counter
agenthub_federation_mirrored_bytes{hub}                    gauge
agenthub_federation_tombstones_total{hub}                  counter
```

Alerts:

| Alert | Condition |
| --- | --- |
| `FederationStale` | No successful sync for 2× interval |
| `FederationVerifyFailing` | Verification failures > 0 for 15 minutes |
| `FederationFrozen` | Upstream frozen for any reason |
| `FederationStorageHigh` | Mirror storage > 80% of quota |
| `FederationPageInvalid` | Page signature/chain failure |

Logs include `upstream`, `generation`, `cursor`, `page`, `entries`, `duration`,
and `result`. Never log tokens, private keys, or full manifests.

---

## 14. Testing

### 14.1 Unit

- JCS canonicalization and page digest stability.
- Snapshot root digest over ordered pages.
- Cursor signing/verification and expiry.
- Generation monotonicity and gap detection.
- Trust policy evaluation: pinned, TOFU, rotation, revocation.
- SSRF address validation.
- Export archive manifest and selective selection.

### 14.2 Integration with a fake upstream

The test server can:

- Serve a well-known document with configurable keys.
- Mint signed pages and snapshots.
- Inject tampering: bad signature, broken chain, gap, duplicate entry,
  tombstone, revoked key.
- Fail at specific pages or blob fetches.

Assertions:

- A tampered page aborts the run and leaves the previous generation active.
- A gap forces a bootstrap.
- A revoked key freezes the upstream.
- A mirrored asset retains its origin URN and signature.
- A yanked version disappears from search but remains installable from an
  existing lockfile during retention.
- Restarting mid-sync resumes safely without duplicating rows.

### 14.3 End-to-end

```text
hub-a (origin)  ──sync──▶  hub-b (mirror)  ──install──▶  client
```

- Publish a pack on A.
- Sync B; assert B serves it and A is down afterward.
- `agenthub add` against B; assert the lockfile pins origin A and both digests.
- Verify with networking off.
- Export A, import into C with no network, install from C.

### 14.4 Compatibility

- Protocol version negotiation: a v2-only upstream is rejected by a v1 hub with
  a clear error.
- Unknown fields in pages and well-known documents are ignored.
- Key rotation across two protocol minors works without re-bootstrap.

---

## 15. Rollout plan

| Step | Scope | Exit criteria |
| --- | --- | --- |
| F0 | Well-known document, hub keys, signatures | Document validates; keys rotate |
| F1 | Reference mode, incremental sync | Two hubs aggregate search; installs redirect to origin |
| F2 | Mirror mode, blob replication | Mirror serves an install with the origin offline |
| F3 | Export/import | Air-gapped hub installs from a signed archive |
| F4 | Proxy mode, webhooks | On-demand sync and signed change notifications |
| F5 | Federation UI | Store shows origin, served-by, trust, and sync health |

Federation is opt-in per upstream and disabled by default. A hub with no
upstreams behaves exactly like an M3 hub.

---

## 16. Open questions

1. Should reference mode cache bundle bytes for a short TTL to survive a brief
   origin outage? Current: no; mirrors exist for that.
2. Is a weekly full snapshot comparison enough to detect silent omission, or do
   we need a signed Merkle tree with inclusion proofs? Current: snapshot compare;
   revisit if hubs become mutually untrusted.
3. Should publisher keys be globally unique or namespace-scoped? Current:
   namespace-scoped; the same key may be registered in multiple namespaces, but
   each registration is independent.
4. Do we need per-asset federation opt-out (`federation: false` in the manifest)?
   Current: yes, planned; a publisher may restrict mirroring while allowing
   reference links.
5. Should imports be allowed to re-sign content for a private namespace? Current:
   no; provenance must survive.
