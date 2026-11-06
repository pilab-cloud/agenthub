# CLI Reference

Complete reference for the `agenthub` command-line tool.

## Installation

```bash
go install go.pilab.hu/agenthub/cmd/agenthub@latest
```

Add to PATH:

```bash
export PATH="$PATH:~/go/bin"
```

Verify:

```bash
agenthub --version
agenthub help
```

## Global Flags

| Flag | Shorthand | Description |
| --- | --- | --- |
| `--help`, `-h` | | Show help for a command |
| `--verbose`, `-v` | | Verbose output |
| `--config <path>` | | Path to config file (default: `deploy/dev/config.yaml`) |
| `--quiet` | | Suppress non-essential output |

## Command Reference

### `agenthub add`

```bash
agenthub add <asset-ref> [flags]
```

**Add an asset to `agenthub.json`, resolve it into `agenthub.lock.json` and install it into the cache.**

| Argument | Description |
| --- | --- |
| `asset-ref` | Asset reference in the form `namespace/kind/name[@constraint]`; no constraint means `*` |

An existing entry for the same asset is replaced. If resolution fails,
`agenthub.json` is restored (exit code 4).

**Flags:**

| Flag | Description |
| --- | --- |
| `--no-install` | Record and resolve only; do not install into the cache |
| `--offline` | Never touch the network (requires an existing lockfile) |
| `--agent <name>` | Target harness recorded with the asset (e.g. `opencode`) |
| `--hub <url>` | Hub URL (default: `$AGENTHUB_HUB`, then `https://agenthub.pilab.hu`) |
| `--cache-dir <path>` | Cache directory |

**Examples:**

```bash
agenthub add ncis/agent/gibbs@1.0.0
agenthub add ncis/agent/gibbs@^0.1.0 --agent opencode
agenthub add pilab/pack/pirat-agents --no-install
```

### `agenthub install`

```bash
agenthub install [flags]
```

**Install assets pinned in `agenthub.lock.json`, then wire them into your agents.**

Assets are fetched into the cache (digests and signatures verified), then written
into each chosen agent's own files. With a terminal and no `--target`, an
interactive wizard (bubbletea) asks which agents and where. Files agenthub wrote
are tracked in `<base>/.agenthub/state.json`; a file you edited since is reported
as drift and left alone unless `--force`.

| Agent (`--target`) | Local | Global |
| --- | --- | --- |
| `claude` | `.claude/` | `~/.claude/` |
| `opencode` | `.opencode/` | `~/.config/opencode/` |
| `copilot` | `.github/` | `~/.copilot/` |
| `cursor` | `.cursor/rules/*.mdc` | not supported |
| `codex` | `AGENTS.md` section | `~/.codex/AGENTS.md` section |

| Flags | Description |
| --- | --- |
| `-t, --target <ids>` | Comma-separated ids, or `auto` (detected), `all`, `none` |
| `-l, --location <loc>` | `global` or `local` |
| `-y, --yes` | Non-interactive; defaults to `--location global --target auto` |
| `--print-config <id>` | Print the files that would be written for an agent; writes nothing |
| `--refresh` | Rewrite what earlier installs wrote, for already-configured agents only |
| `--force` | Overwrite files modified since agenthub wrote them |
| `--frozen`, `--offline`, `--hub`, `--cache-dir`, `--config` | Fetch-step controls |

Without a terminal and without `--target`, assets are only cached.

**Examples:**

```bash
agenthub install                          # wizard
agenthub install -y                       # global, detected agents
agenthub install -t claude,cursor -l local
agenthub install --print-config codex
```

### `agenthub remove`

```bash
agenthub remove <ref> [flags]
```

**Remove an asset from `agenthub.json` and prune the lockfile.**

| Argument | Description |
| --- | --- |
| `ref` | Asset reference to remove (`namespace/kind/name` or `namespace/kind/name@version`) |

