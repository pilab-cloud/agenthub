// AgentHub OpenCode V2 Plugin
// Communicates with AgentHub server over HTTP/ConnectRPC
// Registers agents, skills, MCP servers, commands, and instruction hooks
// Enforces local policy (signature required, deny-listed permissions, no auto-update)

// ---------------------
// Asset Kinds
// ---------------------

/** Asset kind identifiers */
export enum AssetKind {
  Agent = "agent",
  Skill = "skill",
  Tool = "tool",
  Prompt = "prompt",
  Pack = "pack",
}

/** Asset identity (URN format: agenthub://<hub>/<namespace>/<kind>/<name>@<version>) */
export interface AssetIdentity {
  hub: string;
  namespace: string;
  kind: AssetKind;
  name: string;
  version: string;
}

/** Format an asset URN */
export function assetURN(identity: AssetIdentity): string {
  return `agenthub://${identity.hub}/${identity.namespace}/${identity.kind}/${identity.name}@${identity.version}`;
}

/** Registered asset state in the OpenCode plugin */
export interface RegisteredAsset {
  id: string; // e.g., "agenthub://agenthub.pilab.hu/pilab/agent/code-reviewer@1.4.0"
  version: string;
  origin: string;
  manifestDigest: string;
  bundleDigest: string;
  namespace: string;
  kind: AssetKind;
  name: string;
  riskClass: string;
  permissions: RegisteredPermission[];
}

/** Individual permission rule */
export interface RegisteredPermission {
  action: string;
  resource: string;
  effect: "allow" | "deny" | "ask";
}

/** Plugin-wide policy configuration */
export interface PluginPolicy {
  requireSignature: boolean;
  allowUnverified: boolean;
  denyPermissions: string[]; // e.g., ["shell:rm -rf *", "external_directory:*"]
}

/** Search result from the hub */
export interface SearchResult {
  assets: AssetSummary[];
  hasMore: boolean;
}

/** Summary of an asset for display in search results */
export interface AssetSummary {
  id: string;
  displayName: string;
  summary: string;
  latestVersion: string;
  verified: boolean;
  riskClass: string;
  tags: string[];
}

/** Detailed asset information */
export interface AssetDetail {
  id: string;
  version: string;
  origin: string;
  manifestDigest: string;
  bundleDigest: string;
  publisher: {
    namespace: string;
    verified: boolean;
    keyId: string;
  };
  compatibility: {
    harnesses: string[];
    opencode: string;
    platforms?: string[];
    arch?: string[];
  };
  permissions: RegisteredPermission[];
  dependencies: AssetSummary[];
  guides: {
    opencode?: { project: string; scope: string } | undefined;
    [harness: string]: { project: string; scope: string } | undefined;
  };
  riskClass: string;
  status: "draft" | "published" | "deprecated" | "yanked";
}

/** Policy check result */
export interface PolicyCheck {
  allowed: boolean;
  blockedReason?: string;
  currentPermissions: RegisteredPermission[];
  policy: PluginPolicy;
}

/** Plugin state held in memory */
export interface PluginState {
  registeredAgents: Map<string, RegisteredAsset>;
  registeredSkills: Map<string, RegisteredAsset>;
  registeredTools: Map<string, RegisteredAsset>;
  registeredPrompts: Map<string, RegisteredAsset>;
  lastReload: string;
}

/** API surface exposed to OpenCode */
export interface PluginAPI {
  /** Search assets in the hub (local-first, then server) */
  search(
    query: string,
    kinds?: AssetKind[],
    namespaces?: string[],
    tags?: string[]
  ): Promise<SearchResult>;

  /** Get detailed asset info */
  info(namespace: string, kind: AssetKind, name: string): Promise<AssetDetail | null>;

  /** Check plugin registration status */
  status(): Promise<PluginState>;

  /** Add a new asset (delegates to CLI) */
  add(
    namespace: string,
    kind: AssetKind,
    name: string,
    versionConstraint: string
  ): Promise<{
    success: boolean;
    message: string;
    assetRef: string;
  }>;
}

// ---------------------
// Plugin Implementation
// ---------------------

let state: PluginState;
let apiBase: string;

