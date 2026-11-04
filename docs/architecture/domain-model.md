# Domain Model

This document defines the canonical objects AgentHub stores, validates, resolves,
and installs. It is the contract between the server, CLI, and plugin.

---

## 1. Identity

### 1.1 URN

Every version has a globally unique identity:

```text
agenthub://<hub>/<namespace>/<kind>/<name>@<version>
```

Examples:

```text
agenthub://agenthub.pilab.hu/pilab/agent/code-reviewer@1.4.0
agenthub://agenthub.pilab.hu/pirat/pack/pirat-agents@1.0.0
agenthub://hub.internal.example/pimail/skill/mail-triage@0.3.0
```

Rules:

- `hub` is the publishing hub's stable DNS identity. A mirror never changes it.
- `namespace` is lowercase kebab-case: `^[a-z0-9][a-z0-9-]{1,62}$`.
- `kind` is one of `agent`, `skill`, `tool`, `prompt`, `pack`.
- `name` is lowercase kebab-case, unique within `(namespace, kind)`.
- `version` is SemVer 2.0, immutable once published.
- The full URN is stored in lockfiles. Shorthand is only a UI convenience.

### 1.2 Shorthand and resolution

```text
pilab/agent/code-reviewer@^1.4
pirat/pack/pirat-agents
@alice/skill/my-skill@1.0.0
```

Resolution order:

1. Explicit `--registry` / `--hub` flag.
2. Registries in `agenthub.json`, in array order.
3. If more than one registry provides a matching shorthand, fail with a
   disambiguation error and list origins. Never guess across hubs.

The lockfile always records the full URN, origin, and digests, so later installs
do not repeat the ambiguity.

---

## 2. Asset kinds

### 2.1 Common shape

Every kind shares:

| Field | Required | Meaning |
| --- | --- | --- |
| `schemaVersion` | yes | Bundle schema version (currently `1`) |
| `kind` | yes | `agent`, `skill`, `tool`, `prompt`, `pack` |
| `namespace` | yes | Publishing namespace |
| `name` | yes | Asset name |
| `version` | yes | SemVer |
| `displayName` | no | Human label; defaults to `name` |
| `summary` | yes | One-line description for search |
| `description` | no | Long description (Markdown) |
| `license` | yes for public | SPDX identifier |
| `homepage` | no | Project URL |
| `repository` | no | Source repository URL |
| `icon` | no | Relative path to an SVG/PNG (max 256 KiB) |
| `tags` | no | Search tags |
| `categories` | no | Store categories |
| `compatibility` | yes | Harness/OpenCode/platform constraints |
| `permissions` | yes | Declared capabilities (see §5) |
| `dependencies` | no | Asset requirements (see §4) |
| `guides` | yes | Install guide paths per harness/scope |
| `spec` | yes | Kind-specific payload |

### 2.2 `agent`

An agent is a prompt, a mode, a model policy, and permissions.

Authoring example:

```yaml
schemaVersion: 1
kind: agent
namespace: pilab
name: code-reviewer
version: 1.4.0
displayName: Code Reviewer
summary: Independent reviewer for pull requests and branches
description: |
  Reviews a change it did not write, verifies acceptance criteria, and reports
  findings by severity. Read-only.
license: MIT
repository: https://github.com/pilab-dev/pirat-backend
tags: [review, git, quality]
categories: [engineering]
compatibility:
  harnesses: [opencode]
  opencode: ">=2.2.0 <3.0.0"
permissions:
  - action: "*"
    resource: "*"
    effect: deny
  - action: read
    resource: "*"
    effect: allow
  - action: glob
    resource: "*"
    effect: allow
  - action: grep
    resource: "*"
    effect: allow
  - action: shell
    resource: "git *"
    effect: allow
spec:
  agent:
    prompt: ./prompt.md
    mode: subagent
    modelPolicy:
      prefer:
        - anthropic/claude-sonnet-5-5#high
        - opencode-go/mimo-v2.5
      fallback: inherit
    steps: 8
    color: "#4f8cff"
guides:
  opencode:
    project: ./guides/opencode.md
```

`spec.agent` fields:

| Field | Meaning |
| --- | --- |
| `prompt` | Relative path to the Markdown body (required) |
| `mode` | `primary`, `subagent`, or `all` |
| `modelPolicy.prefer` | Ordered list of `provider/model[#variant]` |
| `modelPolicy.fallback` | `inherit` (use the session model) or `none` (fail visibly) |
| `steps` | Optional max model steps |
| `hidden` | Hide from normal listings |
| `color` | Six-digit hex UI color |
| `request` | Optional per-agent request overlay (see note) |

