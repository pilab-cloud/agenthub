// Package cache implements the content-addressed local cache used by the
// agenthub CLI. Keys are lowercase sha256 hex digests of the stored bytes, so
// every read re-verifies the content it returns.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Sentinel errors returned by the cache operations.
var (
	// ErrNotFound is returned when the key is not present in the cache.
	ErrNotFound = errors.New("cache: entry not found")
	// ErrInvalidKey is returned when a key is not a sha256 hex digest.
	ErrInvalidKey = errors.New("cache: invalid key")
	// ErrUnsupportedAlgorithm is returned for digests that are not sha256.
	ErrUnsupportedAlgorithm = errors.New("cache: unsupported digest algorithm")
	// ErrDigestMismatch is returned when bytes do not hash to their key.
	ErrDigestMismatch = errors.New("cache: digest mismatch")
)

const (
	// EnvCacheDir overrides the default cache directory.
	EnvCacheDir = "AGENTHUB_CACHE_DIR"
	// EnvXDGRuntimeDir is the XDG base directory for user caches.
	EnvXDGCacheHome = "XDG_CACHE_HOME"
	// DirName is the cache directory name under the cache home.
	DirName = "agenthub"
	// keyLen is the length of a sha256 hex digest.
	keyLen = sha256.Size * 2
	// objectSubdir groups objects by digest prefix: sha256/<aa>/<key>.
	objectSubdir = "sha256"
)

// DefaultDir returns the default cache directory. Precedence:
// $AGENTHUB_CACHE_DIR, $XDG_CACHE_HOME/agenthub, $HOME/.cache/agenthub, and
// finally a directory under the system temp dir when no home is resolvable.
func DefaultDir() string {
	if v := strings.TrimSpace(os.Getenv(EnvCacheDir)); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv(EnvXDGCacheHome)); v != "" {
		return filepath.Join(v, DirName)
	}
	if h, err := os.UserHomeDir(); err == nil && strings.TrimSpace(h) != "" {
		return filepath.Join(h, ".cache", DirName)
	}
	return filepath.Join(os.TempDir(), DirName)
}

// Key returns the sha256 hex digest of data.
func Key(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// KeyFromDigest converts a digest string ("sha256:<hex>" or a bare "<hex>")
// into the canonical lowercase sha256 hex key.
func KeyFromDigest(digest string) (string, error) {
	s := strings.TrimSpace(digest)
	if s == "" {
		return "", fmt.Errorf("%w: empty digest", ErrInvalidKey)
	}
	if algo, value, ok := strings.Cut(s, ":"); ok {
		if !strings.EqualFold(strings.TrimSpace(algo), "sha256") {
			return "", fmt.Errorf("%w: %q", ErrUnsupportedAlgorithm, algo)
		}
		s = strings.TrimSpace(value)
	}
	return normalizeKey(s)
}

// Cache is a content-addressed local cache rooted at a directory.
type Cache struct {
	dir string
}

// New returns a cache rooted at dir, creating the directory when needed.
func New(dir string) (*Cache, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("cache: directory is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("cache: create %q: %w", dir, err)
	}
	return &Cache{dir: dir}, nil
}

// Dir returns the root directory of the cache.
func (c *Cache) Dir() string { return c.dir }

// Put stores data under key. The key must be the sha256 hex digest of data;
// otherwise ErrDigestMismatch is returned and nothing is written. The write is
// atomic (temp file + rename), so readers never observe partial content.
func (c *Cache) Put(key string, data []byte) error {
	k, err := normalizeKey(key)
	if err != nil {
		return err
	}
	if got := Key(data); got != k {
		return fmt.Errorf("%w: key %s, data hashes to %s", ErrDigestMismatch, k, got)
	}
	path := c.objectPath(k)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("cache: create %q: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".put-*")
	if err != nil {
		return fmt.Errorf("cache: create temp file: %w", err)
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("cache: write: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("cache: sync: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("cache: close: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("cache: commit %s: %w", k, err)
	}
	return nil
}

// Get returns the bytes stored under key, re-hashing them to detect
// corruption. It returns ErrNotFound when the key is absent and
// ErrDigestMismatch when the stored bytes do not hash to key.
func (c *Cache) Get(key string) ([]byte, error) {
	k, err := normalizeKey(key)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(c.objectPath(k)) //nolint:gosec // path is derived from a validated digest under the cache dir
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, k)
		}
		return nil, fmt.Errorf("cache: read %s: %w", k, err)
	}
	if got := Key(data); got != k {
		return nil, fmt.Errorf("%w: %s hashes to %s", ErrDigestMismatch, k, got)
	}
	return data, nil
}

// Has reports whether key is present in the cache. It does not read or verify
// the content; use Get for that.
func (c *Cache) Has(key string) bool {
	k, err := normalizeKey(key)
	if err != nil {
		return false
	}
	if _, err := os.Stat(c.objectPath(k)); err != nil {
		return false
	}
	return true
}

// objectPath is the sharded on-disk location of key: <dir>/sha256/<aa>/<key>.
func (c *Cache) objectPath(key string) string {
	return filepath.Join(c.dir, objectSubdir, key[:2], key)
}

// normalizeKey lowercases and validates a sha256 hex key.
func normalizeKey(key string) (string, error) {
	k := strings.ToLower(strings.TrimSpace(key))
	if len(k) != keyLen {
		return "", fmt.Errorf("%w: %q", ErrInvalidKey, key)
	}
	if _, err := hex.DecodeString(k); err != nil {
		return "", fmt.Errorf("%w: %q", ErrInvalidKey, key)
	}
	return k, nil
}