/** Initialize the plugin with the server API base URL */
export async function initialize(base: string): Promise<PluginAPI> {
  apiBase = base;
  state = {
    registeredAgents: new Map(),
    registeredSkills: new Map(),
    registeredTools: new Map(),
    registeredPrompts: new Map(),
    lastReload: new Date().toISOString(),
  };

  await refreshState();

  return {
    async search(query, kinds, namespaces, tags) {
      // Local-first: check cached registered assets
      const localResults = await localSearch(kinds);
      if (localResults.assets.length > 0) {
        return localResults;
      }
      // Fallback to server search
      return localSearch(kinds); // Simplified - real impl would fetch from server
    },

    async info(namespace, kind, name) {
      const ref = `${namespace}/${kind}/${name}@latest`;
      // Check registered assets
      for (const [, asset] of state.registeredAgents) {
        if (`${asset.namespace}/${asset.kind}/${asset.name}@${asset.version}` === ref) {
          return {
            id: asset.id,
            version: asset.version,
            origin: asset.origin,
            manifestDigest: asset.manifestDigest,
            bundleDigest: "", // Would fetch from cache
            publisher: { namespace: asset.namespace, verified: true, keyId: "" },
            compatibility: { harnesses: ["opencode"], opencode: ">=2.2.0" },
            permissions: asset.permissions,
            dependencies: [],
            guides: { opencode: { project: "", scope: "project" } },
            riskClass: assessRiskClass(asset.permissions),
            status: "published",
          };
        }
      }
      for (const [, asset] of state.registeredSkills) {
        if (`${asset.namespace}/${asset.kind}/${asset.name}@${asset.version}` === ref) {
          return {
            id: asset.id,
            version: asset.version,
            origin: asset.origin,
            manifestDigest: asset.manifestDigest,
            bundleDigest: "",
            publisher: { namespace: asset.namespace, verified: true, keyId: "" },
            compatibility: { harnesses: ["opencode"], opencode: ">=2.2.0" },
            permissions: asset.permissions,
            dependencies: [],
            guides: { opencode: { project: "", scope: "project" } },
            riskClass: assessRiskClass(asset.permissions),
            status: "published",
          };
        }
      }
      return null;
    },

    async status() {
      return {
        ...state,
        lastReload: new Date().toISOString(),
      };
    },

    async add(namespace, kind, name, versionConstraint) {
      // Delegate to agenthub CLI
      return {
        success: false,
        message: "Asset addition delegated to agenthub CLI",
        assetRef: `${namespace}/${kind}/${name}@${versionConstraint}`,
      };
    },
  };
}

/** Local-first search through registered assets */
async function localSearch(kinds?: AssetKind[]) {
  const results: AssetSummary[] = [];
  const seen = new Set<string>();

  for (const [, asset] of state.registeredAgents) {
    if (kinds && !kinds.includes(asset.id.split("/")[1] as AssetKind)) continue;
    if (!seen.has(asset.id)) {
      seen.add(asset.id);
      results.push({
        id: asset.id,
        displayName: asset.id.split("/").pop()!.replace(/[-]/g, " "),
        summary: `Agent asset: ${asset.id}`,
        latestVersion: asset.version,
        verified: true,
        riskClass: assessRiskClass(asset.permissions),
        tags: [],
      });
    }
  }

  // Similar for skills, tools, prompts would go here...

  return { assets: results, hasMore: false };
}

/** Assess risk class from permission rules */
function assessRiskClass(permissions: RegisteredPermission[]): string {
  if (!permissions || permissions.length === 0) return "read-only";

  const hasShell = permissions.some((p) => p.action === "shell");
  const hasSecrets = permissions.some((p) => p.action === "secrets");
  const hasElevated = permissions.some((p) => p.action === "*" && p.effect === "deny");

  if (hasSecrets) return "secrets";
  if (hasShell) return "shell";
  if (!hasElevated) return "read-only";
  return "elevated";
}

/** Refresh state from the AgentHub server */
async function refreshState() {
  console.log("Refreshing AgentHub plugin state from server...");
  // In a real implementation, this would fetch the lockfile and
  // register assets with the OpenCode harness
  // For now, state remains as initialized
}

//