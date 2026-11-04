# Operations

This document covers how AgentHub is deployed, configured, observed, backed up,
and operated. It is written for the on-call engineer and for the CI pipeline that
ships the service.

---

## 1. Deployment topology

### 1.1 MVP (M0–M3)

```mermaid
flowchart TB
    Internet["Internet"] --> Ingress["Ingress + TLS<br/>agenthub.pilab.hu"]
    Ingress --> Server["agenthub-server<br/>2+ replicas"]
    Server --> PG[("PostgreSQL 16<br/>primary + standby")]
    Server --> S3[("Silo / S3<br/>versioned bucket")]
    Server --> VK[("Valkey<br/>sessions + cache")]
    Server --> SSO["Shadow SSO<br/>JWKS / token / introspect"]
    CDN["CDN"] --> S3
    Metrics["Prometheus"] --> Server
    Logs["Loki / OpenSearch"] --> Server
```

| Component | Replicas | State | Notes |
| --- | --- | --- | --- |
| `agenthub-server` | 2–6 | Stateless | Serves landing/store/dashboard (BFF), ConnectRPC, blobs, skills catalog |
| PostgreSQL | 1 primary + 1 standby | Stateful | Metadata, drafts, audit, cursors, catalog revision |
| S3 / Silo | Managed | Durable | Bundles, blobs, icons, exports |
| Valkey | 2 (HA) | Ephemeral | Sessions, hot manifests, landing cache, rate limits |
| Shadow SSO | 1+ (external) | Stateful MongoDB | OIDC issuer at `https://sso.pilab.hu`; namespace `shadow-sso` |
| NATS JetStream | Optional | Durable | Federation jobs and webhooks (M4) |

The server binary has subcommands:

```text
agenthub-server serve     # HTTP/Connect listeners
agenthub-server migrate   # goose up/down/status
agenthub-server worker    # federation + GC jobs (M4; feature-flagged)
agenthub-server version
```

MVP runs `serve` and a single-replica `worker` in the same deployment. Splitting
them is a configuration change, not a code change.

### 1.2 Shadow SSO dependency

AgentHub does not ship or embed an identity provider. It depends on the
Shadow SSO Helm chart deployed in the `shadow-sso` namespace:

| Item | Value |
| --- | --- |
| Chart | `ssso-backend` (release `shadow-sso`) |
| Service | `shadow-sso.shadow-sso.svc.cluster.local:8080` |
| Management | `shadow-sso.shadow-sso.svc.cluster.local:5000` (never public) |
| Issuer | `https://sso.pilab.hu` |
| Ingress | `sso.pilab.hu` + TLS |
| JWKS | `https://sso.pilab.hu/.well-known/jwks.json` |
| Client used by AgentHub | `agenthub-web` (confidential, code + PKCE) |
| Secrets | signing key, admin creds, Mongo URI + config encryption from OpenBao |

AgentHub uses the public issuer for discovery and JWKS so `iss` validation is
consistent. The management port is only reached through `kubectl port-forward`
for `ssoctl`/GraphQL administration. NetworkPolicy allows AgentHub → SSO `8080`;
nothing else. Full manifests, client registration, and the BFF flow are in
[web-and-sso.md](web-and-sso.md).

### 1.3 Growth

- Read replica for catalog queries; the writer stays on the primary.
- CDN in front of `/v1/blobs` and `/v1/bundles` with signed URLs.
- Dedicated federation worker with its own concurrency limits.
- Meilisearch adapter behind the `SearchIndex` port if PostgreSQL FTS becomes
  the bottleneck.

### 1.4 Air-gapped

- Mirror mode + signed export/import.
- No outbound network except to the transfer medium.
- Shadow SSO is deployed locally (or a trusted federation of SSO instances).
- The store runs fully offline; update checks are disabled.

---

## 2. Configuration

Configuration is YAML plus environment overrides. Precedence:

```text
defaults < config file < AGENTHUB_* environment < flags
```

