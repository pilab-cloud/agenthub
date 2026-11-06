# Quickstart

Welcome to **AgentHub** — the package registry and control plane for AI-agent assets.

## In Ten Seconds

Get the CLI, authenticate, and add your first agent pack:

```bash
# 1. Install the AgentHub CLI
go install go.pilab.hu/agenthub/cmd/agenthub@latest

# 2. Authenticate with Shadow SSO (OIDC device flow)
agenthub login

# 3. Add a version-pinned agent pack to your project
agenthub add pilab/pack/pirat-agents@1.0.0

# 4. The OpenCode V2 plugin reads agenthub.lock.json and registers the assets
#    (or run `agenthub install` to install into a project)
```

## What Is AgentHub?

| Plane | Responsibility |
| --- | --- |
| **Catalog** | Search, browse, resolve, and download versioned agent assets |
| **Publishing** | Validate, sign, publish, deprecate, and yank versions |
| **Consumption** | CLI + OpenCode V2 plugin reconcile desired state into local runtime state |
| **Web** | Landing page, store, dashboard, and web-based agent creation |
| **Federation** | Mirror or reference assets from other hubs while preserving origin and signatures |

**AgentHub is not:** an execution runtime, a model provider, a Git host, or a secrets manager. It stores and distributes assets that scheduling runtimes (like PiR@) consume.

## Installation

### Prerequisites

- Go 1.27+ 
- PostgreSQL 16+ (for the server backend)
- Valkey (for cache/sessions)
- NATS JetStream (for sync jobs, optional at MVP)
- SSH access for Docker builds

### Install the CLI

```bash
go install go.pilab.hu/agenthub/cmd/agenthub@latest
```

This installs the `agenthub` binary to `~/go/bin/agenthub` (or `$GOPATH/bin/agenthub`).

### Add the CLI to Your PATH

```bash
export PATH="$PATH:~/go/bin"
```

Verify the installation:

```bash
agenthub --version
```

### Docker Deployment

```bash
# Build the Docker image
make docker

# Or build manually
docker build -t agenthub ./

# Run with environment config
docker run -d \
  -p 8080:8080 \
  -p 9090:9090 \
  -p 9100:9100 \
  -v /path/to/agenthub-secrets:/etc/agenthub/secrets \
  -v /path/to/postgres/data:/var/lib/postgresql/data \
  agenthub:latest
```

## Authentication (Shadow SSO)

AgentHub uses **Shadow SSO** (OIDC) for authentication. The flow is:

1. `agenthub login` — opens a device code flow
2. Visit the URL shown, enter the code
3. SSO authenticates you and redirects back
4. A session cookie is set, and the CLI remembers your identity

```bash
agenthub login
# Follow the device code flow instructions
```

### SSO Configuration

The server expects Shadow SSO at `https://sso.pilab.hu`. Configuration is in `deploy/dev/config.yaml`:

```yaml
auth:
  issuer: "https://sso.pilab.hu"
  audience: agenthub
  client_id: "agenthub-web"
  session_secret: "change-me-32-chars-minimum"
  scopes: ["openid", "profile", "email", "agenthub.read", "agenthub.publish"]
```

## CLI Reference

### Core Commands

| Command | Description |
| --- | --- |
| `agenthub add <asset-ref>` | Resolve and add a desired asset to `agenthub.json` |
| `agenthub install` | Install assets pinned in `agenthub.lock.json` |
| `agenthub remove <ref>` | Remove an asset from `agenthub.json` and prune the lockfile |
| `agenthub sync` | Sync upstream hubs from `agenthub.federation.json` |
| `agenthub update` | Re-resolve desired assets and rewrite the lockfile |
| `agenthub federate add <hub>` | Add a federated upstream hub |
| `agenthub federate remove <name>` | Remove a federated hub |
| `agenthub publish` | Publish a local agent pack to the hub |
| `agenthub import <archive>` | Import a transfer archive into the hub |
| `agenthub gc` | Garbage collect unused assets and versions |

### Asset Management

```bash
# Add an agent pack (pinned to a version)
agenthub add ncis/agent/gibbs@1.0.0

# Add with a version constraint
agenthub add ncis/agent/gibbs@^0.1.0

# Add from a remote hub
agenthub add pilab/pack/my-pack@1.0.0

# List desired assets
cat agenthub.json

# List locked (resolved) assets
cat agenthub.lock.json
```

### Federation

```bash
# Add a federated hub
agenthub federate add --name pilab https://hub.pilab.hu:9443

# List federated hubs
agenthub federate list

# Sync from federated hubs
agenthub sync

# Remove a federated hub
agenthub federate remove pilab
```

### Migration

```bash
# Run database migrations (up)
agenthub migrate

# Check migration status
agenthub migrate status

# Run migrations down
agenthub migrate down
```

### Server Management

```bash
# Start the server with dev config
agenthub serve --config deploy/dev/config.yaml

# Check server health
agenthub healthz

# View server metrics
agenthub metrics
```

### Code Example: Adding an Agent Programmatically