> OpenCode V2's `Agent.Info.request` carries a per-agent request overlay. Whether
> the overlay is applied depends on the V2 version. The adapter passes it through
> and never relies on it for security; the store marks it as advisory.

### 2.3 `skill`

A skill is a directory with a `SKILL.md` entrypoint and optional supporting files.

```yaml
schemaVersion: 1
kind: skill
namespace: pilab
name: pirat-agent-orchestration
version: 2.0.0
summary: Orchestrate PiR@ agent workflows through MCP tools
license: MIT
compatibility:
  harnesses: [opencode]
  opencode: ">=2.2.0"
permissions: []
spec:
  skill:
    entry: ./SKILL.md
    files:
      - references/agent-run.md
      - references/custom-fields.md
guides:
  opencode:
    project: ./guides/opencode.md
```

Rules:

- The skill ID is the asset name; the directory name in the cache must match.
- `SKILL.md` must have a `description`; otherwise OpenCode will not advertise it.
- Supporting files are copied as-is; no symlinks; no files outside the bundle.
- Scripts are allowed but marked `executable: true` and displayed in the store.

### 2.4 `tool`

A tool describes something the harness must connect to or install. It is not
executed by AgentHub.

Tool types:

| Type | Meaning | Adapter |
| --- | --- | --- |
| `mcp-remote` | Remote MCP server | `ctx.mcp.transform` |
| `mcp-local` | Local MCP server command | `ctx.mcp.transform` |
| `opencode-plugin` | npm package plugin | CLI edits `opencode.json`; restart required |
| `cli` | External CLI/binary | Install guide only; no automatic install |

```yaml
schemaVersion: 1
kind: tool
namespace: pilab
name: codegraph
version: 1.0.0
summary: Code intelligence MCP server
license: MIT
compatibility:
  harnesses: [opencode]
  platforms: [darwin, linux, windows]
permissions:
  - action: "codegraph_*"
    resource: "*"
    effect: allow
spec:
  tool:
    type: mcp-local
    mcp:
      name: codegraph
      config:
        type: local
        command: ["codegraph", "serve", "--mcp"]
        enabled: true
    healthcheck:
      command: ["codegraph", "--version"]
      expect: "codegraph"
guides:
  opencode:
    project: ./guides/opencode.md
```

Secret placeholders use `${env:NAME}` or `${secret:NAME}` and are validated:

```yaml
spec:
  tool:
    type: mcp-remote
    mcp:
      name: acme
      config:
        type: remote
        url: https://mcp.acme.example/mcp
        headers:
          Authorization: "Bearer ${env:ACME_MCP_TOKEN}"
    secrets:
      - name: ACME_MCP_TOKEN
        description: Token for the Acme MCP server
        required: true
```

If a required secret is missing, the plugin registers the server as disabled and
reports it in `agenthub status`; it never writes the secret to disk.

### 2.5 `prompt`

A prompt is a reusable template with a target:

| Target | Meaning | Adapter |
| --- | --- | --- |
| `command` | Slash command | `ctx.command.transform` |
| `instruction` | Session instructions | `ctx.session.hook("context")` (system-part injection) |
| `system` | System-prompt fragment | `ctx.session.hook("context")`, scoped to declared agents |

```yaml
schemaVersion: 1
kind: prompt
namespace: pimail
name: mail-triage
version: 0.2.0
summary: Triage an inbox by urgency and thread state
license: MIT
compatibility:
  harnesses: [opencode]
spec:
  prompt:
    target: command
    template: ./prompt.md
    arguments:
      - name: mailbox
        description: Mailbox or folder to triage
        required: true
      - name: limit
        description: Maximum messages to inspect
        required: false
        default: "50"
guides:
  opencode:
    project: ./guides/opencode.md
```

Templates use `{{variable}}` placeholders. Validation rejects undeclared
variables, unknown variables, and unbalanced braces.

### 2.6 `pack`

A pack is a curated set of assets with constraints, plus optional non-typed extras.

```yaml
schemaVersion: 1
kind: pack
namespace: pirat
name: pirat-agents
version: 1.0.0
summary: The PiR@ agent team, orchestration skill, and project instructions
license: MIT
compatibility:
  harnesses: [opencode]
  opencode: ">=2.2.0"
permissions: []
spec:
  pack:
    members:
      - id: pilab/agent/code-reviewer
        version: "^1.4.0"
      - id: pilab/agent/oracle
        version: "^2.0.0"
      - id: pilab/agent/momus
        version: "^2.0.0"
      - id: pilab/skill/pirat-agent-orchestration
        version: "^2.0.0"
      - id: pilab/prompt/pirat-project-instructions
        version: "^1.0.0"
    extras:
      - path: extras/categories.json
        description: PiR@ orchestrator model-routing categories
        target: pirat://ops/categories.json
guides:
  opencode:
    project: ./guides/opencode.md
```

