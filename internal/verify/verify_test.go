package verify

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"

	"google.golang.org/protobuf/proto"
)

func TestCanonicalManifest(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		manifest string
		want     string
	}{
		{
			name:     "drops signed fields and sorts keys",
			manifest: `{"version":"1.4.2","signatures":[{"value":"c2ln"}],"name":"code-reviewer","manifestDigest":"sha256:abc"}`,
			want:     `{"name":"code-reviewer","version":"1.4.2"}`,
		},
		{
			name:     "sorts nested keys and keeps html characters",
			manifest: `{"b":{"z":1,"a":2},"a":"<b>&"}`,
			want:     `{"a":"<b>&","b":{"a":2,"z":1}}`,
		},
		{
			name:     "preserves number literals",
			manifest: `{"steps":1.0,"size":4096}`,
			want:     `{"size":4096,"steps":1.0}`,
		},
		{
			name:     "already canonical input is unchanged",
			manifest: `{"files":[{"path":"a","sha256":"x"}],"name":"n"}`,
			want:     `{"files":[{"path":"a","sha256":"x"}],"name":"n"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := CanonicalManifest([]byte(tc.manifest))
			if err != nil {
				t.Fatalf("CanonicalManifest: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}
			again, err := CanonicalManifest(got)
			if err != nil {
				t.Fatalf("CanonicalManifest twice: %v", err)
			}
			if !bytes.Equal(again, got) {
				t.Fatalf("canonicalization is not stable: %s then %s", got, again)
			}
		})
	}
}

func TestCanonicalManifestRejectsNonManifests(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		manifest string
	}{
		{name: "empty", manifest: "   "},
		{name: "array", manifest: `["a"]`},
		{name: "string", manifest: `"nope"`},
		{name: "null", manifest: `null`},
		{name: "invalid json", manifest: `{"a":}`},
		{name: "trailing data", manifest: `{"a":1}{"b":2}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := CanonicalManifest([]byte(tc.manifest)); !errors.Is(err, ErrMalformedManifest) {
				t.Fatalf("error = %v, want ErrMalformedManifest", err)
			}
		})
	}
}

func TestManifestSignatures(t *testing.T) {
	t.Parallel()

	t.Run("absent", func(t *testing.T) {
		t.Parallel()
		sigs, err := ManifestSignatures([]byte(`{"name":"code-reviewer"}`))
		if err != nil {
			t.Fatalf("ManifestSignatures: %v", err)
		}
		if len(sigs) != 0 {
			t.Fatalf("signatures = %v, want none", sigs)
		}
	})

	t.Run("null", func(t *testing.T) {
		t.Parallel()
		sigs, err := ManifestSignatures([]byte(`{"signatures":null}`))
		if err != nil {
			t.Fatalf("ManifestSignatures: %v", err)
		}
		if len(sigs) != 0 {
			t.Fatalf("signatures = %v, want none", sigs)
		}
	})

	t.Run("decodes entries", func(t *testing.T) {
		t.Parallel()
		manifest := []byte(`{"signatures":[{"keyId":"ed25519:9f2c","algorithm":"ed25519","value":"` +
			base64.StdEncoding.EncodeToString([]byte("sig-bytes")) +
			`","signedAt":"2026-11-04T10:00:00Z"},{"keyId":"ed25519:0000"}]}`)
		sigs, err := ManifestSignatures(manifest)
		if err != nil {
			t.Fatalf("ManifestSignatures: %v", err)
		}
		if len(sigs) != 2 {
			t.Fatalf("signatures = %d, want 2", len(sigs))
		}
		if sigs[0].GetKeyId() != "ed25519:9f2c" || sigs[0].GetAlgorithm() != "ed25519" {
			t.Fatalf("first signature = %v", sigs[0])
		}
		if !bytes.Equal(sigs[0].GetValue(), []byte("sig-bytes")) {
			t.Fatalf("value = %q, want sig-bytes", sigs[0].GetValue())
		}
		want, err := time.Parse(time.RFC3339, "2026-11-04T10:00:00Z")
		if err != nil {
			t.Fatalf("parse time: %v", err)
		}
		if !sigs[0].GetSignedAt().AsTime().Equal(want) {
			t.Fatalf("signedAt = %v, want %v", sigs[0].GetSignedAt().AsTime(), want)
		}
		if sigs[1].GetKeyId() != "ed25519:0000" || sigs[1].GetValue() != nil {
			t.Fatalf("second signature = %v", sigs[1])
		}
	})

	for _, tc := range []struct {
		name     string
		manifest string
	}{
		{name: "not an object", manifest: `["nope"]`},
		{name: "invalid json", manifest: `{"signatures":[}`},
		{name: "signatures not an array", manifest: `{"signatures":"c2ln"}`},
		{name: "bad base64", manifest: `{"signatures":[{"value":"not base64!"}]}`},
		{name: "bad signedAt", manifest: `{"signatures":[{"signedAt":"yesterday"}]}`},
	} {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ManifestSignatures([]byte(tc.manifest)); !errors.Is(err, ErrMalformedManifest) {
				t.Fatalf("error = %v, want ErrMalformedManifest", err)
			}
		})
	}
}