Every key maps to `AGENTHUB_<SECTION>_<KEY>` in upper snake case, e.g.
`server.http_addr` → `AGENTHUB_SERVER_HTTP_ADDR`.

### 2.1 Reference

```yaml
server:
  http_addr: ":8080"
  grpc_addr: ":9090"            # optional native gRPC listener
  metrics_addr: ":9100"         # private
  public_url: "https://agenthub.pilab.hu"
  read_timeout: 10s
  write_timeout: 30s
  idle_timeout: 120s
  shutdown_grace: 20s
  max_request_bytes: 4194304
  trusted_proxies: ["10.0.0.0/8"]

database:
  dsn: "postgres://agenthub:${DB_PASSWORD}@postgres:5432/agenthub?sslmode=require"
  max_open_conns: 20
  max_idle_conns: 10
  conn_max_lifetime: 30m
  statement_timeout: 15s
  migrate_on_start: false

storage:
  kind: s3                       # s3 | filesystem (dev only)
  endpoint: "https://silo.pilab.hu"
  region: "hu-1"
  bucket_bundles: "agenthub-bundles"
  bucket_blobs: "agenthub-blobs"
  bucket_exports: "agenthub-exports"
  use_path_style: false
  presign_ttl: 5m
  server_side_encryption: "AES256"
  max_bundle_bytes: 67108864
  max_file_bytes: 10485760

cache:
  kind: valkey                   # valkey | memory (dev only)
  addr: "valkey:6379"
  password: "${VALKEY_PASSWORD}"
  db: 0
  ttl_manifest: 10m
  ttl_resolve: 2m
  ttl_session: 12h

auth:
  issuer: "https://sso.pilab.hu"
  discovery_url: "https://sso.pilab.hu/.well-known/openid-configuration"
  audience: "agenthub"                   # service-account/federation audience
  # Authorization-code tokens carry aud = client_id; the validator accepts both
  # the configured client_id and this audience.
  audience_allowlist: ["agenthub"]
  jwks_url: "https://sso.pilab.hu/.well-known/jwks.json"
  introspection_url: "https://sso.pilab.hu/oauth2/introspect"
  client_id: "${SSO_CLIENT_ID}"          # agenthub-web
  client_secret: "${SSO_CLIENT_SECRET}"  # from OpenBao
  redirect_url: "https://agenthub.pilab.hu/auth/callback"
  post_logout_redirect_url: "https://agenthub.pilab.hu/"
  scopes: ["openid", "profile", "email", "agenthub.read", "agenthub.publish", "agenthub.admin"]
  clock_skew: 60s
  jwks_cache_ttl: 10m
  require_introspection_for_writes: true

signing:
  hub_key_path: "/run/secrets/agenthub-hub-key"
  require_publisher_signature: true
  allowed_algorithms: ["ed25519"]
  revocation_url: "https://agenthub.pilab.hu/.well-known/agenthub/revocations.json"
  hosted_custody:
    enabled: false                       # per-namespace opt-in
    backend: "openbao-transit"           # openbao-transit | kms
    key_prefix: "agenthub"

federation:
  enabled: false
  public_read: false
  sync_interval: 15m
  max_pages_per_run: 1000
  max_concurrent_blobs: 4
  max_mirror_bytes: 5368709120
  allow_insecure_http: false
  allowed_private_cidrs: []

web:
  enable: true
  theme: "default"
  featured_namespaces: ["pilab", "pirat"]
  landing_cards: 12
  landing_ttl: 60s
  allow_web_authoring: true
  analytics: false

limits:
  anonymous_search_per_min: 60
  authenticated_read_per_min: 600
  publish_per_hour: 30
  federation_sync_per_min: 10
  max_namespaces_per_user: 20
  max_versions_per_asset: 500

observability:
  log_level: info
  log_format: json
  otel_endpoint: "http://otel-collector:4317"
  otel_sample_ratio: 0.1
  metrics_enabled: true
  pprof_enabled: false

features:
  federation: false
  meilisearch: false
  webhooks: false
  sigstore: false
```

