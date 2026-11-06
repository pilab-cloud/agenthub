/**
 * `@pilab/opencode-agenthub` — AgentHub plugin for OpenCode V2.
 *
 * M2.5 minimal scaffold: on activation the plugin reads `agenthub.lock.json`
 * from the project directory and logs `registered <ref>` for every locked
 * asset. It makes **no** OpenCode registration calls yet (`ctx.agent`,
 * `ctx.skill`, `ctx.tool`, …); the real adapters, lockfile watching, and
 * verification land later (see `docs/architecture/cli-plugin.md` §6–§7).
 *
 * The default export is a plain `{ id, setup }` object. That is exactly what
 * `Plugin.define()` from `@opencode/plugin` returns — `define` is an identity
 * function in 2.2.x — so OpenCode loads this module the same way it loads an
 * SDK-defined plugin. The `@opencode/plugin` dependency is intentionally not
 * pulled in yet (no runtime deps until the adapter uses the context domains).
 */
import { consoleLogger, type Logger } from "./log"
import { LOCKFILE_NAME, readLockfile, type LockedAsset, type LockedState } from "./lockfile"

/** Minimal slice of the OpenCode plugin context this plugin uses. */
export interface PluginContext {
  readonly location: {
    /** Directory of the location the plugin instance is loaded for. */
    readonly directory: string
  }
  /** Optional logger override; OpenCode's context does not provide one. */
  readonly logger?: Logger
}

export interface AgenthubPlugin {
  readonly id: string
  setup(ctx: PluginContext): Promise<void> | void
}

export interface PluginOptions {
  readonly logger?: Logger
}

/**
 * registerAssets logs one line per locked asset.
 *
 * M2.5 minimal: logging *is* the registration. When the real registration APIs
 * land, each `registered <ref>` line is replaced by (or followed by) the
 * corresponding `ctx.<domain>.transform` call.
 */
export function registerAssets(assets: readonly LockedAsset[], logger: Logger): void {
  for (const asset of assets) {
    logger(`registered ${asset.ref}`)
  }
}

/** Builds the plugin; the exported default uses the console logger. */
export function createAgenthubPlugin(options: PluginOptions = {}): AgenthubPlugin {
  return {
    id: "agenthub",
    async setup(ctx) {
      const logger = options.logger ?? ctx.logger ?? consoleLogger

      let locked: LockedState | undefined
      try {
        locked = await readLockfile(ctx.location.directory)
      } catch (err) {
        const reason = err instanceof Error ? err.message : String(err)
        logger(`agenthub: failed to read ${LOCKFILE_NAME}: ${reason}`)
        return
      }

      if (!locked) {
        logger(`agenthub: no ${LOCKFILE_NAME}; nothing to register`)
        return
      }

      registerAssets(locked.assets, logger)
    },
  }
}

export default createAgenthubPlugin()
