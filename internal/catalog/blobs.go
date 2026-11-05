package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"
)

// Blob store sentinel errors.
var (
	// ErrInvalidDigest reports a digest that cannot key a blob: no value, an
	// algorithm other than sha256, or a value that is not 64 hex digits.
	ErrInvalidDigest = errors.New("invalid digest")
	// ErrDigestMismatch reports bytes that do not hash to the digest they
	// were filed under.
	ErrDigestMismatch = errors.New("digest mismatch")
)

// BlobStore holds content-addressed file bytes keyed by digest. It is the
// in-memory half of the bundle pipeline (M4.3): ExportBundle reads file
// bytes from it and ImportBundle writes what an import carries, until a
// persistent blob backend lands.
type BlobStore interface {
	// Put files data under digest, refusing bytes that do not hash to it.
	Put(digest *commonv1.Digest, data []byte) error
	// Get returns a copy of the bytes filed under digest.
	Get(digest *commonv1.Digest) ([]byte, bool)
}

// BlobKey normalizes a digest into its map key, "sha256:<64 lowercase hex>".
// An empty algorithm means sha256, the only algorithm this hub stores.
func BlobKey(d *commonv1.Digest) (string, error) {
	if d == nil {
		return "", fmt.Errorf("%w: missing digest", ErrInvalidDigest)
	}
	alg := strings.ToLower(strings.TrimSpace(d.GetAlgorithm()))
	if alg == "" {
		alg = "sha256"
	}
	if alg != "sha256" {
		return "", fmt.Errorf("%w: unsupported algorithm %q", ErrInvalidDigest, alg)
	}
	value := strings.ToLower(strings.TrimSpace(d.GetValue()))
	if len(value) != 2*sha256.Size {
		return "", fmt.Errorf("%w: sha256 value is %d characters, want %d",
			ErrInvalidDigest, len(value), 2*sha256.Size)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", fmt.Errorf("%w: sha256 value is not hex (%w)", ErrInvalidDigest, err)
	}
	return "sha256:" + value, nil
}

// MemBlobs is an in-memory BlobStore. Bytes are verified against their
// digest when filed, so a Get can hand them back without re-hashing; readers
// that must vouch for the bytes they serve (ExportBundle) re-hash anyway.
type MemBlobs struct {
	mu    sync.RWMutex
	blobs map[string][]byte
}

// NewMemBlobs returns an empty in-memory blob store.
func NewMemBlobs() *MemBlobs {
	return &MemBlobs{blobs: map[string][]byte{}}
}

func (m *MemBlobs) Put(digest *commonv1.Digest, data []byte) error {
	key, err := BlobKey(digest)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	if "sha256:"+hex.EncodeToString(sum[:]) != key {
		return fmt.Errorf("%w: bytes hash to sha256:%s, filed as %s",
			ErrDigestMismatch, hex.EncodeToString(sum[:]), key)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.blobs[key] = append([]byte(nil), data...)
	return nil
}

func (m *MemBlobs) Get(digest *commonv1.Digest) ([]byte, bool) {
	key, err := BlobKey(digest)
	if err != nil {
		return nil, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	data, ok := m.blobs[key]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), data...), true
}
