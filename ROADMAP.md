# AgentHub Roadmap & Changeset

This file tracks implementation progress against the milestones in
[ARCHITECTURE.md §13](ARCHITECTURE.md). Each completed slice lists the files
changed and tests added. PiR@ is not used for tracking; this document is the
source of truth.

## M0 — Skeleton (done)

- Go module, buf, Postgres migrations, health endpoints, metrics, CI, Docker.
- Commit: `a92647a`, `f53e2e1`.

## M1 — Publish & read (done)

### Slice 1 — Namespace lifecycle (done)

- `internal/registry/namespace.go`: in-memory `Store` interface + `MemStore`
  with slug validation (per domain-model regex), default-public visibility,
  create/get/list, sentinel errors.
- `internal/api/stubs.go`: `Registry` handler now backed by a store;
  `CreateNamespace`, `GetNamespace`, `ListNamespaces` implemented with proper
  Connect error-code mapping.
- `internal/api/registry_test.go`: create/get/list/duplicate coverage.
- `internal/registry/namespace_test.go`: slug/visibility/duplicate coverage.

### Self-review findings (M1.1)

- Aligned slug regex with `docs/architecture/domain-model.md`
  (`^[a-z0-9][a-z0-9-]{1,62}$`) instead of the stricter earlier draft.
- Default visibility now resolves to `public` when unspecified, matching the
  schema default.