### 2.2 Validation

Startup fails fast on:

- missing or malformed DSN, bucket, issuer, or audience
- `signing.require_publisher_signature: false` in production
- `storage.kind: filesystem` in production
- `cache.kind: memory` with more than one replica
- `federation.enabled: true` without a hub key
- `signing.hosted_custody.enabled: true` without a reachable OpenBao/KMS backend
- a `public_url` that does not match the configured OIDC redirect URL
- a discovery document whose `issuer` differs from `auth.issuer`

A `--check-config` flag validates and exits without starting listeners.

### 2.3 Environments

| Environment | Purpose | Data | Signature policy |
| --- | --- | --- | --- |
| `dev` | Local compose | Seed fixtures | Optional, warning |
| `staging` | Pre-production | Anonymized copy | Required |
| `prod` | Public hub | Real | Required |
| `airgap` | Offline mirror | Imported | Required |

Dev is the only environment allowed to disable signatures, and it must log a
startup warning on every boot.

---

## 3. Secrets

| Secret | Used by | Storage | Rotation |
| --- | --- | --- | --- |
| `DB_PASSWORD` | Server | External Secrets / SOPS | 90 days |
| `SSO_CLIENT_SECRET` | Server (web BFF) | OpenBao `secret/data/AgentHub#SSO_CLIENT_SECRET` | 180 days |
| `VALKEY_PASSWORD` | Server | External Secrets | 90 days |
| S3 credentials | Server, worker | Workload identity if available | 90 days |
| Hub Ed25519 private key | Server (signing) | HSM/KMS or mounted secret | Yearly, with overlap |
| Publisher private keys | CI only | CI secret store | Yearly, per namespace |
| Hosted publisher keys | Server (web publish) | OpenBao Transit / KMS | Yearly, per namespace |
| Federation service-account key | Worker | CI/secret manager | 90 days |
| Webhook HMAC key | Server, upstream | Secret manager | 90 days |

Rules:

- Secrets are never logged, stored in the database, or returned by an API.
- The server reads secrets from files or environment variables at startup; it
  does not watch for changes. Rotation requires a rolling restart.
- Hub and publisher private keys never enter the container image or the repo.
- Hosted publisher keys are referenced by ID from OpenBao Transit/KMS; the key
  material is never stored in PostgreSQL or returned by an API.
- CI uses `private_key_jwt`; long-lived client secrets are not accepted for
  publishing.
- `agenthub-server` runs as a non-root user with a read-only root filesystem;
  `/tmp` is a memory-backed `emptyDir`.

---

## 4. Database

### 4.1 Migrations

- `goose` migrations live in `migrations/`, embedded in the binary.
- `agenthub-server migrate up` is run as a Kubernetes `Job` before rollout, not
  by each replica.
- Migrations acquire a Postgres advisory lock; concurrent runs are safe but
  serialized.
- Every migration is forward-only in production. Destructive changes use a
  two-step expand/contract pattern across two releases.
- `migrate status` is part of the pre-deploy check.

### 4.2 Backups

| Data | Method | Frequency | Retention | RPO | RTO |
| --- | --- | --- | --- | --- | --- |
| PostgreSQL | WAL archiving + daily base backup | continuous / daily | 35 days | 5 min | 1 h |
| S3 bundles/blobs | Bucket versioning + replication | continuous | 90 days versions | ~15 min | 1 h |
| Signing keys | Offline backup + HSM export policy | on change | indefinite | — | 4 h |
| Config | Git | on change | indefinite | — | 15 min |
| Audit log | Postgres + monthly export to S3 | monthly | 7 years | 24 h | 24 h |

Restore drills run quarterly in staging. A drill is successful only if:

1. The database restores to a point in time.
2. The server starts and serves `/readyz`.
3. A known asset resolves and its bundle downloads.
4. Signature verification still passes with restored keys.

### 4.3 Retention and GC

