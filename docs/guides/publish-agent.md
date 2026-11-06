# Publish an Agent Pack

This guide walks through publishing an agent pack to AgentHub, from preparation
through to making it available to consumers.

## Before You Begin

- You have an AgentHub account with Shadow SSO authentication
- You have a `agenthub.json` desired state file (or will create one)
- You have access to the assets you want to publish (agents, skills, tools, prompts)

## Step 1: Prepare Your Assets

### Organize Your Directory

```text
my-agent-pack/
├── pack.json          # Pack metadata
├── agents/            # Agent definitions
├── skills/            # Skill definitions
├── tools/             # Tool definitions
├── prompts/           # Prompt templates
└── icons/             # Optional icon files
```

### Create `pack.json`

```json
{
  "name": "my-agent-pack",
  "version": "1.0.0",
  "kind": "pack",
  "displayName": "My Agent Pack",
  "description": "A collection of agents and tools for common tasks",
  "iconDigest": "sha256-abc123def456...",
  "tags": ["productivity", "automation"],
  "categories": ["tooling", "ai"],
  "landingVisible": true,
  "license": "MIT",
  "repository": "https://github.com/your-org/my-agent-pack"
}
```

### Add Assets to the Pack

```bash
# Add agents
agenthub add ./agents/agent-1@1.0.0
agenthub add ./agents/agent-2@1.0.0

# Add skills
agenthub add ./skills/skill-1@1.0.0
agenthub add ./skills/skill-2@1.0.0

# Add tools
agenthub add ./tools/tool-1@1.0.0

# Add prompts
agenthub add ./prompts/prompt-1@1.0.0
```

### Verify Your Pack

```bash
# List all assets in your pack
agenthub list --kind agent
agenthub list --kind skill
agenthub list --kind tool

# Check the pack metadata
cat agenthub.json
```

## Step 2: Build the Bundle

AgentHub uses a bundling process to collect all assets and their dependencies
into a single, installable package.

```bash
# Materialize the locked assets into .opencode format
agenthub materialize

# Or build for the CLI
agenthub bundle
```

The bundling process:
1. Reads `agenthub.lock.json` for pinned versions
2. Collects all asset files (agent definitions, configs, icons, etc.)
3. Computes content-addressed digests for each file
4. Produces a bundle archive (`bundle.tar.gz` or similar)
5. Updates `agenthub.lock.json` with the bundle digest

## Step 3: Publish to AgentHub

```bash
# Publish the pack to the hub
agenthub publish

# Or publish with a specific tag
agenthub publish --tag 1.0.0

# Verify the publish
agenthub catalog search my-agent-pack
agenthub catalog list --kind pack
```

### Publish with Dependencies

If your pack depends on other published assets:

```bash
# Add dependency references in pack.json
{
  "name": "my-agent-pack",
  "version": "1.0.0",
  "dependencies": {
    "pilab/pack/base-agents": "1.2.0",
    "pilab/pack/common-tools": "0.9.0"
  }
}

# The publisher will resolve these dependencies automatically
agenthub publish
```

### Publish Workflow Checklist

- [ ] All assets have valid metadata (name, version, kind)
- [ ] Version constraints are correct (no conflicting versions)
- [ ] Icons and assets have correct digests
- [ ] License is specified
- [ ] Tags and categories are appropriate
- [ ] `landingVisible` is set appropriately (true for public, false for private)
- [ ] Dependencies are resolved and available
- [ ] `agenthub.lock.json` is up to date
- [ ] Run `agenthub gc` to clean up unused assets
- [ ] Test installation: `agenthub install` in a test project

## Step 4: Verify Installation

```bash
# Install the pack into a project
agenthub install

# Or install a specific version
agenthub install pilab/pack/my-agent-pack@1.0.0

# Verify the install
agenthub list --installed

# Check the OpenCode V2 plugin recognizes the assets
opencode --list-agents
opencode --list-skills
```

## Troubleshooting

### Common Issues

| Issue | Solution |
| --- | --- |
| `asset not found` | Ensure the asset ref is correct (`namespace/kind/name@version`) |
| `version conflict` | Remove conflicting versions and re-run `agenthub update` |
| `dependency not found` | Ensure the dependency is published first, then `agenthub sync` |
| `publish failed` | Check the error output for specific reasons (digest mismatch, signature failure) |
| `permission denied` | Ensure you have publisher role on the namespace |
| `catalog not updating` | Run `agenthub sync` to refresh from upstream hubs |

### Debug Commands

```bash
# Verbose publish output
agenthub publish --verbose

# Check lockfile state
cat agenthub.lock.json

# Verify asset digests
agenthub catalog get <asset-ref>

# View audit log
agenthub audit log --since 24h
```

## Best Practices

### Versioning

- Use **semantic versioning** (`MAJOR.MINOR.PATCH`)
- Increment **MAJOR** for breaking changes
- Increment **MINOR** for new features (backward compatible)
- Increment **PATCH** for bug fixes

### Asset Naming

- Use **lowercase** with **hyphens** for names (`my-agent`, not `MyAgent`)
- Use ** descriptive names** that clearly indicate the asset's purpose
- Avoid special characters and spaces

### Metadata Quality

- Provide **descriptive summaries** (not just one-liners)
- Include **relevant tags** for discoverability
- Add **categories** that group assets logically
- Set `landingVisible` appropriately (true for public hub, false for private)
- Provide **icon digests** for visual identification

### Security

- Sign your assets before publishing
- Use **content-addressed digests** (SHA-256)
- Set appropriate `landingVisible` flags
- Review `dependencies` for transitive risks
- Audit the `audit_log` regularly

## FAQ

### Q: Can I publish the same asset twice with different versions?

**A:** Yes! AgentHub supports multiple versions of the same asset (`namespace/kind/name@1.0.0`, `@2.0.0`). Users can pin to specific versions.

### Q: How do I yank/deprecate a published version?

**A:** Use `agenthub yank <asset-ref>@<version>` to mark a version as yanked, or update the version status to `deprecated` in the manifest.

### Q: What happens if I change an asset's kind?

**A:** Changing the kind (e.g., from `agent` to `skill`) creates a new asset entry. The old entry remains with its version history. Update your `agenthub.json` desired state to point to the new kind.

### Q: Can I have private assets that aren't published to the public hub?

**A:** Yes! Set `landingVisible: false` in your `pack.json`, or publish to a private/federated hub instead of the public one.

### Q: How do I migrate from one version to another?

**A:** Use `agenthub update` to re-resolve desired assets, then `agenthub install` to update installed assets. The lockfile will pin the new versions.

---

*Need more help? Visit [#agenthub on Discord](https://discord.gg/pilab) or [file a GitHub Issue](https://github.com/pilab-cloud/agenthub/issues).*
