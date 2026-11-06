package domain

import "fmt"

// AssetKind is the kind of an agenthub asset.
type AssetKind string

const (
	// ASSET_KIND_AGENT is an agent asset kind.
	ASSET_KIND_AGENT AssetKind = "agent"
	// ASSET_KIND_SKILL is a skill asset kind.
	ASSET_KIND_SKILL AssetKind = "skill"
	// ASSET_KIND_TOOL is a tool asset kind.
	ASSET_KIND_TOOL AssetKind = "tool"
	// ASSET_KIND_PROMPT is a prompt asset kind.
	ASSET_KIND_PROMPT AssetKind = "prompt"
	// ASSET_KIND_PACK is a pack asset kind.
	ASSET_KIND_PACK AssetKind = "pack"
)

// AssetIdentity is the globally unique identity of an asset version.
type AssetIdentity struct {
	Hub        string
	Namespace  string
	Kind       AssetKind
	Name       string
	Version    string
	ManifestDigest string
	BundleDigest string
}

// URN returns the agenthub URN for this identity.
func (i *AssetIdentity) URN() string {
	return fmt.Sprintf("agenthub://%s/%s/%s@%s", i.Hub, i.Namespace, i.Kind, i.Version)
}

// Asset is the core domain entity for an AgentHub asset.
type Asset struct {
	SchemaVersion int
	Kind          AssetKind
	NameSpace     string
	Name          string
	Version       string
	DisplayName   string
	Summary       string
	Description   string
	License       string
	Homepage      string
	Repository    string
	Icon          string
	Tags          []string
	Categories    []string
	Compatibility Compatibility
	Permissions   []PermissionRule
	Dependencies  []Dependency
	Guides        map[string]Guide
	Spec          Spec
	Files         []ManifestFile
	ManifestDigest string
	BundleDigest  string
	OriginHub     string
	Status        AssetStatus
	PublishedAt   string
}

// AssetStatus is the lifecycle status of an asset version.
type AssetStatus string

const (
	// ASSET_STATUS_DRAFT is a draft version, not visible publicly.
	ASSET_STATUS_DRAFT AssetStatus = "draft"
	// ASSET_STATUS_PUBLISHED is a published version.
	ASSET_STATUS_PUBLISHED AssetStatus = "published"
	// ASSET_STATUS_DEPRECATED is a deprecated version.
	ASSET_STATUS_DEPRECATED AssetStatus = "deprecated"
	// ASSET_STATUS_YANKED is a yanked version.
	ASSET_STATUS_YANKED AssetStatus = "yanked"
)

// Compatibility holds harness/platform/arch constraints for an asset.
type Compatibility struct {
	Harnesses  []string `json:"harnesses"`
	Opencode   string   `json:"opencode"`
	Platforms  []string `json:"platforms"`
	Arch       []string `json:"arch"`
}

// PermissionRule is a single permission rule in canonical form.
type PermissionRule struct {
	Action    string `json:"action"`
	Resource  string `json:"resource"`
	Effect    string `json:"effect"`
}

// Guide is an install guide for a specific harness/scope.
type Guide map[string]string // key: harness_scope, value: path

// ManifestFile is a file in the bundle with its hash.
type ManifestFile struct {
	Path   string
	Size   int64
	SHA256 string
	Mode   int
}

// Dependency represents an asset dependency constraint.
type Dependency struct {
	ID        string
	Version   string // SemVer constraint
	Kind      AssetKind
	Namespace string
	TargetNS  string
	TargetKind AssetKind
	TargetName string
	KindType  string // requires | recommends | conflicts
}

// Spec is the kind-specific payload interface.
type Spec interface{}

// AgentSpec is the spec for agent assets.
type AgentSpec struct {
	Prompt         string
	Mode           string // primary, subagent, all
	ModelPolicy    ModelPolicy
	Steps          int
	Hidden         bool
	Color          string
	Request        string
}

// ModelPolicy holds model preference and fallback configuration.
type ModelPolicy struct {
	Prefer []string `json:"prefer"`
	Fallback string `json:"fallback"` // inherit or none
}

// SkillSpec is the spec for skill assets.
type SkillSpec struct {
	Entry      string   // path to SKILL.md
	Files      []string // supporting files
	Description string  // required
}

// ToolSpec is the spec for tool assets.
type ToolSpec struct {
	Type      string   // mcp-remote, mcp-local, opencode-plugin, cli
	MCP       *MCPConfig `json:"mcp,omitempty"`
	Healthcheck *Healthcheck `json:"healthcheck,omitempty"`
}

// MCPConfig holds MCP server configuration.
type MCPConfig struct {
	Name    string            `json:"name"`
	Config  map[string]string `json:"config,omitempty"`
	Type    string          `json:"type"` // local or remote
}

// Healthcheck holds health check configuration.
type Healthcheck struct {
	Command string `json:"command"`
	Expect  string `json:"expect"`
}

// PromptSpec is the spec for prompt assets.
type PromptSpec struct {
	Target    string    // command, instruction, or system
	Template  string    // path to template file
	Arguments []Argument `json:"arguments,omitempty"`
}

// Argument is a template argument.
type Argument struct {
	Name      string `json:"name"`
	Desc      string `json:"description,omitempty"`
	Required  bool   `json:"required,omitempty"`
	Default   string `json:"default,omitempty"`
}

// Publisher holds publisher key information.
type Publisher struct {
	Namespace string `json:"namespace"`
	KeyID     string `json:"keyId"`
	Algorithm string `json:"algorithm"`
	Custody   string `json:"custody"` // client or hosted
}

// Signature holds an Ed25519 signature.
type Signature struct {
	KeyID      string `json:"keyId"`
	Algorithm  string `json:"algorithm"`
	Value      string `json:"value"` // base64-encoded
	SignedAt   string `json:"signedAt"`
}

// Manifest is the canonical signed manifest of a published version.
type Manifest struct {
	SchemaVersion int
	ID            string
	Kind          AssetKind
	Name          string
	Namespace     string
	Version       string
	DisplayName   string
	Summary       string
	Description   string
	License       string
	Homepage      string
	Repository    string
	Icon          string
	Tags          []string
	Categories    []string
	Compatibility Compatibility
	Permissions   []PermissionRule
	Spec          Spec
	Files         []ManifestFile
	Dependencies  []Dependency
	Guides        map[string]Guide
	Publisher     Publisher
	ManifestDigest string
	BundleDigest  string
	Signatures    []Signature
}

// Lockfile is the resolved install plan lockfile.
type Lockfile struct {
	LockfileVersion int
	GeneratedAt   string
	Resolver      string
	Assets        []Asset
	Policy        map[string]interface{}
}

// Fmt returns a formatted string of the identity.
func Fmt(hub, ns, kind, name, version string) string {
	return fmt.Sprintf("agenthub://%s/%s/%s@%s", hub, ns, kind, version)
}