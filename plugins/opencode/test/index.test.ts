import { describe, expect, test } from "bun:test"
import { mkdtemp, rm, writeFile } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join } from "node:path"
import plugin, { createAgenthubPlugin, registerAssets } from "../src/index"
import { LOCKFILE_NAME, parseLockfile } from "../src/lockfile"

/** Sample lockfile in the exact shape `agenthub add` writes (Go CLI). */
const SAMPLE_LOCKFILE = `{
  "lockfileVersion": 1,
  "generatedAt": "2026-11-04T10:00:00Z",
  "resolver": "agenthub/0.1.0",
  "assets": [
    {
      "ref": "pilab/agent/code-reviewer@1.4.0",
      "origin_hub": "agenthub.pilab.hu",
      "manifest_digest": "sha256:aaaa",
      "bundle_digest": "sha256:bbbb"
    },
    {
      "ref": "pilab/tool/codegraph@1.0.3"
    }
  ]
}`

async function projectWithLockfile(contents?: string): Promise<string> {
  const dir = await mkdtemp(join(tmpdir(), "agenthub-plugin-"))
  if (contents !== undefined) {
    await writeFile(join(dir, LOCKFILE_NAME), contents, "utf8")
  }
  return dir
}

describe("parseLockfile", () => {
  test("parses a sample lockfile", () => {
    const state = parseLockfile(SAMPLE_LOCKFILE)

    expect(state.lockfileVersion).toBe(1)
    expect(state.generatedAt).toBe("2026-11-04T10:00:00Z")
    expect(state.resolver).toBe("agenthub/0.1.0")
    expect(state.assets).toHaveLength(2)

    const [agent, tool] = state.assets
    expect(agent.ref).toBe("pilab/agent/code-reviewer@1.4.0")
    expect(agent.origin_hub).toBe("agenthub.pilab.hu")
    expect(agent.manifest_digest).toBe("sha256:aaaa")
    expect(agent.bundle_digest).toBe("sha256:bbbb")

    expect(tool.ref).toBe("pilab/tool/codegraph@1.0.3")
    expect(tool.origin_hub).toBeUndefined()
    expect(tool.manifest_digest).toBeUndefined()
  })

  test("defaults missing version and assets like the Go CLI", () => {
    const state = parseLockfile("{}")
    expect(state.lockfileVersion).toBe(1)
    expect(state.assets).toEqual([])
  })

  test("rejects malformed documents", () => {
    expect(() => parseLockfile("not json")).toThrow(/invalid agenthub\.lock\.json/)
    expect(() => parseLockfile("[1, 2]")).toThrow(/expected a JSON object/)
    expect(() => parseLockfile('{"assets": "nope"}')).toThrow(/"assets" must be an array/)
    expect(() => parseLockfile('{"assets": [{"origin_hub": "h"}]}')).toThrow(/has no "ref"/)
  })
})

describe("plugin setup", () => {
  test("registers every locked asset from the project directory", async () => {
    const dir = await projectWithLockfile(SAMPLE_LOCKFILE)
    const messages: string[] = []
    try {
      const p = createAgenthubPlugin({ logger: (m) => messages.push(m) })
      expect(p.id).toBe("agenthub")
      await p.setup({ location: { directory: dir } })
    } finally {
      await rm(dir, { recursive: true, force: true })
    }

    expect(messages).toEqual([
      "registered pilab/agent/code-reviewer@1.4.0",
      "registered pilab/tool/codegraph@1.0.3",
    ])
  })

  test("reports a missing lockfile instead of registering anything", async () => {
    const dir = await projectWithLockfile()
    const messages: string[] = []
    try {
      await createAgenthubPlugin({ logger: (m) => messages.push(m) }).setup({
        location: { directory: dir },
      })
    } finally {
      await rm(dir, { recursive: true, force: true })
    }

    expect(messages).toEqual(["agenthub: no agenthub.lock.json; nothing to register"])
  })

  test("reports a malformed lockfile without throwing", async () => {
    const dir = await projectWithLockfile("{ not json")
    const messages: string[] = []
    try {
      await createAgenthubPlugin({ logger: (m) => messages.push(m) }).setup({
        location: { directory: dir },
      })
    } finally {
      await rm(dir, { recursive: true, force: true })
    }

    expect(messages).toHaveLength(1)
    expect(messages[0]).toContain("failed to read agenthub.lock.json")
  })
})

describe("registerAssets", () => {
  test("logs one line per asset", () => {
    const messages: string[] = []
    registerAssets(parseLockfile(SAMPLE_LOCKFILE).assets, (m) => messages.push(m))
    expect(messages).toHaveLength(2)
    expect(messages[0]).toBe("registered pilab/agent/code-reviewer@1.4.0")
  })
})

describe("default export", () => {
  test("is the plugin with the agenthub id", () => {
    expect(plugin.id).toBe("agenthub")
    expect(typeof plugin.setup).toBe("function")
  })
})
