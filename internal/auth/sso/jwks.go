package sso

import (
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// JWKS fetching and caching.
//
// The key document is fetched lazily on the first token, kept for
// JWKSCacheTTL and refetched when a token names an unknown `kid` (key
// rotation), at most once per cooldown. A failed refresh keeps serving the
// already-fetched keys so a short IdP outage does not invalidate every
// cached token (operations.md §11.4).
const (
	// maxDocumentBytes caps JWKS and discovery responses.
	maxDocumentBytes = 1 << 20
	// defaultKidRefreshCooldown rate-limits forced refreshes triggered by
	// an unknown `kid`, so a bogus token cannot hammer the IdP.
	defaultKidRefreshCooldown = 30 * time.Second
)

// jwksCache holds the provider's verification keys. Callers hold c.mu for
// every method: key sets are small and fetches are rare, so a single lock
// keeps the refresh logic free of stampedes.
type jwksCache struct {
	mu sync.Mutex

	client       *http.Client
	jwksURL      string
	discoveryURL string
	ttl          time.Duration
	cooldown     time.Duration
	now          func() time.Time

	keys        []jose.JSONWebKey
	fetchedAt   time.Time
	lastAttempt time.Time
	lastErr     error
	resolvedURL string
}

// keyFor returns the RSA verification key for kid. An unknown kid triggers
// a rate-limited refresh; a missing kid is only accepted when the JWKS holds
// exactly one key.
func (c *jwksCache) keyFor(kid string) (jose.JSONWebKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.refreshLocked(false); err != nil {
		return jose.JSONWebKey{}, err
	}
	if k, ok := c.lookupLocked(kid); ok {
		return k, nil
	}

	// Unknown kid: the provider may have rotated its keys since the last
	// fetch. Refresh once (subject to the cooldown) and look again.
	if err := c.refreshLocked(true); err != nil {
		return jose.JSONWebKey{}, err
	}
	if k, ok := c.lookupLocked(kid); ok {
		return k, nil
	}
	return jose.JSONWebKey{}, fmt.Errorf("%w: kid %q", ErrKeyNotFound, kid)
}

// refreshLocked fetches the key document when the cache is empty, stale or
// (with force) a token referenced an unknown key. Errors are returned only
// when no usable keys are available at all; a stale set keeps validating
// tokens, and a repeated failure surfaces the original cause instead of
// hammering the IdP once per request.
func (c *jwksCache) refreshLocked(force bool) error {
	now := c.now()
	if !force && c.fetchedAt.Add(c.ttl).After(now) {
		return nil
	}
	if now.Sub(c.lastAttempt) < c.cooldown {
		if len(c.keys) == 0 {
			return c.lastErr
		}
		return nil
	}

	c.lastAttempt = now
	keys, err := c.fetchLocked()
	if err != nil {
		c.lastErr = err
		if len(c.keys) > 0 {
			return nil // keep validating with the cached set
		}
		return err
	}
	c.keys = keys
	c.fetchedAt = now
	c.lastErr = nil
	return nil
}

// fetchLocked resolves the key document URL (through OIDC discovery when
// only the issuer is known) and parses it. Only RSA public keys are kept:
// this verifier accepts RS256 exclusively.
func (c *jwksCache) fetchLocked() ([]jose.JSONWebKey, error) {
	endpoint := c.jwksURL
	if endpoint == "" {
		resolved, err := c.resolveDiscoveryLocked()
		if err != nil {
			return nil, err
		}
		endpoint = resolved
	}

	body, err := c.getLocked(endpoint)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrJWKSFetch, endpoint, err)
	}

	keys := make([]jose.JSONWebKey, 0, len(doc.Keys))
	for _, raw := range doc.Keys {
		var k jose.JSONWebKey
		// Skip keys this verifier cannot use (or cannot parse) instead of
		// rejecting the whole document.
		if err := json.Unmarshal(raw, &k); err != nil {
			continue
		}
		if !k.Valid() {
			continue
		}
		if _, ok := k.Key.(*rsa.PublicKey); !ok {
			continue
		}
		if k.Algorithm != "" && k.Algorithm != string(jose.RS256) {
			continue
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: %s holds no RSA public keys", ErrJWKSFetch, endpoint)
	}
	return keys, nil
}

// resolveDiscoveryLocked fetches the OIDC discovery document once and
// remembers its jwks_uri.
func (c *jwksCache) resolveDiscoveryLocked() (string, error) {
	if c.resolvedURL != "" {
		return c.resolvedURL, nil
	}
	body, err := c.getLocked(c.discoveryURL)
	if err != nil {
		return "", err
	}
	var disc struct {
		JWKSURI string `json:"jwks_uri"`
	}
	if err := json.Unmarshal(body, &disc); err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrJWKSFetch, c.discoveryURL, err)
	}
	if disc.JWKSURI == "" {
		return "", fmt.Errorf("%w: %s has no jwks_uri", ErrJWKSFetch, c.discoveryURL)
	}
	c.resolvedURL = disc.JWKSURI
	return c.resolvedURL, nil
}

// getLocked performs a bounded GET of a provider document.
func (c *jwksCache) getLocked(endpoint string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrJWKSFetch, endpoint, err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrJWKSFetch, endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: %s returned %s", ErrJWKSFetch, endpoint, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDocumentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrJWKSFetch, endpoint, err)
	}
	if len(body) > maxDocumentBytes {
		return nil, fmt.Errorf("%w: %s exceeds %d bytes", ErrJWKSFetch, endpoint, maxDocumentBytes)
	}
	return body, nil
}

// lookupLocked selects the key for kid from the cached set.
func (c *jwksCache) lookupLocked(kid string) (jose.JSONWebKey, bool) {
	if kid != "" {
		for _, k := range c.keys {
			if k.KeyID == kid {
				return k, true
			}
		}
		return jose.JSONWebKey{}, false
	}
	// No kid in the token header: only an unambiguous single-key set can be
	// matched without guessing.
	if len(c.keys) == 1 {
		return c.keys[0], true
	}
	return jose.JSONWebKey{}, false
}