- `GetNamespace`/`ListNamespaces` return a placeholder `owner` caller role;
  real role resolution lands with Shadow SSO in M3 (resolved in M3.2: the
  handlers now report the caller's real role).
- Pagination cursor in `ListNamespaces` is ignored (M1.7).
- Stores are in-memory only; persistence swap is M1.7.

### Slice exit criteria

Every slice is "done" only when: (1) handler implemented against the `Store`
interface, (2) unit tests added and passing under `go test -race`, (3) wired
into `internal/server`, (4) `golangci-lint` and `buf lint` clean, (5) ROADMAP
updated.

### Slice 2 — Asset + version models and catalog read RPCs (done)

- `internal/catalog/store.go`: `Store` interface + `MemStore` with Asset/Version,
  sentinel `ErrNotFound`, cloning via proto.Clone.
- `internal/api/stubs.go`: `Catalog` handler now backed by a store;
  `SearchAssets`, `GetAsset`, `ListVersions`, `GetVersion` implemented with
  Connect error-code mapping.
- `internal/api/catalog_test.go`: search/get/listversions/getversion + not-found.
- `internal/catalog/store_test.go`: mem store asset/version coverage.

### Slices remaining

- [x] M1.2 Asset + version models and catalog read RPCs (SearchAssets, GetAsset,
      ListVersions, GetVersion) backed by store.
- [x] M1.3 BeginPublish/CommitPublish flow with bundle digest + manifest (in-memory).
  - `internal/api/stubs.go`: Registry holds reg store + catalog store + in-memory publish records (mutex).
  - `BeginPublish` validates namespace, creates publish record (rand hex id, expires +15m), returns empty uploads.
  - `CommitPublish` verifies sha256 manifest digest, idempotent by (asset, version, manifest digest), upserts asset/version.
  - `internal/api/registry_publish_test.go`: happy path, unknown namespace, digest mismatch, idempotency, unknown publish id.
- [x] M1.4 Signing keys + signature verification.
  - `internal/registry/signing_key.go`: in-memory `SigningKeyStore` (`Register`, `Revoke`, `Get`, `List`) with `SigningKey` struct and sentinel errors.
  - `internal/api/stubs.go`: `Registry` holds signing key store (default in `NewRegistry`); `RegisterSigningKey`, `RevokeSigningKey` implemented with Connect error-code mapping (namespace existence, duplicate kid, defaults ed25519/client).
  - `internal/api/registry_signing_test.go`: happy path + duplicate + unknown namespace + revoke.
  - Note: crypto signature verification is out of scope for M1.4 (key lifecycle only).
- [x] M1.5 Drafts (Create/Update/Discard/Delete/Publish).
- [x] M1.6 Store pages (Go SSR) for namespace/asset/version.
  - `internal/web/web.go`: Handler gains optional CatalogStore/RegistryStore; NewWithStores; Namespace/Asset/Version handlers.
  - templates: namespace.tmpl, asset.tmpl, version.tmpl.
  - server routes: /ns/:slug, /a/:ns/:kind/:name, /a/:ns/:kind/:name/:version.
  - `internal/server/server_test.go`: TestStorePages.
- [x] M1.7 Swap in-memory stores for Postgres (bun) repositories.
- [x] M1.8 Integration tests against a real Postgres.

### CI

- `go test -race ./...`, `go vet`, `golangci-lint`, `buf lint` all pass on M1.1.

## M2 — Consume (done)

- [x] M2.1 Catalog.Resolve RPC (server-side) + CLI `add` command basics.
- [x] M2.2 Lockfile writing on `add`.
- [x] M2.3 Install from lockfile.
  - `cmd/agenthub/main.go`: `install` command (`runInstall`) reads `agenthub.lock.json` from cwd (missing file is an error), prints `installing <ref>` per locked asset, and `--frozen` fails when the lockfile has no assets. No cache download yet (M2.4).
  - `cmd/agenthub/main_test.go`: missing lockfile, per-asset output, `--frozen` empty/with-assets, plus cobra wiring (`install`, `install --frozen`) run against a temp cwd.
- [x] M2.4 Bundle fetch + verification.
  - [x] Content-addressed manifest fetch + local cache (M2.4 minimal).
    - `internal/cache/cache.go`: content-addressed local cache. Keys are sha256
      hex digests (`Key`, `KeyFromDigest` accepts `sha256:<hex>` or bare hex);
      `Put(key, data)` refuses data that does not hash to the key, writes
      atomically (temp + fsync + rename), `Get(key)` re-hashes on read and
      returns `ErrNotFound` / `ErrInvalidKey` / `ErrUnsupportedAlgorithm` /
      `ErrDigestMismatch`, `Has(key)` is a stat-only presence check. Objects
      live under `<dir>/sha256/<aa>/<key>`; default dir is
      `$AGENTHUB_CACHE_DIR` → `$XDG_CACHE_HOME/agenthub` → `~/.cache/agenthub`.
    - `cmd/agenthub/main.go`: `install` gains `--hub`, `--offline`,
      `--cache-dir`, `--config`. Per locked asset it serves the manifest from
      the cache first (local-first), otherwise calls `Catalog.GetBundle` — by
      manifest digest when the lockfile pins one, by ref otherwise — and stores
      the manifest in the cache. Any RPC error (hub unreachable, `GetBundle`
      unimplemented server-side) prints `offline; using cache` and degrades the
      rest of the run to cache-only; an empty manifest or a digest mismatch is
      a hard error (verification). Ends with
      `summary: total=… fetched=… cached=… missing=…`.
    - `resolveHub` / `normalizeHubURL` extracted and shared by `add` and
      `install`; hub requests are bounded by a 15s timeout.
    - Tests: `internal/cache/cache_test.go` (round-trip, digest mismatch,
      corruption detection, key parsing, default-dir precedence, concurrent
      puts) and `cmd/agenthub/main_test.go` (offline hit/miss/no-digest, fetch
      through an httptest Connect handler for both selectors, unimplemented
      and unreachable-hub fallback, digest mismatch rejection, cobra wiring
      with `--frozen`/`--offline`/`--cache-dir`).
    - `gofumpt`, `golangci-lint`, `go test -race ./...`, `buf lint` clean; no
      new dependencies.
  - [x] Bundle/blob download, bundle digest + signature verification, offline
        exit 6 (M2.4 complete).
    - `cmd/agenthub/bundle.go`: `downloadBlobs` GETs every `BlobLocation`
      `GetBundle` hands out, verifies the body against its declared sha256 and
      stores it in the content-addressed cache under that digest (`cache.Put`
      refuses bytes that do not hash to it). Blobs already present are re-read —
      which re-verifies them — instead of downloaded. Transport failures
      (`file unavailable <path> for <ref>`), blobs without a usable sha256 and
      unsupported digest algorithms are reported and counted as *missing*; a
      delivered body that does not match its digest is a hard error, never a
      silent install. `fetchBlob` bounds the read by the declared size plus a
      256 MiB cap, and blobs run sequentially (bounded concurrency is still
      open, per cli-plugin.md §3).
    - Bundle digest per `docs/architecture/domain-model.md` §3.2
      (`bundleDigest = sha256(tar.zst payload)`): verified when the payload is
      one of the served blobs — those bytes were hashed during download — and
      reported as `bundle digest not verified <ref>: bundle payload not
      available` otherwise. It is deliberately *not* derived from the file
      digests (that is not what the hub pins); each file digest is verified on
      its own instead. A hub whose bundle digest conflicts with the lockfile pin
      is a hard verification error.
    - `internal/verify/verify.go`: `CanonicalManifest` returns the bytes a
      signature covers — the manifest without `signatures` and `manifestDigest`,
      keys sorted at every depth, number literals preserved, no HTML escaping
      (RFC 8785 JCS for manifests as `agenthub pack` emits them);
      `ManifestSignatures` parses the manifest's `signatures` array into
      `commonv1.Signature` (base64 value, RFC 3339 `signedAt`); `CheckSignature`
      verifies Ed25519 over the canonical bytes with `crypto/ed25519` (stdlib)
      and rejects non-ed25519 algorithms, wrong-size keys and nil signatures.
      Every failure is a sentinel error (`ErrMalformedManifest`,
      `ErrInvalidSignature`, `ErrUnsupportedAlgorithm`, `ErrNoPublicKey`,
      `ErrNoSignature`).
    - Signature verification in `install`: a manifest that carries signatures is
      checked against the namespace key from `installOptions.KeyResolver`.
      **M2.4 ships no key source** — the Registry has no key-listing RPC and
      `/.well-known/agenthub/keys.json` does not exist yet — so with no resolver
      install prints `signature unverified <ref>: no public key source` and
      continues (as it does when a resolver cannot find the key). A retrievable
      key that fails to verify is a hard error: install never falls back to
      unsigned silently. The check also runs on the cached-manifest path; one
      valid signature among the manifest's entries is enough.
    - Exit codes in `cmd/agenthub/main.go`, per cli-plugin.md §2.1: **5** for
      digest or signature verification failures, **6** when `--frozen` and
      nothing is installable offline (empty lockfile, or the cache cannot cover
      the locked assets/files — including after GetBundle degrades the run to
      cache-only), **1** otherwise (including `--frozen` with an incomplete
      install while the hub answers). Non-frozen installs keep reporting
      `summary: … missing=…` and exiting 0.
    - `cmd/agenthub/bundle_test.go`: blob download + cache round-trip, cached
      blob reuse, blob digest mismatch (exit 5), unavailable blob report,
      `--frozen` incomplete bundle online (exit 1) and offline (exit 6), bundle
      digest verified / unverifiable / conflicting (exit 5), signature verified
      / invalid (exit 5) / unverified without key source (fetch and cache paths)
      / key lookup failure, plus an `exitCode` table.
      `internal/verify/verify_test.go`: canonical manifest (field removal,
      nested key order, number literals, stability, non-manifest rejection),
      signature parsing, and every `CheckSignature` rejection.
    - Test-stub change: `stubCatalog` now serves blobs from its own httptest
      server (`/blobs/<path>`, per-path content, optional failure status)
      instead of a public URL, and `TestRunInstallFrozenWithAssets` now expects
      the documented exit-6 failure when `--frozen` finds nothing installable
      offline.
    - Checks: `gofumpt`, `golangci-lint` (0 issues), `go vet`,
      `go test -race ./...`, `buf lint` clean; no new dependencies.
    - Still open for later milestones: key distribution (well-known keys JSON)
      and `requireSignature` policy (M3), `agenthub verify --repair`,
      parallel downloads, and full JCS edge cases (non-BMP key ordering).
- [x] M2.5 OpenCode V2 plugin scaffold (`plugins/opencode`).
  - `plugins/opencode/package.json`: `@pilab/opencode-agenthub` (bun, ESM,
    exports `./src/index.ts`), scripts `test` (`bun test`) and `build`
    (`tsc --noEmit`); dev dependencies are only `@types/bun` + `typescript`,
    no runtime dependencies.
  - `plugins/opencode/src/index.ts`: default export shaped like
    `Plugin.define({ id: "agenthub", setup })`. On activation it reads
    `agenthub.lock.json` from `ctx.location.directory` and logs
    `registered <ref>` for every locked asset. **M2.5 minimal: logging *is*
    the registration** — no `ctx.<domain>.transform` / registration API calls
    yet; the real adapters land per `docs/architecture/cli-plugin.md` §6.
    Missing lockfile and malformed lockfile are logged, not thrown, so plugin
    activation never fails the server.
  - `plugins/opencode/src/lockfile.ts`: `parseLockfile` / `readLockfile`
    mirroring `internal/lockfile` (defaults for missing `lockfileVersion` and
    `assets`; rejects non-object documents, non-array `assets`, and assets
    without `ref`); `src/log.ts` is the injectable `Logger` seam
    (OpenCode's plugin context has no logger field).
  - `plugins/opencode/test/index.test.ts` (bun:test): sample-lockfile parse,
    Go-CLI-compatible defaults, malformed-input rejection, per-asset
    `registered <ref>` output, missing/malformed lockfile handling, default
    export shape — 8 tests.
  - Checks: `bun install`, `bun test` (8 pass), `bun run build`
    (`tsc --noEmit`) clean.
  - Note: the scaffold lives in `plugins/opencode/`, not the
    `clients/opencode-plugin/` path sketched in
    `docs/architecture/cli-plugin.md` §12.2.
- [x] M2.6 `update` / `remove` commands (renumbered from M2.5 to make room for
      the plugin scaffold).
  - `cmd/agenthub/main.go`: `update` re-resolves every asset in `agenthub.json`
    with one `Catalog.Resolve` call (desired list, current pins as `lock`,
    `update: true`) and rewrites `agenthub.lock.json` — one pin per desired
    asset, origin hub falling back to the resolved hub's host. `--offline`
    skips the network entirely: it keeps the existing pins, prunes assets that
    are no longer desired, and reports desired assets that have no pin; a
    missing lockfile is an error in offline mode. An empty desired state prints
    a message instead of resolving, and a desired asset the hub cannot resolve
    fails the run *before* the lockfile is written (server warnings are printed
    first), so pins are never dropped silently. `remove <asset-ref>` drops the
    entry from `agenthub.json` and prunes the lockfile without it (cached blobs
    stay; that is `agenthub gc`'s job) — it does not touch the network and only
    rewrites the lockfile when one exists. Kind aliases (`agents`) and version
    suffixes/constraints are normalized on both sides of the match.
  - `internal/lockfile/lockfile.go`: `DesiredAsset.Identity()` /
    `LockedAsset.Identity()` (versionless `namespace/kind/name`),
    `PruneLocked(desired, locked)` (drop pins that are no longer desired,
    order and metadata preserved) and `RemoveAsset(desired, locked, ref)`
    (remove the matching desired asset and recompute the lockfile in one
    step).
  - `cmd/agenthub/main_test.go`: update online (resolution output, pin
    contents, request shape), offline (pin kept + stale pruned, unpinned
    report, missing lockfile), empty desired state, unresolvable asset
    leaving the lockfile byte-identical, unknown desired kind, digest
    round-trip; remove (canonical ref with version + kind alias, not-found,
    missing lockfile) plus cobra wiring for `remove`, `update --offline` and
    `update --hub` against a stub Resolve handler.
    `internal/lockfile/lockfile_test.go`: identity parsing, prune
    (survivor/metadata/input immutability), remove-and-recompute, not-found,
    malformed refs.
  - Checks: `gofumpt`, `golangci-lint`, `go vet`, `go test -race ./...`,
    `buf lint` clean; no new dependencies.

## M3 — Operate (in progress)

### Slice 1 — M3.1 Audit log + rate limiting (done)

- `internal/audit/audit.go`: `AuditStore` interface (`Log(action, actor,
  namespace, asset, version, detail)` and `List(Query)`) with `Entry`/`Query`,
  sentinel `ErrActionRequired`, and the default in-memory `MemStore`
  (concurrency-safe, capped at 4096 entries with oldest-first eviction,
  newest-first listing, namespace/actor/action filters, limit/offset paging).
- `internal/audit/postgres.go`: `PostgresStore` backed by `agenthub.audit_log`
  (2 s write timeout so a slow DB cannot stall a request, jsonb `detail`
  stored raw when it is valid JSON and wrapped as a JSON string otherwise,
  `ErrNilDatabase`).
- `internal/db/models.go`: `AuditLog` bun model (`id,pk,nullzero` so the
  bigserial default applies on insert).
- `internal/server/ratelimit.go`: per-IP token bucket limiter
  (`tokenBucketLimiter`, refill = configured rate, burst = rate) with an idle
  bucket sweep (5 m TTL, at most once a minute) so the map cannot grow
  unbounded. Emits `RateLimit-Limit`/`RateLimit-Remaining`/`RateLimit-Reset`
  and, on denial, `Retry-After` plus `429` via `echo.HTTPError`. Skips
  `/healthz`, `/readyz` and `/static/*`; denied requests are recorded as
  `ratelimit.blocked` audit entries (audit write failures never fail the
  request).
- `internal/server/server.go`: limiter installed between `RequestID` and the
  authenticator, i.e. before every handler; `newAuditStore` picks Postgres
  when a database is configured and the in-memory store otherwise.
- `internal/config/config.go`: `server.rate_limit_per_sec` (default `100`,
  `0` or negative disables the limiter).
- Tests: `internal/audit/audit_test.go` (record + list order, filters, paging,
  missing action, cap eviction, concurrent logging, nil-db store, detail JSON
  coercion); `internal/server/ratelimit_test.go` (allows then blocks with
  headers, healthz/static skip, disabled limiter, per-IP isolation, audit
  entry on denial, token-bucket refill math);
  `internal/audit/postgres_integration_test.go` (`-tags integration`,
  `AGENTHUB_TEST_DSN`) verified against the local dev database.
- Checks: `gofumpt`, `golangci-lint`, `go vet`, `go test -race ./...`,
  `buf lint` clean; no new dependencies.
- Explicitly out of scope for M3.1: no registry/publish handler writes audit
  entries yet and the `ListAudit` RPC stays unimplemented — the slice ships
  the store and the middleware only.

### Slice 2 — M3.2 Namespace RBAC (done)

- `internal/authz/authz.go`: `Role` enum (`reader < publisher < maintainer <
  owner`) with `ParseRole`/`Valid`/`Rank`/`AtLeast`; sentinel errors
  (`ErrInvalidRole`, `ErrInvalidSubject`, `ErrInvalidNamespace`,
  `ErrBindingNotFound`, `ErrUnauthenticated`, `ErrDenied`); `Identity`
  (subject + scopes) with `Authenticated`/`IsAdmin`; `ScopeAdmin`
  (`agenthub.admin`, bare `admin` accepted as shorthand); `ParseScopes`
  (comma/whitespace separated, never nil); request-context plumbing
  (`WithIdentity`/`IdentityFromContext`).
- `internal/authz/store.go`: `Store` interface — `GetRole(hub, namespace,
  subject)`, `ListRoles(hub, namespace)` (bindings sorted by subject),
  `Grant`, `Revoke` — with the concurrency-safe in-memory `MemStore`
  (upserting grants, `ErrBindingNotFound` when revoking a missing binding),
  plus the `Check` helper: anonymous callers get `ErrUnauthenticated`, hub
  admins bypass the namespace role check, everyone else needs a binding of at
  least the required role (missing binding is a denial, never a fallback).
- `internal/server/auth.go`: `HeaderAuthenticator` — the M3.2 placeholder
  interceptor reads `X-Auth-Subject`/`X-Auth-Scopes`, stores them on the echo
  context under `server.SubjectKey`/`ScopesKey` and mirrors them into the
  request context (`authz.WithIdentity`) so Connect handlers resolve the
  caller with `authz.IdentityFromContext`. It replaces
  `NoopAuthenticator` in the server and makes `SubjectFromContext` return the
  real subject; Shadow SSO token validation replaces the header trust in
  M3.3.
- `internal/server/server.go`: installs `HeaderAuthenticator` and builds the
  Registry with `api.NewRegistryWithRoles(regStore, catStore,
  authz.NewMemStore(), hubID)` — role bindings are in-memory only until M3.3.
- `internal/api/stubs.go`: `Registry` holds an `authz.Store` plus hub id.
  `CreateNamespace` requires the admin scope or an authenticated subject
  (`CodeUnauthenticated` otherwise) and grants the creator `owner` on the new
  namespace; `RegisterSigningKey` requires the `publisher` role on the
  namespace (`CodePermissionDenied`, `CodeUnauthenticated` for anonymous
  callers, admin scope bypasses); `GetNamespace`/`ListNamespaces` return the
  caller's real role instead of the placeholder `owner` (admins report
  `owner`, unbound subjects `""`).
- Fixed a latent routing bug in `internal/server/server.go`: connect v2 hands
  out service prefixes with a trailing slash, so the previous `mount`
  registered `"/<service>//*"` and every Connect RPC 404'd through the echo
  server; `mount` now trims the trailing slash. Regression-covered by the
  end-to-end test below.
- Tests: `internal/authz/authz_test.go` (role parsing/ranking, scope parsing,
  admin detection, identity context), `internal/authz/store_test.go`
  (grant/get/revoke, input validation, hub+namespace scoping and ordering,
  concurrent access under `-race`, full `Check` matrix),
  `internal/api/registry_authz_test.go` (anonymous create denied, creator
  becomes owner, real caller roles, signing keys gated on `publisher` with
  anonymous/unbound/reader/publisher/admin variants),
  `internal/server/auth_test.go` (headers → echo context + request context,
  anonymous defaults, and the full path over HTTP through `server.New`).
  Existing API tests pass a placeholder identity via the `identCtx` helper in
  `internal/api/identity_test.go`.
- Decisions: no admin is seeded as owner of every namespace (minimal scope) —
  the first creator of a namespace becomes its owner and callers holding the
  admin scope bypass namespace role checks.
- Checks: `gofumpt`, `golangci-lint`, `go vet`, `go test -race ./...`,
  `buf lint` clean; no new dependencies.
- Explicitly out of scope for M3.2: `RevokeSigningKey`, the publish/draft RPCs
  and the web routes are not role-checked yet; no RPC exposes
  `Grant`/`Revoke` (they stay library-level admin operations until the
  identity RPCs land); role bindings are not persisted.

### Slice 3 — M3.3 Shadow SSO OIDC validation (JWT + JWKS) (done)

- `internal/auth/sso`: `Config` (issuer, audience + audience allowlist,
  `jwks_url`, discovery URL, clock skew, JWKS cache TTL, default scopes)
  with fail-fast `New` validation (absolute http(s) issuer/JWKS/discovery
  URLs, issuer and at least one audience required, non-negative skew) and
  `Verifier.Verify`: RS256-only parse (`jwt.ParseSigned` is restricted to
  RS256, so `none` and HS256 algorithm-confusion never reach a key lookup),
  key selection by `kid` (single-key fallback when the token carries no
  `kid`), signature verification, then `iss` == issuer, `aud` in the
  allowlist, `exp`/`nbf`/`iat` inside the 60s clock skew, and a non-empty
  `sub`. Result: `sso.Claims` (subject, scopes, issuer, audience, `jti`,
  iat/nbf/exp). Every failure wraps one of the package's sentinel errors
  (`ErrExpired`, `ErrAudienceMismatch`, `ErrKeyNotFound`, ...) so callers
  classify with `errors.Is`.
- `internal/auth/sso/jwks.go`: cached JWKS fetcher — lazy fetch on the first
  token, `auth.jwks_cache_ttl` (10m default) reuse, a rate-limited (30s)
  forced refetch when a token names an unknown `kid` (key rotation), OIDC
  discovery fallback (`issuer/.well-known/openid-configuration` →
  `jwks_uri`) when `auth.jwks_url` is empty, a 1 MiB response cap, and only
  RSA public keys retained. A failed refresh keeps serving the cached set,
  so an IdP outage does not invalidate tokens already accepted
  (operations.md §11.4).
- `internal/server/auth.go`: `SSOAuthenticator` validates
  `Authorization: Bearer <jwt>` and installs `sub` plus the `scope`
  (space/comma-separated) or `scopes` (array) claim as the identity; when
  the token carries neither, the verifier's default scopes apply — left
  empty by the server, so a scope-less token stays unprivileged (fail
  closed). A request with no bearer token is anonymous (public reads keep
  working, privileged RPCs fail their authz check) and the M3.2 `X-Auth-*`
  headers are no longer an identity source in this mode.
- `internal/server/server.go`: `newAuthenticator` installs
  `SSOAuthenticator` when `auth.issuer` is set and keeps
  `HeaderAuthenticator` as the dev/trusted-proxy fallback otherwise; a
  broken SSO block aborts startup instead of silently downgrading to header
  trust.
- `internal/config/config.go`: `Validate` checks the SSO block (absolute
  issuer/JWKS/discovery URLs, audience required when the issuer is set,
  non-negative clock skew), so `--check-config` reports the same problems
  before the server binds a port.
- Dependencies: none new — `github.com/go-jose/go-jose/v4` was already in
  the module graph (via the Vault API client) and is now a direct
  dependency; `go mod tidy` also dropped the stale, unused
  `connectrpc.com/connect/v2` requirement.
- Tests: `internal/auth/sso/sso_test.go` — RSA key + RS256 token against an
  httptest JWKS: valid token, expired (and inside-skew) token, wrong `aud`
  (plus allowlist), wrong `iss`, wrong signature, `alg=none`, HS256,
  missing `exp`/`sub`, future `nbf`, every scope shape (string, array,
  default, fail-closed), JWKS fetched once and cached, refetch after
  rotation, cached keys served during an IdP outage, JWKS via discovery,
  fetch failure, 16 concurrent verifies under `-race`, and `New` config
  errors. `internal/server/sso_auth_test.go` — identity on the echo and
  request contexts, 401 for expired/garbage/wrong-aud/wrong-iss tokens,
  headers ignored while SSO is enabled, and the full path over HTTP through
  `server.New` (valid bearer creates the namespace, expired token is 401,
  no token is `CodeUnauthenticated`).
- Checks: `gofumpt`, `golangci-lint`, `go vet`, `go test -race ./...`,
  `buf lint` clean.
- Explicitly out of scope for M3.3: the OIDC BFF routes (`/auth/login`,
  callback, logout — landed in M3.5), token introspection/revocation, ES256
  keys, and persisted RBAC bindings.

### Slice 4 — M3.4 Audit wiring + ListAudit (done)

- `internal/api/stubs.go`: `Registry` holds an `audit.AuditStore` (default
  in-memory) and gains `NewRegistryWithAudit(regStore, catStore, roles, hub,
  audits)`; the private `auditEvent` helper records the caller's subject from
  the request context and swallows write failures (same policy as the
  rate-limit middleware, M3.1). Actions recorded, each only after the state
  change succeeded: `CreateNamespace` (visibility in the JSON detail),
  `BeginPublish`/`CommitPublish` (asset + version, publish id, `idempotent`
  flag on a repeat commit), `CreateDraft`/`PublishDraft` (draft id, version on
  publish), `RegisterSigningKey`/`RevokeSigningKey` (kid).
- `ListAudit` implemented: the namespace filter is mandatory
  (`CodeInvalidArgument` when empty, `CodeNotFound` for an unknown namespace),
  then `authz.Check` with `RoleMaintainer` — owner passes, hub admins bypass,
  anonymous gets `CodeUnauthenticated`, unbound/reader/publisher get
  `CodePermissionDenied` (`docs/architecture/api.md`: "Namespace audit log
  (maintainer/owner)"). Paging is the simple offset form: `page.cursor` is the
  decimal offset of the next entry, `page.limit` defaults to and is capped at
  100 (`common.v1.PageRequest` documents the cap), and `next_cursor` is set
  only when the page came back full. Events map onto `AuditEvent` (`at`,
  `actor_sub`, `action`, `asset`, `version`, `detail_json`); `actor_email` and
  `ip` stay empty because neither the store nor the handler context carries
  them, and the namespace is the request filter, not a message field.
- `internal/server/server.go`: one audit store per server — hoisted out of the
  rate limiter and handed to `api.NewRegistryWithAudit`, so `ListAudit` sees
  both the RPC entries and `ratelimit.blocked`.
- Tests: `internal/api/audit_test.go` — all seven actions logged with actor/
  namespace/asset/version plus correlating detail, failed calls never logged,
  the maintainer/owner/admin vs anonymous/unbound/reader/publisher matrix,
  required/unknown namespace and bad cursors, namespace filtering with
  offset paging and `next_cursor` progression, the 100-entry page cap, and a
  broken audit store (writes never fail a request, a failing read is
  `CodeInternal`).
- Decisions: action strings are the RPC names as scoped for M3.4, not the
  dotted aliases in `docs/architecture/operations.md` §8.3; a failed call is
  never audited (only committed state changes are), and a repeat
  `CommitPublish` still records an entry flagged `idempotent`.
- Checks: `gofumpt`, `golangci-lint`, `go vet`, `go test -race ./...`,
  `buf lint` clean; no new dependencies.
- Explicitly out of scope for M3.4: `UpdateDraft`/`DiscardDraft`/`DeleteDraft`,
  yank/unyank/deprecate and the other unimplemented RPCs are not audited yet;
  audit writes are not yet in the same transaction as the state change (the
  stores are separate); `AuditEvent` gains no email/IP/user-agent fields.

### Slice 5 — M3.5 OIDC BFF routes (done)

- `internal/server/bff.go`: the three BFF routes, built only when the
  deployment configured a browser client (`auth.client_id` + `auth.issuer`;
  `newBFF` returns nil otherwise and `/auth/*` stays 404, so a hub without a
  browser login is unchanged).
  - `GET /auth/login` resolves `authorization_endpoint`, `token_endpoint` and
    `end_session_endpoint` from OIDC discovery (`auth.discovery_url` or
    `issuer/.well-known/openid-configuration`; only a successful lookup is
    cached, so an IdP outage costs one failed request per attempt instead of a
    permanently broken login), mints `state`, `nonce` and a 32-byte PKCE
    verifier, parks them plus the validated `return_to` in an HMAC-SHA256
    signed cookie (`agenthub_login`, 10m, `HttpOnly`, `SameSite=Lax`, `Secure`
    behind TLS or `X-Forwarded-Proto`) and redirects with `response_type=code`,
    `client_id`, `redirect_uri`, `scope`, `state`, `nonce` and
    `code_challenge=<S256>`. `return_to` accepts same-origin paths only:
    absolute URLs, protocol-relative `//host`, backslashes, control characters
    and values over 1 KiB fall back to `/`, so login cannot become an open
    redirect. Discovery or endpoint failure is `503` (web-and-sso.md §4).
  - `GET /auth/callback` consumes the transaction — the login cookie is
    cleared on every attempt, so a state is good exactly once — and rejects a
    missing or mismatched state with `400` before any network call. The code is
    exchanged at the token endpoint as a form-encoded POST with the
    `code_verifier` and `client_secret_basic` (RFC 6749 §2.3.1; an empty
    `auth.client_secret` makes it a public client identified in the body,
    PKCE still binding the code). The `id_token` is verified with the existing
    `sso.Verifier` (RS256 only, `iss`, `aud` = client id, `exp`, `sub`) and its
    `nonce` is then compared with the transaction — the payload is trusted only
    because that verification succeeded. A 4xx from the token endpoint maps to
    `400` (restart the login), transport/5xx failures to `502`, provider
    `error=` to `400`. On success the session cookie (`agenthub_session`, 12h,
    `HttpOnly`, `SameSite=Lax`) is written with `sub` and scopes only and the
    browser is sent `303` back to `return_to`.
  - `GET`/`POST /auth/logout` clears the session cookie and redirects to the
    provider's `end_session_endpoint` with `client_id` and
    `post_logout_redirect_uri`. Logout never fails: when discovery is
    unreachable the browser still signs out locally and lands on
    `auth.post_logout_redirect_url` (or `/`).
- `internal/server/server.go`: `/auth/login`, `/auth/callback` and
  `/auth/logout` are registered when the BFF is configured, together with
  `bff.sessionMiddleware()` installed directly after the authenticator: the
  signed session cookie becomes an identity source for every route when no
  bearer token or `X-Auth-*` header produced one (bearer/header identity keeps
  precedence, so an API client is never shadowed by a stale cookie;
  `SameSite=Lax` keeps the cookie off cross-site state-changing requests).
- `internal/config/config.go`: new `auth.session_secret` (>= 32 characters)
  keys the cookie HMAC. `auth.client_id` now requires `auth.issuer`, the
  session secret and a callback URI (`auth.redirect_url`, or
  `server.public_url` from which `/auth/callback` is derived), so
  `serve --check-config` reports a half-configured BFF before the port binds.
  `deploy/dev/config.yaml` gained a dev-only session secret.
- Dependencies: none new — `golang.org/x/oauth2` is not in the module, so the
  code exchange is a plain `application/x-www-form-urlencoded` POST and
  id_token verification reuses `github.com/go-jose/go-jose/v4` through
  `internal/auth/sso`.
- Tests: `internal/server/bff_test.go` against a fake IdP over `httptest`
  (discovery + JWKS + token endpoint, RSA id_token): authorize redirect with
  PKCE/state/nonce and cookie attributes, the unsafe-`return_to` table, `Secure`
  behind `X-Forwarded-Proto`, IdP down → 503, the full callback path (form
  fields, `Basic` credentials, nonce binding, session cookie contents, spent
  login cookie), state mismatch/missing/absent cookie → 400 without touching
  the token endpoint, provider `error=`, 4xx vs 5xx token-endpoint mapping,
  bad nonce and unknown signing key, logout redirect + cookie clearing (and
  the degraded path), routes absent without `client_id`, the session-middleware
  matrix (valid/no/tampered/foreign-key/expired/empty-subject cookie, bearer
  identity wins), `safeReturnTo` and the cookie codec round trip/tamper.
  `internal/config/config_test.go`: the new session-secret/issuer/redirect
  validation rules.
- Decisions: the minimal session is an HMAC-signed cookie carrying subject and
  scopes instead of the Valkey-backed server session of `web-and-sso.md` §4 —
  M3.5 has no server-side session store at all — and tokens are never stored
  anywhere, so refresh and revocation stay out of scope. `POST /auth/logout`
  is registered next to `GET` to match `api.md` §4.2.
- Checks: `gofumpt`, `golangci-lint`, `go vet`, `go test -race ./...`,
  `buf lint` clean; no new dependencies.
- Explicitly out of scope for M3.5: server-side sessions, idle timeout and
  token refresh/revocation (including `id_token_hint` on logout), CSRF
  double-submit tokens for the dashboard forms, and calling the provider with
  the user's access token on their behalf.

### Slice 6 — M3.6 RBAC for the remaining RPCs + persisted role bindings (done)

- `internal/api/stubs.go`: every remaining Registry RPC now runs
  `authz.Check` before touching state, via two helpers — `requireRole(ctx,
  namespace, min)` for namespace-scoped checks and `requireDraftRole(ctx,
  draft, min)` for draft mutations (the draft's author always passes,
  everyone else needs the role on the draft's namespace). Enforcement per
  `docs/architecture/api.md` §5.5: `CreateDraft` and `PublishDraft` →
  `publisher`; `UpdateDraft`/`DiscardDraft`/`DeleteDraft` → author or
  `maintainer`; `SetLandingVisibility` → `maintainer`; `ValidateBundle` →
  `publisher`; `YankVersion`/`UnyankVersion`/`DeprecateVersion` →
  `maintainer`; `RevokeSigningKey` → `owner` (`RegisterSigningKey` stays
  `publisher`, `ListAudit` stays `maintainer`). The five previously
  unimplemented RPCs check existence → role → *then* return
  `CodeUnimplemented`, so an unauthorized caller gets
  `Unauthenticated`/`PermissionDenied` instead of leaking that the method
  exists unimplemented (deny by default). `CreateDraft` records the caller
  as `AuthorSub`, which is what makes the author-or-maintainer rule work.
- Argument/existence validation keeps its M3.4 order (bad input →
  `CodeInvalidArgument`, unknown namespace/draft → `CodeNotFound` before the
  role check), so error codes for malformed requests are unchanged; the
  helpers parse `asset_id` as `<namespace>/<kind>/<name>` and require a
  non-empty `ref.namespace`.
- `internal/authz/postgres.go`: `PostgresRoleStore` implements `authz.Store`
  against `agenthub.namespace_roles` — same validation and sentinel errors
  as `MemStore` (`ErrInvalidSubject`/`ErrInvalidNamespace`/`ErrInvalidRole`,
  `ErrBindingNotFound` on a missing revoke), upsert on grant, subject-ordered
  listing, a 2s per-operation timeout (audit-store policy) and fail-closed
  reads: a lookup error or nil database reports "no binding", never a
  fallback. `GetRole`'s read path feeds `authz.Check` unchanged.
- `migrations/000002_roles.sql`: `agenthub.namespace_roles`
  (`hub_id, namespace, subject` primary key, `role`, `granted_at`,
  `updated_at`) plus an index on `(hub_id, namespace)`. No foreign key to
  `namespaces` on purpose — bindings are keyed by slug like the in-memory
  store, and `CreateNamespace` grants the owner binding after the namespace
  row exists either way. `internal/db/models.go` gained the `NamespaceRole`
  bun model.
- `internal/server/server.go`: `newRoleStore(database)` picks the backend —
  Postgres when a database is configured, the in-memory store otherwise —
  so a deployment with a database keeps its role bindings across restarts.
- Tests: `internal/api/registry_rbac_test.go` — the full
  anonymous/unbound/reader/publisher/maintainer/owner/admin matrix for
  `CreateDraft` (plus `AuthorSub` recording), `UpdateDraft`/`DiscardDraft`/
  `DeleteDraft` (author passes without a role, non-author publisher denied,
  maintainer/owner/admin pass, unknown draft is `NotFound`), `PublishDraft`
  (an author whose publisher binding was revoked is still denied),
  `RevokeSigningKey` (maintainer denied, owner/admin allowed), the five
  unimplemented RPCs (each identity × each role, authorized callers still
  see `Unimplemented`), and the validation/existence-before-role order.
  `internal/authz/postgres_integration_test.go` (integration build tag) —
  grant/get/list/revoke round trip against a real Postgres, overwrite on
  re-grant, validation errors, hub/namespace scoping and sorted listing,
  `Check` over the persisted store, and nil-database fail-closed behavior.
  `internal/server/rolestore_test.go` — backend selection.
- Decisions: the author check on draft mutations is subject-equality against
  `draft.AuthorSub` and bypasses the role lookup entirely, so revoking a
  publisher's binding does not strand their own drafts; publishing a draft
  still requires the namespace's `publisher` role even for the author,
  because it writes to the shared catalog. `SetLandingVisibility` derives
  its namespace from the `asset_id` string (`<ns>/<kind>/<name>`, the same
  shape as the `/a/:ns/:kind/:name` web route) since the message carries no
  `AssetRef`.
- Checks: `gofumpt`, `golangci-lint`, `go vet`, `go test -race ./...`,
  `go test -tags integration -race ./...` (against the dev Postgres, after
  `migrate` applied 000002), `buf lint` clean; no new dependencies.
- Explicitly out of scope for M3.6: audit entries for the draft/yank/landing
  RPCs (M3.4's "not audited yet" list is unchanged), an RPC to grant/revoke
  role bindings (bindings are still written by `CreateNamespace` and
  operational tooling), scope (`agenthub.publish`/`agenthub.admin`) checks
  alongside roles, and caching role lookups.

### Slice 7 — M3.7 OpenTelemetry metrics/tracing + Prometheus endpoint (done)

- `internal/metrics/metrics.go`: `New` now takes
  `Options{Enabled, OTelEndpoint, SampleRatio}` instead of a bool. The meter
  provider is built from one or two readers — the existing Prometheus
  exporter when `observability.metrics_enabled` is on, plus a periodic
  OTLP/HTTP reader when an endpoint is configured — and falls back to a
  reader-less (no-op) provider when neither applies, so disabled metrics
  still export nothing.
- `internal/metrics/otel.go` (new): OTLP export is optional — the
  `observability.otel_endpoint` config first, the standard
  `OTEL_EXPORTER_OTLP_ENDPOINT` environment variable as fallback. With
  neither set there is no tracer provider at all (no-op tracing) and no OTLP
  metric export. The tracer provider batches spans to the collector with
  `ParentBased(TraceIDRatioBased)` sampling from
  `observability.otel_sample_ratio` (clamped to `[0,1]`, default `0.1`).
  The exporter's `WithEndpointURL` uses the path as-is, so the signal paths
  (`/v1/traces`, `/v1/metrics`) are appended here following the
  `OTEL_EXPORTER_OTLP_ENDPOINT` base-URL semantics.
- `internal/server/health.go`: `metricsMiddleware` records the counter and
  histogram as before and, when a tracer provider is configured, wraps the
  request in a server span named `<METHOD> <route template>` carrying
  `http.request.method`, `http.route` and `http.response.status_code`
  (semconv v1.43.0), with span status `Error` for 5xx. The route follows the
  registered template, not the concrete path — the same cardinality rule as
  the Prometheus labels (docs/architecture/operations.md §7.2).
- Scrape endpoint: unchanged — `cmd/agenthub-server` serves `m.Handler()` on
  the private `server.metrics_addr` (`:9100`) while
  `observability.metrics_enabled` is on; no `/metrics` route was added to
  the public listener.
- Tests: `internal/metrics/metrics_test.go` — Prometheus exposition with
  `agenthub_http_requests_total{route=...}`, the no-endpoint no-op, OTLP
  trace+metric export flushed to a fake collector (via
  `OTEL_EXPORTER_OTLP_ENDPOINT`) and the config-provided endpoint path.
  `internal/server/metrics_test.go` — one request produces one Prometheus
  sample and one server span (attributes asserted, `Error` status on a 5xx
  `HTTPError`).
- Checks: `gofumpt`, `golangci-lint` (v2.12.2 via `make lint`), `go vet`,
  `go test -race ./...`, `buf lint` all clean.
- New dependencies: `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp`
  and `go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp`
  (pulling `go.opentelemetry.io/proto/otlp`, `google.golang.org/grpc`,
  `github.com/cenkalti/backoff/v5`); `go.opentelemetry.io/otel/sdk` and
  `go.opentelemetry.io/otel/trace` promoted from indirect to direct.
- Explicitly out of scope for M3.7: gRPC/JSON OTLP protocols, resource
  detectors and `service.name`/`otel.resource.*` attributes, connect-RPC,
  DB and S3 span instrumentation, metrics exemplars, `X-Request-ID` being the
  trace ID, and remote-write/Prometheus scraping config.

### Slice 8 — M3.8 Kubernetes deploy manifests + probes (done)

- `deploy/k8s/namespace.yaml` (new `agenthub` namespace),
  `deployment.yaml` (2 replicas of `agenthub-server`, RollingUpdate
  `maxSurge=1`/`maxUnavailable=0` per operations.md §10.3), `service.yaml`
  (ClusterIP: `http` 8080 + private `metrics` 9100), `ingress.yaml`
  (`agenthub.pilab.hu`, TLS via the cert-manager `agenthub-tls` Secret,
  nginx annotations for 64 MiB bundle uploads and streaming timeouts) and
  `hpa.yaml` (`autoscaling/v2`, 2–6 replicas, CPU 70 % / memory 80 %,
  scale-down stabilization so a dip does not shed capacity).
- Probes map onto the existing endpoints (`internal/server/health.go`, no code
  change): liveness and startup hit `/healthz`, which returns a constant 200
  with no database involvement, so a DB outage never restarts pods; readiness
  hits `/readyz`, which pings the DB with a 2 s timeout and answers 503 until
  it is reachable, so traffic only reaches pods that can serve.
- Configuration: plain `AGENTHUB_*` env for the non-secret production values
  (`env: prod`, `public_url`, OIDC BFF issuer/client/redirect) plus
  `envFrom: secretRef agenthub-secrets` for the DSN and the BFF/Valkey/S3
  credentials; expected keys are documented in the Deployment header.
  Secrets stay out of the repo — provisioning (External Secrets/OpenBao) is
  out of scope for this slice.
- Security/ops hardening: distroless `runAsNonRoot` (uid 65532),
  `readOnlyRootFilesystem`, all capabilities dropped, `RuntimeDefault`
  seccomp, pod anti-affinity on `kubernetes.io/hostname`, `preStop` omitted
  on purpose (the image has no shell; SIGTERM is handled by
  `server.shutdown_grace`).
- Validation: `kubectl` could not reach a cluster from this environment, so
  the manifests were checked by parsing every document and asserting the
  contract (5 kinds, probe paths, `envFrom` secretRef, resources, TLS host,
  HPA bounds) instead of `kubectl apply --dry-run=server`.
- Explicitly out of scope for M3.8: the migration `Job` and rollout runbook
  execution, Secret/IngressClass/cert-manager installation, NetworkPolicy,
  PodDisruptionBudget, ServiceMonitor, kustomize/helm packaging, and pushing
  a real image (the tag `ghcr.io/pilab/agenthub-server:latest` is a
  placeholder until CI publishes images).

### Slices remaining

- [x] M3.1 Audit store (`internal/audit`, in-memory default + optional
      Postgres) and per-IP token bucket rate limiting middleware, configurable
      via `server.rate_limit_per_sec`.
- [x] M3.2 Namespace RBAC: `internal/authz` roles + in-memory role store,
      header-based auth interceptor (`X-Auth-Subject`/`X-Auth-Scopes`, echo
      context + request context), Registry enforcement (create namespace →
      creator becomes owner; signing-key registration → `publisher`), real
      caller roles in `GetNamespace`/`ListNamespaces`.
- [x] M3.3 Shadow SSO token validation: `internal/auth/sso` OIDC config +
      cached JWKS (discovery fallback) + RS256 JWT verification (iss/aud/exp,
      nbf/iat, sub, scopes), `SSOAuthenticator` behind `auth.issuer` with the
      header authenticator kept as the dev fallback, config validation.
- [x] M3.4 Audit wiring + `ListAudit`: registry/publish/draft/signing RPCs
      write audit entries with the caller as actor, one shared audit store
      between the rate limiter and the handlers, and `ListAudit` filtered by
      namespace with offset paging behind the `maintainer`/`owner` role.
- [x] M3.5 OIDC BFF routes: `/auth/login` (discovery + PKCE verifier and
      state/nonce in an HMAC-signed cookie), `/auth/callback` (form-encoded
      code exchange with `client_secret_post`, id_token + nonce validation,
      session cookie with subject and scopes) and `/auth/logout`
      (`end_session` redirect), with the session cookie as a fallback
      identity source and `auth.session_secret` validated in config.
- [x] M3.6 RBAC for the remaining Registry RPCs + persisted role bindings:
      draft lifecycle (create/publish → `publisher`, edit/discard/delete →
      author or `maintainer`), `SetLandingVisibility`/yank/unyank/deprecate →
      `maintainer`, `ValidateBundle` → `publisher`, `RevokeSigningKey` →
      `owner`, enforced before the unimplemented stubs, plus
      `authz.PostgresRoleStore` on the new `agenthub.namespace_roles` table
      (migration 000002) wired in behind `server.newRoleStore`.
- [x] M3.7 OpenTelemetry metrics/tracing + Prometheus endpoint: OTLP/HTTP
      export behind `observability.otel_endpoint`/`OTEL_EXPORTER_OTLP_ENDPOINT`
      (no-op without an endpoint), ratio-based sampling, HTTP server spans
      with semconv attributes recorded alongside the existing Prometheus
      instruments, and the scrape endpoint on the private metrics port.
- [x] M3.8 Kubernetes deploy manifests: `deploy/k8s/{namespace,deployment,
      service,ingress,hpa}.yaml` with `/healthz` liveness/startup and
      `/readyz` (DB ping) readiness probes, resource requests/limits,
      production env + `envFrom` secret, TLS ingress for
      `agenthub.pilab.hu` and a 2–6 replica HPA.
- [x] Deploy execution: image published to
      `ghcr.io/pilab-dev/agenthub-server:20261105-r2`, secrets provisioned
      (`DATABASE_DSN`, `SESSION_SECRET`, `SSO_CLIENT_ID`,
      `SSO_CLIENT_SECRET`), the migration Job completed, the `agenthub-web`
      OIDC client registered, and the HPA reports CPU and memory metrics.
- [ ] Remaining M3 work: audit coverage for the remaining RPCs and
      observability depth (DB/S3 spans, resource attributes). The storage/cache
      config keys are validated but have no server backend yet.

## M4 — Federate (done)

### Slice 1 — M4.1 Federation snapshot + sync (server-side) (done)

- `internal/api/federation.go`: the `Federation` handler moves out of
  `stubs.go` and is backed by the shared `catalog.Store` plus a hub Ed25519
  key (`NewFederationWithStore`; an absent/wrong-sized key generates an
  ephemeral one, `LoadHubKey` reads `signing.hub_key_path` as PKCS#8 PEM or
  as base64/hex of a 32-byte seed or 64-byte private key).
  - `GetSnapshot` renders the catalog as one ordered canonical change log
    (every non-draft version, ordered by namespace/kind/name/version; yanked
    versions become tombstones; namespace filter applied), returns
    `generation`, `pages`, `entryCount`, `tombstoneCount`, a content-derived
    `rootDigest` and an Ed25519 signature over the canonical snapshot JSON
    (`createdAt` is `signature.signed_at` rendered as RFC3339Nano, since the
    response carries no timestamp field of its own).
  - `SyncChanges` pages that log from an opaque hub-signed cursor
    (`<base64url payload>.<base64url signature>` holding generation, offset
    and the namespace fingerprint it was issued for). `limit` defaults to and
    is capped at the canonical 500 entries; `prevDigest`/`pageDigest` chain
    over the change-log prefix, so the chain is independent of the page size
    and a fully paged walk ends on `pageDigest == snapshot.rootDigest`.
    `include_yanked` repeats yanked versions in `entries` (status `yanked`)
    without moving any digest.
  - `generation` is a monotonic counter that advances when the digest of the
    *unfiltered* catalog changes, so peers with different namespace filters
    still agree on one counter. A tampered/foreign cursor is
    `InvalidArgument`; a cursor from another generation or another namespace
    selection is `FailedPrecondition` (re-bootstrap from `GetSnapshot`),
    which is the gap detection of `federation.md` §7.2. The counter lives in
    the handler and restarts with the process — a pre-restart cursor then
    looks stale, which is the safe direction.
  - `GetHubInfo` now returns `public_key`, and
    `/.well-known/agenthub.json` carries the same key base64-encoded so
    non-ConnectRPC peers can pin it.
- `internal/server/server.go`: the handler is wired with the catalog store
  (Postgres or in-memory) and `api.LoadHubKey(cfg.Signing.HubKeyPath)`; a
  configured but unreadable key fails startup.
- Tests: `internal/api/federation_test.go` (snapshot counts/pages/signature
  verification, generation advance, namespace filter, paged walk with
  prevDigest chain and final rootDigest equality, limit default + clamp,
  `include_yanked`, empty catalog, invalid/tampered/foreign cursor, stale
  generation, filter mismatch, concurrent snapshots under `-race`),
  `internal/api/hubkey_test.go` (every accepted key format plus rejections),
  `internal/server/server_test.go` (well-known publishes the key).
- Checks: `gofumpt`, `golangci-lint`, `go vet`, `go test -race ./...`,
  `buf lint` clean; no proto changes and no new dependencies.
- Deliberate deviations from `docs/architecture/federation.md` §4.2/§4.3
  (documented in the handler's package comment): `pageDigest`/`rootDigest`
  commit to the ordered change-log prefix instead of the page document /
  the concat of fixed-size pages, which keeps verification working for any
  page size, and canonical JSON is Go's `encoding/json` with HTML escaping
  off rather than RFC 8785 JCS.
- Explicitly out of scope for M4.1: authentication/authorization of the two
  RPCs (`agenthub.federation` token, `federation.public_read`), namespace
  visibility filtering (private namespaces are not hidden yet, the catalog
  API does not filter them either), `/.well-known/agenthub/keys.json`, the
  upstream stores and sync worker (M4.2), export/import (M4.3), persisted
  tombstone timestamps (`YankVersion` is still a stub, so a tombstone
  currently only comes from a version written as `yanked`), and change-log
  caching: the log is rebuilt and re-digested on every request (O(n) per
  call), which is fine for M4.1 scale but will need a persisted,
  incrementally maintained log before large catalogs sync efficiently.

### Slice 2 — M4.2 Federation upstream sync client (done)

- `internal/federation/federation.go`: `UpstreamClient` dials a remote hub
  and calls `GetHubInfo`, validating the self-description before anything
  else is read: protocol must be 1 (`ErrProtocol`), signing algorithm
  `ed25519` (`ErrUnsupportedAlgorithm`), the key 32 bytes
  (`ErrNoPublicKey`), and both match the pins when `WithPinnedKey` /
  `WithPinnedHubID` are set (`ErrKeyMismatch` / `ErrHubIDMismatch`),
  otherwise the key is trusted on first use. The verified identity carries
  the derived `KeyID` (`ed25519:<16 hex>`) and `Fingerprint`
  (`sha256:<64 hex>`, federation.md §6.2), is cached for later calls, and
  ships with `ParseFingerprint`/`ParsePublicKey`/`EncodePublicKey` helpers.
  The URL is normalized (adds `https://`, drops trailing slashes, rejects
  non-http(s) hosts).
- `internal/federation/verify.go`: the peer-side copy of the signed
  documents (snapshot, page, entry, tombstone, change log + the
  `canonicalJSON` rendering), re-derived independently of `internal/api`
  instead of importing the signer's code — federation peers implement both
  sides, and the interop tests below pin the two together.
  `verifySnapshot`/`verifyPage` re-render the document the hub signed
  (`createdAt`/`issuedAt` from `signature.signed_at`, `cursor` from the
  cursor the client sent) and check algorithm, key id and Ed25519
  signature; `changeLog` merges each verified page's entries and tombstones
  back into the hub's canonical order (two-pointer merge keyed on
  `namespace\x00ASSET_KIND_X\x00name\x00version`, parseable from the entry
  fields and the asset URN of a tombstone) so the root digest can be
  recomputed from the fetched pages.
- `internal/federation/sync.go`: `Snapshot` (verified descriptor) and
  `SyncChanges` (one verified page, the API a future incremental sync
  worker resumes from) plus `Sync`, the bootstrap walk of federation.md
  §7.1: every page signature verified, one consistent generation
  (`ErrGenerationGap`), the `prevDigest` chain, the final
  `pageDigest == snapshot.rootDigest`, the entry/tombstone counts and the
  recomputed root digest against the signed snapshot — any disagreement
  aborts the walk (`ErrDigest`) and nothing half-verified is returned.
  `collectSync` is that state machine with the transport injected, bounded
  by `MaxPages` (1000, federation.md §7.2) with a cursor-progress guard.
  The result is a `SyncResult` of verified assets and tombstones; storing
  them locally is deliberately not part of this slice.
- `internal/federation/config.go`: `agenthub.federation.json` —
  `ReadConfig` (missing file → empty, a future `schemaVersion` is an
  error), `WriteConfig` (indented, temp + rename like the other CLI state
  files) and `Config.Upsert`, which upserts by base URL (keeping the
  original `added_at`), rejects a name already taken by another hub and
  validates mode (`reference`/`mirror`/`proxy`), trust
  (`tofu`/`pinned`), fingerprint and public key.
- `cmd/agenthub/federate.go`: `agenthub federate add <hub-url>` (flags
  `--name`, `--mode`, `--fingerprint`, `--file`) fetches and verifies the
  hub identity, refuses to write a record when verification fails, records
  the upstream (trust on first use by default, printing the fingerprint and
  a warning; `--fingerprint` pins it) and prints the decision;
  `agenthub federate list` prints what is recorded. Federation trust and
  integrity failures (`ErrKeyMismatch`, `ErrHubIDMismatch`, `ErrSignature`,
  `ErrDigest`, `ErrNoPublicKey`, `ErrUnsupportedAlgorithm`) now map to exit
  code 5 in `cmd/agenthub/main.go`.
- Tests: `internal/federation/client_test.go` (an httptest server running
  the real `api.Federation` handler: full bootstrap with tombstone/entry
  interleaving, a paged walk that exercises the `prevDigest` chain and the
  cross-page merge, empty catalog, a tampered snapshot, a foreign signer,
  pinned key/hub-id mismatch, bad self-descriptions, a manual
  `Snapshot`+`SyncChanges` walk, concurrent syncs under `-race`, plus URL
  and fingerprint helpers), `internal/federation/sync_test.go`
  (hand-signed scripted pages for the failures a live hub cannot produce on
  demand: a page signed for another cursor, a broken chain, a generation
  gap, lying counts, a wrong root, an entry and a tombstone for the same
  version, a stuck cursor, an endless walk bounded at `MaxPages`),
  `internal/federation/config_test.go` (file round-trip, future schema,
  upsert/validation, modes, public key round-trip),
  `cmd/agenthub/federate_test.go` (add/list/idempotent re-add, pinned vs
  TOFU trust, wrong fingerprint rejected with no file written and exit code
  5, unknown mode).
- Checks: `gofumpt`, `golangci-lint`, `go vet`, `go test -race ./...`,
  `buf lint` clean; no proto changes and no new dependencies.
- Drive-by fix for a pre-existing flake found while running the suite:
  `api.parseHubKey` no longer trims the raw file content before parsing a
  binary key — a random seed/private key whose first or last byte is
  whitespace used to be silently shortened to 31/63 bytes
  (`TestLoadHubKeyEncodings` failed ~5% of runs); `hubkey_test.go` gained a
  deterministic regression case.
- Explicitly out of scope for M4.2: persisting the synced catalog and the
  upstream state (the sync worker itself; `ListUpstreams`, `UpsertUpstream`,
  `DeleteUpstream` and `TriggerSync` stay `Unimplemented`), blob mirroring,
  namespace visibility filtering, and the periodic scheduler
  (federation.md §7.5).

### Slice 3 — M4.3 ExportBundle/ImportBundle (air-gapped transfer) (done)

- `api/proto/agenthub/federation/v1/federation.proto`: the byte-level half of
  the transfer gets a self-describing framing (`gen/go` and `gen/ts`
  regenerated; `buf lint` and `buf breaking` clean):
  - `ExportBundleRequest` gains `ref` (field 2) next to `bundle_digest`: a ref
    with an empty version resolves the asset's latest, and when both
    selectors are set they must agree.
  - `BundleHeader` opens a stream: `ref` (`hub` is the exporting hub), the
    manifest bytes with `manifest_digest`, `bundle_digest`/`bundle_size`,
    status, `published_at`, the asset projection (display name, summary,
    tags, categories, license, repository), readme/changelog, and one
    `BundleFile` (`common.v1.FileDescriptor` + `has_bytes`) per declared
    file.
  - `BundleFileChunk{path, offset}` names the bytes carried in the enclosing
    message's `data`; `ExportBundleResponse` and `ImportBundleRequest` hold
    `data` plus `oneof part { BundleHeader header; BundleFileChunk file; }`,
    so every message is self-describing and a stream needs no state machine
    on the receiving side.
- `internal/api/federation_bundle.go`: both handlers.
  - `ExportBundle` resolves the version (by ref → asset → version, an empty
    version meaning the asset's latest; or by bundle digest in the catalog's
    asset order; drafts are not federated, so they are `NotFound` like an
    unknown version), re-verifies `sha256(manifest)` against the recorded
    `manifest_digest` (`CodeInternal` when the stored bytes no longer match),
    turns the manifest's `files` list into descriptors
    (`CodeFailedPrecondition` when a declared file has no path or no usable
    sha256) and looks each descriptor up in the blob store. A held blob is
    re-hashed before it is declared `has_bytes` (`CodeInternal` on mismatch)
    and streamed in 64 KiB messages tagged with path and offset; a file the
    hub does not hold travels as a descriptor only — no publish flow uploads
    file content, so on an origin hub that is the normal case.
  - `ImportBundle` validates as it reads: exactly one header first (no data
    beside it), a ref with namespace/kind/name/version, an exportable status
    (published/deprecated/yanked; draft/unspecified are `InvalidArgument`), a
    manifest of at most 4 MiB that hashes to its `manifest_digest`, a
    `bundle_digest` shaped like sha256 when present, a valid `published_at`,
    at most 32768 files, and declared file bytes within the 64 MiB
    `maxBundleBytes` (`CodeResourceExhausted` past the limits). The
    descriptor set must agree with the manifest's `files` on paths and
    sha256. Chunks must target a declared file that has bytes, arrive
    contiguously from offset 0, never grow past the declared size, and the
    stream must end with every such file complete and hashing to its
    descriptor — otherwise `CodeInvalidArgument` and nothing is written.
    After verification the blobs are filed content-addressed and the asset
    and version are upserted; re-importing the same bundle is idempotent
    (the asset keeps its `created_at`). The response echoes `bundle_digest`
    and reports `bytes_written` as manifest bytes plus stored file bytes.
- `internal/catalog/blobs.go`: `BlobStore` (`Put` refuses bytes that do not
  hash to their digest, `Get` returns a copy) with the in-memory `MemBlobs`,
  keyed by the normalized `sha256:<64 hex>` of `BlobKey`, plus the
  `ErrInvalidDigest`/`ErrDigestMismatch` sentinels. `Federation` takes it
  through the new `NewFederationWithBlobs`; `NewFederationWithStore` keeps
  its signature and defaults to a private in-memory store.
- Tests: `internal/api/federation_bundle_test.go` (full export → import round
  trip over httptest — a 153,600-byte file in three chunks plus a
  descriptor-only file, header metadata and catalog/blob state on the
  receiving side; bundles whose manifest declares no files; a selector table
  covering ref/latest/digest/both, bad refs, unknown and draft targets and a
  yanked version; corrupt stored data; the import rejection tables for header
  shape, limits, digests, chunk order/truncation and manifest disagreement;
  a missing bundle digest; idempotent re-import; concurrent export and import
  of the same version under `-race`), `internal/catalog/blobs_test.go` (key
  normalization and rejections, put/get with copy independence, digest
  mismatch, concurrent use).
- Checks: `gofumpt`, `golangci-lint`, `go vet`, `go test -race ./...`,
  `buf lint` and `buf breaking` clean; no new dependencies.
- Deliberate deviations from `docs/architecture/federation.md` §9: the RPC
  stream is a framing over the existing `data` byte field, not the tar.zst
  archive of §9.1 — `bundle_digest = sha256(tar.zst bytes)` exists on neither
  side of an RPC hop, so it is carried through and validated for shape only
  (F3, "the bundle digest survives the hop", still holds), and the transfer
  carries exactly one version per call rather than an archive of many.
- Explicitly out of scope for M4.3: serving the stored bytes
  (`Catalog.GetBundle` stays `Unimplemented` — `MemBlobs` is the map it will
  read, reachable with `NewFederationWithBlobs` sharing one instance), a
  persistent blob backend, manifest signature verification, `originHub`
  marking of mirrored versions (the catalog store has no origin column yet;
  the exporting hub id travels in `ref.hub` but is not persisted),
  `LatestVer` selection across out-of-order imports (the store has no version
  comparator, so — like `CommitPublish` — the imported version becomes the
  latest), the `import.completed`/`import.rejected` audit events of
  federation.md §13 (the Federation handler has no audit plumbing), the
  archive-based `agenthub export/import` CLI of §9.1/§9.3, selective and
  delta export (§9.4), and authorization of the two RPCs (the open
  federation-token question of M4.1).

### Slice 4 — M4.4 `agenthub export` / `agenthub import` (air-gap CLI) (done)

- `cmd/agenthub/transfer.go`: both commands, registered in `newRoot`.
  - `export <asset-ref> --hub <url> [-o file]` parses the ref with the
    existing `parseAssetRef` (an empty version selects the asset's latest
    published version), dials `Federation.ExportBundle` and writes the stream
    to a JSON-lines archive: one message per line, protojson-encoded, so
    line 1 is the `BundleHeader` and every later line is a file-chunk record
    whose bytes travel base64-encoded in `data`. `-o -` (the default) writes
    the archive to stdout; a path writes it to a temporary file in the target
    directory that is renamed only on success, so a failed export never
    leaves a truncated archive behind to be carried across the air gap.
    Progress always goes to stderr, so stdout stays a clean archive.
  - `import <file> --hub <url>` (`-` reads stdin) decodes those records back
    into `ImportBundleRequest`s and forwards them to
    `Federation.ImportBundle` as it reads, so only the hub buffers the
    bundle. The archive shape is checked before anything is sent — the first
    record must be the header, a second header and a record that is neither
    header nor chunk are rejected — and lines are capped at 2 MiB (the
    1 MiB chunk limit of M4.3, base64-encoded). A hub that rejects the
    stream mid-send surfaces its own error, since `Send` reports that as
    `io.EOF` and the real error only comes back from `CloseAndReceive`.
  - `federationClient` builds the Connect client without a whole-request
    timeout (a slow link must not cut a 64 MiB transfer short; the default
    transport still bounds the dial) and `resolveHub` fills the hub from
    `--hub`, the config or the default, like the other commands.
- Tests: `cmd/agenthub/transfer_test.go` — a full export → archive → import
  round trip between two `httptest` hubs running the real `Federation`
  handler (a 156,000-byte file over several chunks plus a descriptor-only
  file; catalog metadata, manifest and blob bytes on the air-gapped side,
  the descriptor-only file stays without bytes), the stdout/stdin pipe path
  with the versionless ref selecting the latest, a chunk-first archive
  rejected locally with nothing stored, and an archive cut down to its
  header rejected by the hub with `CodeInvalidArgument` and nothing stored.
- Checks: `gofumpt`, `golangci-lint`, `go vet`, `go test -race ./...`,
  `buf lint` clean; no new dependencies.
- Deliberate deviations from `docs/architecture/federation.md` §9 and the
  "Federation and offline transfer" block of `docs/architecture/cli-plugin.md`:
  the archive is the JSON-lines export stream of M4.3 — one version per
  call — not the signed multi-version `tar.zst` of §9.1, so it carries no
  `export.json`, no archive signature, no `--trust-key`/`--verify` of §9.2,
  and the selector is a ref rather than `--namespace`.
- Explicitly out of scope for M4.4: multi-version, selective and delta
  export (§9.4), archive signatures and trust keys (§9.1/§9.2), `--mode
  mirror` metadata (`originHub` marking, blocked on the catalog schema of
  M4.3), the `import.completed`/`import.rejected` audit events (§13), and
  authorization of the two RPCs (the open federation-token question of
  M4.1).

### Slice 5 — M4.5 Upstream store + `agenthub sync` (done)

- `internal/federation/store.go`: `UpstreamStore` interface + `MemUpstreamStore` (concurrency-safe, keyed by hub+ref).
- `cmd/agenthub/sync.go`: `sync` command reads `agenthub.federation.json`, syncs each upstream via `UpstreamClient.Sync`, stores assets, writes config back.
- `cmd/agenthub/main.go`: registered `sync` command.
- `ROADMAP.md`: M4.5 slice.

Checks: `gofumpt`, `golangci-lint` (0 issues), `go vet`, `go test -race`, `buf lint` all pass.

## M5 — Ecosystem (in progress)

### Slice 1 — M5.1 Portable materialization (done)

- `cmd/agenthub/materialize.go`: `materialize` command writes each locked asset into `.opencode/` (portable mode) with `asset.json` metadata.
- Registered in `cmd/agenthub/main.go`.

Checks: `gofumpt`, `golangci-lint` (0 issues), `go vet`, `go test -race`, `buf lint` all pass.

### Slice 2 — `agenthub add` (done)

- `cmd/agenthub/update.go`: `add` upserts the desired entry, runs `update`, then `install` (unless `--no-install`); a failed resolve restores `agenthub.json`.
- `cmd/agenthub/main_test.go`: add/replace/rollback/bad-ref coverage.

### Slice 3 — `agenthub install` wizard (done)

- `cmd/agenthub/harness.go`: claude/opencode/copilot/cursor/codex writers, drift-aware apply, state file.
- `cmd/agenthub/wizard.go`: bubbletea wizard (targets, location, confirm).
- `cmd/agenthub/install_cmd.go`: `-t/-l/-y/--print-config/--refresh/--force`; fetch step runs first.

### Slice 4 — integration suite (done)

- `test/integration/`: builds the real `agenthub` binary and runs it against an in-process hub (`server.New` over HTTP): publish, blob upload/download, `GetBundle`, `add`/`install`/`update`/`remove`, all five harnesses, drift/`--force`, `--refresh`, offline/frozen, exit codes.
- Server: `PUT/GET /blobs/<sha256>`, upload targets from `BeginPublish`, `GetBundle`, semver-constraint `Resolve`.
- Fixed: `BeginPublish`/`CommitPublish` had no role check; the catalog page crashed on `.Verified`.
- Known gap: the `/ns/<slug>` page does not list the namespace's assets.

### Remaining M5 work (out of scope for this session)

- M5.2: ratings/recommendations (no proto surface yet).
- M5.3: Sigstore keyless signing (requires OIDC keyless + Rekor).
