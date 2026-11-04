# CLI and OpenCode Plugin

This document defines the two consumer-side components:

- **`agenthub` CLI** — the only component that mutates local state.
- **`@pilab/opencode-agenthub`** — the OpenCode V2 plugin that turns installed
  state into live agents, skills, MCP servers, commands, and tools.

The split is deliberate: one installer, one runtime adapter. The plugin never
downloads or installs anything; the CLI never registers anything at runtime.

---

## 1. Responsibilities

| Concern | CLI | Plugin |
| --- | --- | --- |
| Authenticate | Device flow, keychain, service-account key | Uses no user token; reads local state only |
| Resolve versions | Yes (server-side resolver) | No |
| Download/verify | Yes | Verifies cache before use |
| Write `agenthub.json` | Yes (`add`, `remove`) | No |
| Write `agenthub.lock.json` | Yes | No |
| Populate cache | Yes | Reads only |
| Register agents/skills/MCP | No | Yes |
| Materialize files (portable) | Yes | No |
| Publish | Yes | No |
| Update checks | Yes (`update --check`) | Optional notification only |
| Mutations from inside OpenCode | — | Delegates to the CLI, opt-in |

Non-negotiable rules:

1. **The plugin performs no network installs.** It may query the catalog for
   search/info when the user explicitly asks, but it never writes the cache or
   the lockfile.
2. **The CLI never edits `opencode.json`** in managed mode. The only exception
   is an explicit `agenthub plugin install`, which adds one entry to the
   `plugins` array (see §9).
3. **No install hooks.** Verification is hashing and signature checking, not
   code execution.
4. **No silent updates.** A newer version is reported, never installed.

---

## 2. CLI surface

Binary: `agenthub` (Go, Cobra). Module: `go.pilab.hu/agenthub/cmd/agenthub`.

### 2.1 Global flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `--config <path>` | `~/.config/agenthub/config.yaml` | Global config |
| `--registry <url>` | from config | Override registry for one command |
| `--scope project\|global` | `project` | Which desired/lock state to touch |
| `--json` | off | Machine-readable output (stable schema) |
| `--offline` | off | Never touch the network; fail if the cache is incomplete |
| `--frozen` | off | Require lockfile agreement; no resolution changes |
| `--allow-unverified` | off | Accept unsigned assets (policy must also allow) |
| `--token <jwt>` | — | Explicit access token (prefer `AGENTHUB_TOKEN` or the keychain) |
| `--mode managed\|portable` | `managed` | Write behavior |
| `--verbose` | off | Debug logging to stderr |
| `--no-color` | auto | Disable ANSI colors |

Exit codes:

| Code | Meaning |
| --- | --- |
| `0` | Success |
| `1` | Generic failure |
| `2` | Usage error |
| `3` | Not authenticated / token expired |
| `4` | Resolution failure (no version satisfies constraints) |
| `5` | Verification failure (digest/signature/policy) |
| `6` | Network unavailable and cache incomplete |
| `7` | Lockfile drift (`--frozen` or `verify`) |
| `8` | Permission/policy denial |

### 2.2 Command reference

#### Authentication

```bash
agenthub login                     # device flow; prints code + URL
agenthub login --no-browser        # print only, do not open a browser
agenthub logout                    # revoke refresh token, clear keychain
agenthub whoami                    # subject, email, scopes, namespace roles
agenthub token                     # print a short-lived access token (for scripts)
agenthub token --service-account ./sa-key.json  # CI: exchange a service-account key
```

`login` behavior:

1. Read `client_id` from config (default `agenthub-cli`).
2. POST to `/oauth2/device_authorization` with `scope=openid profile email agenthub.read agenthub.publish`.
3. Print `verification_uri_complete` and `user_code`; open the browser unless `--no-browser`.
4. Poll `/oauth2/token` with `grant_type=urn:ietf:params:oauth:grant-type:device_code`
   at the server-provided interval, honoring `slow_down` and `expires_in`.
5. Store the refresh token in the OS keychain; keep the access token in memory.
6. Fail with exit code `3` on `access_denied` or `expired_token`.

CI alternative:

```bash
export AGENTHUB_TOKEN="$(agenthub token --service-account ./sa-key.json)"
agenthub publish --token "$AGENTHUB_TOKEN"
```

#### Discovery

```bash
agenthub search "code review" --kind agent --limit 20
agenthub search --namespace pirat --tag orchestration
agenthub info pilab/agent/code-reviewer
agenthub info pilab/agent/code-reviewer@1.4.0 --json
agenthub versions pilab/agent/code-reviewer
agenthub guides pilab/pack/pirat-agents --harness opencode --scope project
```

`info` shows: publisher, signature status, permissions (exact rules), dependencies,
required secrets, compatibility, install size, and the source repository.

#### Desired state

```bash
agenthub add pilab/agent/code-reviewer@^1.4
agenthub add pilab/pack/pirat-agents@1.0.0 --scope project
agenthub add pilab/tool/codegraph --no-install
agenthub remove pilab/agent/code-reviewer
agenthub enable pilab/agent/oracle
agenthub disable pilab/agent/oracle
agenthub list
```

