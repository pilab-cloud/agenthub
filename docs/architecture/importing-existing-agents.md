# Importing Existing Agents

This document maps the assets that already exist in the PiLab repositories into
AgentHub. It records what was found, the exact V1 → V2 conversion, the pack
layout, the source-of-truth cleanup, and the publish pipeline.

Nothing here changes the source repositories by itself. The conversion is a
build step that produces AgentHub bundles from the existing authoring files.

---

## 1. Inventory

### 1.1 `pirat-backend`

Path: `/Users/paalgyula/Workspace/pilab/pirat/pirat-backend`.

| Asset | Location | Count | Notes |
| --- | --- | --- | --- |
| Agent Markdown | `agents/*.md` | 13 | Authoring source: frontmatter + prompt body |
| Category routing | `agents/categories.json` | 1 | Model routing categories, not agent definitions |
| Embedded agent copy | `internal/agent/provision/agents/*.md` | 13 | Byte-identical except `momus.md` |
| Orchestration skill | `internal/agent/provision/skills/pirat-agent-orchestration/SKILL.md` | 1 | Valid `name` + `description` frontmatter |
| Project instructions | `internal/agent/provision/AGENTS.md` | 1 | CodeGraph section injected into provisioned repos |
| Built-in registry | `internal/agent/config.go` | — | Duplicates descriptions, modes, models, tool denies |
| Tool restrictions | `internal/agent/config.go` (`ToolRestriction`) | — | Duplicates permission denies |
| Team eligibility | `internal/agent/config.go` (`TeamEligibility`) | — | Orchestrator-only policy |

Agents found:

```text
atlas  code-reviewer  explore  hephaestus  librarian  metis  momus
multimodal-looker  oracle  pirat-story-agent  pirat-task-agent
prometheus  sisyphus
```

`sisyphus-junior` is referenced by `DefaultAgentConfigs`, `categories.json`, and
`TeamEligibility`, but **has no Markdown definition**. Decision: it is **not**
published as an AgentHub asset. It has no authoring source and the pack does not
need it; `pirat-task-agent` is the canonical published agent for that role.

### 1.2 `pimail`

Path: `/Users/paalgyula/Workspace/pilab/pimail`.

| Asset | Location | Notes |
| --- | --- | --- |
| `AGENTS.md` | repository root | Repository contribution guide, not a mail-triage agent |
| Agent Markdown | — | None found |
| `SKILL.md` | — | None found |
| `.opencode/` | — | Does not exist |

`AGENTS.md` covers the repository layout, daemons, commands, coding conventions,
testing, tracing, deployment, and security rules. It is a **project instruction**
asset, not an agent prompt. It also references production topology and secrets
policy, so it should be published as a `pimail`-scoped instruction asset, not as
a generic reusable prompt.

### 1.3 What this means

- The PiR@ agent team is the first real pack.
- The orchestration skill is the first real skill.
- PiMail contributes one instruction asset and no agents.
- There is no `tool` asset yet; `codegraph` and the PiR@ MCP server are natural
  first candidates, but they need their own manifests.

---

## 2. Current formats

### 2.1 V1 agent frontmatter

All 13 files use the same V1 shape:

```yaml
---
description: Independent pull-request code reviewer. …
mode: subagent
model: opencode-go/mimo-v2.5
permission:
  "*": deny
  read: allow
  glob: allow
  grep: allow
  list: allow
  bash: allow
  pirat_get_issue: allow
  pirat_get_issue_custom_fields: allow
  codegraph_*: allow
---
# prompt body
```

Observed details:

- `description`, `mode`, `model`, and `permission` are present in all 13.
- Only `momus.md` has a `name:` key; the rest rely on the filename.
- Values in `permission` are `allow` (no `ask`/`deny` examples beyond `"*"`).
- `permission` is a YAML mapping, so **source order matters** and a Go `map`
  parse would lose it.