| Data | Retention |
| --- | --- |
| Drafts | 30 days after last edit, then deleted |
| Yanked bundles | 180 days after yank, then `410 Gone` |
| Audit events | 7 years (exported monthly) |
| Download counters | 400 days, then aggregated |
| Federation runs | 90 days |
| Expired sessions | TTL in Valkey |
| Unreferenced blobs | 30-day grace, then deleted |

GC runs as a worker job:

```text
1. Mark blobs with no manifest/version reference.
2. Wait out the grace period (default 30 days).
3. Delete from S3 in batches.
4. Emit metrics and an audit summary.
```

GC never deletes a blob referenced by any lockfile window the hub still honors.

---

## 5. Object storage

### 5.1 Layout and lifecycle

```text
s3://agenthub-bundles/sha256/<bundleDigest>.tar.zst
s3://agenthub-blobs/sha256/ab/cd/<sha256>
s3://agenthub-icons/sha256/<sha256>
s3://agenthub-exports/<hub>/<generation>/<digest>.tar.zst
```

- Buckets are private. Public reads go through the API or a signed CDN URL.
- Versioning is enabled; delete markers are cleaned by lifecycle policy.
- SSE-S3 or SSE-KMS at rest; TLS in transit.
- Incomplete multipart uploads are aborted after 7 days.
- Pre-signed URLs expire in 5 minutes and are scoped to a single object.

### 5.2 CDN

- Cache `/v1/blobs/*` and `/v1/bundles/*` immutably (`Cache-Control: public, max-age=31536000, immutable`).
- Do not cache authenticated responses.
- Respect `Range` requests for large blobs.
- Purge is never required because paths are content-addressed.

---

## 6. Caching and rate limits

### 6.1 Valkey keys

```text
session:<id>                 JSON session, TTL 12h, sliding 30m
jwks:<issuer>                JWKS document, TTL 10m
manifest:<digest>            Manifest JSON, TTL 10m
resolve:<hash>               Resolve response, TTL 2m
landing:page:<revision>      Rendered landing page, TTL 60s
catalog:revision             Monotonic catalog revision mirrored from PostgreSQL
ratelimit:<scope>:<id>       Counter, TTL = window
upstream:<hub>:etag          Well-known ETag, TTL 5m
```

- Keys are namespaced by environment (`prod:`, `stg:`).
- The cache is never authoritative. A cache miss falls through to Postgres/S3.
- Sessions are opaque; the token material stays server-side.
- On Valkey outage, the server degrades to database sessions and in-memory rate
  limits (with a warning) rather than failing all requests.

### 6.2 Rate limits

| Scope | Limit | Burst |
| --- | --- | --- |
| Anonymous search | 60/min/IP | 10 |
| Authenticated read | 600/min/subject | 60 |
| Draft create/update | 60/hour/subject | 10 |
| Publish begin | 30/hour/namespace | 5 |
| Publish commit / web publish | 60/hour/namespace | 5 |
| Download | 120/min/subject or IP | 20 |
| Federation sync | 10/min/upstream | 2 |
| Login start | 20/min/IP | 5 |
| Login callback | 60/min/IP | 10 |

Limits are enforced at the edge and in the app. A `429` includes `Retry-After`
and a `RateLimit-*` header set. Authenticated principals are never limited by IP
alone.

---

## 7. Observability

### 7.1 Logs

- Structured JSON via zerolog.
- Required fields: `time`, `level`, `service`, `version`, `env`, `request_id`,
  `route`, `status`, `duration_ms`, `subject` (hashed), `namespace`.
- Never log tokens, cookies, secrets, manifest bodies, or full file contents.
- Client errors log at `warn`; server errors at `error` with a stack only in
  staging.
- Audit events are separate from application logs and are immutable.

### 7.2 Metrics

