# AgentHub Documentation

Welcome to the AgentHub documentation. This site covers everything from getting
started to advanced deployment and federation.

## Documentation Structure

```
docs/
├── getting-started/      # Quickstart, installation, first steps
├── guides/              # Step-by-step tutorials
├── reference/           # CLI commands, API reference
├── architecture/        # System design, components, roadmap
├── deployment/          # Kubernetes, scaling, backup
└── INDEX.md             # This file
```

## Quick Navigation

| Topic | Documentation |
| --- | --- |
| **Quickstart** | [getting-started/quickstart.md](docs/getting-started/quickstart.md) |
| **Installation** | [getting-started/](#installation) |
| **CLI Commands** | [reference/cli-reference.md](docs/reference/cli-reference.md) |
| **Architecture** | [architecture/overview.md](docs/architecture/overview.md) |
| **Kubernetes Deploy** | [deployment/kubernetes.md](docs/deployment/kubernetes.md) |
| **Publishing Guide** | [guides/publish-agent.md](guides/publish-agent.md) |

---

# AgentHub

**AgentHub** is the package registry and control plane for AI-agent assets: agents,
skills, tools, prompts, and packs. It publishes them with immutable versions,
install guides, permission manifests, and signatures; the CLI and the OpenCode V2
plugin install and run them from any machine.

> **Status:** M0 skeleton provisioned. The server, CLI, buf modules, migrations,
> health endpoints, metrics and CI/Docker scaffolding are in place; the catalog,
> registry, federation and identity RPCs are stubbed and filled in during M1+.

## What Is AgentHub

| Plane | Responsibility |
| --- | --- |
| **Catalog** | Search, browse, resolve, and download versioned agent assets |
| **Publishing** | Validate, sign, publish, deprecate, and yank versions |
| **Consumption** | CLI + OpenCode V2 plugin reconcile desired state into local runtime state |
| **Web** | Landing page, store, dashboard, and web-based agent creation |
| **Federation** | Mirror or reference assets from other hubs while preserving origin and signatures |

**AgentHub is not:** an execution runtime, a model provider, a Git host, or a secrets manager. It stores and distributes assets that scheduling runtimes (like PiR@) consume.

## Quickstart

```bash
# 1. Install the CLI
go install go.pilab.hu/agenthub/cmd/agenthub@latest

# 2. Authenticate with Shadow SSO
agenthub login

# 3. Add a version-pinned agent pack
agenthub add pilab/pack/pirat-agents@1.0.0

# 4. Install into your project
agenthub install
```

## Installation

### CLI Installation

```bash
go install go.pilab.hu/agenthub/cmd/agenthub@latest
```

### Kubernetes Deployment

See [deployment/kubernetes.md](docs/deployment/kubernetes.md) for full Kubernetes
instructions.

### Docker

```bash
make docker
# Then: docker run -p 8080:8080 -p 9090:9090 agenthub:latest
```

### Development

```bash
# Clone the repo
git clone https://github.com/pilab-cloud/agenthub.git
cd agenthub

# Install dependencies
go mod tidy

# Start the server
agenthub serve --config deploy/dev/config.yaml

# Visit http://localhost:8080
```

## Authentication (Shadow SSO)

AgentHub uses **Shadow SSO** (OIDC) for authentication.

```bash
agenthub login
# Follow the device code flow

# Or programmatically:
# import { silentLogin, generatePkcePair } from '@pilab/shadow-sso'

# The SSO issuer is at https://sso.pilab.hu
# The audience is `agenthub`
# Client ID is `agenthub-web`
```

## CLI Reference

See [reference/cli-reference.md](docs/reference/cli-reference.md) for the complete
list of commands and flags.

## Architecture

See [architecture/overview.md](docs/architecture/overview.md) for the system
design, components, and roadmap.

## Guides

See [guides/](docs/guides/) for step-by-step tutorials on publishing, federating,
and other common workflows.

---

*Documentation generated for AgentHub. Last updated: {date}.*