func TestCheckSignature(t *testing.T) {
	t.Parallel()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	manifest := []byte(`{"name":"code-reviewer","permissions":[{"action":"*","effect":"deny"}]}`)
	canonical, err := CanonicalManifest(manifest)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	sig := &commonv1.Signature{
		KeyId:     "ed25519:9f2c",
		Algorithm: "ed25519",
		Value:     ed25519.Sign(priv, canonical),
	}

	if err := CheckSignature(manifest, sig, pub); err != nil {
		t.Fatalf("CheckSignature: %v", err)
	}

	t.Run("wrong key", func(t *testing.T) {
		t.Parallel()
		otherPub, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("keygen: %v", err)
		}
		if err := CheckSignature(manifest, sig, otherPub); !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("error = %v, want ErrInvalidSignature", err)
		}
	})

	t.Run("tampered manifest", func(t *testing.T) {
		t.Parallel()
		tampered := []byte(`{"name":"evil","permissions":[{"action":"*","effect":"deny"}]}`)
		if err := CheckSignature(tampered, sig, pub); !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("error = %v, want ErrInvalidSignature", err)
		}
	})

	t.Run("signing covers the unsigned manifest only", func(t *testing.T) {
		t.Parallel()
		// Adding the signature and manifestDigest back must not change what is
		// covered.
		signed := []byte(`{"manifestDigest":"sha256:abc","name":"code-reviewer",` +
			`"permissions":[{"action":"*","effect":"deny"}],"signatures":[{"value":"c2ln"}]}`)
		if err := CheckSignature(signed, sig, pub); err != nil {
			t.Fatalf("CheckSignature: %v", err)
		}
	})

	t.Run("unsupported algorithm", func(t *testing.T) {
		t.Parallel()
		bad := proto.Clone(sig).(*commonv1.Signature)
		bad.Algorithm = "rsa"
		if err := CheckSignature(manifest, bad, pub); !errors.Is(err, ErrUnsupportedAlgorithm) {
			t.Fatalf("error = %v, want ErrUnsupportedAlgorithm", err)
		}
	})

	t.Run("missing algorithm defaults to ed25519", func(t *testing.T) {
		t.Parallel()
		bare := proto.Clone(sig).(*commonv1.Signature)
		bare.Algorithm = ""
		if err := CheckSignature(manifest, bare, pub); err != nil {
			t.Fatalf("CheckSignature: %v", err)
		}
	})

	t.Run("nil signature", func(t *testing.T) {
		t.Parallel()
		if err := CheckSignature(manifest, nil, pub); !errors.Is(err, ErrNoSignature) {
			t.Fatalf("error = %v, want ErrNoSignature", err)
		}
	})

	t.Run("public key of the wrong size", func(t *testing.T) {
		t.Parallel()
		if err := CheckSignature(manifest, sig, ed25519.PublicKey{0x01}); !errors.Is(err, ErrNoPublicKey) {
			t.Fatalf("error = %v, want ErrNoPublicKey", err)
		}
	})

	t.Run("malformed manifest", func(t *testing.T) {
		t.Parallel()
		if err := CheckSignature([]byte("not json"), sig, pub); !errors.Is(err, ErrMalformedManifest) {
			t.Fatalf("error = %v, want ErrMalformedManifest", err)
		}
	})
}
