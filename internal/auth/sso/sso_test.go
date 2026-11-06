package sso

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/require"
)

const (
	testIssuer   = "https://sso.example.test"
	testAudience = "agenthub"
	testKid      = "kid-1"
)

// fakeIdP serves a mutable JWKS (and optionally an OIDC discovery document)
// over httptest, counting the requests it answers.
type fakeIdP struct {
	mu       sync.Mutex
	jwks     []byte
	discover bool
	hits     int
	server   *httptest.Server
}

func newFakeIdP(t *testing.T, discover bool, keys ...jose.JSONWebKey) *fakeIdP {
	t.Helper()
	idp := &fakeIdP{discover: discover}
	idp.setKeys(t, keys...)
	idp.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idp.mu.Lock()
		idp.hits++
		jwks, discover := idp.jwks, idp.discover
		idp.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch {
		case discover && r.URL.Path == "/.well-known/openid-configuration":
			_, _ = fmt.Fprintf(w, `{"issuer":%q,"jwks_uri":%q}`, testIssuer, idp.server.URL+"/jwks.json")
		case r.URL.Path == "/jwks.json":
			_, _ = w.Write(jwks)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(idp.server.Close)
	return idp
}

func (p *fakeIdP) setKeys(t *testing.T, keys ...jose.JSONWebKey) {
	t.Helper()
	body, err := json.Marshal(jose.JSONWebKeySet{Keys: keys})
	require.NoError(t, err)
	p.mu.Lock()
	p.jwks = body
	p.mu.Unlock()
}

func (p *fakeIdP) jwksURL() string { return p.server.URL + "/jwks.json" }

func (p *fakeIdP) requests() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.hits
}

// testKey returns a fresh RSA key published under testKid.
func testKey(t *testing.T) (*rsa.PrivateKey, jose.JSONWebKey) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	jwk := jose.JSONWebKey{Key: &priv.PublicKey, KeyID: testKid, Algorithm: string(jose.RS256), Use: "sig"}
	return priv, jwk
}

// newVerifier builds a Verifier pointed at idp with the standard claims.
func newVerifier(t *testing.T, idp *fakeIdP, mutate func(*Config)) *Verifier {
	t.Helper()
	cfg := Config{
		Issuer:   testIssuer,
		Audience: testAudience,
		JWKSURL:  idp.jwksURL(),
	}
	if mutate != nil {
		mutate(&cfg)
	}
	v, err := New(cfg)
	require.NoError(t, err)
	return v
}

// sign claims with priv under kid using RS256.
func sign(t *testing.T, priv *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	opts := (&jose.SignerOptions{}).WithType("JWT")
	if kid != "" {
		opts = opts.WithHeader("kid", kid)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: priv}, opts)
	require.NoError(t, err)
	payload, err := json.Marshal(claims)
	require.NoError(t, err)
	tok, err := signer.Sign(payload)
	require.NoError(t, err)
	raw, err := tok.CompactSerialize()
	require.NoError(t, err)
	return raw
}

// baseClaims is a valid, unexpired claim set.
func baseClaims() map[string]any {
	now := time.Now()
	return map[string]any{
		"iss":   testIssuer,
		"aud":   testAudience,
		"sub":   "alice",
		"scope": "agenthub.read agenthub.publish",
		"iat":   now.Unix(),
		"exp":   now.Add(5 * time.Minute).Unix(),
	}
}

func TestVerifyAcceptsValidToken(t *testing.T) {
	priv, jwk := testKey(t)
	idp := newFakeIdP(t, false, jwk)
	v := newVerifier(t, idp, nil)

	claims, err := v.Verify(sign(t, priv, testKid, baseClaims()))
	require.NoError(t, err)
	require.Equal(t, "alice", claims.Subject)
	require.Equal(t, []string{"agenthub.read", "agenthub.publish"}, claims.Scopes)
	require.Equal(t, testIssuer, claims.Issuer)
	require.Equal(t, []string{testAudience}, claims.Audience)
	require.False(t, claims.ExpiresAt.IsZero())
}

