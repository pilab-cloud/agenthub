/**
 * Reading `agenthub.lock.json` from the project directory.
 *
 * The schema mirrors the Go CLI's `internal/lockfile` package (see
 * `docs/architecture/cli-plugin.md` §12.3: the lockfile is the only contract
 * between the CLI and this plugin). Parsing is deliberately lenient like the
 * Go reader — missing optional fields fall back to defaults — but rejects a
 * document that is not an object, has a non-array `assets`, or has an asset
 * without a `ref`.
 */
import { readFile } from "node:fs/promises"
import { join } from "node:path"

/** File name of the resolved state written by `agenthub add` / `install`. */
export const LOCKFILE_NAME = "agenthub.lock.json"

/** One pinned asset entry of `agenthub.lock.json`. */
export interface LockedAsset {
  readonly ref: string
  readonly origin_hub?: string
  readonly manifest_digest?: string
  readonly bundle_digest?: string
}

/** Top-level document of `agenthub.lock.json`. */
export interface LockedState {
  readonly lockfileVersion: number
  readonly generatedAt?: string
  readonly resolver?: string
  readonly assets: readonly LockedAsset[]
}

function optionalString(value: unknown, field: string, where: string): string | undefined {
  if (value === undefined || value === null) return undefined
  if (typeof value !== "string") {
    throw new Error(`invalid ${LOCKFILE_NAME}: ${where} ${field} must be a string`)
  }
  return value
}

/**
 * parseLockfile parses an `agenthub.lock.json` document.
 *
 * @throws Error when the document is not valid JSON or does not match the
 * lockfile shape. An absent `assets` array and a missing `lockfileVersion`
 * default to the same values the Go CLI writes (`[]` and `1`).
 */
export function parseLockfile(text: string): LockedState {
  let raw: unknown
  try {
    raw = JSON.parse(text)
  } catch (err) {
    throw new Error(`invalid ${LOCKFILE_NAME}: ${err instanceof Error ? err.message : String(err)}`)
  }
  if (typeof raw !== "object" || raw === null || Array.isArray(raw)) {
    throw new Error(`invalid ${LOCKFILE_NAME}: expected a JSON object`)
  }

  const doc = raw as Record<string, unknown>
  if (doc.assets !== undefined && !Array.isArray(doc.assets)) {
    throw new Error(`invalid ${LOCKFILE_NAME}: "assets" must be an array`)
  }

  const rawAssets: unknown[] = Array.isArray(doc.assets) ? doc.assets : []
  const assets = rawAssets.map((entry, index) => {
    const where = `asset #${index}`
    if (typeof entry !== "object" || entry === null || Array.isArray(entry)) {
      throw new Error(`invalid ${LOCKFILE_NAME}: ${where} must be an object`)
    }
    const asset = entry as Record<string, unknown>
    if (typeof asset.ref !== "string" || asset.ref === "") {
      throw new Error(`invalid ${LOCKFILE_NAME}: ${where} has no "ref"`)
    }
    return {
      ref: asset.ref,
      origin_hub: optionalString(asset.origin_hub, "origin_hub", where),
      manifest_digest: optionalString(asset.manifest_digest, "manifest_digest", where),
      bundle_digest: optionalString(asset.bundle_digest, "bundle_digest", where),
    } satisfies LockedAsset
  })

  const lockfileVersion =
    doc.lockfileVersion === undefined || doc.lockfileVersion === null ? 1 : doc.lockfileVersion
  if (typeof lockfileVersion !== "number") {
    throw new Error(`invalid ${LOCKFILE_NAME}: "lockfileVersion" must be a number`)
  }

  return {
    lockfileVersion,
    generatedAt: optionalString(doc.generatedAt, "generatedAt", "document"),
    resolver: optionalString(doc.resolver, "resolver", "document"),
    assets,
  }
}

/**
 * readLockfile loads `<directory>/agenthub.lock.json`.
 *
 * Returns `undefined` when the file does not exist (the plugin has nothing to
 * register); malformed files still throw so a broken lockfile is visible
 * instead of silently registering nothing.
 */
export async function readLockfile(directory: string): Promise<LockedState | undefined> {
  let text: string
  try {
    text = await readFile(join(directory, LOCKFILE_NAME), "utf8")
  } catch (err) {
    if (err instanceof Error && "code" in err && err.code === "ENOENT") {
      return undefined
    }
    throw err
  }
  return parseLockfile(text)
}