- `add` writes the desired state, resolves, verifies, updates the lockfile, and
  installs into the cache (unless `--no-install`).
- `remove` removes the entry from desired state and prunes the lockfile but does
  not delete cached blobs (run `agenthub gc`).
- `enable`/`disable` flip `enabled` without removing the pin.

#### Reconciliation

```bash
agenthub install                   # reconcile desired state -> lock + cache
agenthub install --frozen          # exact lockfile, no changes allowed
agenthub install --offline         # cache only
agenthub update                    # re-resolve within constraints
agenthub update pilab/agent/code-reviewer --latest
agenthub lock                      # resolve and write lockfile without downloading
agenthub verify                    # re-hash cache, re-check signatures and lock
agenthub status                    # desired vs locked vs cached vs runtime
agenthub status --json
agenthub diff                      # show pending changes and policy diffs
agenthub gc --older-than 30d
```

`status` is the operational truth. Example:

```text
PROJECT  /Users/alice/src/app
MODE     managed
REGISTRY agenthub.pilab.hu

ASSET                                      VERSION  STATE     SIGNATURE  RUNTIME
pilab/agent/code-reviewer                  1.4.2    ok        verified   registered
pilab/skill/pirat-agent-orchestration      2.1.0    ok        verified   registered
pilab/tool/codegraph                       1.0.3    ok        verified   disabled (missing CODEGRAPH_TOKEN)
pirat/pack/pirat-agents                    1.2.0    update    verified   —

3 assets, 1 update available, 1 missing secret.
Run `agenthub update` to apply, `agenthub diff` to preview.
```

#### Authoring and publishing

```bash
agenthub init                                  # scaffold agenthub.bundle.yaml
agenthub validate                              # schema + file + permission checks
agenthub pack                                  # build manifest + bundle tarball
agenthub sign --key ~/.config/agenthub/keys/pilab.ed25519
agenthub publish --namespace pilab
agenthub publish --dry-run                     # server-side ValidateBundle
agenthub draft list --namespace pilab          # web-created drafts
agenthub draft pull <draft-id> --out ./my-agent
agenthub publish --draft <draft-id> --key ~/.config/agenthub/keys/pilab.ed25519
agenthub yank pilab/agent/code-reviewer@1.4.2 --reason "leaked token"
agenthub deprecate pilab/agent/code-reviewer@1.4.0 --note "use 1.4.2"
```

`publish --draft` fetches an owner-visible web draft, builds the bundle, signs
the manifest locally, uploads it, and marks the draft published. It is the
client-signed fallback when a namespace has no hosted signing key.

`validate` is safe to run in pre-commit. `pack` is deterministic: sorted paths,
fixed mtime `0`, fixed uid/gid `0`, no compression timestamps.

#### Federation and offline transfer

```bash
agenthub export --hub agenthub.pilab.hu -o agenthub-export.tar.zst
agenthub import ./agenthub-export.tar.zst --verify
agenthub remote add internal https://hub.internal.example --mode mirror
agenthub remote sync internal
```

Details in [federation.md](federation.md).

#### Plugin bootstrap

```bash
agenthub plugin install            # add @pilab/opencode-agenthub to opencode.json
agenthub plugin install --global   # add to ~/.config/opencode/opencode.json
agenthub plugin status
agenthub plugin remove
```

`plugin install` is the only command that edits OpenCode's config, and it edits
only the `plugins` array. It is idempotent and preserves formatting where the
existing JSON is already canonical; otherwise it rewrites the file and prints a
diff.

### 2.3 `agenthub.json` precedence

1. Built-in defaults.
2. Global config: `~/.config/agenthub/config.yaml` (registries, policy, cache).
3. Global desired state: `~/.config/agenthub/agenthub.json`.
4. Project desired state: `<repo>/agenthub.json`.
5. Environment: `AGENTHUB_REGISTRY`, `AGENTHUB_TOKEN`, `AGENTHUB_CACHE`,
   `AGENTHUB_OFFLINE`.
6. Flags.

Project entries replace global entries with the same asset ID. A project
`enabled: false` disables a globally enabled asset in that project only.
Lockfiles remain separate per scope; the plugin applies global first, then
project.

### 2.4 Repository discovery

Commands that operate on a project find the project root by walking up from the
current directory until one of these exists:

```text
agenthub.json
agenthub.lock.json
.git/
```

If none is found, project-scoped commands fail with a clear error and suggest
`--scope global`. This prevents accidental writes into `$HOME`.

---

## 3. Install algorithm

`agenthub add` and `agenthub install` share one reconciler.

```text
1. Acquire an exclusive lock on the target lockfile (`.agenthub.lock`).
2. Load global and project desired state; merge with project precedence.
3. Build the resolve request:
   - desired refs + constraints
   - existing lock pins (unless --update or --latest)
   - harness/platform compatibility (OpenCode version if detectable)
4. Call CatalogService.Resolve.
5. For each resolved asset, in dependency order:
   a. If manifestDigest is in the cache: verify manifest signature.
   b. Else if offline: fail with exit 6.
   c. Else: fetch manifest + bundle URLs, download to a staging file.
   d. Verify bundleDigest, then every file hash.
   e. Verify the signature against the active namespace key.
   f. Extract into a staging directory, then rename into the cache.
6. Run policy checks (signature, denied permissions, conflicts, secrets).
7. Write the lockfile atomically (temp + fsync + rename).
8. In portable mode, materialize into `.opencode/` (see §5.2).
9. Print a summary, required secrets, and the install guide.
```

