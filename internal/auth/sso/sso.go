// Package sso validates Shadow SSO OIDC bearer tokens against the identity
// provider's JWKS.
//
// Scope for M3.3: RS256 access tokens verified locally. A token is accepted
// when the signature checks out against a cached JWKS key, `iss` equals the
// configured issuer, `aud` matches the configured audience (or one of the
// allowlist entries), `exp`/`nbf`/`iat` hold within the clock skew, and `sub`
// is present. Introspection, revocation and the authorization-code BFF flow
// are out of scope here.
package sso

import (
	"crypto/rsa"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"go.pilab.hu/agenthub/internal/authz"
)

// Defaults applied when the corresponding Config field is left at its zero
// value.
const (
	// DefaultClockSkew tolerates clock drift between hub and IdP.
	DefaultClockSkew = 60 * time.Second
	// DefaultJWKSCacheTTL is how long a fetched key set stays usable.
	DefaultJWKSCacheTTL = 10 * time.Minute
	// DefaultFetchTimeout bounds JWKS and discovery requests.
	DefaultFetchTimeout = 10 * time.Second
)

// Configuration and validation errors. They are raised before any token is
// looked at, so a misconfigured hub fails loudly instead of rejecting every
// caller with a token error.
var (
	// ErrConfig reports an unusable Config.
	ErrConfig = errors.New("sso: invalid configuration")
	// ErrInvalidToken reports a token that is malformed or carries
	// unacceptable claims.
	ErrInvalidToken = errors.New("sso: invalid token")
	// ErrExpired reports a token past its expiry (plus clock skew).
	ErrExpired = errors.New("sso: token expired")
	// ErrNotYetValid reports a token whose nbf/iat lies in the future.
	ErrNotYetValid = errors.New("sso: token not yet valid")
	// ErrIssuerMismatch reports an unexpected `iss` claim.
	ErrIssuerMismatch = errors.New("sso: issuer mismatch")
	// ErrAudienceMismatch reports an `aud` claim outside the allowlist.
	ErrAudienceMismatch = errors.New("sso: audience mismatch")
	// ErrKeyNotFound reports that the JWKS holds no key for the token.
	ErrKeyNotFound = errors.New("sso: verification key not found")
	// ErrJWKSFetch reports that the key document could not be fetched or
	// holds no usable key.
	ErrJWKSFetch = errors.New("sso: jwks unavailable")
)

// Config describes the OIDC provider the hub trusts.
type Config struct {
	// Issuer is the expected `iss` claim and, unless DiscoveryURL or
	// JWKSURL says otherwise, the OIDC discovery base URL. Required.
	Issuer string
	// Audience is the primary expected `aud` claim.
	Audience string
	// AudienceAllowlist lists further accepted audiences (web/CLI client
	// ids, the federation audience, ...).
	AudienceAllowlist []string
	// JWKSURL is the key document URL. When empty it is resolved through
	// OIDC discovery (see DiscoveryURL).
	JWKSURL string
	// DiscoveryURL overrides the discovery document URL, which defaults to
	// `issuer + /.well-known/openid-configuration`.
	DiscoveryURL string
	// ClockSkew tolerates clock drift; zero means DefaultClockSkew.
	ClockSkew time.Duration
	// JWKSCacheTTL is how long a fetched key set is used before refetching;
	// zero means DefaultJWKSCacheTTL.
	JWKSCacheTTL time.Duration
	// FetchTimeout bounds JWKS/discovery requests; zero means
	// DefaultFetchTimeout. Ignored when HTTPClient is set.
	FetchTimeout time.Duration
	// DefaultScopes are granted when the token carries neither a `scope`
	// nor a `scopes` claim. Left empty by the server so a scope-less token
	// stays unprivileged (fail closed).
	DefaultScopes []string
	// HTTPClient overrides the client used for JWKS/discovery requests.
	HTTPClient *http.Client
	// Now overrides the clock; tests use it to move time.
	Now func() time.Time
}

