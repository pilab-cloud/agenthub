# AgentHub

**AgentHub** is the package registry and control plane for AI-agent assets: agents,
skills, tools, prompts, and packs. It publishes them with immutable versions,
install guides, permission manifests, and signatures; the CLI and the OpenCode V2
plugin install and run them from any machine.

- Public hub: `https://agenthub.pilab.hu`
- Go module: `go.pilab.hu/agenthub`
- OpenCode plugin: `@pilab/opencode-agenthub`
- CLI binary: `agenthub`

> **Status:** Milestones M0–M4 and the first M5 slice are implemented (see
> [ROADMAP.md](ROADMAP.md)): registry, catalog, publish/sign, federation, auth
> and the CLI (`install`, `update`, `remove`, `export`, `import`, `federate`,
> `sync`, `materialize`). Stores are in-memory; `GetBundle`, `GetInstallGuide`,
> `WatchCatalog` and the yank/deprecate RPCs are still unimplemented.

## What AgentHub is

| Plane | Responsibility |
| --- | --- |
| Catalog | Search, browse, resolve, and download versioned agent assets |
| Publishing | Validate, sign, publish, deprecate, and yank versions |
| Consumption | CLI + OpenCode V2 plugin reconcile desired state into local runtime state |
| Web | Landing page, store, dashboard, and web-based agent creation |
| Federation | Mirror or reference assets from other hubs while preserving origin and signatures |

It is **not** an execution runtime. Scheduling agent runs stays with the PiR@
orchestrator; AgentHub only stores and distributes the assets those runs consume.
It is also **not** a model provider, a Git host, or a secrets manager.

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

## Ten-second version

```bash
# install the CLI
go install go.pilab.hu/agenthub/cmd/agenthub@latest

# authenticate with Shadow SSO (OIDC device flow)
agenthub login

# add a version-pinned agent pack to the current project
agenthub add pilab/pack/pirat-agents@1.0.0

# the OpenCode V2 plugin reads agenthub.lock.json and registers the assets
opencode
```

The lockfile pins the exact version, origin hub, manifest digest, and bundle
digest. Installs are reproducible, offline-capable after the first fetch, and
verified before they reach OpenCode.

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