```text
# HTTP / RPC
agenthub_http_requests_total{route,method,status}
agenthub_http_request_duration_seconds{route,method}
agenthub_connect_errors_total{procedure,code}

# Catalog
agenthub_catalog_search_total{result}
agenthub_catalog_resolve_total{result}
agenthub_catalog_resolve_duration_seconds
agenthub_catalog_cache_hits_total{kind}

# Publishing
agenthub_publish_total{namespace,kind,result}
agenthub_publish_duration_seconds{phase}
agenthub_publish_bytes_total{namespace}

# Downloads
agenthub_downloads_total{kind,result}
agenthub_download_bytes_total

# Storage
agenthub_db_pool_in_use
agenthub_db_query_duration_seconds{query}
agenthub_s3_request_duration_seconds{op}
agenthub_s3_errors_total{op,code}
agenthub_blob_gc_deleted_total

# Auth
agenthub_auth_failures_total{reason}
agenthub_jwks_refresh_total{result}
agenthub_web_login_total{result}
agenthub_web_sessions_active
agenthub_drafts_total{status}
agenthub_landing_cache_hits_total{result}
agenthub_catalog_revision

# Federation
agenthub_federation_*       (see federation.md)
```

Cardinality rules:

- Never label by asset ID, subject, or raw URL.
- Route labels use the registered template, not the concrete path.
- Namespace labels are allowed only on low-cardinality metrics (publish and
  download totals), capped by a configured namespace allowlist.

### 7.3 Traces

- OpenTelemetry, W3C trace context, 10% sampling in production (100% on errors).
- Spans: HTTP handler → use case → repository → S3; publish commit includes
  validation and transaction spans.
- Do not attach request bodies or tokens. Attach digests, asset URNs, and
  namespace.
- Trace IDs are returned as `X-Request-ID` when the client does not supply one.

### 7.4 SLOs

| SLO | Target | Window | Measurement |
| --- | --- | --- | --- |
| Public read availability | 99.9% | 30 days | Non-5xx / total read requests |
| Publish availability | 99.5% | 30 days | Successful commits / attempts |
| Search latency | p95 < 200 ms | 7 days | Server-side duration |
| Asset page latency | p95 < 300 ms | 7 days | Server-side duration |
| Bundle download start | p95 < 500 ms | 7 days | Time to first byte |
| Resolve latency | p95 < 400 ms | 7 days | Server-side duration |
| Error budget | 0.1% reads | 30 days | Burn-rate alerts |

Burn-rate alerts:

- Fast burn: 14.4× over 1 hour → page.
- Slow burn: 6× over 6 hours → ticket.

### 7.5 Alerts

| Alert | Condition | Severity |
| --- | --- | --- |
| `ReadAvailabilityBurn` | Fast burn on read SLO | Page |
| `PublishFailures` | > 10% publish failures for 15m | Page |
| `DatabaseDown` | `pg_up == 0` for 2m | Page |
| `DatabasePoolSaturated` | pool in use > 90% for 10m | Ticket |
| `S3ErrorRate` | > 5% 5xx for 10m | Page |
| `ValkeyDown` | cache unavailable for 5m | Ticket |
| `SSOUnavailable` | JWKS fetch failures for 5m | Ticket |
| `DiskSpaceLow` | Postgres disk > 85% | Page |
| `CertificateExpiring` | TLS cert < 14 days | Ticket |
| `BackupFailed` | no successful base backup in 26h | Page |
| `FederationStale` | see federation.md | Ticket |
| `AuditWriteFailing` | audit insert errors > 0 for 5m | Page |

---

## 8. Security operations

### 8.1 Key management

- Hub key: generated offline, stored in KMS/HSM or a mounted secret, never in
  the database. Public key is served in `/.well-known/agenthub.json`.
- Publisher keys: registered through the API by a namespace owner; the private
  key stays with the publisher.
- Rotation: publish the new public key, sign with both for one release window,
  then retire the old key.
- Revocation: mark `revokedAt`, publish the revocation list, and reject versions
  signed after that instant.
- Access to the hub key is audited and limited to the signing service identity.

### 8.2 Vulnerability management

- `govulncheck` on every CI run.
- Container scanning (Trivy) fails the build on `HIGH`/`CRITICAL` with a fix
  available.