Pack rules:

- Members are resolved to exact versions and appear in the lockfile.
- A member may be a pack (nested packs are allowed, cycles are rejected).
- Extras are not installed by the OpenCode adapter; they are available for other
  harnesses/ops tooling and are shown as files.
- A pack's own `permissions` are the union of its members' permissions, plus any
  pack-level extras. This is what the store displays.

---

## 3. Bundle source vs. manifest

Two artifacts must not be confused:

| Artifact | Author | Mutable? | Purpose |
| --- | --- | --- | --- |
| `agenthub.bundle.yaml` | Human/CI | Yes (before publish) | Authoring input |
| `manifest.json` | `agenthub pack` | No | Canonical, signed release metadata |
| `*.tar.zst` | `agenthub pack` | No | Content-addressed payload |
| `agenthub.lock.json` | `agenthub add/update` | Yes | Local resolved state |

### 3.1 Authoring layout

```text
my-agent/
├── agenthub.bundle.yaml
├── prompt.md
├── guides/
│   └── opencode.md
└── references/
    └── checklist.md
```

For skills:

```text
my-skill/
├── agenthub.bundle.yaml
├── SKILL.md
├── scripts/
│   └── check.sh
└── references/
    └── policy.md
```

### 3.2 Generated manifest

```json
{
  "schemaVersion": 1,
  "id": "agenthub://agenthub.pilab.hu/pilab/agent/code-reviewer",
  "kind": "agent",
  "namespace": "pilab",
  "name": "code-reviewer",
  "version": "1.4.0",
  "displayName": "Code Reviewer",
  "summary": "Independent reviewer for pull requests and branches",
  "license": "MIT",
  "compatibility": {
    "harnesses": ["opencode"],
    "opencode": ">=2.2.0 <3.0.0",
    "platforms": ["darwin", "linux", "windows"]
  },
  "permissions": [
    { "action": "*", "resource": "*", "effect": "deny" },
    { "action": "read", "resource": "*", "effect": "allow" },
    { "action": "shell", "resource": "git *", "effect": "allow" }
  ],
  "spec": {
    "agent": {
      "prompt": "prompt.md",
      "mode": "subagent",
      "modelPolicy": {
        "prefer": ["anthropic/claude-sonnet-5-5#high", "opencode-go/mimo-v2.5"],
        "fallback": "inherit"
      },
      "steps": 8
    }
  },
  "files": [
    { "path": "prompt.md", "size": 4096, "sha256": "…", "mediaType": "text/markdown", "mode": 420 },
    { "path": "guides/opencode.md", "size": 1024, "sha256": "…", "mediaType": "text/markdown", "mode": 420 }
  ],
  "dependencies": [],
  "guides": {
    "opencode": { "project": "guides/opencode.md" }
  },
  "publisher": {
    "namespace": "pilab",
    "keyId": "ed25519:9f2c…"
  },
  "createdAt": "2026-11-04T10:00:00Z",
  "manifestDigest": "sha256:…",
  "bundleDigest": "sha256:…",
  "signatures": [
    {
      "keyId": "ed25519:9f2c…",
      "algorithm": "ed25519",
      "custody": "client",
      "value": "base64…",
      "signedAt": "2026-11-04T10:00:00Z"
    }
  ]
}
```

Canonicalization:

- The signature covers the manifest with `signatures` and `manifestDigest`
  removed, serialized with **RFC 8785 JSON Canonicalization Scheme (JCS)**.
- `manifestDigest = sha256(JCS(unsigned manifest))`.
- `bundleDigest = sha256(tar.zst bytes)`.
- File hashes are computed over raw file bytes.

### 3.3 Signing keys

- One Ed25519 key per namespace (rotation allowed; multiple active keys).
- Public keys are published at `/.well-known/agenthub/keys.json`.
- Key registration requires the namespace `owner` role.
- Revoked keys remain listed with a revocation timestamp; versions signed after
  revocation are rejected.

Two custody modes:

| Custody | Private key | Who signs | Default |
| --- | --- | --- | --- |
| `client` | Publisher machine or CI secret store | CLI/CI | Yes |
| `hosted` | OpenBao Transit or KMS; never in PostgreSQL | Server, on behalf of the namespace | No; owner opt-in |

Hosted custody exists so the web dashboard can publish immediately without
shipping a private key to every browser. It is audited, scoped to one namespace,
and can be revoked. The signature carries `custody` as metadata; clients still
verify it with the published public key. See
[web-and-sso.md](web-and-sso.md#54-publishing-hosted-vs-client-signing).

```json
{
  "hub": "agenthub.pilab.hu",
  "keys": [
    {
      "kid": "ed25519:9f2c…",
      "namespace": "pilab",
      "algorithm": "ed25519",
      "custody": "client",
      "publicKey": "base64…",
      "validFrom": "2026-01-01T00:00:00Z",
      "revokedAt": null
    },
    {
      "kid": "ed25519:hosted-pilab-2026-01",
      "namespace": "pilab",
      "algorithm": "ed25519",
      "custody": "hosted",
      "kmsKeyRef": "openbao:transit/keys/agenthub-pilab",
      "publicKey": "base64…",
      "validFrom": "2026-11-01T00:00:00Z",
      "revokedAt": null
    }
  ]
}
```

---

## 4. Dependencies

```yaml
dependencies:
  - id: pilab/skill/pirat-agent-orchestration
    version: "^2.0.0"
    kind: requires
  - id: pilab/tool/codegraph
    version: "^1.0.0"
    kind: requires
  - id: pilab/agent/oracle
    version: "^2.0.0"
    kind: recommends
  - id: pilab/agent/legacy-reviewer
    version: "*"
    kind: conflicts
```

| Kind | Behavior |
| --- | --- |
| `requires` | Must resolve and install; failure is fatal |
| `recommends` | Offered during install; skipped with a warning in `--frozen`/CI |
| `conflicts` | Resolution fails if the asset is present |

Resolution:

- SemVer ranges; pre-releases only when the constraint explicitly includes one.
- Highest stable version wins, unless the lockfile already satisfies the constraint
  and `--update` was not requested.
- Nested packs are flattened; a pack member's exact pin wins over a broader
  top-level constraint, but a top-level exact pin that conflicts with a pack pin
  fails loudly.
- Compatibility constraints are intersected; an empty intersection is an error.
- Cycles are rejected with the full cycle path.

---

## 5. Permissions

Canonical permissions use ordered rules with `allow`, `ask`, `deny`:

```yaml
permissions:
  - action: "*"
    resource: "*"
    effect: deny
  - action: read
    resource: "*"
    effect: allow
  - action: shell
    resource: "git status"
    effect: allow
```

### 5.1 Required prefix

Every agent installed from AgentHub is registered with a leading:

```json
{ "action": "*", "resource": "*", "effect": "deny" }
```

Why: OpenCode applies global permission rules before agent-specific rules, and the
last matching rule wins. Without the prefix, a permissive global config could
grant a hub agent more access than its manifest declares. The prefix makes the
manifest the ceiling.

### 5.2 Risk classes shown in the store

| Class | Examples | UI |
| --- | --- | --- |
| `read-only` | read/glob/grep only | green |
| `workspace-write` | edit/write inside the workspace | amber |
| `shell` | shell commands | amber |
| `network` | webfetch/websearch/MCP remote | amber |
| `secrets` | required secret placeholders | red |
| `elevated` | external directories, unrestricted shell, destructive git | red |

The store must show the exact rules, not just the class. Risk classes are a
summary, not a safety guarantee.

### 5.3 Adapter fidelity

| Harness | Fidelity | Notes |
| --- | --- | --- |
| OpenCode V2 | Exact | Canonical shape is V2's rule shape |
| Claude Code | Lossy | Maps to allowed/disallowed tools; shell patterns are coarse |
| Codex | Lossy | Maps to sandbox/approval policy |
| Cursor | Lossy | Rules only; no runtime enforcement |

Lossy adapters must print a warning and link to the exact manifest rules.

---

## 6. Compatibility and model policy

### 6.1 Compatibility

```yaml
compatibility:
  harnesses: [opencode]
  opencode: ">=2.2.0 <3.0.0"
  platforms: [darwin, linux, windows]
  arch: [arm64, amd64]
```

- `harnesses` is required. An asset with no compatible harness is not installable.
- `opencode` is a SemVer range checked against `ctx.app.version`.
- `platforms`/`arch` are optional; when set, the CLI refuses to install on a
  mismatch unless `--ignore-platform` is explicit.
- The resolver returns the reason for every incompatible candidate, so the CLI can
  explain why a version was skipped.

### 6.2 Model policy

Model names are environment-specific. Assets should not hard-code a single model.

```yaml
modelPolicy:
  prefer:
    - anthropic/claude-sonnet-5-5#high
    - opencode-go/mimo-v2.5
  fallback: inherit
```

Resolution at runtime:

1. Ask the plugin's `ctx.model.list()` for available models.
2. Pick the first preference that exists and is enabled.
3. If none exist and `fallback: inherit`, leave the agent's model unset so it
   inherits the session model.
4. If none exist and `fallback: none`, do not register the agent; report the
   missing model in `agenthub status`.

Portable mode writes the first available preference into the Markdown
frontmatter. The store displays "recommended models" and never claims they are
available to the user.

---

## 7. Install guides

Guides are Markdown, validated, and versioned with the asset.

```yaml
guides:
  opencode:
    project: ./guides/opencode-project.md
    global: ./guides/opencode-global.md
  claude-code:
    project: ./guides/claude.md
```

Guide frontmatter:

```yaml
---
harness: opencode
scope: project
requires:
  opencode: ">=2.2.0"
steps:
  - title: Install the CLI
    command: go install go.pilab.hu/agenthub/cmd/agenthub@latest
  - title: Add the asset
    command: agenthub add pilab/agent/code-reviewer@1.4.0
verify: agenthub status --json | jq '.assets[] | select(.name=="code-reviewer")'
---
```

Validation:

- Every command is a single line, no shell metacharacter surprises.
- The verify command must be non-destructive and idempotent.
- Guides never contain secrets; placeholders are allowed.
- The store renders guides as tabs and shows copy buttons.

---

## 8. Local state

### 8.1 Global configuration

`~/.config/agenthub/config.yaml`:

```yaml
registries:
  - name: pilab
    url: https://agenthub.pilab.hu
    hub: agenthub.pilab.hu
    trust: required
  - name: internal
    url: https://hub.internal.example
    hub: hub.internal.example
    trust: required
default_namespace: pilab
cache_dir: ~/.cache/agenthub
telemetry: false
policy:
  requireSignature: true
  allowUnverified: false
  denyPermissions:
    - "shell:rm -rf *"
    - "shell:git push --force*"
    - "external_directory:*"
```

### 8.2 Desired state

Project: `<repo>/agenthub.json`. Global: `~/.config/agenthub/agenthub.json`.

```json
{
  "schemaVersion": 1,
  "registries": ["https://agenthub.pilab.hu"],
  "assets": [
    {
      "id": "pilab/pack/pirat-agents",
      "version": "^1.0.0",
      "enabled": true
    },
    {
      "id": "pilab/tool/codegraph",
      "version": "^1.0.0",
      "enabled": true
    }
  ],
  "policy": {
    "requireSignature": true,
    "allowUnverified": false
  }
}
```

Precedence:

- Global assets load first; project assets with the same ID replace them.
- `enabled: false` in the project disables a globally enabled asset for that project.
- An asset ID is active at most once per location.
- Project and global lockfiles are separate; the plugin loads both and applies
  project overrides last.

### 8.3 Lockfile

`agenthub.lock.json` (next to the desired state):

```json
{
  "lockfileVersion": 1,
  "generatedAt": "2026-11-04T10:00:00Z",
  "resolver": "agenthub/0.1.0",
  "registries": [
    { "name": "pilab", "url": "https://agenthub.pilab.hu", "hub": "agenthub.pilab.hu" }
  ],
  "assets": [
    {
      "id": "agenthub://agenthub.pilab.hu/pilab/agent/code-reviewer",
      "kind": "agent",
      "version": "1.4.0",
      "origin": "agenthub.pilab.hu",
      "manifestDigest": "sha256:…",
      "bundleDigest": "sha256:…",
      "signature": { "keyId": "ed25519:9f2c…", "verified": true },
      "dependencies": [
        "agenthub://agenthub.pilab.hu/pilab/skill/pirat-agent-orchestration@2.0.0"
      ],
      "files": [
        { "path": "prompt.md", "sha256": "…", "size": 4096 }
      ],
      "scope": "project",
      "enabled": true,
      "requestedBy": "pilab/pack/pirat-agents@1.0.0"
    }
  ],
  "policy": {
    "requireSignature": true,
    "verifiedAt": "2026-11-04T10:00:01Z"
  }
}
```

Lockfile rules:

- Written atomically (temp file + `rename`).
- Never hand-edited; `agenthub verify` detects drift and `agenthub install --frozen`
  refuses it.
- Contains enough information to install offline from the cache.
- Does not contain secrets, tokens, or absolute cache paths.
- `requestedBy` preserves why an asset was pulled in (direct or via a pack).

### 8.4 Cache layout

```text
~/.cache/agenthub/
├── blobs/sha256/ab/cd/<digest>          # individual files, deduplicated
├── bundles/sha256/<bundleDigest>.tar.zst
├── manifests/sha256/<manifestDigest>.json
├── extracted/<manifestDigest>/          # read-only materialized tree
└── metadata/
    ├── registry-<hash>.json             # ETag/cursor cache
    └── resolve-<hash>.json              # short-lived resolve cache
```

- Extracted trees are read-only (`0555` dirs, `0444` files) so runtime edits are
  visible as corruption.
- `agenthub gc` removes unreferenced entries older than a retention window.
- `agenthub verify` re-hashes everything and re-checks signatures.

---

## 9. Server storage schema

PostgreSQL, one schema (`agenthub`), goose migrations.

### 9.1 Tables

```sql
-- Hub identity and federation
CREATE TABLE hubs (
  id             text PRIMARY KEY,          -- agenthub.pilab.hu
  base_url       text NOT NULL,
  public_key     text,                      -- hub signing key
  protocol       int  NOT NULL DEFAULT 1,
  created_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE upstreams (
  id             uuid PRIMARY KEY,
  local_hub_id   text NOT NULL REFERENCES hubs(id),
  remote_hub_id  text NOT NULL,
  base_url       text NOT NULL,
  mode           text NOT NULL,             -- reference | mirror | proxy
  trust_policy   jsonb NOT NULL,
  sync_cursor    text,
  last_sync_at   timestamptz,
  status         text NOT NULL DEFAULT 'active',
  UNIQUE (local_hub_id, remote_hub_id)
);

-- Publishing
CREATE TABLE namespaces (
  id             uuid PRIMARY KEY,
  hub_id         text NOT NULL REFERENCES hubs(id),
  slug           text NOT NULL,
  display_name   text NOT NULL,
  description    text,
  visibility     text NOT NULL DEFAULT 'public', -- public | unlisted | private
  created_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (hub_id, slug)
);

CREATE TABLE assets (
  id             uuid PRIMARY KEY,
  hub_id         text NOT NULL REFERENCES hubs(id),
  namespace_id   uuid NOT NULL REFERENCES namespaces(id),
  kind           text NOT NULL,
  name           text NOT NULL,
  display_name   text,
  summary        text NOT NULL,
  description    text,
  license        text,
  homepage       text,
  repository     text,
  icon_digest    text,
  tags           text[] NOT NULL DEFAULT '{}',
  categories     text[] NOT NULL DEFAULT '{}',
  latest_version text,
  landing_visible boolean NOT NULL DEFAULT true,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (namespace_id, kind, name)
);

CREATE TABLE asset_versions (
  id                uuid PRIMARY KEY,
  asset_id          uuid NOT NULL REFERENCES assets(id),
  version           text NOT NULL,
  status            text NOT NULL DEFAULT 'draft', -- draft|published|deprecated|yanked (web drafts are in asset_drafts)
  manifest          jsonb NOT NULL,
  manifest_digest   text NOT NULL,
  bundle_digest     text NOT NULL,
  origin_hub_id     text,
  origin_version_id uuid,
  published_by      text,
  published_at      timestamptz,
  yanked_at         timestamptz,
  deprecation_note  text,
  readme            text,
  changelog         text,
  UNIQUE (asset_id, version),
  UNIQUE (manifest_digest)
);

CREATE TABLE asset_files (
  version_id     uuid NOT NULL REFERENCES asset_versions(id) ON DELETE CASCADE,
  path           text NOT NULL,
  media_type     text NOT NULL,
  size           bigint NOT NULL,
  sha256         text NOT NULL,
  mode           int NOT NULL DEFAULT 420,
  executable     boolean NOT NULL DEFAULT false,
  PRIMARY KEY (version_id, path)
);

CREATE TABLE dependencies (
  version_id     uuid NOT NULL REFERENCES asset_versions(id) ON DELETE CASCADE,
  kind           text NOT NULL,             -- requires|recommends|conflicts
  target_hub     text,
  target_ns      text NOT NULL,
  target_kind    text NOT NULL,
  target_name    text NOT NULL,
  constraint     text NOT NULL,
  PRIMARY KEY (version_id, kind, target_ns, target_kind, target_name)
);

CREATE TABLE install_guides (
  version_id     uuid NOT NULL REFERENCES asset_versions(id) ON DELETE CASCADE,
  harness        text NOT NULL,
  scope          text NOT NULL,             -- project|global
  path           text NOT NULL,
  body           text NOT NULL,
  PRIMARY KEY (version_id, harness, scope)
);

CREATE TABLE signing_keys (
  id             uuid PRIMARY KEY,
  namespace_id   uuid NOT NULL REFERENCES namespaces(id),
  kid            text NOT NULL,
  algorithm      text NOT NULL,
  custody        text NOT NULL DEFAULT 'client', -- client | hosted
  public_key     text NOT NULL,
  kms_key_ref    text,                           -- OpenBao/KMS reference; never the key
  created_by     text,
  valid_from     timestamptz NOT NULL,
  revoked_at     timestamptz,
  UNIQUE (namespace_id, kid)
);

-- Dashboard authoring. Drafts never appear in the public catalog.
CREATE TABLE asset_drafts (
  id             uuid PRIMARY KEY,
  namespace_id   uuid NOT NULL REFERENCES namespaces(id),
  kind           text NOT NULL,
  name           text NOT NULL,
  author_sub     text NOT NULL,
  manifest       jsonb NOT NULL,             -- unsigned
  files          jsonb NOT NULL,             -- path -> inline content or blob digest
  status         text NOT NULL DEFAULT 'draft', -- draft|publishing|published|discarded
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (namespace_id, kind, name, author_sub)
);

-- Monotonic revision for landing/store cache invalidation.
CREATE TABLE catalog_state (
  hub_id     text PRIMARY KEY REFERENCES hubs(id),
  revision   bigint NOT NULL DEFAULT 1,
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- Operations
CREATE TABLE audit_log (
  id             bigserial PRIMARY KEY,
  at             timestamptz NOT NULL DEFAULT now(),
  actor_sub      text,
  actor_email    text,
  action         text NOT NULL,
  namespace      text,
  asset          text,
  version        text,
  detail         jsonb,
  ip             inet
);

CREATE TABLE download_counters (
  asset_id       uuid NOT NULL REFERENCES assets(id),
  version_id     uuid REFERENCES asset_versions(id),
  day            date NOT NULL,
  count          bigint NOT NULL DEFAULT 0,
  PRIMARY KEY (asset_id, version_id, day)
);
```

### 9.2 Indexes

```sql
CREATE INDEX idx_assets_search
  ON assets USING gin (to_tsvector('simple', coalesce(display_name,'') || ' ' || coalesce(summary,'') || ' ' || coalesce(description,'')));

CREATE INDEX idx_assets_tags ON assets USING gin (tags);
CREATE INDEX idx_assets_categories ON assets USING gin (categories);
CREATE INDEX idx_versions_status ON asset_versions (status, published_at DESC);
CREATE INDEX idx_versions_landing
  ON asset_versions (published_at DESC)
  WHERE status = 'published';
CREATE INDEX idx_versions_origin ON asset_versions (origin_hub_id, origin_version_id);
CREATE INDEX idx_assets_namespace_updated ON assets (namespace_id, updated_at DESC);
CREATE INDEX idx_assets_landing
  ON assets (namespace_id, updated_at DESC)
  WHERE landing_visible;
CREATE INDEX idx_drafts_author ON asset_drafts (namespace_id, author_sub, updated_at DESC);
CREATE INDEX idx_audit_at ON audit_log (at DESC);
```

### 9.3 Blob layout

```text
s3://agenthub-blobs/sha256/ab/cd/<sha256>
s3://agenthub-bundles/sha256/<bundleDigest>.tar.zst
s3://agenthub-icons/sha256/<sha256>
s3://agenthub-exports/<hub>/<date>/<digest>.tar.zst
```

Buckets are versioned and private; blobs are served through the API or a signed
CDN URL. Public metadata reads never require an S3 credential.

---

## 10. Validation rules

### 10.1 Bundle validation

- Schema version known; kind/name/version valid.
- SemVer valid; version not already published for the asset.
- `summary` non-empty, ≤ 160 chars; `description` ≤ 64 KiB.
- License valid SPDX identifier for public assets.
- All referenced files exist in the bundle; no unreferenced files above 1 MiB.
- No absolute paths, `..` traversal, symlinks, hardlinks, or special files.
- Total uncompressed size ≤ 50 MiB (configurable); per-file ≤ 10 MiB.
- No duplicate paths or case-only collisions.
- Executable bits allowed only for declared scripts; listed in the manifest.
- Agent prompt non-empty; `mode` valid; permissions start with deny-all.
- Skill `SKILL.md` has `description`; ID matches the directory.
- Prompt variables declared and used; no unknown placeholders.
- Tool MCP config is valid JSON-compatible YAML; no inline secrets.
- Dependencies parse; target assets exist (unless the version is a draft).
- Compatibility range valid; at least one harness.
- Install guide steps validate; verify command is non-destructive.
- Signature verifies against an active namespace key.

### 10.2 Publish-time checks

- Caller has `agenthub.publish` and a publisher/maintainer/owner role on the namespace.
- If the namespace requires review, the version stays `draft` until approved.
- A hosted publish requires an active `custody = 'hosted'` key for the namespace
  and a reachable OpenBao/KMS backend; the private key is never returned.
- Blobs exist in S3 and hashes match.
- No yanked version resurrection; publish a new version instead.
- Manifest digest is not already used by another asset (content-addressing collision).
- The catalog revision is bumped and landing caches are invalidated after commit.
- Audit event written in the same transaction.

### 10.2.1 Draft and landing checks

- A draft is visible only to its author, namespace maintainers, and owners.
- A draft cannot be installed or resolved by the CLI.
- Only `published` versions in `public` namespaces appear on the landing page.
- `landing_visible = false` hides a published asset from `/` without unpublishing it.
- Private and unlisted namespaces never render public landing cards.

### 10.3 Install-time checks

- Signature required by policy; otherwise explicit `--allow-unverified`.
- Digest matches; every file hash matches.
- Compatibility matches the running OpenCode version and platform.
- No conflicts with already-locked assets.
- Policy deny-list does not match any permission.
- Required secrets are reported, not resolved by the installer.

---

## 11. Versioning and lifecycle

```text
draft ──publish──▶ published ──deprecate──▶ deprecated
                       │                        │
                       └────────yank────────────┴──▶ yanked
```

| Action | Effect | Reversible? |
| --- | --- | --- |
| Publish | Creates an immutable version | No (new version required) |
| Deprecate | Marks a version/asset as discouraged; shows a message | Yes |
| Yank | Hides from search/new resolutions; blocks new installs | Yes (unyank) |
| Delete draft | Removes an unpublished draft | Yes (draft only) |
| Delete version | Not supported | — |

Retention:

- Yanked bundles stay downloadable for lockfiles for 180 days by default.
- After retention, downloads return `410 Gone` with a migration hint.
- Audit records and manifests are retained indefinitely (small).

---

## 12. Example: complete pack

```text
pirat-agents/
├── agenthub.bundle.yaml
├── guides/
│   └── opencode.md
└── extras/
    └── categories.json
```

```yaml
schemaVersion: 1
kind: pack
namespace: pirat
name: pirat-agents
version: 1.0.0
displayName: PiR@ Agent Team
summary: The PiR@ agent team, orchestration skill, and project instructions
license: MIT
repository: https://github.com/pilab-dev/pirat-backend
tags: [pirat, agents, orchestration]
categories: [packs]
compatibility:
  harnesses: [opencode]
  opencode: ">=2.2.0 <3.0.0"
permissions: []
spec:
  pack:
    members:
      - id: pilab/agent/atlas
        version: "^2.0.0"
      - id: pilab/agent/code-reviewer
        version: "^1.4.0"
      - id: pilab/agent/explore
        version: "^2.0.0"
      - id: pilab/agent/hephaestus
        version: "^2.0.0"
      - id: pilab/agent/librarian
        version: "^2.0.0"
      - id: pilab/agent/metis
        version: "^2.0.0"
      - id: pilab/agent/momus
        version: "^2.0.0"
      - id: pilab/agent/multimodal-looker
        version: "^2.0.0"
      - id: pilab/agent/oracle
        version: "^2.0.0"
      - id: pilab/agent/pirat-story-agent
        version: "^2.0.0"
      - id: pilab/agent/pirat-task-agent
        version: "^2.0.0"
      - id: pilab/agent/prometheus
        version: "^2.0.0"
      - id: pilab/agent/sisyphus
        version: "^2.0.0"
      - id: pilab/skill/pirat-agent-orchestration
        version: "^2.0.0"
      - id: pilab/prompt/pirat-project-instructions
        version: "^1.0.0"
    extras:
      - path: extras/categories.json
        description: PiR@ orchestrator model-routing categories
        target: pirat://ops/categories.json
guides:
  opencode:
    project: ./guides/opencode.md
```
