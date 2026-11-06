package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"

	federationv1 "go.pilab.hu/agenthub/gen/go/agenthub/federation/v1"
)

func TestLoadHubKeyEmptyPathGeneratesEphemeral(t *testing.T) {
	first, err := LoadHubKey("")
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != ed25519.PrivateKeySize {
		t.Fatalf("key size = %d, want %d", len(first), ed25519.PrivateKeySize)
	}
	second, err := LoadHubKey("")
	if err != nil {
		t.Fatal(err)
	}
	if string(first) == string(second) {
		t.Fatal("an empty path must generate a fresh ephemeral key each time")
	}
}

func TestLoadHubKeyPEM(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	path := writeKeyFile(t, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))

	loaded, err := LoadHubKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(loaded) != string(key) {
		t.Fatal("PEM round-trip changed the key")
	}
}

func TestLoadHubKeyEncodings(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	seed := key[:ed25519.SeedSize]
	// A raw seed may end in a whitespace byte; the parser must not trim it
	// into a 31-byte key.
	trailingNewlineSeed := append([]byte(nil), seed...)
	trailingNewlineSeed[len(trailingNewlineSeed)-1] = '\n'

	for _, tc := range []struct {
		name string
		body []byte
		want ed25519.PrivateKey
	}{
		{
			name: "hex seed",
			body: []byte(hex.EncodeToString(seed)),
			want: key,
		},
		{
			name: "base64 seed",
			body: []byte(base64.StdEncoding.EncodeToString(seed)),
			want: key,
		},
		{
			name: "raw base64 seed",
			body: []byte(base64.RawStdEncoding.EncodeToString(seed)),
			want: key,
		},
		{
			name: "url-safe base64 seed",
			body: []byte(base64.RawURLEncoding.EncodeToString(seed)),
			want: key,
		},
		{
			name: "base64 private key",
			body: []byte(base64.StdEncoding.EncodeToString(key)),
			want: key,
		},
		{
			name: "raw seed bytes",
			body: append([]byte(nil), seed...),
			want: key,
		},
		{
			// Regression: a raw seed may end in a whitespace byte, which
			// trimming must not eat (it would leave a 31-byte "seed").
			name: "raw seed bytes ending in newline",
			body: trailingNewlineSeed,
			want: ed25519.NewKeyFromSeed(trailingNewlineSeed),
		},
		{
			name: "raw private key bytes",
			body: append([]byte(nil), key...),
			want: key,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeKeyFile(t, tc.body)
			loaded, err := LoadHubKey(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(loaded) != string(tc.want) {
				t.Fatal("loaded key differs from the written one")
			}
		})
	}
}

func TestLoadHubKeyRejectsBadInput(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		if _, err := LoadHubKey(filepath.Join(t.TempDir(), "nope")); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("garbage", func(t *testing.T) {
		if _, err := LoadHubKey(writeKeyFile(t, []byte("not a key at all"))); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("short key", func(t *testing.T) {
		// 20 bytes: neither a seed nor a private key, in any encoding.
		if _, err := LoadHubKey(writeKeyFile(t, []byte(hex.EncodeToString(make([]byte, 20))))); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("inconsistent private key", func(t *testing.T) {
		_, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		key[ed25519.PrivateKeySize-1] ^= 0xff
		if _, err := LoadHubKey(writeKeyFile(t, key)); err == nil {
			t.Fatal("expected an error for a key whose public half does not match its seed")
		}
	})

	t.Run("not ed25519", func(t *testing.T) {
		ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		der, err := x509.MarshalPKCS8PrivateKey(ecKey)
		if err != nil {
			t.Fatal(err)
		}
		path := writeKeyFile(t, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
		if _, err := LoadHubKey(path); err == nil {
			t.Fatal("expected an error for a non-ed25519 PKCS#8 key")
		}
	})
}

// TestNewFederationEphemeralKey checks the constructor fallback: no key
// means an ephemeral one, which still signs and verifies.
func TestNewFederationEphemeralKey(t *testing.T) {
	f := NewFederation(testHubID, testBaseURL)
	if len(f.key) != ed25519.PrivateKeySize {
		t.Fatalf("key size = %d", len(f.key))
	}
	if f.store == nil {
		t.Fatal("store must not be nil")
	}
	resp, err := f.GetHubInfo(context.Background(), connect.NewRequest(&federationv1.GetHubInfoRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Msg.GetHub().GetPublicKey()) != ed25519.PublicKeySize {
		t.Fatal("ephemeral key is not published")
	}
}

func writeKeyFile(t *testing.T, body []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hub.key")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