```go
import "go.pilab.hu/agenthub/cmd/agenthub"

// Or via the library:
// client := catalogv1connect.NewCatalogServiceClient(http.DefaultClient, hub)
// resp, err := client.Resolve(ctx, connect.NewRequest(&catalogv1.ResolveRequest{
//     Desired: []*catalogv1.DesiredAsset{{Ref: ref},
//     })),
}
```

### CI/CD Integration

```yaml
# .github/workflows/agenthub.yml
name: AgentHub CI

on:
  push:
    branches: [main]
  pull_request:

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: '1.27'
      - name: Install deps
        run: go mod tidy
      - name: Run tests
        run: go test ./...
      - name: Build CLI
        run: go build -o agenthub ./cmd/agenthub
      - name: Publish
        run: agenthub publish
```

## Directory Structure

```text
agenthub/
├── agenthub.json        # Desired state (CLI)
├── agenthub.lock.json   # Resolved, pinned state
├── agenthub.federation.json  # Federated upstreams
├── migrations/          # SQL goose migrations
├── deploy/              # Kubernetes manifests
├── internal/            # Server code (internal packages)
├── api/                 # Protobuf definitions
├── gen/go/              # GENERATED protobuf/gRPC Go code
├── cmd/                 # CLI entry point
├── web/                 # Web frontend (HTML + Tailwind)
├── go.mod               # Module definition
├── Makefile             # Build targets
└── README.md            # Overview + ten-second version
```

## Architecture Overview

AgentHub follows a **desired-state** model:

1. **Desired State** (`agenthub.json`) — defined by the CLI/user
2. **Resolution** (`agenthub add`) — resolves asset refs into desired entries
3. **Lockfile** (`agenthub.lock.json`) — pinned, resolved state after `agenthub install`
4. **Installation** (`agenthub install`) — materializes assets into `.opencode/` or a project
5. **Federation** (`agenthub federate`) — sync from upstream hubs

### Key Components

| Component | Tech |
| --- | --- |
| **Server** | Go 1.27, Echo, ConnectRPC, bun (Postgres) |
| **CLI** | Go, Cobra, Shadow SSO device flow |
| **Web** | Go templates + Tailwind + HTMX |
| **Protobuf** | ConnectRPC (gRPC-compatible) |
| **Blobs** | S3-compatible (Silo/MinIO), content-addressed |
| **Cache** | Valkey |
| **Events** | NATS JetStream (optional) |
| **Search** | PostgreSQL full-text, Meilisearch adapter later |
| **Identity** | Shadow SSO (OIDC, device flow) |
| **Observability** | zerolog, OpenTelemetry, Prometheus |

## Guides

### Guide: Publish Your First Agent Pack

```bash
# 1. Create your agent pack directory
mkdir my-agent-pack
cd my-agent-pack

# 2. Create the pack manifest
cat > pack.json << 'PACKEOF'
{
  "name": "my-agent-pack",
  "version": "1.0.0",
  "kind": "pack",
  "displayName": "My Agent Pack",
  "description": "A collection of agents and tools",
  "iconDigest": "sha256-abc123...",
  "tags": ["productivity", "automation"],
  "categories": ["tooling"],
  "landingVisible": true
}
PACKEOF

# 2. Add assets (agents, skills, tools)
agenthub add ./agents/...
agenthub add ./skills/...
agenthub add ./tools/...

# 3. Build the bundle
agenthub materialize

# 4. Publish to the hub
agenthub publish

# 5. Verify it's available
agenthub catalog search my-agent-pack
```

### Guide: Set Up a Federated Hub

```bash
# 1. Add the upstream hub
agenthub federate add --name corporate-hub https://corp-hub.pilab.hu:9443

# 2. Sync assets from the upstream
agenthub sync

# 3. View assets from both hubs
agenthub catalog list --all

# 4. Remove the federation when done
agenthub federate remove corporate-hub
```

### Guide: Run AgentHub in Production

```bash
# 1. Provision PostgreSQL (16+ recommended)
# 2. Provision Valkey for cache
# 3. Provision NATS for sync jobs (optional)

# 2. Apply the Kubernetes manifests
kubectl apply -f deploy/namespace.yaml
kubectl apply -f deploy/k8s/postgres.yaml
kubectl apply -f deploy/k8s/secret.yaml
kubectl apply -f deploy/k8s/deployment.yaml
kubectl apply -f deploy/k8s/migrate-job.yaml

# 3. Apply the ingress
kubectl apply -f deploy/k8s/ingress.yaml

# 4. Verify the deployment
kubectl -n agenthub get pods
kubectl -n agenthub get services

# 5. Access the frontend
#    URL: https://agenthub.pilab.hu (TLS via Let's Encrypt)
```

## Reference

### CLI Commands Complete List

