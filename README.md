# AgentHub

**AgentHub** is the package registry and control plane for AI-agent assets: agents,
skills, tools, prompts, and packs. It publishes them with immutable versions,
install guides, permission manifests, and signatures; the CLI and the OpenCode V2
plugin install and run them from any machine.

- Public hub: `https://agenthub.pilab.hu`
- Go module: `go.pilab.hu/agenthub`
- OpenCode plugin: `@pilab/opencode-agenthub`
- CLI binary: `agenthub`

> **Status:** milestones M0–M4 and the first M5 slices are implemented (see
> [ROADMAP.md](ROADMAP.md)). The registry, catalog, publish/sign flow, federation,
> auth, blob storage and the CLI all work end to end and are covered by an
> integration suite. Stores are still **in-memory** (Postgres/S3 are wired but
> not the default), and there is no `agenthub publish` / `login` command yet:
> publishing goes through the Connect API (see [Publishing](#publishing-an-asset)).
> Not yet implemented: `GetInstallGuide`, `WatchCatalog`, yank/deprecate RPCs.

## The problem

Teams that use AI coding agents end up with the same mess everywhere:

- Agent prompts, skills and slash-commands are **copy-pasted** between repos and
  laptops, and silently drift apart.
- Every tool wants them somewhere else: `.claude/agents`, `.opencode/agents`,
  `.cursor/rules`, `AGENTS.md`, `.github/agents`, ...
- There is **no versioning, no provenance and no review**: you cannot tell which
  version of a prompt produced a result, or whether the file you run is the one
  someone approved.
- Sharing across teams or companies means "send me the file".

## What AgentHub does

AgentHub is a **package registry for AI-agent assets**: what npm/crates.io are to
libraries, but for agents, skills, tools, prompts and packs.

- **Publish once** an immutable, versioned, content-addressed bundle.
- **Pin it** in `agenthub.lock.json` (exact version, manifest digest, bundle
  digest) so every machine and CI run gets byte-identical assets.
- **Install it into any agent harness** with one command: Claude Code, opencode,
  GitHub Copilot, Cursor and Codex CLI each get the files in their own layout.
- **Verify** digests (and publisher signatures) before anything touches disk.
- **Federate** between hubs (mirror, proxy, reference) and move assets across
  air gaps with export/import archives.

It is **not** an execution runtime (scheduling runs stays with the PiR@
orchestrator), a model provider, a Git host or a secrets manager.

| Plane | Responsibility |
| --- | --- |
| Catalog | Search, browse, resolve and download versioned agent assets |
| Publishing | Validate, sign, publish; namespaces with RBAC and audit |
| Consumption | CLI reconciles `agenthub.json` into the lockfile, cache and agent harnesses |
| Web | Landing page, catalog, asset and version pages |
| Federation | Mirror or reference assets from other hubs, preserving origin and signatures |

## How it works

```text
 publisher                          hub                               consumer
 ─────────                          ───                               ────────
 manifest.json + files   ──►  BeginPublish  (namespace role check)
 PUT /blobs/<sha256>     ──►  content-addressed blob store
 CommitPublish           ──►  immutable version  ─────────────►  agenthub add ns/kind/name
                                                                  │  Resolve   → agenthub.lock.json
                                                                  │  GetBundle → ~/.cache/agenthub (digest-verified)
                                                                  ▼
                                                           agenthub install
                                                           └─► .claude/  .opencode/  .github/  .cursor/  AGENTS.md
```

Three files describe a project:

| File | Written by | Purpose |
| --- | --- | --- |
| `agenthub.json` | you / `agenthub add` | **Desired** assets and version constraints (`pilab/agent/reviewer@^1`) |
| `agenthub.lock.json` | `add` / `update` | **Locked** exact versions and digests; commit it |
| `.agenthub/state.json` | `install` | Which files agenthub wrote (hash per file) so it can detect your edits |

Asset kinds: `agent`, `skill`, `tool`, `prompt`, `pack`. A reference is
`namespace/kind/name[@version-or-constraint]`, e.g. `pilab/agent/reviewer@^1.2`.

## Quick start (local playground)

```bash
# 1. Build and start a hub on 127.0.0.1:8080 (in-memory, no SSO, no database)
make build
./bin/agenthub-server serve --config deploy/dev/config.local.yaml

# 2. Publish an asset (see below), then in any project directory:
export AGENTHUB_HUB=http://127.0.0.1:8080
agenthub add acme/agent/reviewer         # resolve + lock + download into the cache
agenthub install                         # wizard: pick agents and global/local
agenthub install -y                      # or non-interactive: detected agents, global
agenthub install -t claude,cursor -l local
```

`deploy/dev/config.yaml` is the same config with Shadow SSO enabled; use it
against a real identity provider. `deploy/dev/config.local.yaml` trusts the
`X-Auth-Subject` header and must never be exposed publicly.

## Publishing an asset

An asset is a **manifest** plus the **files** it lists. Each file is identified
by its sha256; the manifest is identified by its own sha256.

**1. Write the files and the manifest**

```bash
printf 'You are a meticulous code reviewer.\n' > prompt.md
P=$(shasum -a 256 prompt.md | cut -d' ' -f1)
SZ=$(wc -c < prompt.md | tr -d ' ')
cat > manifest.json <<EOF
{"kind":"agent","namespace":"acme","name":"reviewer","version":"1.0.0",
 "summary":"Reviews pull requests",
 "files":[{"path":"prompt.md","sha256":"sha256:$P","size":$SZ}],
 "spec":{"agent":{"prompt":"prompt.md"}}}
EOF
M=$(shasum -a 256 manifest.json | cut -d' ' -f1)
```

Spec by kind: `agent.prompt` (+ optional `mode`), `skill.entry` (usually
`SKILL.md`, copied verbatim), `prompt.template` (+ `target`).

**2. Create the namespace, begin, upload, commit** (Connect JSON over HTTP)

```bash
H=http://127.0.0.1:8080; R=$H/agenthub.registry.v1.RegistryService
A='X-Auth-Subject: alice'; J='Content-Type: application/json'   # dev auth; real hubs use a Shadow SSO bearer token

curl -s -H "$A" -H "$J" -d '{"slug":"acme"}' $R/CreateNamespace        # creator becomes owner

B=$(curl -s -H "$A" -H "$J" $R/BeginPublish -d "{
  \"namespace\":\"acme\",\"kind\":\"ASSET_KIND_AGENT\",\"name\":\"reviewer\",\"version\":\"1.0.0\",
  \"bundleDigest\":{\"algorithm\":\"sha256\",\"value\":\"$M\"},
  \"files\":[{\"path\":\"prompt.md\",\"sha256\":\"$P\",\"size\":$SZ}]}")
ID=$(echo "$B" | jq -r .publishId); URL=$(echo "$B" | jq -r '.uploads[0].url')

curl -s -X PUT -H "$A" --data-binary @prompt.md "$URL"                 # one PUT per upload target

curl -s -H "$A" -H "$J" $R/CommitPublish -d "{
  \"publishId\":\"$ID\",\"manifest\":\"$(base64 < manifest.json | tr -d '\n')\",
  \"manifestDigest\":{\"algorithm\":\"sha256\",\"value\":\"$M\"}}"
```

Rules enforced by the hub:

- `BeginPublish` and `CommitPublish` need the **publisher** role in the namespace.
- Uploads need an authenticated caller; the hub rejects bytes whose sha256 does
  not match the digest in the URL.
- Versions are **immutable**: re-committing the same manifest is idempotent,
  committing a different manifest for the same version is rejected.
- Blobs already on the hub are not requested again (no upload target returned).

## Using assets

```bash
agenthub add acme/agent/reviewer@^1     # record constraint, resolve, lock, download
agenthub update                         # re-resolve to the newest matching versions
agenthub remove acme/agent/reviewer     # drop from agenthub.json and the lockfile
agenthub install --frozen --offline     # CI: fail unless the cache already has everything
```

Constraints are semver (`*`, `^2`, `>=1.2 <2`, or an exact version). Yanked
versions are never selected by a constraint.

### `agenthub install`

First fetches and verifies every locked asset into the content-addressed cache,
then writes each chosen agent's own files:

| Agent (`-t`) | Local | Global |
| --- | --- | --- |
| `claude` | `.claude/{agents,skills,commands}` | `~/.claude/` |
| `opencode` | `.opencode/` | `~/.config/opencode/` |
| `copilot` | `.github/{agents,skills,prompts}` | `~/.copilot/` |
| `cursor` | `.cursor/rules/*.mdc` | not supported |
| `codex` | `AGENTS.md` (managed section) | `~/.codex/AGENTS.md` |

- **Wizard:** in a terminal without `-t`/`-l`, a TUI asks which agents and where.
- **Safe re-runs:** agenthub remembers what it wrote. A file you edited is
  reported as `drift` and left alone until you pass `--force`; files from assets
  you removed are pruned.
- **`--print-config <agent>`** shows what would be written, without writing.
- **`--refresh`** rewrites only agents configured by an earlier install.
- Without a terminal and without `-t`, assets are only cached.

Exit codes: `4` resolution failed, `5` verification failed (digest/signature),
`6` offline and incomplete. Full flag list: [CLI reference](docs/reference/cli-reference.md).

## Security model

- Everything is content-addressed; the CLI verifies each blob's sha256 against the
  manifest and the manifest against the lockfile before use.
- Publisher signatures (ed25519) can be required per hub
  (`signing.require_publisher_signature`).
- Namespace RBAC (owner / maintainer / publisher) with an audit log; Shadow SSO
  (OIDC) in production.
- Federation snapshots are signed with the hub key (`/.well-known/agenthub.json`).

## Development

```bash
make check                     # fmt, golangci-lint, vet, buf lint/breaking
make test                      # go test -race ./...
go test ./test/integration/    # end-to-end: real CLI binary vs. in-process hub
make smoke                     # start the server and probe /healthz
```

The integration suite (`test/integration/`) builds the `agenthub` binary and runs
publish → search → add → install → update → remove, all five harnesses,
drift/`--force`, offline/frozen and exit codes against a real HTTP hub.

## Architecture documents

| Document | Contents |
| --- | --- |
| [ARCHITECTURE.md](ARCHITECTURE.md) | System overview, principles, flows, decisions, roadmap |
| [docs/architecture/domain-model.md](docs/architecture/domain-model.md) | Asset kinds, manifest, lockfile, permission model, database schema |
| [docs/architecture/api.md](docs/architecture/api.md) | ConnectRPC/gRPC surface, HTTP endpoints, auth and authorization |
| [docs/architecture/web-and-sso.md](docs/architecture/web-and-sso.md) | Landing page cards, web agent creation, Shadow SSO clients and Kubernetes deployment |
| [docs/architecture/cli-plugin.md](docs/architecture/cli-plugin.md) | CLI UX, install algorithm, OpenCode V2 plugin lifecycle |
| [docs/architecture/federation.md](docs/architecture/federation.md) | Hub identity, signed catalogs, mirror/proxy/reference modes |
| [docs/architecture/operations.md](docs/architecture/operations.md) | Deployment, security, observability, testing, runbooks |
| [docs/architecture/importing-existing-agents.md](docs/architecture/importing-existing-agents.md) | How `pirat-backend` and `pimail` map into the hub |

## Stack at a glance

| Concern | Choice |
| --- | --- |
| Server | Go 1.27, Echo, ConnectRPC (gRPC-compatible), buf |
| Metadata | PostgreSQL 16+ (Bun ORM, goose migrations) |
| Blobs | S3-compatible object store (Silo/MinIO), content-addressed |
| Cache / sessions | Valkey |
| Events / sync jobs | NATS JetStream (M4; optional at MVP) |
| Search | PostgreSQL full-text search first; Meilisearch adapter later |
| Web | Go `html/template` + Tailwind + HTMX; SSR landing/store/dashboard with BFF sessions |
| CLI | Go, Cobra, Shadow SSO device flow, OS keychain |
| Plugin | TypeScript, OpenCode V2 `@opencode/plugin` |
| Identity | Shadow SSO only (OIDC, device flow, service accounts, JWKS) |
| Observability | zerolog, OpenTelemetry, Prometheus |

## Repository layout (target)

```text
agenthub/
├── api/proto/agenthub/           # buf modules: common, catalog, registry, federation
├── buf.yaml
├── buf.gen.yaml
├── cmd/
│   ├── agenthub/                 # CLI
│   └── agenthub-server/          # server
├── gen/go/                       # generated Go
├── gen/ts/                       # generated TypeScript (plugin)
├── internal/
│   ├── domain/                   # entities, invariants, policy
│   ├── ports/                    # repository/blob/search/event interfaces
│   ├── app/                      # use cases: catalog, registry, federation
│   ├── infrastructure/           # postgres, s3, valkey, nats, oidc, signing
│   ├── api/                      # Connect handlers, auth interceptors
│   └── web/                      # landing page + store (Go templates)
├── pkg/
│   ├── manifest/                 # manifest/bundle schemas + validation
│   ├── lockfile/
│   └── semver/
├── clients/opencode-plugin/      # @pilab/opencode-agenthub (Bun workspace)
├── migrations/
├── deploy/
└── docs/architecture/
```

## Next implementation milestones

1. **M0 — skeleton:** module, buf, migrations, health endpoints, CI.
2. **M1 — publish/read:** namespaces, signed bundles, catalog API, store pages, landing agent cards.
3. **M2 — consume:** CLI resolve/install/lock, OpenCode V2 plugin registration.
4. **M3 — operate:** Shadow SSO BFF, web agent creation, RBAC, audit, observability, deploy.
5. **M4 — federate:** upstream sync, mirror/proxy modes, offline export/import.
6. **M5 — ecosystem:** additional harness adapters, Sigstore, recommendations.

See [ARCHITECTURE.md](ARCHITECTURE.md) for the full plan.