- `mode` is `primary` or `subagent`.
- `model` is a single string; there is no fallback chain in the file.
- `temperature` exists in `config.go` but not in any Markdown file.

### 2.2 V2 agent shape

OpenCode V2 expects (verified against `@opencode/plugin` 2.2.5):

```yaml
---
description: Independent pull-request code reviewer. …
mode: subagent
model: opencode-go/mimo-v2.5
permissions:
  - action: "*"
    resource: "*"
    effect: deny
  - action: read
    resource: "*"
    effect: allow
  - action: shell
    resource: "*"
    effect: allow
  - action: codegraph_*
    resource: "*"
    effect: allow
  - action: read
    resource: "*.env"
    effect: ask
  - action: read
    resource: "*.env.*"
    effect: ask
  - action: read
    resource: "*.env.example"
    effect: allow
---
# prompt body
```

The `.env` guards come last so no earlier broad `read` allow can override them.

Key differences:

| V1 | V2 | Action required |
| --- | --- | --- |
| `permission` map | `permissions` ordered array | Convert, preserve order |
| `bash` | `shell` | Rename |
| `task` | `subagent` | Rename |
| `write` + `edit` | `edit` (covers `edit`, `write`, `patch`) | Merge, dedupe |
| `list` | no V2 action | Drop; `read`/`glob` cover listing |
| `webfetch` | `webfetch` | Keep |
| `websearch` | `websearch` | Keep |
| `read`/`glob`/`grep` | same | Keep |
| MCP tool names (`pirat_*`, `codegraph_*`) | same action namespace | Keep; V2 supports action wildcards |
| No rule for `.env` | default base policy asks | Must re-add after a broad `read` allow |
| `model` string | `model` string or model policy | Wrap in `modelPolicy.prefer` |
| `name` optional | `name` display label | Derive from filename |

---

## 3. Conversion rules

The converter is a Go package (`internal/opencode/agents.go`) with golden-file
tests. It is deterministic and idempotent.

### 3.1 Frontmatter

| Output field | Source | Rule |
| --- | --- | --- |
| `name` | filename or `name:` | Prefer explicit; slugify; reject collisions |
| `description` | `description` | Keep full text |
| `summary` | `description` | First sentence, max 160 chars; used by the hub catalog |
| `mode` | `mode` | `primary`/`subagent`/`all`; default `subagent` |
| `modelPolicy.prefer` | `model` | One-element list; strip `#variant` only if invalid |
| `modelPolicy.fallback` | — | `inherit` |
| `permissions` | `permission` | Ordered conversion (below) |
| `spec.agent.prompt` | body | Written to `prompt.md` |
| `spec.agent.steps` | — | Unset |
| `spec.agent.color` | — | Unset |
| `spec.agent.request` | — | Unset |

`temperature` from `config.go` has no V1 Markdown representation. It is not
carried over automatically. If a model needs it, it can be expressed as
`spec.agent.request.body.temperature` and treated as advisory; V2's support for
agent request overlays is version-dependent.

### 3.2 Permission conversion

Algorithm:

```text
1. Parse the `permission` mapping with yaml.Node to preserve source order.
2. Reject duplicate keys and non-string effects.
3. Always emit {action:"*", resource:"*", effect:"deny"} first.
4. Skip the source's own "*" rule (it is replaced by step 3).
5. For each remaining entry, in source order:
     - map the action name (table below)
     - effect stays allow/ask/deny
     - resource is "*" unless the V1 value is an object (not present today)
6. After every entry is mapped, if `read` is allowed with resource "*",
   append the `.env` guards at the very end:
     read *.env -> ask
     read *.env.* -> ask
     read *.env.example -> allow
7. If `edit` is allowed, ensure `write` and `patch` are covered by `edit`.
8. Deduplicate identical (action, resource, effect) triples, keeping the last.
9. If `subagent` is allowed, keep it; otherwise no rule (default is ask).
```

Action mapping:

| V1 action | V2 action | Reason |
| --- | --- | --- |
| `bash` | `shell` | V2 renamed the action |
| `task` | `subagent` | V2 renamed the action |
| `write` | `edit` | V2 `edit` covers `edit`, `write`, `patch` |
| `list` | — (drop) | No V2 action; covered by `read`/`glob` |
| `read` | `read` | Same |
| `glob` | `glob` | Same |
| `grep` | `grep` | Same |
| `webfetch` | `webfetch` | Same |
| `websearch` | `websearch` | Same |
| `question` | `question` | Same |
| `skill` | `skill` | Same |
| `external_directory` | `external_directory` | Same |
| `pirat_*` / `codegraph_*` | same | V2 action wildcards match MCP tool actions |
| anything else | same | Pass through; validation warns if unknown |

Why the `.env` guards are mandatory:

V2 appends agent rules after global and default rules, and the last match wins.
A converted agent with:

```yaml
- action: read
  resource: "*"
  effect: allow
```

would otherwise override OpenCode's default `read *.env -> ask`, silently
allowing secret reads. The converter re-adds the guards **after** the allow rule.

### 3.3 Ordering

V2 evaluates rules in order with the last match winning. The converter preserves
the author's intent by keeping the source order of allows, then appending:

1. deny-all prefix
2. converted allows in source order
3. `.env` guards
4. any safety rules from the pack policy (none by default)

A pack may add stricter rules, but the converter never removes an author's deny.

### 3.4 Example

Input (`agents/oracle.md`):

```yaml
---
description: Strategic technical advisor with deep reasoning capabilities. …
mode: subagent
model: opencode-go/mimo-v2.5
permission:
  "*": deny
  read: allow
  glob: allow
  grep: allow
  list: allow
  bash: allow
  pirat_*: allow
  codegraph_*: allow
---
```

Output (`build/agents/oracle/agenthub.bundle.yaml` excerpt):

```yaml
schemaVersion: 1
kind: agent
namespace: pilab
name: oracle
version: 1.0.0
displayName: Oracle
summary: Strategic technical advisor with deep reasoning capabilities.
description: |
  Strategic technical advisor with deep reasoning capabilities. Read-only
  consultant for architecture decisions, complex debugging, and high-stakes
  technical choices. …
license: MIT
compatibility:
  harnesses: [opencode]
  opencode: ">=2.2.0 <3.0.0"
permissions:
  - { action: "*", resource: "*", effect: deny }
  - { action: read, resource: "*", effect: allow }
  - { action: glob, resource: "*", effect: allow }
  - { action: grep, resource: "*", effect: allow }
  - { action: shell, resource: "*", effect: allow }
  - { action: "pirat_*", resource: "*", effect: allow }
  - { action: "codegraph_*", resource: "*", effect: allow }
  - { action: read, resource: "*.env", effect: ask }
  - { action: read, resource: "*.env.*", effect: ask }
  - { action: read, resource: "*.env.example", effect: allow }
spec:
  agent:
    prompt: ./prompt.md
    mode: subagent
    modelPolicy:
      prefer: ["opencode-go/mimo-v2.5"]
      fallback: inherit
guides:
  opencode:
    project: ./guides/opencode.md
```

Note: `oracle` grants unrestricted `shell` in V1. The converted asset preserves
that behavior but is classified as `elevated` in the store. Tightening is
deferred: v1 has no `--tighten` mode and never narrows permissions silently. A
future review can propose narrower `shell` patterns as an explicit, reviewed
version change.

---

## 4. Pack layout

### 4.1 Namespaces

| Namespace | Contents |
| --- | --- |
| `pilab` | Shared agents, skills, prompts used across PiLab |
| `pirat` | The PiR@ pack and PiR@-specific extras |
| `pimail` | PiMail instruction assets |

The existing docs use `pilab/agent/<name>` and `pirat/pack/pirat-agents`. Keep
that split: agents are reusable outside PiR@; the pack is the PiR@ curation.

### 4.2 Authoring tree