- Dependency updates via Renovate/Dependabot, grouped weekly.
- SBOM (SPDX) generated and attached to each release.
- Images signed with cosign; deployment verifies the signature.
- Base image rebuilt weekly even without code changes.

### 8.3 Audit

Audited actions:

```text
namespace.created
namespace.visibility.changed
publish.begin
publish.commit
publish.rejected
version.yanked
version.unyanked
version.deprecated
key.registered
key.revoked
upstream.added
upstream.updated
upstream.sync.*
export.created
import.completed
admin.action
```

Each event stores actor subject, email, action, namespace, asset, version, IP,
user agent, correlation ID, and a JSON detail blob with secrets redacted.
Audit writes are in the same transaction as the state change they describe.

### 8.4 Incident response

1. Declare severity and open an incident channel.
2. Freeze publishing (`features.publish=false`) if integrity is in doubt.
3. Preserve logs and audit exports; do not delete buckets.
4. Rotate the affected keys; publish revocations.
5. Notify lockfile owners for affected assets (email from namespace owners).
6. Publish a post-mortem within 5 business days.

A compromised publisher key is handled by revoking the key, yanking affected
versions, and adding a store banner. Existing installs are not silently changed;
clients see the revocation on their next verify.

---

## 9. Performance and capacity

### 9.1 Assumptions

| Metric | MVP | Year 1 |
| --- | --- | --- |
| Assets | 500 | 5,000 |
| Versions | 5,000 | 75,000 |
| Average bundle | 200 KiB | 500 KiB |
| Blob storage | 2 GiB | 60 GiB |
| Metadata DB | 1 GiB | 15 GiB |
| Reads | 50 rps peak | 500 rps peak |
| Publishes | 50/day | 1,000/day |
| Downloads | 20 rps peak | 200 rps peak |

Metadata is small; blobs dominate storage and bandwidth. The database is not the
bottleneck at these sizes; S3 latency and egress are.

### 9.2 Tuning

- Resolve responses are cached for 2 minutes keyed by the request hash.
- Manifests are cached for 10 minutes (immutable by digest, so a longer TTL is
  safe; 10 minutes bounds memory).
- Search uses PostgreSQL FTS with a GIN index; add `pg_trgm` for fuzzy matching.
- Use keyset pagination, never `OFFSET` on large tables.
- Bundle downloads stream from S3; the API never buffers a whole bundle.
- Publishing validates file hashes in parallel but commits in one transaction.

### 9.3 Load tests

- `k6` scenarios: search, asset page, resolve, download, publish commit.
- Target: 2× projected peak with p95 within SLO and no error rate increase.
- Run before each major release and after schema/index changes.
- Soak test for 2 hours to catch connection leaks and GC pauses.

---

## 10. CI/CD

### 10.1 Pipeline

```text
lint        gofmt, go vet, golangci-lint, buf lint, buf breaking
unit        go test -race ./...
integration docker compose: postgres, s3, valkey, shadow-sso
web         BFF OIDC contract, draft/publish, landing card invalidation
plugin      bun test + OpenCode contract tests (pinned 2.2.x, 2.3.x)
e2e         publish -> install -> opencode serve -> assert agent
build       server + CLI binaries, plugin bundle
scan        govulncheck, trivy, license check
sign        cosign image + SBOM
deploy      staging -> smoke -> prod (manual approval)
```

### 10.2 Caching and reproducibility

- Go builds use `-trimpath` and a pinned toolchain.
- `go.sum`, `bun.lock`, and container digests are committed.
- Release artifacts include checksums and provenance.
- The plugin bundle is built once and published to npm with provenance.

### 10.3 Deployment

```text
1. Run migrations Job (expand phase).
2. Roll out new replicas with maxSurge=1, maxUnavailable=0.
3. Wait for readiness (DB, S3, JWKS reachable).
4. Run smoke tests against the new version.
5. Shift traffic.
6. Keep the old ReplicaSet for rollback (10 minutes).
7. Run the contract phase of migrations in the next release.
```