Properties:

- **Idempotent.** Re-running with the same inputs produces byte-identical
  lockfiles (timestamps are taken from the lock, not the clock, unless changed).
- **Crash-safe.** Staging directories are removed on the next run. The lockfile
  is only replaced after every artifact verifies.
- **Parallel downloads.** Blobs download with bounded concurrency (default 4);
  extraction and verification are ordered by dependency.
- **Cancellable.** `Ctrl-C` leaves the previous lockfile intact.

### 3.1 Resolution failures

| Situation | Behavior |
| --- | --- |
| No version satisfies constraints | Exit 4; list candidates and why each was rejected |
| Two registries provide the same shorthand | Exit 4; require `--registry` or a full hub URN |
| Pack pin conflicts with a top-level exact pin | Exit 4; show both requesters |
| Cycle in packs | Exit 4; print the full cycle |
| Compatibility mismatch | Exit 4; show required vs running harness version |
| Signature invalid | Exit 5; never fall back to unsigned |
| Cache corrupted | Exit 5; print the file and `agenthub verify --repair` hint |

`--json` errors use a stable envelope:

```json
{
  "error": {
    "code": "RESOLUTION_CONFLICT",
    "message": "pilab/agent/code-reviewer@2.0.0 conflicts with pack pin ^1.4.0",
    "details": [
      { "asset": "pilab/agent/code-reviewer", "requestedBy": "pilab/pack/pirat-agents@1.0.0", "constraint": "^1.4.0" },
      { "asset": "pilab/agent/code-reviewer", "requestedBy": "agenthub.json", "constraint": "2.0.0" }
    ]
  }
}
```

---

## 4. Verification

Every install and every plugin cold start performs the same checks, in order:

```text
1. Lockfile schema is valid and not hand-edited into an impossible state.
2. Manifest digest matches the canonical manifest bytes.
3. Manifest signature verifies with an active key for the namespace.
4. Key was valid at signedAt and has not been revoked.
5. Bundle digest matches the downloaded bundle.
6. Every manifest file exists in the extracted tree with the exact SHA-256 and size.
7. No extra files exist in the extracted tree.
8. Permissions still satisfy local policy (deny-list, required signature).
```

Caching the expensive parts:

- The plugin stores `{manifestDigest, extractedPath, treeDigest, verifiedAt}` in
  OpenCode plugin storage.
- On startup, it re-stats the extracted files and re-verifies only when mtime,
  size, or inode changed. A full verification runs when the lockfile changes or
  `agenthub verify` is invoked.
- Signature verification is cheap (Ed25519) and always runs; it is never cached
  across a lockfile change.

`agenthub verify --repair` re-downloads missing or corrupt blobs from the origin
hub when online, otherwise fails with exit 5.

---

## 5. Managed vs portable mode

| Aspect | Managed (default) | Portable |
| --- | --- | --- |
| Writes into project | `agenthub.json`, `agenthub.lock.json` only | Plus `.opencode/agents`, `.opencode/skills`, `.opencode/commands` |
| OpenCode plugin required | Yes | No (for materialized asset kinds) |
| Config edits | None | None; prints instructions for `opencode.json` if needed |
| Runtime updates | Plugin re-registers on lockfile change | Requires `agenthub install` and an OpenCode restart |
| Collision risk | Low (runtime IDs are namespaced) | Medium (files live in the repo) |
| Best for | OpenCode V2 users | CI images, non-plugin harnesses, vendored repos |

### 5.1 Managed mode

The plugin reads:

```text
<project>/agenthub.json
<project>/agenthub.lock.json
~/.config/agenthub/agenthub.json
~/.config/agenthub/agenthub.lock.json
~/.cache/agenthub/...
```

It registers assets at runtime. Nothing appears in `.opencode/` except files the
user already had. `agenthub remove` takes effect on the next lockfile watch
event or OpenCode restart.

### 5.2 Portable mode

`agenthub install --mode portable` materializes:

```text
.opencode/
├── agents/
│   └── pilab-code-reviewer.md
├── skills/
│   └── pirat-agent-orchestration/
│       ├── SKILL.md
│       └── references/...
├── commands/
│   └── mail-triage.md
└── agenthub/
    ├── instructions/
    │   └── pirat-project-instructions.md
    ├── manifest.json                  # what was materialized, with digests
    └── README.md                      # generated; do not edit
```

Rules:

- Generated files carry a header: `<!-- generated by agenthub; do not edit -->`.
- A file is overwritten only if the previous content matches the recorded digest;
  otherwise the CLI refuses and reports drift.
- Agent frontmatter is converted to OpenCode V2's `permissions:` array.
- Skills are copied as complete directories; the `SKILL.md` name must match the
  directory.
- Instruction prompts are **not** auto-added to `opencode.json`. The CLI prints
  the exact snippet:

```json
{
  "instructions": [".opencode/agenthub/instructions/pirat-project-instructions.md"]
}
```

- Switching back to managed mode leaves materialized files in place but reports
  them as unmanaged in `agenthub status`; `agenthub clean --portable` removes
  files whose digests still match the manifest.

---

## 6. OpenCode V2 mapping

Verified against OpenCode **2.2.5** and `@opencode/plugin` 2.2.5. The adapter
uses only documented context domains and fails closed when a domain is absent.

### 6.1 Capability detection

```ts
const caps = {
  agents: !!ctx.agent?.transform,
  skills: !!ctx.skill?.transform,
  mcp: !!ctx.mcp?.transform,
  commands: !!ctx.command?.transform,
  tools: !!ctx.tool?.transform,
  instructions: !!ctx.session?.hook,
  models: !!ctx.model?.list,
} as const
```

If a required capability is missing, the plugin logs a structured error and
registers what it can. It never writes files as a fallback in managed mode; the
user is told to use `--mode portable`.

### 6.2 Agent mapping

Canonical asset → `Agent.Info`:

| Manifest | `Agent.Info` | Notes |
| --- | --- | --- |
| `namespace` + `name` | `id` = `<namespace>-<name>` | Stable runtime ID |
| `displayName` / `name` | `name` | Display label |
| `spec.agent.prompt` | `system` | File contents, not a path |
| `summary` | `description` | One line |
| `spec.agent.mode` | `mode` | `primary`/`subagent`/`all` |
| `spec.agent.hidden` | `hidden` | Default `false` |
| `spec.agent.color` | `color` | Optional |
| `spec.agent.steps` | `steps` | Optional |
| `permissions` | `permissions` | Prefixed with deny-all |
| `spec.agent.modelPolicy` | `model` | Structured `{ providerID, id, variant? }`; first available preference, else unset |
| `spec.agent.request` | `request` | Merged over V2 defaults; version-dependent, advisory |

Registration:

```ts
const ownedRecord = (await ctx.storage.get("agenthub/owned-agents")) as { ids?: string[] } | undefined
const owned = new Set(ownedRecord?.ids ?? [])

const registration = await ctx.agent.transform((editor) => {
  const existing = editor.get(agentID)
  if (existing && !owned.has(agentID)) {
    // Do not overwrite a user-defined agent with the same ID.
    conflicts.push(agentID)
    return
  }
  editor.update(agentID, (agent) => {
    agent.name = displayName
    agent.description = summary
    agent.system = promptText
    agent.mode = mode
    agent.hidden = hidden
    agent.color = color
    agent.steps = steps
    agent.permissions = [
      { action: "*", resource: "*", effect: "deny" },
      ...manifest.permissions.filter((r) => !(r.action === "*" && r.resource === "*" && r.effect === "deny")),
    ]
    if (resolvedModel) agent.model = resolvedModel // { providerID: "opencode-go", id: "mimo-v2.5" }
  })
})
```

Important details:

- `editor.update(id, fn)` **creates the agent if it does not exist** (verified on
  2.2.5). There is no `add` method on `AgentEditor`.
- `Agent.Info` has no `managedBy` field. Ownership is tracked in plugin storage:
  the set of IDs this plugin created or adopted. An existing ID that is not in
  the set is treated as user-owned and is never modified.
- On first run, if an ID exists but is not in the ownership record, the plugin
  does not touch it. It reports a conflict instead.
- The ownership set is updated only after a successful transform, and it is
  pruned when an asset is removed from the lockfile.
- `permissions` must start with the explicit deny-all. The manifest's own
  deny-all is removed first to avoid duplication.
- An agent whose `modelPolicy.fallback` is `none` and whose models are all
  unavailable is not registered. `agenthub status` reports `missing-model`.

### 6.2.1 Ownership and collisions across domains

Every adapter follows the same rule: **the plugin only mutates entries it owns.**
Ownership is recorded per domain in plugin storage:

| Domain | Storage key | Collision behavior |
| --- | --- | --- |
| Agent | `agenthub/owned-agents` | Skip, report `conflict` |
| Skill | `agenthub/owned-skills` | Skip, report `conflict` |
| MCP | `agenthub/owned-mcp` | Skip, report `conflict` |
| Command | `agenthub/owned-commands` | Skip, report `conflict` (`add` may throw on duplicate) |
| Tool | `agenthub/owned-tools` | Skip, report `conflict` |