func TestVerifyRejectsExpiredToken(t *testing.T) {
	priv, jwk := testKey(t)
	idp := newFakeIdP(t, false, jwk)
	v := newVerifier(t, idp, func(c *Config) { c.ClockSkew = time.Minute })

	c := baseClaims()
	c["exp"] = time.Now().Add(-10 * time.Minute).Unix()
	_, err := v.Verify(sign(t, priv, testKid, c))
	require.ErrorIs(t, err, ErrExpired)

	// Inside the clock skew the same token is still accepted.
	c["exp"] = time.Now().Add(-30 * time.Second).Unix()
	claims, err := v.Verify(sign(t, priv, testKid, c))
	require.NoError(t, err)
	require.Equal(t, "alice", claims.Subject)
}

func TestVerifyRejectsWrongAudience(t *testing.T) {
	priv, jwk := testKey(t)
	idp := newFakeIdP(t, false, jwk)
	v := newVerifier(t, idp, func(c *Config) { c.AudienceAllowlist = []string{"agenthub-cli"} })

	c := baseClaims()
	c["aud"] = "someone-else"
	_, err := v.Verify(sign(t, priv, testKid, c))
	require.ErrorIs(t, err, ErrAudienceMismatch)

	// The allowlist accepts further audiences.
	c["aud"] = "agenthub-cli"
	claims, err := v.Verify(sign(t, priv, testKid, c))
	require.NoError(t, err)
	require.Equal(t, []string{"agenthub-cli"}, claims.Audience)

	// A missing aud claim is rejected too.
	delete(c, "aud")
	_, err = v.Verify(sign(t, priv, testKid, c))
	require.ErrorIs(t, err, ErrAudienceMismatch)
}

func TestVerifyRejectsWrongIssuer(t *testing.T) {
	priv, jwk := testKey(t)
	idp := newFakeIdP(t, false, jwk)
	v := newVerifier(t, idp, nil)

	c := baseClaims()
	c["iss"] = "https://evil.example.test"
	_, err := v.Verify(sign(t, priv, testKid, c))
	require.ErrorIs(t, err, ErrIssuerMismatch)
}

func TestVerifyRejectsWrongSignature(t *testing.T) {
	_, jwk := testKey(t)   // the published key...
	other, _ := testKey(t) // ...but the token is signed by another key
	idp := newFakeIdP(t, false, jwk)
	v := newVerifier(t, idp, nil)

	_, err := v.Verify(sign(t, other, testKid, baseClaims()))
	require.ErrorIs(t, err, ErrInvalidToken)
}

