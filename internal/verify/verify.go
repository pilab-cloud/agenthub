// Package verify implements the client-side checks the agenthub CLI runs
// before installing an asset: the canonical bytes a manifest signature covers
// and Ed25519 verification over them, as defined by
// docs/architecture/domain-model.md §3.2.
package verify

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"
)

// Sentinel errors returned by the verification helpers.
var (
	// ErrMalformedManifest is returned for manifest bytes that are not a JSON
	// object, or that carry unparsable signature entries.
	ErrMalformedManifest = errors.New("verify: malformed manifest")
	// ErrInvalidSignature is returned when a signature does not verify over the
	// canonical manifest bytes.
	ErrInvalidSignature = errors.New("verify: invalid signature")
	// ErrUnsupportedAlgorithm is returned for signature algorithms other than
	// ed25519.
	ErrUnsupportedAlgorithm = errors.New("verify: unsupported signature algorithm")
	// ErrNoPublicKey is returned when a public key cannot be used for
	// verification: missing or not an Ed25519 key of the expected size.
	ErrNoPublicKey = errors.New("verify: no ed25519 public key")
	// ErrNoSignature is returned when a caller passes no signature at all.
	ErrNoSignature = errors.New("verify: no signature")
)

// Manifest fields excluded from the signed payload, per
// docs/architecture/domain-model.md §3.2: the signature covers "the manifest
// with signatures and manifestDigest removed, serialized with JCS".
const (
	fieldSignatures     = "signatures"
	fieldManifestDigest = "manifestDigest"
)

// CanonicalManifest returns the bytes a manifest signature covers: the manifest
// without its "signatures" and "manifestDigest" fields, object keys sorted
// lexicographically at every depth and number literals preserved. That is RFC
// 8785 JCS for manifests as `agenthub pack` emits them (canonical JSON in,
// canonical JSON out); it is deliberately not a re-serializer.
func CanonicalManifest(manifest []byte) ([]byte, error) {
	doc, err := parseObject(manifest)
	if err != nil {
		return nil, err
	}
	delete(doc, fieldSignatures)
	delete(doc, fieldManifestDigest)

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// No HTML escaping: JCS forbids it, and Go's encoder would otherwise rewrite
	// "<", ">" and "&" inside string values.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("%w: canonicalize: %w", ErrMalformedManifest, err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// ManifestSignatures returns the publisher signatures a manifest carries in its
// "signatures" array. A manifest without that field yields (nil, nil); a
// manifest that is not a JSON object or holds unparsable signature entries is
// an ErrMalformedManifest.
func ManifestSignatures(manifest []byte) ([]*commonv1.Signature, error) {
	if err := requireObject(manifest); err != nil {
		return nil, err
	}
	var doc struct {
		Signatures []struct {
			KeyID     string `json:"keyId"`
			Algorithm string `json:"algorithm"`
			Value     string `json:"value"`
			SignedAt  string `json:"signedAt"`
		} `json:"signatures"`
	}
	if err := json.Unmarshal(manifest, &doc); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMalformedManifest, err)
	}
	if doc.Signatures == nil {
		return nil, nil
	}

	sigs := make([]*commonv1.Signature, 0, len(doc.Signatures))
	for _, e := range doc.Signatures {
		sig := &commonv1.Signature{
			KeyId:     strings.TrimSpace(e.KeyID),
			Algorithm: strings.TrimSpace(e.Algorithm),
		}
		if v := strings.TrimSpace(e.Value); v != "" {
			decoded, err := base64.StdEncoding.DecodeString(v)
			if err != nil {
				return nil, fmt.Errorf("%w: signature value for key %q: %w",
					ErrMalformedManifest, e.KeyID, err)
			}
			sig.Value = decoded
		}
		if t := strings.TrimSpace(e.SignedAt); t != "" {
			signedAt, err := time.Parse(time.RFC3339, t)
			if err != nil {
				return nil, fmt.Errorf("%w: signedAt for key %q: %w",
					ErrMalformedManifest, e.KeyID, err)
			}
			sig.SignedAt = timestamppb.New(signedAt)
		}
		sigs = append(sigs, sig)
	}
	return sigs, nil
}

// CheckSignature verifies sig over the canonical unsigned form of manifest
// with pub. The signature must use ed25519 (the only algorithm the domain
// model defines) and pub must be an Ed25519 public key; anything else is an
// error rather than a pass, so an unusable signature never installs unsigned.
func CheckSignature(manifest []byte, sig *commonv1.Signature, pub ed25519.PublicKey) error {
	if sig == nil {
		return ErrNoSignature
	}
	if algo := strings.ToLower(strings.TrimSpace(sig.GetAlgorithm())); algo != "" && algo != "ed25519" {
		return fmt.Errorf("%w: %q", ErrUnsupportedAlgorithm, sig.GetAlgorithm())
	}
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: got %d bytes, want %d", ErrNoPublicKey, len(pub), ed25519.PublicKeySize)
	}
	canonical, err := CanonicalManifest(manifest)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, canonical, sig.GetValue()) {
		return fmt.Errorf("%w: key %q", ErrInvalidSignature, sig.GetKeyId())
	}
	return nil
}

// parseObject reads a manifest as a JSON object, keeping number literals as
// written so canonicalization only reorders keys.
func parseObject(manifest []byte) (map[string]any, error) {
	if err := requireObject(manifest); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(manifest))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMalformedManifest, err)
	}
	if dec.More() {
		return nil, fmt.Errorf("%w: trailing data after the manifest object", ErrMalformedManifest)
	}
	return doc, nil
}

// requireObject rejects input that cannot be a manifest: empty bytes or
// anything whose first token is not an object.
func requireObject(manifest []byte) error {
	trimmed := bytes.TrimSpace(manifest)
	if len(trimmed) == 0 {
		return fmt.Errorf("%w: empty manifest", ErrMalformedManifest)
	}
	if trimmed[0] != '{' {
		return fmt.Errorf("%w: manifest is not an object", ErrMalformedManifest)
	}
	return nil
}