| Flags | Description |
| --- | --- |
| `--prune` | Also remove associated lockfile entries |
| `--dry-run` | Show what would be removed |

**Examples:**

```bash
# Remove a specific version
agenthub remove ncis/agent/gibbs@1.0.0

# Remove all versions of an agent
agenthub remove ncis/agent/gibbs

# Dry run
agenthub remove ncis/agent/gibbs --dry-run
```

### `agenthub update`

```bash
agenthub update [flags]
```

**Re-resolve desired assets and rewrite the lockfile.**

| Flags | Description |
| --- | --- |
| `--force` | Force re-resolution even if lockfile seems up to date |
| `--dry-run` | Show what would change |

**Examples:**

```bash
# Update lockfile
agenthub update

# Force update even if lockfile appears current
agenthub update --force
```

### `agenthub sync`

```bash
agenthub sync [flags]
```

**Sync upstream hubs from `agenthub.federation.json`.**

| Flags | Description |
| --- | --- |
| `--all` | Sync all configured upstreams |
| `--upstream <name>` | Sync a specific upstream only |
| `--dry-run` | Show what would be synced |

**Examples:**

```bash
# Sync all upstreams
agenthub sync

# Sync a specific upstream
agenthub sync --upstream corporate-hub

# Dry run
agenthub sync --dry-run
```

### `agenthub federate`

| Command | Description |
| --- | --- |
| `agenthub federate add <name> <url>` | Add a federated upstream hub |
| `agenthub federate remove <name>` | Remove a federated hub |
| `agenthub federate list` | List configured federated hubs |

### `agenthub publish`

```bash
agenthub publish [flags]
```

**Publish desired assets to the hub.**

| Flags | Description |
| --- | --- |
| `--tag <version>` | Override the default version |
| `--force` | Force publish even if validation warnings exist |
| `--dry-run` | Show what would be published |

**Examples:**

```bash
# Publish the pack
agenthub publish

# Publish with a specific tag
agenthub publish --tag 2.0.0

# Dry run to see what will be published
agenthub publish --dry-run
```

### `agenthub import`

```bash
agenthub import <archive> [flags]
```

**Import a transfer archive into the hub.**

| Argument | Description |
| --- | --- |
| `archive` | Path to the transfer archive (`.tar.gz`, `.zip`) |

| Flags | Description |
| --- | --- |
| `--validate` | Validate the archive before importing |
| `--dry-run` | Show what would be imported |

**Examples:**

```bash
# Import an archive
agenthub import ./transfer-archive.tar.gz

# Validate before importing
agenthub import ./archive.tar.gz --validate
```

### `agenthub gc`

```bash
agenthub gc [flags]
```

**Garbage collect unused assets and versions.**

| Flags | Description |
| --- | --- |
| `--dry-run` | Show what would be collected |
| `--age <days>` | Only collect assets unused for N days (default: 30) |
| `--force` | Collect without confirmation |

**Examples:**

```bash
# Collect unused assets (30-day default)
agenthub gc

# Collect assets unused for 90 days
agenthub gc --age 90

# Dry run
agenthub gc --dry-run
```

### `agenthub migrate`

```bash
agenthub migrate [flags]
```

**Run goose database migrations.**

| Flags | Description |
| --- | --- |
| `--direction <up|down|status>` | Migration direction (default: `up`) |
| `--config <path>` | Path to config file |

**Examples:**

```bash
# Run migrations up
agenthub migrate

# Check migration status
agenthub migrate status

# Run migrations down
agenthub migrate down
```

### `agenthub login`

```bash
agenthub login [flags]
```

**Authenticate with Shadow SSO via device code flow.**

| Flags | Description |
| --- | --- |
| `--no-browser` | Skip browser open, print device code only |
| `--port <port>` | Custom port for the callback (default: random available) |
| `--timeout <seconds>` | Timeout for the device code flow (default: 300) |

**Examples:**

```bash
# Standard login
agenthub login

# Login without opening browser
agenthub login --no-browser

# Custom timeout
agenthub login --timeout 600
```