Rollback is `kubectl rollout undo` plus a forward-compatible schema. A migration
that cannot be rolled back requires an explicit approval and a restore plan.

### 10.4 Release versioning

- Server/CLI: SemVer, `v0.x` until public, then `v1.0`.
- Proto: package `v1`, additive changes only; `buf breaking` enforces it.
- Plugin: independent SemVer, compatibility range documented.
- Manifest schema: `schemaVersion` integer, additive changes only within a major.

---

## 11. Runbooks

### 11.1 Server returns 5xx

1. Check `ReadAvailabilityBurn` and recent deploys.
2. `kubectl logs` for `error` with the request ID.
3. Check `/readyz` dependencies: DB, S3, JWKS.
4. If a deploy is the cause, roll back.
5. If DB is saturated, check long-running queries and pool metrics.
6. If S3 is failing, check provider status and credentials.

### 11.2 Publish fails with `SIGNATURE_INVALID`

1. `agenthub info <asset>@<version> --json` shows the key ID and digest.
2. Confirm the publisher key is registered and not revoked.
3. Confirm the manifest was canonicalized with JCS and the signature covers the
   unsigned manifest.
4. Confirm the CLI and server agree on `schemaVersion`.
5. If the key rotated, re-sign with the active key and publish a new version.

### 11.3 Publish fails with `BLOB_MISSING`

1. `BeginPublish` issued pre-signed URLs; the client may have uploaded to the
   wrong bucket or the URL expired.
2. Check S3 access logs for the publish ID.
3. Re-run `agenthub publish`; `BeginPublish` is idempotent for 24 hours.
4. If the blob exists but the hash mismatches, the upload was corrupted; retry.

### 11.4 SSO unavailable

1. Public reads continue; logins and publishes fail with `unauthenticated`.
2. Cached JWKS continues to validate existing tokens until expiry.
3. Check the SSO status page and the JWKS endpoint.
4. In the cluster:
   ```bash
   kubectl -n shadow-sso get pods,svc,ingress
   kubectl -n shadow-sso logs deploy/shadow-sso --tail=200
   kubectl -n shadow-sso port-forward svc/shadow-sso 5000:5000
   ssoctl client get "$SSO_CLIENT_ID"
   ```
5. Confirm the `agenthub-web` redirect URI exactly matches
   `https://agenthub.pilab.hu/auth/callback`, and that the client secret in
   OpenBao matches the one generated by SSO.
6. Do not disable signature or auth checks to "work around" the outage.
5. If the outage is prolonged, increase the accepted token lifetime only with an
   explicit incident decision.

### 11.5 Valkey unavailable

1. Sessions fall back to the database; rate limits use in-memory counters.
2. Expect higher DB load and less precise limits.
3. Restore Valkey; sessions rehydrate on demand.
4. If the cache is corrupted, flush only the `session:*` keys after confirming
   no active incident requires them.

### 11.6 Database failover

1. The managed Postgres promotes the standby automatically.
2. The server reconnects; `/readyz` turns green.
3. Verify migrations are at the expected version.
4. Check for duplicate audit gaps and re-run the audit export if needed.
5. If the primary is lost, restore from the latest base backup + WAL.

### 11.7 Corrupt or missing blob

1. `agenthub verify` reports the digest mismatch.
2. `agenthub verify --repair` re-fetches from the origin or a mirror.
3. If the origin is gone and no mirror has it, mark the version `unavailable`
   and notify affected lockfile owners.
4. Never serve unverified bytes to make an install succeed.

### 11.8 Disk full (Postgres)

1. Identify the largest tables (`pg_size_pretty`).
2. Check WAL retention and replication slots.
3. Vacuum/analyze; archive audit partitions.
4. Expand the volume before deleting anything.
5. Never delete audit events or manifests to reclaim space.

### 11.9 Federation stuck