// Claims is the validated subset of a token the hub acts on.
type Claims struct {
	// Subject is the `sub` claim: the stable SSO principal id.
	Subject string
	// Scopes are the coarse OAuth scopes attached to the caller.
	Scopes []string
	// Issuer is the (validated) `iss` claim.
	Issuer string
	// Audience is the `aud` claim.
	Audience []string
	// ID is the `jti` claim, used for audit correlation.
	ID string
	// IssuedAt/NotBefore/ExpiresAt mirror iat/nbf/exp; the zero Time means
	// the claim was absent (exp is always present, see Verify).
	IssuedAt  time.Time
	NotBefore time.Time
	ExpiresAt time.Time
}

// Verifier validates bearer tokens against a cached JWKS. It is safe for
// concurrent use.
type Verifier struct {
	issuer        string
	audiences     []string
	skew          time.Duration
	defaultScopes []string
	now           func() time.Time
	cache         *jwksCache
}

// New builds a Verifier from cfg. It fails fast on a misconfiguration
// (missing issuer, no audience, unparsable URL, negative skew).
func New(cfg Config) (*Verifier, error) {
	issuer := strings.TrimRight(strings.TrimSpace(cfg.Issuer), "/")
	if issuer == "" {
		return nil, fmt.Errorf("%w: auth.issuer is required", ErrConfig)
	}
	if err := requireAbsoluteURL("auth.issuer", issuer); err != nil {
		return nil, err
	}

	audiences := make([]string, 0, 1+len(cfg.AudienceAllowlist))
	addAudience := func(a string) {
		a = strings.TrimSpace(a)
		if a != "" && !slices.Contains(audiences, a) {
			audiences = append(audiences, a)
		}
	}
	addAudience(cfg.Audience)
	for _, a := range cfg.AudienceAllowlist {
		addAudience(a)
	}
	if len(audiences) == 0 {
		return nil, fmt.Errorf("%w: auth.audience (or auth.audience_allowlist) is required", ErrConfig)
	}

	if cfg.ClockSkew < 0 {
		return nil, fmt.Errorf("%w: auth.clock_skew must not be negative", ErrConfig)
	}
	skew := cfg.ClockSkew
	if skew == 0 {
		skew = DefaultClockSkew
	}
	ttl := cfg.JWKSCacheTTL
	if ttl <= 0 {
		ttl = DefaultJWKSCacheTTL
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	client := cfg.HTTPClient
	if client == nil {
		timeout := cfg.FetchTimeout
		if timeout <= 0 {
			timeout = DefaultFetchTimeout
		}
		client = &http.Client{Timeout: timeout}
	}
	discoveryURL := strings.TrimSpace(cfg.DiscoveryURL)
	if discoveryURL == "" {
		discoveryURL = issuer + "/.well-known/openid-configuration"
	}
	jwksURL := strings.TrimSpace(cfg.JWKSURL)
	if jwksURL != "" {
		if err := requireAbsoluteURL("auth.jwks_url", jwksURL); err != nil {
			return nil, err
		}
	}
	if cfg.DiscoveryURL != "" || jwksURL == "" {
		if err := requireAbsoluteURL("auth.discovery_url", discoveryURL); err != nil {
			return nil, err
		}
	}

	return &Verifier{
		issuer:        issuer,
		audiences:     audiences,
		skew:          skew,
		defaultScopes: append([]string(nil), cfg.DefaultScopes...),
		now:           now,
		cache: &jwksCache{
			client:       client,
			jwksURL:      jwksURL,
			discoveryURL: discoveryURL,
			ttl:          ttl,
			cooldown:     defaultKidRefreshCooldown,
			now:          now,
		},
	}, nil
}

// Verify validates a raw JWT (compact serialization, RS256) and returns its
// claims. Every failure mode wraps one of the package's sentinel errors, so
// callers can classify with errors.Is.
func (v *Verifier) Verify(raw string) (Claims, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Claims{}, fmt.Errorf("%w: empty token", ErrInvalidToken)
	}

	// Restricting the allowed algorithms at parse time rejects `none` and
	// algorithm-confusion attacks (HS256 signed with a public key) before
	// any key is fetched.
	tok, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		return Claims{}, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	if len(tok.Headers) == 0 {
		return Claims{}, fmt.Errorf("%w: missing JOSE header", ErrInvalidToken)
	}

	key, err := v.cache.keyFor(tok.Headers[0].KeyID)
	if err != nil {
		return Claims{}, err
	}
	pub, ok := key.Key.(*rsa.PublicKey)
	if !ok {
		return Claims{}, fmt.Errorf("%w: key %q is not an RSA public key", ErrKeyNotFound, key.KeyID)
	}

	var tc tokenClaims
	if err := tok.Claims(pub, &tc); err != nil {
		// Signature verification and claim decoding both surface here;
		// neither is safe to attribute further, so report the token as
		// invalid and keep the cause for the operator.
		return Claims{}, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	return v.validate(tc)
}

// tokenClaims is the claim set the hub reads: the standard registered
// claims plus the two shapes OAuth scope claims arrive in.
type tokenClaims struct {
	jwt.Claims
	Scope  string   `json:"scope"`
	Scopes []string `json:"scopes"`
}

// validate applies the issuer, audience, time and subject rules and derives
// the caller's scopes.
func (v *Verifier) validate(tc tokenClaims) (Claims, error) {
	if tc.Issuer != v.issuer {
		return Claims{}, fmt.Errorf("%w: got %q, want %q", ErrIssuerMismatch, tc.Issuer, v.issuer)
	}
	if len(tc.Audience) == 0 {
		return Claims{}, fmt.Errorf("%w: missing aud claim", ErrAudienceMismatch)
	}
	matched := false
	for _, a := range tc.Audience {
		if slices.Contains(v.audiences, a) {
			matched = true
			break
		}
	}
	if !matched {
		return Claims{}, fmt.Errorf("%w: got %q, want one of %q", ErrAudienceMismatch, []string(tc.Audience), v.audiences)
	}
	if tc.Expiry == nil {
		return Claims{}, fmt.Errorf("%w: missing exp claim", ErrInvalidToken)
	}

	now := v.now()
	exp := time.Unix(int64(*tc.Expiry), 0)
	if now.After(exp.Add(v.skew)) {
		return Claims{}, fmt.Errorf("%w: expired at %s", ErrExpired, exp.UTC().Format(time.RFC3339))
	}
	if tc.NotBefore != nil {
		nbf := time.Unix(int64(*tc.NotBefore), 0)
		if now.Add(v.skew).Before(nbf) {
			return Claims{}, fmt.Errorf("%w: valid from %s", ErrNotYetValid, nbf.UTC().Format(time.RFC3339))
		}
	}
	if tc.IssuedAt != nil {
		iat := time.Unix(int64(*tc.IssuedAt), 0)
		if now.Add(v.skew).Before(iat) {
			return Claims{}, fmt.Errorf("%w: issued at %s", ErrNotYetValid, iat.UTC().Format(time.RFC3339))
		}
	}
	if tc.Subject == "" {
		return Claims{}, fmt.Errorf("%w: missing sub claim", ErrInvalidToken)
	}

	var issuedAt, notBefore time.Time
	if tc.IssuedAt != nil {
		issuedAt = time.Unix(int64(*tc.IssuedAt), 0)
	}
	if tc.NotBefore != nil {
		notBefore = time.Unix(int64(*tc.NotBefore), 0)
	}
	return Claims{
		Subject:   tc.Subject,
		Scopes:    v.scopesOf(tc),
		Issuer:    tc.Issuer,
		Audience:  []string(tc.Audience),
		ID:        tc.ID,
		IssuedAt:  issuedAt,
		NotBefore: notBefore,
		ExpiresAt: exp,
	}, nil
}

// scopesOf prefers the `scopes` array, then the OAuth `scope` claim
// (space- or comma-separated, the same splitting authz.ParseScopes applies
// to header values), and falls back to the configured defaults.
func (v *Verifier) scopesOf(tc tokenClaims) []string {
	src := tc.Scopes
	if len(src) == 0 && tc.Scope != "" {
		src = authz.ParseScopes(tc.Scope)
	}
	if len(src) == 0 {
		src = v.defaultScopes
	}
	out := make([]string, 0, len(src))
	for _, s := range src {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func requireAbsoluteURL(field, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("%w: %s must be an absolute http(s) URL, got %q", ErrConfig, field, raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: %s must use http or https, got %q", ErrConfig, field, u.Scheme)
	}
	return nil
}