```text
pirat-backend/
├── agents/                          # canonical agent Markdown (existing)
├── packs/
│   └── pirat-agents/
│       ├── agenthub.bundle.yaml     # pack manifest
│       ├── guides/opencode.md
│       └── extras/categories.json   # copied from agents/categories.json
└── tools/                           # generated by `agenthub import-v1`
    ├── atlas/
    │   ├── agenthub.bundle.yaml
    │   ├── prompt.md
    │   └── guides/opencode.md
    ├── code-reviewer/
    └── ...
```

Keeping generated bundles out of `agents/` avoids mixing authored Markdown with
generated manifests. `tools/` is gitignored; CI regenerates it.

### 4.3 Pack manifest

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
      - { id: pilab/agent/atlas, version: "^1.0.0" }
      - { id: pilab/agent/code-reviewer, version: "^1.0.0" }
      - { id: pilab/agent/explore, version: "^1.0.0" }
      - { id: pilab/agent/hephaestus, version: "^1.0.0" }
      - { id: pilab/agent/librarian, version: "^1.0.0" }
      - { id: pilab/agent/metis, version: "^1.0.0" }
      - { id: pilab/agent/momus, version: "^1.0.0" }
      - { id: pilab/agent/multimodal-looker, version: "^1.0.0" }
      - { id: pilab/agent/oracle, version: "^1.0.0" }
      - { id: pilab/agent/pirat-story-agent, version: "^1.0.0" }
      - { id: pilab/agent/pirat-task-agent, version: "^1.0.0" }
      - { id: pilab/agent/prometheus, version: "^1.0.0" }
      - { id: pilab/agent/sisyphus, version: "^1.0.0" }
      - { id: pilab/skill/pirat-agent-orchestration, version: "^1.0.0" }
      - { id: pilab/prompt/pirat-project-instructions, version: "^1.0.0" }
    extras:
      - path: extras/categories.json
        description: PiR@ orchestrator model-routing categories
        target: pirat://ops/categories.json
guides:
  opencode:
    project: ./guides/opencode.md
```

### 4.4 Dependencies

The agents rely on two MCP servers. The pack declares them so the store shows
the full blast radius and the resolver installs them:

```yaml
dependencies:
  - id: pilab/tool/codegraph
    version: "^1.0.0"
    kind: requires
  - id: pilab/tool/pirat-mcp
    version: "^1.0.0"
    kind: requires
```

Without these, `pirat_*` and `codegraph_*` permissions refer to tools that are
not present, and the agents silently lose capabilities.

### 4.5 Extras

- `categories.json` is not an OpenCode asset. It drives the PiR@ orchestrator's
  model routing. It ships as a pack extra with a `target` URI and is not
  installed by the OpenCode adapter.
- `extras` are visible in the store and downloadable, but no harness auto-applies
  them. The PiR@ orchestrator reads them from its own deployment process.
- The pack's `permissions` are the union of its members; the store computes and
  displays the union, not a hand-written summary.

### 4.6 PiMail

PiMail contributes:

```text
pimail/
├── AGENTS.md                      # existing
├── agenthub.bundle.yaml           # generated or hand-written
└── guides/opencode.md
```

Manifest:

```yaml
schemaVersion: 1
kind: prompt
namespace: pimail
name: project-instructions
version: 0.1.0
displayName: PiMail Project Instructions
summary: Repository layout, daemons, conventions, testing, and operations rules
license: MIT
repository: https://github.com/pilab-dev/pimail
compatibility:
  harnesses: [opencode]
spec:
  prompt:
    target: instruction
    template: ./AGENTS.md
guides:
  opencode:
    project: ./guides/opencode.md
