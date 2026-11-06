# @pilab/opencode-agenthub

AgentHub plugin for OpenCode V2 (server part).

## M2.5 minimal

On activation the plugin reads `agenthub.lock.json` from the project directory
(`ctx.location.directory`) and logs one line per locked asset:

```text
registered pilab/agent/code-reviewer@1.4.0
registered pilab/tool/codegraph@1.0.3
```

There is **no real registration yet** — no `ctx.agent` / `ctx.skill` /
`ctx.tool` / `ctx.mcp` calls. Logging the refs is the whole "registration" in
this milestone; the adapters, lockfile watching, and verification are specified
in `docs/architecture/cli-plugin.md` §6–§7 and land later.

The default export is a plain `{ id: "agenthub", setup }` object, which is
exactly what `Plugin.define()` from `@opencode/plugin` returns (`define` is an
identity function in 2.2.x). The `@opencode/plugin` dependency is deliberately
not installed yet: no runtime dependencies until the adapter uses the context
domains.

## Layout

```text
src/
├── index.ts      # plugin definition (setup + registerAssets)
├── lockfile.ts   # agenthub.lock.json reader (mirrors internal/lockfile)
└── log.ts        # Logger seam (console by default, injectable in tests)
test/
└── index.test.ts # bun:test
```

## Development

```sh
bun install   # dev deps only: @types/bun, typescript
bun test      # bun test
bun run build # tsc --noEmit
```

Load it locally with `"plugins": ["./plugins/opencode"]` in `opencode.json(c)`.

## Notes

- `src/lockfile.ts` is the M2.5 subset of the planned `state.ts` (lockfile +
  config loading) from `docs/architecture/cli-plugin.md` §12.2; the scaffold
  lives in `plugins/opencode/` rather than `clients/opencode-plugin/`.