1. `agenthub remote status` shows generation, cursor, last sync, and error.
2. Check the upstream well-known document and key fingerprint.
3. If the cursor is invalid or expired, trigger a bootstrap.
4. If pages fail verification, freeze the upstream and inspect the page digest.
5. If storage is full, stop mirroring and keep reference mode.

### 11.10 Certificate expiry

1. Alert fires at 14 days.
2. Renew via cert-manager or the ingress provider.
3. Verify the new certificate chain and HSTS.
4. Re-run the smoke test.

---

## 12. Disaster recovery

### 12.1 Scenarios

| Scenario | Impact | Recovery |
| --- | --- | --- |
| Single pod crash | None | Kubernetes restarts |
| Zone outage | Read latency | Survive with 2 replicas in another zone |
| Postgres primary loss | Writes fail | Automatic failover; restore if standby is lost |
| S3 region loss | Downloads fail | Restore from replica bucket |
| Hub key loss | Cannot sign | Restore offline backup; re-sign catalog |
| Accidental deletion of a version | Data loss | Restore Postgres + S3 to a point in time |
| Ransomware | Full compromise | Isolated backups + rebuild |

### 12.2 Targets

- **RPO:** 5 minutes for metadata, 15 minutes for blobs.
- **RTO:** 1 hour for read service, 4 hours for publish service.
- **Backup verification:** quarterly restore drill, automated checksum check
  weekly.

### 12.3 Rebuild from scratch

1. Provision Postgres, S3, Valkey, and secrets.
2. Restore the latest Postgres base backup + WAL.
3. Restore S3 buckets from the replica or versioned backups.
4. Restore the hub key from offline backup.
5. Run migrations to the expected version.
6. Start one replica; verify a known asset end to end.
7. Scale out; re-enable publishing.
8. Reconcile federation cursors; force a bootstrap if generations moved.

---

## 13. Cost model

| Item | MVP/month | Year 1/month |
| --- | --- | --- |
| Compute (2 × 1 vCPU, 1 GiB) | ~€30 | ~€120 |
| PostgreSQL (managed, HA) | ~€60 | ~€200 |
| Object storage (60 GiB + requests) | ~€5 | ~€30 |
| Egress (CDN) | ~€10 | ~€150 |
| Valkey | ~€15 | ~€40 |
| Observability | ~€20 | ~€80 |
| **Total** | **~€140** | **~€620** |

Cost drivers are egress and observability retention, not compute. Content
addressing and deduplication keep storage small.

---

## 14. Delivery plan

| Milestone | Deliverables | Exit criteria |
| --- | --- | --- |
| **M0 Skeleton** | module, buf, migrations, health, Docker, CI | `make test`, `buf lint`, server starts |
| **M1 Publish/read** | namespaces, signing, publish API, catalog, store | Signed pack published and browsable |
| **M2 Consume** | CLI resolve/install/lock/verify, plugin | `agenthub add` + `opencode` loads it; offline install works |
| **M3 Operate** | SSO, RBAC, audit, rate limits, dashboards, deploy | Public hub live; SLOs measured |
| **M4 Federate** | sync, mirror/proxy, export/import | Two hubs exchange a signed pack; air-gap install works |
| **M5 Ecosystem** | more harness adapters, Sigstore, recommendations | Non-OpenCode path documented and tested |

Each milestone ships behind a feature flag and has a rollback plan. Federation
and Meilisearch stay off until their exit criteria are met.

---

## 15. Open questions

1. Managed Postgres vs self-hosted: managed is assumed. Self-hosted needs a
   tested failover runbook and backup tooling.
2. Single region vs multi-region: single region for MVP; multi-region read
   replicas only if latency requires it.
3. Audit retention of 7 years may be excessive; confirm with legal.
4. Should the hub key live in KMS (slower, stronger) or a mounted secret
   (faster, weaker)? Current: mounted secret with strict RBAC; KMS for the
   offline root.
5. Do we need per-tenant quotas at MVP? Current: per-namespace publish limits
   only; storage quotas in M5.