```bash
# Installation
agenthub --version
agenthub completion [shell]  # Generate shell completion

# Asset Management
agenthub add <asset-ref>
agenthub remove <ref>
agenthub list [--all]       # List assets from all hubs
agenthub search <query>     # Search asset display names/summaries

# Federation
agenthub federate add <name> <url>
agenthub federate remove <name>
agenthub federate list
agenthub sync

# Publishing
agenthub publish            # Publish desired assets to the hub
agenthub import <archive>   # Import a transfer archive
agenthub gc                 # Garbage collect unused assets/versions

# Migration
agenthub migrate            # Run goose migrations up
agenthub migrate status     # Check migration status
agenthub migrate down       # Run goose migrations down

# Server
agenthub serve              # Start the HTTP/Connect server
agenthub healthz            # Health check endpoint
agenthub metrics            # Prometheus metrics endpoint

# Authentication
agenthub login              # Shadow SSO device flow
agenthub logout             # Clear session

# Tooling
agenthub config             # Print effective configuration (secrets redacted)
agenthub version            # Print version and build info
agenthub export             # Export one catalog version to a transfer archive
```

### Asset Kinds

| Kind | Description |
| --- | --- |
| `agent` | An AI agent definition |
| `skill` | A skill or capability |
| `tool` | A tool or integration |
| `prompt` | A prompt template |
| `pack` | A pack (grouping of assets) |
| `ASSET_KIND_UNSPECIFIED` | Default/unspecified kind |

### Asset Version States

| Status | Meaning |
| --- | --- |
| `draft` | Being worked on, not yet published |
| `published` | Publicly visible and installable |
| `deprecated` | No longer recommended, still available |
| `yanked` | Removed, should not be installed |

### Database Schema Highlights

| Table | Purpose |
| --- | --- |
| `agency.hubs` | Hub identity and federation |
| `agency.upstreams` | Federated hub connections |
| `agency.namespaces` | Tenant/namespace isolation |
| `agency.assets` | Asset definitions (one per namespace+kind+name) |
| `agency.asset_versions` | Immutable versions of assets |
| `agency.asset_files` | Binary blobs (bundles, icons, readmes) |
| `agency.dependencies` | Cross-asset dependencies |
| `agency.install_guides` | Harness/scope/path instructions |
| `agency.signing_keys` | Publisher key custody |
| `agency.asset_drafts` | Work-in-progress manifests |
| `agency.catalog_state` | Catalog revision tracking |
| `agency.audit_log` | Complete audit trail |
| `agency.download_counters` | Download analytics |

## Deployment

### Development

```bash
# 1. Clone the repo
git clone https://github.com/pilab-cloud/agenthub.git
cd agenthub

# 3. Install dependencies
go mod tidy

# 4. Start the server with dev config
agenthub serve --config deploy/dev/config.yaml

# 5. Visit http://localhost:8080
#    http://localhost:8080/healthz -> ok
#    http://localhost:8080/.well-known/agenthub.json -> config
```

### Production (Kubernetes)

```bash
# 1. Provision infrastructure
# - PostgreSQL 16+ (shared-pg or your own)
# - Valkey for cache
# - NATS for sync jobs (optional)
# - Object storage for blobs (S3-compatible)

# 2. Apply manifests
kubectl apply -f deploy/namespace.yaml
kubectl apply -f deploy/k8s/configmap.yaml
kubectl apply -f deploy/k8s/secret.yaml          # agenthub-secrets
kubectl apply -f deploy/k8s/postgres.yaml
kubectl apply -f deploy/k8s/deployment.yaml
kubectl apply -f deploy/k8s/migrate-job.yaml
kubectl apply -f deploy/k8s/ingress.yaml

# 5. Wait for migration to complete
kubectl -n agenthub wait --for=condition=complete job/agenthub-migrate --timeout=120s

# 6. Verify
kubectl -n agenthub get pods
kubectl -n agenthub get services
# Visit: https://agenthub.pilab.hu
```

### Environment Variables

| Variable | Default | Description |
| --- | --- | --- |
| `AGENTHUB_ENV` | `dev` | Environment name |
| `AGENTHUB_SERVER_HTTP_ADDR` | `:8080` | Server HTTP bind address |
| `AGENTHUB_SERVER_METRICS_ADDR` | `:9100` | Metrics bind address |
| `AGENTHUB_SERVER_PUBLIC_URL` | `https://agenthub.pilab.hu` | Public URL |
| `AGENTHUB_DATABASE_DSN` | _(from secret)_ | Postgres connection string |
| `AGENTHUB_AUTH_ISSUER` | `https://sso.pilab.hu` | SSO issuer URL |
| `AGENTHUB_AUTH_CLIENT_ID` | `agenthub-web` | OIDC client ID |
| `AGENTHUB_AUTH_CLIENT_SECRET` | _(from secret)_ | OIDC client secret |

### Docker Build

```bash
make docker
# Builds: ghcr.io/pilab-dev/agenthub-server:<tag>
# Replace the tag or pin a digest before promoting a release.
```

## Support

- **Issues**: [GitHub Issues](https://github.com/pilab-cloud/agenthub/issues)
- **Discussions**: [GitHub Discussions](https://github.com/pilab-cloud/agenthub/discussions)
- **Contact**: `#agenthub` on the Progressive Innovation LAB Discord
- **Security**: Report security issues to `gyula@pilab.hu` (private disclosure)

---

*Generated for AgentHub v{version}. Last updated: {date}.*
