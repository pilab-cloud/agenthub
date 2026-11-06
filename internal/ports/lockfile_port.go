package ports

// LockfilePort is the interface for lockfile operations used by the resolver.
// This allows the core resolver to remain independent of the concrete
// lockfile format implementation.
type LockfilePort interface {
	// Read reads the lockfile from the given path.
	Read(path string) (*Lockfile, error)
	// Write atomically writes the lockfile to the given path.
	Write(path string, l *Lockfile) error
	// Exists reports whether the lockfile exists.
	Exists(path string) bool
}

// Lockfile is the lockfile domain model.
type Lockfile struct {
	LockfileVersion int
	GeneratedAt   string
	Resolver      string
	Registries    []Registry
	Assets        []Asset
	Policy        map[string]interface{}
}

// Registry is a configured registry/hub endpoint.
type Registry struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Hub     string `json:"hub"`
	Trust   string `json:"trust"`
	ETag    string
	Cursor  string
}

// Asset is a resolved asset in the lockfile.
type Asset struct {
	ID           string            `json:"id"`
	Kind         string            `json:"kind"`
	Version      string            `json:"version"`
	Origin       string            `json:"origin"`
	ManifestDigest string          `json:"manifestDigest"`
	BundleDigest string            `json:"bundleDigest"`
	Signature    *Signature        `json:"signature"`
	Dependencies []string          `json:"dependencies"`
	Files        []ManifestFile    `json:"files"`
	Scope        string            `json:"scope"`
	Enabled      bool              `json:"enabled"`
	RequestedBy  string            `json:"requestedBy"`
}

// Signature represents a verified signature.
type Signature struct {
	KeyID    string `json:"keyId"`
	Verified bool   `json:"verified"`
}

// ManifestFile represents a file in the cached bundle.
type ManifestFile struct {
	Path string `json:"path"`
	SHA256 string `json:"sha256"`
	Size int64  `json:"size"`
}