### `agenthub logout`

```bash
agenthub logout
```

**Clear the SSO session and refresh tokens.**

**Examples:**

```bash
# Log out
agenthub logout
```

### `agenthub config`

```bash
agenthub config [flags]
```

**Print effective configuration (secrets redacted).**

| Flags | Description |
| --- | --- |
| `--all` | Show all config values including redacted secrets |

**Examples:**

```bash
# Print config
agenthub config

# Print all config including secrets
agenthub config --all
```

### `agenthub version`

```bash
agenthub version
```

**Print version and build info.**

**Examples:**

```bash
agenthub version
```

Output:

```
agenthub v2026.11.05-r4
commit: a1b2c3d4e5f6g7h8
go version: 1.27.0
```

### `agenthub help`

```bash
agenthub help [command]
```

**Show help for a specific command or list all commands.**

**Examples:**

```bash
agenthub help
agenthub help add
agenthub help install
```

### `agenthub completion`

```bash
agenthub completion [shell]
```

**Generate shell completion script.**

| Argument | Description |
| --- | --- |
| `shell` | `bash`, `zsh`, or `fish` |

**Examples:**

```bash
# Bash completion
agenthub completion bash

# Add to bashrc
agenthub completion bash > ~/.bash_completion.d/agenthub

# Zsh completion
agenthub completion zsh > "${fpath[1]}/_agenthub"

# Fish completion
agenthub completion fish > ~/.config/fish/completions/agenthub.fish
```

### `agenthub catalog`

| Subcommand | Description |
| --- | --- |
| `agenthub catalog search <query>` | Search assets by display name/summary |
| `agenthub catalog list [--kind kind] [--namespace ns]` | List assets |
| `agenthub catalog get <ref>` | Get detailed asset info |
| `agenthub catalog versions <ref>` | List asset versions |

### `agenthub audit`

| Subcommand | Description |
| --- | --- |
| `agenthub audit log [--since <duration>]` | View audit log |
| `agenthub audit stats` | View download statistics |

## Asset Reference

### Asset Fields

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | `string` | ✓ | Asset name (lowercase, hyphens allowed) |
| `version` | `string` | ✓ | Version string (semantic recommended) |
| `kind` | `enum` | ✓ | `agent`, `skill`, `tool`, `prompt`, `pack` |
| `displayName` | `string` | Optional | Human-readable name |
| `summary` | `string` | Optional | Brief description |
| `description` | `string` | Optional | Full description |
| `iconDigest` | `string` | Optional | SHA-256 digest of icon file |
| `tags` | `string[]` | Optional | Keywords for discoverability |
| `categories` | `string[]` | Optional | Grouping categories |
| `landingVisible` | `boolean` | ✓ | Should this appear in the public landing page? |
| `license` | `string` | Optional | SPDX license identifier |
| `homepage` | `string` | Optional | URL to the asset's repository |
| `repository` | `string` | Optional | Git repository URL |
| `dependencies` | `map` | Optional | Asset dependencies (namespace/kind/name @ version) |

### Version States

| Status | Meaning | Behavior |
| --- | --- | --- |
| `draft` | In development | Not visible in public catalog |
| `published` | Ready for installation | Visible and installable |
| `deprecated` | Discouraged but available | Still installable, marked as deprecated |
| `yanked` | Removed | Not installable, listed in audit log |

### Asset Reference Format

```
namespace/kind/name[@version]
```

Examples:

```
ncis/agent/gibbs@1.0.0
pilab/pack/pirat-agents@1.0.0
agent@latest
skill/my-skill@^0.1.0
```

### Asset Reference Parsing

The agenthub CLI parses references in the following order:

1. If `@version` is present, use that version
2. If no version and a lockfile exists, use the locked version
3. If no version and no lockfile, use the latest published version
4. If `latest` is specified, use the most recently published version

### Dependency Format

Dependencies are specified in `pack.json` or `agenthub.json`:

```json
{
  "name": "my-pack",
  "dependencies": {
    "namespace/kind/name": "version constraint"
  }
}
```

Examples:

```json
{
  "dependencies": {
    "ncis/agent/gibbs": "1.0.0",
    "pilab/pack/tools": "^0.1.0"
  }
}
```

## API Reference (ConnectRPC)

AgentHub uses **ConnectRPC** (gRPC-compatible) for all server-client communication.

### Base Path

```
/api/
```

### Services

| Service | Method | Description |
| --- | --- | --- |
| `CatalogService` | `Resolve` | Resolve an asset reference |
| | `GetAsset` | Get asset details |
| | `ListAssets` | List assets with filters |
| | `ListVersions` | List asset versions |
| `FederationService` | `Federate` | Sync from upstream hub |
| | `GetHubInfo` | Get hub identity |
| `IdentityService` | `Login` | Shadow SSO device flow |
| | `Logout` | Clear session |
| | `GetJWKS` | Get JSON Web Key Set |
| `RegistryService` | `GetRole` | Check user permissions |
| | `ListNamespaces` | List available namespaces |

### ConnectRPC Messages

| Message | Field(s) | Description |
| --- | --- | --- |
| `AssetRef` | `hub`, `namespace`, `kind`, `name`, `version` | Asset identity |
| `Digest` | `algorithm`, `value` | Content digest (SHA-256) |
| `PermissionRule` | `action`, `resource`, `effect` | RBAC rules |
| `Compatibility` | `harnesses`, `opencode`, `platforms`, `arch` | Install compatibility |

### Error Codes

| Code | Meaning | HTTP Equivalent |
| --- | --- | --- |
| `not_found` | Asset or resource not found | 404 |
| `invalid_argument` | Invalid parameter value | 400 |
| `permission_denied` | Insufficient permissions | 403 |
| `already_exists` | Asset already exists | 409 |
| `unavailable` | Service temporarily unavailable | 503 |
| `failed_precondition` | Precondition not met | 400 |

### Authentication

All API endpoints require authentication via one of:

1. **Shadow SSO bearer token** (recommended)
2. **API key** (for backend-to-backend)
3. **Session cookie** (from `agenthub login`)

Authentication is performed via the `Authorization: Bearer <token>` header or the
`sso` session cookie.

### ConnectRPC Code Generation

```bash
# Generate Go stubs
go install connectrpc.com/connect/cmd/connectgo@latest
connectgo -remote https://agenthub.pilab.hu/api/connectrpc.v1.CatalogService/ \
  -output ./gen/go/agenthub/connect

# Or use the provided Makefile target
make generate
```

## Web Interface

The AgentHub web interface is available at `https://agenthub.pilab.hu` and includes:

| Page | Description |
| --- | --- |
| `/` | Landing page with asset browsing |
| `/assets` | Full asset catalog |
| `/a/:namespace/:kind/:name` | Asset detail page |
| `/auth/login` | Shadow SSO login |
| `/auth/callback` | SSO callback handler |
| `/auth/logout` | Log out |
| `/.well-known/agenthub.json` | Hub metadata (OpenID configuration) |
| `/healthz` | Health check endpoint |
| `/readyz` | Readiness check endpoint |
| `/debug/healthz` | Debug health endpoint |

The web interface uses:
- **Tailwind CSS** for styling
- **HTMX** for interactivity
- **Go templates** for rendering
- **Shadow SSO** for authentication

### Web Interface Features

- **Asset browsing** with filters (kind, namespace, tags, categories)
- **Asset detail views** with metadata and files
- **Authentication** via Shadow SSO (OIDC device flow)
- **One-tap corner login** (`mountOneTap`)
- **Silent login** via hidden iframe
- **Asset installation** via OpenCode V2 plugin integration
- **Search** across all asset fields

---

*For the full protobuf schema, run `buf lint` or refer to `api/proto/` in the repository.*