An existing entry that is not in the ownership record is treated as user-owned
and is never modified. Registration code checks the target domain's current
entries before applying a transform (`editor.get` where available, otherwise the
domain's `list()`).

Conflicts are visible in `agenthub status`. For agents, `agentIdPrefix` changes
the runtime ID; for other domains the namespaced ID is already unique unless the
user created an entry with the same name. The plugin never resolves a conflict
by overwriting.

### 6.3 Skill mapping

`Skill.Info` requires `id`, `name`, `path`, and `content`.

```ts
await ctx.skill.transform((editor) => {
  editor.add({
    id: `${namespace}-${name}`,
    name,
    description: frontmatter.description,
    autoinvoke: manifest.spec.skill.autoInvoke ?? true,
    path: absolutePathToSkillMd,
    content: skillMdBody,
  })
})
```

- Supporting files are resolved relative to `path`; managed mode requires the
  extracted cache tree to stay on disk until OpenCode exits.
- `agenthub gc` never removes a tree referenced by a lockfile, even if it is old.
- If the installed OpenCode version lacks `skill.transform.add`, the plugin falls
  back to the HTTP skills catalog only when the skill is public; otherwise it
  reports `unsupported-skill-api`.
- The ownership rule in §6.2.1 applies: an existing skill ID that the plugin does
  not own is skipped, not replaced.

### 6.4 MCP mapping

```ts
await ctx.mcp.transform((editor) => {
  editor.set(name, canonicalToMcpServerConfig(toolSpec.mcp))
})
```

Mapping:

| Manifest | `Mcp.ServerConfig` |
| --- | --- |
| `type: mcp-local` | `{ type: "local", command, environment, disabled }` |
| `type: mcp-remote` | `{ type: "remote", url, headers, disabled }` |
| `secrets[]` | Environment placeholders resolved at registration time |

- If a required secret is missing, set `disabled: true` and record a warning.
  Never register a remote server with a literal `${env:...}` header.
- Local MCP servers are not spawned by the plugin; OpenCode owns the process.
- The ownership rule in §6.2.1 applies: an existing MCP server with the same name
  that the plugin does not own is skipped, not replaced.
- MCP changes take effect immediately in V2; no restart is required.
- A tool of type `opencode-plugin` cannot be registered through `ctx.mcp`. The
  CLI adds it to `opencode.json`'s `plugins` array and marks the change as
  requiring a restart.

### 6.5 Prompt mapping

| `target` | Runtime mapping |
| --- | --- |
| `command` | `ctx.command.transform(editor => editor.add({ name, description, execute }))` |
| `instruction` | `ctx.session.hook("context", ...)` appends a system part for each enabled instruction asset |
| `system` | `ctx.session.hook("context", ...)` appends a system part, scoped by the asset's declared agents |

Command registration wraps the template:

```ts
await ctx.command.transform((editor) => {
  editor.add({
    name: asset.name,
    description: asset.summary,
    async execute(invocation) {
      const rendered = renderTemplate(promptTemplate, invocation.prompt.text)
      await ctx.session.prompt({
        sessionID: invocation.sessionID,
        text: rendered,
        files: invocation.prompt.files,
        delivery: invocation.delivery,
      })
    },
  })
})
```

Notes:

- `CommandEditor` only exposes `add`; removal is handled by disposing the
  registration returned by `transform`.
- The ownership rule in §6.2.1 applies: a command name that exists and is not in
  `agenthub/owned-commands` is skipped. `ctx.command.list()` is checked before
  the transform because the editor cannot read individual entries.
- `ctx.session.instructions.entry.put` is per-session and experimental. The
  plugin does **not** use it as a global instruction mechanism. Instruction
  assets are appended through the `context` hook, which applies to every model
  request in every session.
- Instruction text is cached and injected by reference with a `<!-- agenthub:... -->`
  marker; the plugin never injects the same asset twice in one request.

### 6.6 Plugin-provided tools

The plugin registers four tools through `ctx.tool.transform`:

| Tool | Mutates | Purpose |
| --- | --- | --- |
| `agenthub_search` | No | Search the configured hub(s) |
| `agenthub_info` | No | Read manifest, permissions, guides from the lock/cache |
| `agenthub_status` | No | Desired vs locked vs cached vs registered |
| `agenthub_add` | Yes, opt-in | Invoke the CLI to add an asset |

Tool names are reserved under the `agenthub_` prefix. If a tool with one of these
names already exists, the plugin skips registration and reports a conflict
(§6.2.1); it never replaces another plugin's tool.

`agenthub_add` is disabled unless the plugin option `allowMutations: true` is
set and the OpenCode permission rule allows `agenthub_add`. It shells out to the
`agenthub` binary with a fixed argument vector:

```ts
const proc = Bun.spawn(["agenthub", "add", ref, "--json"], {
  cwd: ctx.location.directory,
  env: { ...process.env, AGENTHUB_NONINTERACTIVE: "1" },
  stdout: "pipe",
  stderr: "pipe",
})
```

No user-provided string is interpolated into a shell. The ref is validated
against the asset-ref grammar before spawning.

Tool results are small JSON documents. Full manifests are never dumped into the
model context; `agenthub_info` returns a summary and a path.

### 6.7 Plugin options

```json
{
  "plugins": [
    {
      "package": "@pilab/opencode-agenthub",
      "options": {
        "lockfile": "agenthub.lock.json",
        "config": "agenthub.json",
        "allowMutations": false,
        "requireSignature": true,
        "allowUnverified": false,
        "checkForUpdates": "daily",
        "agentIdPrefix": ""
      }
    }
  ]
}
```

| Option | Default | Meaning |
| --- | --- | --- |
| `lockfile` | `agenthub.lock.json` | Project lockfile path, relative to the project root |
| `config` | `agenthub.json` | Project desired state, relative to the project root |
| `allowMutations` | `false` | Enable `agenthub_add` |
| `requireSignature` | `true` | Refuse unsigned assets |
| `allowUnverified` | `false` | Allow unsigned only if the lockfile says so |
| `checkForUpdates` | `daily` | `off`, `daily`, `always` |
| `agentIdPrefix` | `""` | Prefix for runtime IDs (e.g. `hub-`) |

Options are validated; unknown keys produce a warning, not a crash.

---

## 7. Plugin lifecycle

```text
setup(ctx)
  ├─ read options + project/global config
  ├─ resolve project root via ctx.location.project.directory
  ├─ load lockfile(s) (project overrides global)
  ├─ verify cache (fast path; full verify on change)
  ├─ build an install plan: MCP -> skills -> commands -> agents -> instructions
  ├─ apply transforms, storing each Registration
  ├─ start lockfile watcher
  ├─ optionally start a background update check
  └─ return cleanup()

cleanup()
  ├─ stop watcher and background tasks
  ├─ dispose registrations in reverse order
  └─ persist ownership + verification metadata
```

### 7.1 Startup order

Dependencies register before dependents:

1. **MCP servers** — agents may reference them by name.
2. **Skills** — agents may reference them in their prompts.
3. **Commands** — independent, but cheap.
4. **Agents** — after their tools and skills exist.
5. **Instruction hooks** — last, so they can reference agent metadata.

If one asset fails, the plugin continues with the rest and records a structured
error. A failed dependency skips its dependents: for example, if a required MCP
server cannot be registered because its secret is missing, the MCP server is
registered as `disabled` and the dependent agent is still registered. The agent
prompt is never rewritten to add warnings — changing prompts based on local
state would make behavior non-reproducible. `agenthub status` reports the
degraded dependency.

### 7.2 Lockfile watching

- Watch the project lockfile and global lockfile with `fs.watch` (debounced
  250 ms).
- On change: compare `generation` (hash of lockfile contents). If unchanged,
  ignore.
- Dispose all registrations, reload and verify only changed assets, re-register.
- Never watch the cache tree; external mutation is detected by verification, not
  by watching.
- If a reload fails, keep the previous registrations and report the error. A
  broken install must not remove a working agent set.

### 7.3 Update checks

- At most once per `checkForUpdates` interval, call `CatalogService.Resolve` with
  the current lock pins and `update: false`.
- If newer versions satisfy the constraints, record `updateAvailable` in plugin
  storage and expose it through `agenthub_status`.
- No toast, no prompt, no auto-install. OpenCode's UI is not the place to make
  supply-chain decisions.
- `checkForUpdates: off` disables all network access from the plugin except
  explicit tool calls.

### 7.4 Shutdown and reload

- `cleanup()` disposes every transform registration. Disposal removes exactly the
  entries the plugin added and leaves user-defined agents/skills/MCP untouched.
- The ownership record is the source of truth for what may be removed.
- If OpenCode crashes, the record remains; the next startup reconciles it against
  the current lockfile.

---

## 8. Local state and cache

### 8.1 Config

`~/.config/agenthub/config.yaml`:

```yaml
registries:
  - name: pilab
    url: https://agenthub.pilab.hu
    hub: agenthub.pilab.hu
    trust: required
default_namespace: pilab
cache_dir: ~/.cache/agenthub
mode: managed
policy:
  requireSignature: true
  allowUnverified: false
  denyPermissions:
    - "shell:rm -rf *"
    - "external_directory:*"
```

### 8.2 Desired state

```json
{
  "schemaVersion": 1,
  "registries": ["https://agenthub.pilab.hu"],
  "assets": [
    { "id": "pilab/pack/pirat-agents", "version": "^1.0.0", "enabled": true }
  ],
  "policy": { "requireSignature": true }
}
```

### 8.3 Lockfile

Defined in [domain-model.md](domain-model.md#83-lockfile). The CLI is the only
writer. The plugin treats it as read-only and refuses to start if it is invalid.

Lockfile generation is deterministic:

- Assets sorted by full URN.
- Files sorted by path.
- No absolute paths, timestamps other than `generatedAt`, or machine-specific
  values.
- `resolver` records the CLI version; `generatedAt` is the only volatile field.

### 8.4 Cache

```text
~/.cache/agenthub/
├── blobs/sha256/...
├── bundles/sha256/<bundleDigest>.tar.zst
├── manifests/sha256/<manifestDigest>.json
├── extracted/<manifestDigest>/...
└── metadata/
```

- Extracted trees are read-only.
- `agenthub gc` uses reference counting from all known lockfiles plus a grace
  period; it never removes a tree referenced by a lockfile it can see.
- `agenthub verify --repair` re-fetches corrupt entries.
- Cache paths are never recorded in lockfiles, so the cache can be shared or
  moved.

---

## 9. Installing the plugin

Published as `@pilab/opencode-agenthub` (ESM, peer dependency
`@opencode/plugin >= 2.2 < 3`).

```bash
# add to the project's opencode.json
agenthub plugin install

# or add to the global config
agenthub plugin install --global
```

Result:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "plugins": ["@pilab/opencode-agenthub"]
}
```

With options:

```json
{
  "plugins": [
    {
      "package": "@pilab/opencode-agenthub",
      "options": { "allowMutations": false, "checkForUpdates": "daily" }
    }
  ]
}
```

Manual installation is supported and documented; the CLI command is a
convenience, not a requirement. OpenCode installs the npm package using its
normal plugin mechanism. AgentHub does not ship a `postinstall` script.

### 9.1 Version compatibility

| Plugin | OpenCode | Behavior |
| --- | --- | --- |
| `0.1.x` | `>=2.2 <3` | Full support for agents, skills, MCP, commands, tools |
| `0.2.x` | `>=2.3 <3` | Tracks API changes; same manifest schema |

At startup the plugin checks `ctx.app.version` against its compatibility range.
Out of range: log a warning and register read-only tools only.

---

## 10. Security

### 10.1 Threat model for the consumer side

| Threat | Control |
| --- | --- |
| Malicious hub serves a tampered bundle | Signature + digest verification; origin pinned in lock |
| Lockfile edited by hand | `agenthub verify` detects digest mismatch; `--frozen` refuses |
| Cache poisoned on disk | Full hash verification on change; read-only trees |
| Malicious agent overwrites a local agent | Namespaced IDs + ownership record; conflicts are reported, never overwritten |
| Permission escalation via update | Policy diff before registration; high-risk additions require explicit approval |
| Secret exfiltration | Only `${env:NAME}` resolution; no secrets in assets, lockfiles, or logs |
| Prompt injection via guides | Guides are rendered as text; commands are never auto-run |
| Supply-chain script | No install hooks; executable files are listed in the store and never run by the installer |
| Token leakage | Refresh token in OS keychain; access token in memory; no tokens in lockfiles or logs |

### 10.2 Permission-policy diff

When an update changes permissions, the CLI computes a diff:

```text
pilab/agent/code-reviewer 1.4.2 -> 2.0.0
  + shell "git push *"        (new)
  + external_directory "/etc" (new)
  - read "docs/**"            (removed)
Risk: elevated. This update requires --allow-permission-change.
```

Rules:

- New `deny` rules are always allowed.
- New `allow` rules of class `shell`, `network`, `secrets`, or `elevated`
  require `--allow-permission-change` in the CLI.
- In the plugin, an update with new high-risk allows is **not** registered until
  the user runs the CLI with the flag. The lockfile records the approval
  (`policy.approvedPermissions` digest).
- The plugin never prompts for this; approvals happen in the CLI, where the full
  diff is visible.

### 10.3 Secret handling

- Assets declare `secrets[].name` and optional `description`.
- Values are read from the process environment at registration time.
- `${env:NAME}` is resolved only for declared names; undeclared placeholders are
  a validation error.
- Missing required secrets disable the dependent MCP server and appear in
  `agenthub status`.
- `agenthub install` prints required secret names, never values.
- The plugin redacts any value that matches a known secret when logging.

### 10.4 Agent runtime isolation

AgentHub cannot sandbox an agent; OpenCode enforces permissions. What AgentHub
guarantees:

- Every hub agent starts from deny-all.
- Every permission is visible in the store and in `agenthub info`.
- The plugin registers the manifest rules verbatim, with only the deny-all prefix
  added.
- Global OpenCode allow rules cannot widen a hub agent beyond its manifest.

---

## 11. Offline behavior

| Operation | Offline behavior |
| --- | --- |
| `agenthub search/info` | Fails unless the asset is in the local metadata cache; prints cached results with a staleness warning |
| `agenthub install --frozen --offline` | Works if cache is complete |
| `agenthub install` (no `--frozen`) | Works if desired state and lock are unchanged; otherwise exit 6 |
| `agenthub update` | Fails with exit 6 |
| Plugin startup | Works from lock + cache; no network |
| Plugin update check | Skipped silently |
| `agenthub publish` | Fails with exit 6 |
| `agenthub verify` | Works fully offline |

The lockfile plus cache is a complete, self-contained install. This is a hard
requirement, tested in CI with networking disabled.

---

## 12. Implementation layout

### 12.1 Go packages

```text
cmd/agenthub/                 # Cobra entrypoint
internal/cli/
├── root.go
├── login.go
├── search.go
├── add.go
├── install.go
├── update.go
├── verify.go
├── status.go
├── draft.go
├── publish.go
├── pack.go
├── export.go
└── plugin.go
internal/install/
├── resolver.go               # server client + local pin reuse
├── downloader.go
├── verifier.go
├── cache.go
├── lockfile.go
├── policy.go
└── portable.go
internal/opencode/
├── config.go                 # safe read/modify of opencode.json
├── agents.go                 # V1 -> V2 frontmatter conversion
└── detect.go                 # version detection
pkg/manifest/
pkg/lockfile/
pkg/semver/
```

### 12.2 Plugin package

```text
clients/opencode-plugin/
├── package.json              # @pilab/opencode-agenthub
├── tsconfig.json
├── src/
│   ├── index.ts              # Plugin.define
│   ├── options.ts
│   ├── state.ts              # lockfile + config loading
│   ├── verify.ts             # digest + signature checks
│   ├── plan.ts               # dependency order
│   ├── adapters/
│   │   ├── agent.ts
│   │   ├── skill.ts
│   │   ├── mcp.ts
│   │   ├── command.ts
│   │   ├── instruction.ts
│   │   └── tool.ts
│   ├── tools/
│   │   ├── search.ts
│   │   ├── info.ts
│   │   ├── status.ts
│   │   └── add.ts
│   ├── watch.ts
│   └── log.ts
└── test/
    ├── unit/
    └── e2e/
```

Build: Bun, ESM only, no runtime dependencies except `@opencode/plugin` as a
peer. A single bundled `dist/index.js` keeps startup fast.

### 12.3 CLI/plugin contract

The only contract is the files:

- `agenthub.json` (schema-versioned)
- `agenthub.lock.json` (schema-versioned)
- the cache layout

The plugin does not import Go code and does not call the CLI except for the
opt-in `agenthub_add` tool. This keeps the plugin releasable independently.

---

## 13. Testing

### 13.1 CLI unit tests

- Manifest canonicalization (JCS) and digest stability.
- SemVer range parsing and pin reuse.
- Lockfile determinism: same inputs → byte-identical output.
- Path validation: traversal, absolute paths, symlinks, case collisions.
- Permission-policy diff classification.
- Portable-mode frontmatter conversion (golden files).
- `opencode.json` editing preserves unrelated keys and is idempotent.

### 13.2 Plugin unit tests

- Lockfile/config merging with project precedence.
- Verification fast path vs full path.
- Agent ID collision → skip + report, never overwrite.
- Deny-all prefix insertion and duplicate removal.
- Missing secret → MCP disabled, no placeholder leaked.
- Instruction hook injects once per asset per request.
- Disposal removes only owned registrations.

### 13.3 Contract tests against OpenCode

Run against pinned OpenCode versions in CI:

```bash
opencode serve --port 4099 --print-logs &
OPENCODE_PASSWORD=... opencode api get /api/agent --server http://127.0.0.1:4099
```

Assertions:

- The plugin loads without errors.
- `pilab-code-reviewer` appears in `/api/agent`.
- Its `permissions[0]` is the deny-all rule.
- Its `system` equals the manifest prompt.
- Removing the lockfile entry removes the agent after reload.
- A user-defined agent with the same ID is not modified.

This is the same harness used to verify that `editor.update` upserts agents on
OpenCode 2.2.5.

### 13.4 End-to-end

```text
docker compose up postgres s3 shadow-sso agenthub
agenthub publish testdata/packs/hello
agenthub add pilab/pack/hello@1.0.0 --registry http://localhost:8080
opencode serve --port 4099
curl /api/agent | jq '.[] | select(.id=="pilab-hello")'
```

Also test:

- Offline install from a pre-populated cache with networking disabled.
- Tampered bundle → exit 5, cache unchanged, lockfile unchanged.
- Revoked signing key → exit 5 with a key-revoked message.
- Lockfile drift → `verify` exit 7, plugin refuses to register.
- Missing secret → MCP disabled, agent still registered.

### 13.5 Golden files

Maintain fixtures under `testdata/golden/`:

```text
manifest/minimal-agent.json
manifest/full-pack.json
lockfile/basic.lock.json
portable/agent-frontmatter.md
portable/skill-tree/
```

CI fails on any unexpected diff in generated JSON or materialized files.

---

## 14. Failure modes and UX

| Failure | User sees | System does |
| --- | --- | --- |
| Not logged in | "Run `agenthub login`" | Exit 3; no partial writes |
| Token expired | CLI refreshes silently; if refresh fails, exit 3 | Plugin never needs a token |
| Registry down | Cached results + warning; install fails if cache incomplete | Exit 6 |
| Signature invalid | Asset name, key ID, expected/actual digest prefix | Exit 5; no cache write |
| Version conflict | Both requesters and constraints | Exit 4; no lock write |
| Missing secret | Secret name + which asset needs it | MCP disabled; status warning |
| Permission escalation | Full diff + required flag | No update until approved |
| Plugin API missing | "OpenCode X is not supported; use portable mode" | Register read-only tools only |
| Corrupt cache | File path + expected digest | Exit 5; suggest `verify --repair` |
| Lockfile hand-edited | Drift report | Exit 7; plugin refuses |

Logging:

- CLI logs to stderr; `--json` keeps stdout machine-readable.
- Plugin uses `ctx.app` metadata in every log line; no secrets, no tokens.
- Correlation IDs come from the hub response headers and are shown in errors.

---

## 15. Open questions

1. Should `agenthub_add` be allowed at all inside OpenCode, or should the plugin
   stay strictly read-only? Current default: disabled.
2. Should portable mode write a vendor manifest (`.opencode/agenthub/manifest.json`)
   into the repo? Current: yes, for drift detection; it is safe to commit.
3. Should the plugin expose one agent per mode (`primary`/`all`) or rely on
   OpenCode's mode field? Current: pass through the manifest mode.
4. How should instruction assets interact with the user's own `AGENTS.md`?
   Current: additive context hook, no file edits; the store recommends moving
   project-wide rules into a prompt asset, not duplicating them.
5. Do we need a `--locked` alias for `--frozen` to match other package managers?
   Current: no; one spelling.