func TestVerifyRejectsUnsupportedAlgorithms(t *testing.T) {
	_, jwk := testKey(t)
	idp := newFakeIdP(t, false, jwk)
	v := newVerifier(t, idp, nil)

	// alg=none: unsigned token assembled by hand.
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"` + testIssuer + `","aud":"` + testAudience + `","sub":"alice","exp":9999999999}`))
	_, err := v.Verify(header + "." + payload + ".")
	require.ErrorIs(t, err, ErrInvalidToken)

	// HS256: symmetric signature, the classic algorithm-confusion attempt.
	hmacSigner, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.HS256, Key: []byte("0123456789abcdef0123456789abcdef")},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	require.NoError(t, err)
	body, err := json.Marshal(baseClaims())
	require.NoError(t, err)
	tok, err := hmacSigner.Sign(body)
	require.NoError(t, err)
	raw, err := tok.CompactSerialize()
	require.NoError(t, err)
	_, err = v.Verify(raw)
	require.ErrorIs(t, err, ErrInvalidToken)
}

func TestVerifyRejectsMissingRequiredClaims(t *testing.T) {
	priv, jwk := testKey(t)
	idp := newFakeIdP(t, false, jwk)
	v := newVerifier(t, idp, nil)

	c := baseClaims()
	delete(c, "exp")
	_, err := v.Verify(sign(t, priv, testKid, c))
	require.ErrorIs(t, err, ErrInvalidToken)

	c = baseClaims()
	delete(c, "sub")
	_, err = v.Verify(sign(t, priv, testKid, c))
	require.ErrorIs(t, err, ErrInvalidToken)

	c = baseClaims()
	c["nbf"] = time.Now().Add(10 * time.Minute).Unix()
	_, err = v.Verify(sign(t, priv, testKid, c))
	require.ErrorIs(t, err, ErrNotYetValid)

	_, err = v.Verify("")
	require.ErrorIs(t, err, ErrInvalidToken)
}

func TestVerifyScopes(t *testing.T) {
	priv, jwk := testKey(t)
	idp := newFakeIdP(t, false, jwk)

	t.Run("scope string", func(t *testing.T) {
		v := newVerifier(t, idp, nil)
		c := baseClaims()
		c["scope"] = "agenthub.read,agenthub.admin"
		claims, err := v.Verify(sign(t, priv, testKid, c))
		require.NoError(t, err)
		require.Equal(t, []string{"agenthub.read", "agenthub.admin"}, claims.Scopes)
	})

	t.Run("scopes array", func(t *testing.T) {
		v := newVerifier(t, idp, nil)
		c := baseClaims()
		delete(c, "scope")
		c["scopes"] = []string{"agenthub.read"}
		claims, err := v.Verify(sign(t, priv, testKid, c))
		require.NoError(t, err)
		require.Equal(t, []string{"agenthub.read"}, claims.Scopes)
	})

	t.Run("default when absent", func(t *testing.T) {
		v := newVerifier(t, idp, func(c *Config) { c.DefaultScopes = []string{"agenthub.read"} })
		c := baseClaims()
		delete(c, "scope")
		claims, err := v.Verify(sign(t, priv, testKid, c))
		require.NoError(t, err)
		require.Equal(t, []string{"agenthub.read"}, claims.Scopes)
	})

	t.Run("fail closed without config", func(t *testing.T) {
		v := newVerifier(t, idp, nil)
		c := baseClaims()
		delete(c, "scope")
		claims, err := v.Verify(sign(t, priv, testKid, c))
		require.NoError(t, err)
		require.Empty(t, claims.Scopes)
		require.NotNil(t, claims.Scopes) // never nil, callers compare slices
	})
}

func TestJWKSCachedAcrossTokens(t *testing.T) {
	priv, jwk := testKey(t)
	idp := newFakeIdP(t, false, jwk)
	v := newVerifier(t, idp, nil)

	for i := range 5 {
		c := baseClaims()
		c["sub"] = fmt.Sprintf("user-%d", i)
		claims, err := v.Verify(sign(t, priv, testKid, c))
		require.NoError(t, err)
		require.Equal(t, fmt.Sprintf("user-%d", i), claims.Subject)
	}
	require.Equal(t, 1, idp.requests(), "the JWKS must be fetched once and cached")
}

func TestJWKSRefetchedAfterRotation(t *testing.T) {
	oldPriv, oldJWK := testKey(t)
	idp := newFakeIdP(t, false, oldJWK)
	v := newVerifier(t, idp, nil)
	v.cache.cooldown = 0 // tests do not wait out the refresh cooldown

	_, err := v.Verify(sign(t, oldPriv, testKid, baseClaims()))
	require.NoError(t, err)
	require.Equal(t, 1, idp.requests())

	// The IdP rotates: a new key replaces the old one.
	newPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	idp.setKeys(t, jose.JSONWebKey{
		Key: &newPriv.PublicKey, KeyID: "kid-2", Algorithm: string(jose.RS256), Use: "sig",
	})

	// Until the cached set goes stale the old key keeps validating: rotation
	// must not invalidate tokens that are still legitimately signed.
	_, err = v.Verify(sign(t, oldPriv, testKid, baseClaims()))
	require.NoError(t, err)

	// Once the TTL lapses the cache is refetched and the retired key is gone.
	v.cache.ttl = 0
	_, err = v.Verify(sign(t, oldPriv, testKid, baseClaims()))
	require.ErrorIs(t, err, ErrKeyNotFound)

	// ...while the rotated key is picked up by the same refetch.
	claims, err := v.Verify(sign(t, newPriv, "kid-2", baseClaims()))
	require.NoError(t, err)
	require.Equal(t, "alice", claims.Subject)
	require.GreaterOrEqual(t, idp.requests(), 3)
}

func TestJWKSKeepsServingCachedKeysDuringOutage(t *testing.T) {
	priv, jwk := testKey(t)
	idp := newFakeIdP(t, false, jwk)
	v := newVerifier(t, idp, nil)
	v.cache.cooldown = 0

	_, err := v.Verify(sign(t, priv, testKid, baseClaims()))
	require.NoError(t, err)

	// The IdP goes away; tokens already validated against the cache keep
	// working until the TTL expires (operations.md §11.4).
	idp.server.Close()

	claims, err := v.Verify(sign(t, priv, testKid, baseClaims()))
	require.NoError(t, err)
	require.Equal(t, "alice", claims.Subject)

	// An unknown kid cannot be resolved during the outage, but the error
	// names the missing key rather than a transport failure.
	_, err = v.Verify(sign(t, priv, "kid-unknown", baseClaims()))
	require.ErrorIs(t, err, ErrKeyNotFound)
}

func TestJWKSViaDiscovery(t *testing.T) {
	priv, jwk := testKey(t)
	idp := newFakeIdP(t, true, jwk)
	v, err := New(Config{
		Issuer:       testIssuer,
		Audience:     testAudience,
		DiscoveryURL: idp.server.URL + "/.well-known/openid-configuration",
	})
	require.NoError(t, err)

	claims, err := v.Verify(sign(t, priv, testKid, baseClaims()))
	require.NoError(t, err)
	require.Equal(t, "alice", claims.Subject)

	_, err = v.Verify(sign(t, priv, testKid, baseClaims()))
	require.NoError(t, err)
	require.Equal(t, 2, idp.requests(), "discovery + JWKS, resolved once and cached")
}

func TestJWKSFetchFailure(t *testing.T) {
	priv, jwk := testKey(t)
	idp := newFakeIdP(t, false, jwk)
	v := newVerifier(t, idp, nil)
	v.cache.cooldown = 0

	// Nothing was fetched yet, so the failure surfaces as a fetch error.
	idp.server.Close()
	_, err := v.Verify(sign(t, priv, testKid, baseClaims()))
	require.ErrorIs(t, err, ErrJWKSFetch)
}

func TestVerifyConcurrent(t *testing.T) {
	priv, jwk := testKey(t)
	idp := newFakeIdP(t, false, jwk)
	v := newVerifier(t, idp, nil)
	raw := sign(t, priv, testKid, baseClaims())

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claims, err := v.Verify(raw)
			require.NoError(t, err)
			require.Equal(t, "alice", claims.Subject)
		}()
	}
	wg.Wait()
}

func TestNewValidation(t *testing.T) {
	idp := newFakeIdP(t, false)

	t.Run("issuer required", func(t *testing.T) {
		_, err := New(Config{Audience: testAudience, JWKSURL: idp.jwksURL()})
		require.ErrorIs(t, err, ErrConfig)
	})

	t.Run("audience required", func(t *testing.T) {
		_, err := New(Config{Issuer: testIssuer, JWKSURL: idp.jwksURL()})
		require.ErrorIs(t, err, ErrConfig)
	})

	t.Run("absolute issuer url", func(t *testing.T) {
		_, err := New(Config{Issuer: "sso.pilab.hu", Audience: testAudience})
		require.ErrorIs(t, err, ErrConfig)
	})

	t.Run("absolute jwks url", func(t *testing.T) {
		_, err := New(Config{Issuer: testIssuer, Audience: testAudience, JWKSURL: "/jwks.json"})
		require.ErrorIs(t, err, ErrConfig)
	})

	t.Run("negative clock skew", func(t *testing.T) {
		_, err := New(Config{Issuer: testIssuer, Audience: testAudience, ClockSkew: -time.Second})
		require.ErrorIs(t, err, ErrConfig)
	})
}