```

Caveats:

- This asset belongs to `pimail` only. It is not a general-purpose prompt and
  must not be promoted for other repositories.
- It contains production hostnames and operational details. The `pimail`
  namespace must be **private** (or unlisted at most); the asset is not part of
  the public catalog.
- It is large. The instruction hook injects it into every request; the store
  should show its token estimate, and the plugin should warn above a threshold
  (default 8k tokens).

---

## 5. Source-of-truth cleanup

The current repository has four overlapping sources for the same agent metadata.
AgentHub should consume one.

| Source | Current role | Target role |
| --- | --- | --- |
| `agents/*.md` | Authoring | **Canonical** |
| `internal/agent/provision/agents/*.md` | Runtime copy | Generated at build time from `agents/` |
| `internal/agent/config.go` `DefaultAgentConfigs` | Descriptions, modes, models, tool denies | Routing/category metadata only; no duplicated prose |
| `internal/agent/config.go` `ToolRestriction` | Tool denies | Removed; permissions come from frontmatter |

### 5.1 Drift evidence

`internal/agent/provision/agents/momus.md` lacks the `name: momus` line that
`agents/momus.md` has. Every other file is identical. This is exactly the drift
the single-source rule prevents.

### 5.2 Recommended steps

1. Make `agents/` the only authored copy.
2. Replace the embedded copy with `go:embed` over a generated directory, or
   generate `internal/agent/provision/agents/` in `go generate` and fail CI if it
   differs.
3. Move category routing (`categories.json`) to `packs/pirat-agents/extras/` and
   embed it from there; keep one copy.
4. Reduce `DefaultAgentConfigs` to fields not present in Markdown (categories,
   team eligibility) and derive the rest from the converted manifests.
5. Delete `ToolRestriction`; it duplicates `permission` denies.
6. Remove `sisyphus-junior` from the published pack metadata, `categories.json`,
   and `TeamEligibility`; do not create an AgentHub asset for it. If the source
   orchestrator still needs the routing name, keep it internal and map it to
   `pirat-task-agent` at runtime.
7. Keep `pirat-task-agent` as the only canonical published agent for that role.

### 5.3 CI guard

```bash
agenthub import-v1 --source agents --namespace pilab --out build/agents --check
```

`--check` exits non-zero if regenerating would change any file. This replaces
"remember to update the copy" with a build failure.

---

## 6. Publish pipeline

### 6.1 Local dry run

```bash
# 1. Convert V1 Markdown to bundles
agenthub import-v1 --source agents --namespace pilab --out build/agents

# 2. Validate every bundle
for d in build/agents/*/; do
  agenthub validate --dir "$d"
done

# 3. Pack and sign
for d in build/agents/*/; do
  agenthub pack --dir "$d" --out dist/
done
agenthub sign --key "$AGENTHUB_SIGNING_KEY" dist/*.tar.zst

# 4. Server-side dry run
agenthub publish --dry-run --namespace pilab dist/*.tar.zst
```

### 6.2 CI job

```yaml
publish-agents:
  when: tag ^agents/v
  steps:
    - run: agenthub import-v1 --source agents --namespace pilab --out build/agents --check
    - run: agenthub validate --recursive build/agents
    - run: agenthub pack --recursive build/agents --out dist
    - run: agenthub publish --namespace pilab --token "$AGENTHUB_TOKEN" dist/*
    - run: agenthub publish --namespace pirat --token "$AGENTHUB_TOKEN" dist/packs/*
```

Rules:

- CI authenticates with a Shadow SSO service account (`private_key_jwt`), scoped
  to `agenthub.publish` and the `pilab`/`pirat` publisher role.
- The signing key is a CI secret; the public key is registered once by a
  namespace owner.
- Publishing is idempotent per `(asset, version, manifestDigest)`. Re-running a
  failed pipeline is safe; changing content requires a new version.
- No `latest` tags are published manually; the hub's `latest` pointer is derived
  from published versions and channels.
- A failed signature or validation stops the job; there is no `--force`.

### 6.3 Versioning

| Change | Version bump |
| --- | --- |
| Prompt wording only | patch |
| Description, tags, guides | patch |
| Permission change | minor (and a policy-diff review) |
| Mode/model policy change | minor |
| Breaking prompt contract or rename | major |
| Pack membership change | minor |
| New agent added to pack | minor |

Start every imported agent at `1.0.0`; start the pack at `1.0.0`. The first
publish is a baseline, not a claim of maturity.

### 6.4 Rollback

- A bad version is **deprecated** (still installable, discouraged) or **yanked**
  (hidden, existing locks keep working during retention).
- Never delete a published version.
- Publish a fixed version and update the pack constraint; consumers get it on
  `agenthub update`.
- The store shows the deprecation reason and the replacement version.

---

## 7. Store presentation

For each imported agent, the store must show:

- Publisher (`pilab`) and signature verification.
- Exact permission rules, with `shell: *` flagged red.
- Required MCP servers and their install state.
- Model preferences and the fallback behavior.
- Token estimate of the prompt and instruction assets.
- Source repository and the originating file path (`agents/oracle.md`).
- Compatibility range (`opencode >=2.2 <3`).

The pack page shows the union of member permissions and a per-agent breakdown.
The `pirat-agents` pack will therefore display unrestricted shell access; that
is accurate and should not be hidden behind a "verified" badge.

---

## 8. Migration checklist

- [ ] Freeze `agents/*.md` as the canonical source.
- [ ] Add `agenthub.bundle.yaml` generation (`import-v1`) with golden tests.
- [ ] Convert all 13 agents; verify permission parity manually for `oracle`,
      `code-reviewer`, `momus`, and `pirat-task-agent`.
- [x] Decided: no `sisyphus-junior` asset; `pirat-task-agent` is canonical.
- [ ] Publish `pilab/tool/codegraph` and `pilab/tool/pirat-mcp` (or mark them as
      external prerequisites with install guides).
- [ ] Publish `pilab/skill/pirat-agent-orchestration@1.0.0`.
- [ ] Publish `pilab/prompt/pirat-project-instructions@1.0.0`.
- [ ] Publish the 13 agents as `pilab/agent/*@1.0.0`.
- [ ] Publish `pirat/pack/pirat-agents@1.0.0`.
- [ ] Convert PiMail `AGENTS.md` to `pimail/prompt/project-instructions@0.1.0`
      (pimail namespace only; private visibility).
- [ ] Install the pack on a clean machine with `agenthub add` and confirm all 13
      agents register in `opencode serve`.
- [ ] Verify offline install from cache.
- [ ] Add the CI drift check and the publish job.
- [ ] Remove the duplicated embedded agents and `ToolRestriction`, or generate
      them from the canonical source.

---

## 9. Acceptance tests

| Test | Assertion |
| --- | --- |
| Convert `oracle.md` | Output matches the golden manifest byte-for-byte |
| Convert all agents | 13 bundles, unique names, valid SemVer, all validate |
| Permission parity | Every V1 allow has a V2 counterpart; `.env` guards present |
| Order preservation | `"*": deny` is first; source order otherwise preserved |
| Pack resolution | `pirat/pack/pirat-agents@1.0.0` resolves all 15 members |
| Install | `agenthub add` writes a lockfile with 15 assets and origin digests |
| Runtime | OpenCode `/api/agent` lists all 13 agents with the expected modes |
| Permissions | `pilab-oracle` starts with deny-all; `shell` is allowed |
| Offline | `agenthub install --frozen --offline` succeeds from cache |
| Drift | `import-v1 --check` fails when the embedded copy differs |
| PiMail | Instruction asset registers and is injected once per request |

---

## 10. Open questions

Resolved during review and recorded in the checklist and the decision log:

- No `sisyphus-junior` asset is published; `pirat-task-agent` is canonical.
- PiMail instructions stay `pimail`-scoped and private; they are not a public
  reusable prompt.
- Imported `shell` permissions are preserved in v1; tightening is deferred.

Remaining:

1. Where do the MCP server manifests live: in `pirat-backend` or a shared
   `pilab/tools` repository? Current: `pirat-backend` for `pirat-mcp`,
   `codegraph` published from its own repository.
