package server

// OIDC BFF routes (M3.5): /auth/login, /auth/callback and /auth/logout.
//
// The browser never holds a token. `login` starts an authorization-code flow
// with PKCE and parks state, nonce, the code verifier and the return path in
// an HMAC-signed cookie; `callback` consumes that transaction, exchanges the
// code at the provider's token endpoint (client_secret_post) and stores the
// resulting subject and scopes in a second signed cookie; `logout` clears that
// cookie and sends the browser to the provider's end_session endpoint. The
// session cookie doubles as the caller identity for every other route (see
// sessionMiddleware), so there is no server-side session store at all.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"

	"go.pilab.hu/agenthub/internal/auth/sso"
	"go.pilab.hu/agenthub/internal/authz"
	"go.pilab.hu/agenthub/internal/config"
)

const (
	// loginCookieName carries the pending login transaction.
	loginCookieName = "agenthub_login"
	// sessionCookieName carries the established session (subject + scopes).
	sessionCookieName = "agenthub_session"
	// loginTTL bounds an authorization-code round trip, matching the 10m
	// transaction TTL of docs/architecture/web-and-sso.md §4.
	loginTTL = 10 * time.Minute
	// sessionTTL is the absolute lifetime of a BFF session. The idle
	// timeout and server-side refresh of web-and-sso.md §4 are not
	// implemented yet (see ROADMAP M3.5).
	sessionTTL = 12 * time.Hour
	// bffHTTPTimeout bounds discovery and token-endpoint calls.
	bffHTTPTimeout = 10 * time.Second
	// maxProviderBodyBytes caps discovery and token responses.
	maxProviderBodyBytes = 1 << 20
	// maxReturnToLen caps the return path stored in the login cookie.
	maxReturnToLen = 1024
	// authCallbackPath is the default redirect URI path.
	authCallbackPath = "/auth/callback"
)

// Sentinel causes for the callback's status mapping: a rejected request (bad
// code, bad client credentials) is the flow's problem and restarts the login,
// anything else is an upstream outage the operator has to hear about.
var (
	errIdPRejected      = errors.New("bff: identity provider rejected the request")
	errInvalidSignature = errors.New("bff: invalid signature")
)

// httpError writes a plain-text status for the browser-facing BFF routes
// (they are not JSON clients) and reports the request as handled to echo.
func httpError(c echo.Context, msg string, code int) error {
	http.Error(c.Response(), msg, code)
	return nil
}

// oidcEndpoints is the subset of the OIDC discovery document the BFF uses.
type oidcEndpoints struct {
	Authorize  string `json:"authorization_endpoint"`
	Token      string `json:"token_endpoint"`
	EndSession string `json:"end_session_endpoint"`
}

// loginClaims is the pending login transaction kept in loginCookieName.
type loginClaims struct {
	State    string `json:"state"`
	Nonce    string `json:"nonce"`
	Verifier string `json:"verifier"`
	ReturnTo string `json:"return_to"`
	Expires  int64  `json:"exp"`
}

// sessionClaims is the established session kept in sessionCookieName. Only the
// identity is stored: access and refresh tokens are deliberately dropped at
// the callback, so a stolen cookie cannot be replayed at the provider.
type sessionClaims struct {
	Subject string   `json:"sub"`
	Scopes  []string `json:"scopes,omitempty"`
	Expires int64    `json:"exp"`
}

// tokenResponse is the subset of the token endpoint answer the BFF acts on.
type tokenResponse struct {
	IDToken string `json:"id_token"`
	Scope   string `json:"scope"`
}

// bffHandler serves the three BFF routes. It is safe for concurrent use: the
// only mutable state is the cached discovery document, guarded by mu.
type bffHandler struct {
	clientID      string
	clientSecret  string
	redirectURL   string
	postLogoutURL string
	scopes        []string
	secret        []byte // HMAC key for both cookies
	discoveryURL  string
	client        *http.Client
	idToken       *sso.Verifier

	mu        sync.Mutex
	endpoints *oidcEndpoints
}

// newBFF builds the BFF handlers when the deployment configured a browser
// client (`auth.client_id` plus `auth.issuer`), and returns nil otherwise —
// the routes then simply do not exist and /auth/* answers 404. A broken
// id_token verifier aborts startup, exactly like newAuthenticator.
func newBFF(cfg *config.Config) *bffHandler {
	if cfg == nil || cfg.Auth.ClientID == "" || cfg.Auth.Issuer == "" {
		return nil
	}
	redirectURL := strings.TrimSpace(cfg.Auth.RedirectURL)
	if redirectURL == "" {
		// config.Validate requires public_url alongside client_id, so the
		// conventional /auth/callback path can usually be derived. Without
		// either value there is nowhere to send the browser back to and the
		// BFF stays disabled.
		if publicURL := strings.TrimSpace(cfg.Server.PublicURL); publicURL != "" {
			redirectURL = strings.TrimRight(publicURL, "/") + authCallbackPath
		}
	}
	if redirectURL == "" {
		return nil
	}

	idToken, err := sso.New(sso.Config{
		Issuer: cfg.Auth.Issuer,
		// An authorization-code id_token is issued to the client, so its
		// `aud` is the client id (web-and-sso.md §2.5), not the API
		// audience used for bearer tokens.
		Audience:          cfg.Auth.ClientID,
		AudienceAllowlist: []string{cfg.Auth.ClientID},
		JWKSURL:           cfg.Auth.JWKSURL,
		DiscoveryURL:      cfg.Auth.DiscoveryURL,
		ClockSkew:         cfg.Auth.ClockSkew,
		JWKSCacheTTL:      cfg.Auth.JWKSCacheTTL,
	})
	if err != nil {
		panic(fmt.Errorf("bff id_token verification: %w", err))
	}

	discoveryURL := strings.TrimSpace(cfg.Auth.DiscoveryURL)
	if discoveryURL == "" {
		discoveryURL = strings.TrimRight(cfg.Auth.Issuer, "/") + "/.well-known/openid-configuration"
	}

	return &bffHandler{
		clientID:      cfg.Auth.ClientID,
		clientSecret:  cfg.Auth.ClientSecret,
		redirectURL:   redirectURL,
		postLogoutURL: strings.TrimSpace(cfg.Auth.PostLogoutRedirectURL),
		scopes:        append([]string(nil), cfg.Auth.Scopes...),
		secret:        []byte(cfg.Auth.SessionSecret),
		discoveryURL:  discoveryURL,
		client:        &http.Client{Timeout: bffHTTPTimeout},
		idToken:       idToken,
	}
}

// discover resolves and caches the provider's endpoints. Only a successful
// resolution is remembered, so a transient IdP outage costs one failed lookup
// per attempt instead of a permanently broken login.
func (b *bffHandler) discover(ctx context.Context) (oidcEndpoints, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.endpoints != nil {
		return *b.endpoints, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.discoveryURL, nil)
	if err != nil {
		return oidcEndpoints{}, fmt.Errorf("bff: discovery: %w", err)
	}
	req.Header.Set(echo.HeaderAccept, "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		return oidcEndpoints{}, fmt.Errorf("bff: discovery %s: %w", b.discoveryURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return oidcEndpoints{}, fmt.Errorf("bff: discovery %s returned %s", b.discoveryURL, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderBodyBytes+1))
	if err != nil {
		return oidcEndpoints{}, fmt.Errorf("bff: discovery %s: %w", b.discoveryURL, err)
	}
	if len(body) > maxProviderBodyBytes {
		return oidcEndpoints{}, fmt.Errorf("bff: discovery %s exceeds %d bytes", b.discoveryURL, maxProviderBodyBytes)
	}

	var doc oidcEndpoints
	if err := json.Unmarshal(body, &doc); err != nil {
		return oidcEndpoints{}, fmt.Errorf("bff: discovery %s: %w", b.discoveryURL, err)
	}
	if doc.Authorize == "" || doc.Token == "" {
		return oidcEndpoints{}, fmt.Errorf("bff: discovery %s has no authorization/token endpoint", b.discoveryURL)
	}
	b.endpoints = &doc
	return doc, nil
}

// login redirects the browser to the provider's authorization endpoint with a
// fresh state, nonce and PKCE challenge. The verifier never leaves the signed
// login cookie.
func (b *bffHandler) login(c echo.Context) error {
	ctx := c.Request().Context()
	ep, err := b.discover(ctx)
	if err != nil {
		c.Logger().Error(err)
		return httpError(c, "identity provider unavailable, retry later", http.StatusServiceUnavailable)
	}

	state, err := randomToken()
	if err != nil {
		return err
	}
	nonce, err := randomToken()
	if err != nil {
		return err
	}
	verifier, err := randomToken()
	if err != nil {
		return err
	}

	lc := loginClaims{
		State:    state,
		Nonce:    nonce,
		Verifier: verifier,
		ReturnTo: safeReturnTo(c.QueryParam("return_to")),
		Expires:  time.Now().Add(loginTTL).Unix(),
	}
	if err := b.setCookie(c, loginCookieName, lc, loginTTL); err != nil {
		return err
	}

	u, err := url.Parse(ep.Authorize)
	if err != nil || u.Scheme == "" || u.Host == "" {
		c.Logger().Error("bff: unusable authorization endpoint ", ep.Authorize)
		return httpError(c, "identity provider unavailable, retry later", http.StatusServiceUnavailable)
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", b.clientID)
	q.Set("redirect_uri", b.redirectURL)
	if len(b.scopes) > 0 {
		q.Set("scope", strings.Join(b.scopes, " "))
	}
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("code_challenge", pkceChallenge(verifier))
	q.Set("code_challenge_method", "S256")
	u.RawQuery = q.Encode()
	return c.Redirect(http.StatusFound, u.String())
}

// callback consumes the login transaction, exchanges the code for tokens and
// installs the session cookie. The transaction cookie is cleared on every
// attempt, so a state is good for exactly one callback.
func (b *bffHandler) callback(c echo.Context) error {
	if e := c.QueryParam("error"); e != "" {
		c.Logger().Error("bff: provider reported ", e, ": ", c.QueryParam("error_description"))
		return httpError(c, "login failed, start again", http.StatusBadRequest)
	}

	lc, ok := b.readLogin(c)
	b.clearCookie(c, loginCookieName)
	if !ok {
		return httpError(c, "login session expired or invalid, start again", http.StatusBadRequest)
	}
	code := c.QueryParam("code")
	if code == "" || !hmac.Equal([]byte(c.QueryParam("state")), []byte(lc.State)) {
		return httpError(c, "invalid login state", http.StatusBadRequest)
	}

	ctx := c.Request().Context()
	ep, err := b.discover(ctx)
	if err != nil {
		c.Logger().Error(err)
		return httpError(c, "identity provider unavailable, retry later", http.StatusServiceUnavailable)
	}
	tok, err := b.exchangeCode(ctx, ep, code, lc.Verifier)
	if err != nil {
		c.Logger().Error(err)
		if errors.Is(err, errIdPRejected) {
			return httpError(c, "login failed, start again", http.StatusBadRequest)
		}
		return httpError(c, "identity provider unavailable, retry later", http.StatusBadGateway)
	}

	// Signature, issuer, audience and expiry first (the verifier fetches
	// the provider's JWKS), then the nonce that binds the token to this
	// very login transaction. The payload is integrity-protected by the
	// verification above, so reading it afterwards is safe.
	claims, err := b.idToken.Verify(tok.IDToken)
	if err != nil {
		c.Logger().Error("bff: id_token rejected: ", err)
		return httpError(c, "login failed, start again", http.StatusBadRequest)
	}
	nonce, err := idTokenNonce(tok.IDToken)
	if err != nil || !hmac.Equal([]byte(nonce), []byte(lc.Nonce)) {
		c.Logger().Error("bff: id_token nonce does not match the login transaction")
		return httpError(c, "login failed, start again", http.StatusBadRequest)
	}

	scopes := claims.Scopes
	if len(scopes) == 0 && tok.Scope != "" {
		scopes = authz.ParseScopes(tok.Scope)
	}
	sess := sessionClaims{
		Subject: claims.Subject,
		Scopes:  scopes,
		Expires: time.Now().Add(sessionTTL).Unix(),
	}
	if err := b.setCookie(c, sessionCookieName, sess, sessionTTL); err != nil {
		return err
	}
	return c.Redirect(http.StatusSeeOther, lc.ReturnTo)
}

// logout clears the local session and redirects to the provider's
// end_session_endpoint (web-and-sso.md §4). It never fails: with the provider
// unreachable the browser still lands back on the hub.
func (b *bffHandler) logout(c echo.Context) error {
	b.clearCookie(c, sessionCookieName)

	target := b.postLogoutURL
	if target == "" {
		target = "/"
	}
	ep, err := b.discover(c.Request().Context())
	if err != nil {
		c.Logger().Error(err)
		return c.Redirect(http.StatusFound, target)
	}
	if ep.EndSession == "" {
		return c.Redirect(http.StatusFound, target)
	}
	u, err := url.Parse(ep.EndSession)
	if err != nil || u.Scheme == "" || u.Host == "" {
		c.Logger().Error("bff: unusable end_session endpoint ", ep.EndSession)
		return c.Redirect(http.StatusFound, target)
	}
	q := u.Query()
	q.Set("client_id", b.clientID)
	if b.postLogoutURL != "" {
		q.Set("post_logout_redirect_uri", b.postLogoutURL)
	}
	u.RawQuery = q.Encode()
	return c.Redirect(http.StatusFound, u.String())
}

// sessionMiddleware installs the signed session cookie as the caller identity
// when no authenticator produced one (M3.5). A bearer token or the trusted
// proxy headers keep precedence, so an API client is never shadowed by a
// stale browser cookie. SameSite=Lax keeps the cookie off cross-site
// state-changing requests, which is what makes it usable without CSRF tokens.
func (b *bffHandler) sessionMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if sub, _ := c.Get(SubjectKey).(string); sub != "" {
				return next(c)
			}
			if sess, ok := b.readSession(c); ok {
				setIdentity(c, authz.Identity{Subject: sess.Subject, Scopes: sess.Scopes})
			}
			return next(c)
		}
	}
}

// exchangeCode performs the back-channel code exchange. Confidential clients
// authenticate with client_secret_basic (RFC 6749 §2.3.1); a deployment that
// left the secret empty is treated as a public client and identified in the
// form body instead, with PKCE still binding the code.
func (b *bffHandler) exchangeCode(ctx context.Context, ep oidcEndpoints, code, verifier string) (tokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", b.redirectURL)
	form.Set("code_verifier", verifier)
	// Shadow SSO reads client credentials from the POST body only. Send
	// client_secret_post for confidential clients; the client id is part of
	// every exchange so public clients work the same way.
	form.Set("client_id", b.clientID)
	if b.clientSecret != "" {
		form.Set("client_secret", b.clientSecret)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.Token, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("bff: token request: %w", err)
	}
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	req.Header.Set(echo.HeaderAccept, "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("bff: token endpoint: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderBodyBytes+1))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("bff: token endpoint: %w", err)
	}
	if len(body) > maxProviderBodyBytes {
		return tokenResponse{}, fmt.Errorf("bff: token response exceeds %d bytes", maxProviderBodyBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// A 4xx is the flow's fault (expired code, wrong client
		// credentials): the browser starts over. A 5xx or an unreachable
		// endpoint is an upstream outage.
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return tokenResponse{}, fmt.Errorf("%w: token endpoint returned %s", errIdPRejected, resp.Status)
		}
		return tokenResponse{}, fmt.Errorf("bff: token endpoint returned %s", resp.Status)
	}

	var tok tokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return tokenResponse{}, fmt.Errorf("bff: token response: %w", err)
	}
	if tok.IDToken == "" {
		return tokenResponse{}, fmt.Errorf("%w: token response has no id_token", errIdPRejected)
	}
	return tok, nil
}

// setCookie writes an HMAC-signed cookie holding payload.
func (b *bffHandler) setCookie(c echo.Context, name string, payload any, ttl time.Duration) error {
	value, err := signCookie(b.secret, payload)
	if err != nil {
		return err
	}
	// gosec G124 demands Secure: true; a dev login runs over plain HTTP, so
	// the flag follows the request scheme (cookieSecure) instead. HttpOnly
	// and SameSite=Lax are set unconditionally below.
	//nolint:gosec // Secure is assigned by cookieSecure, see above.
	c.SetCookie(&http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   int(ttl / time.Second),
		Expires:  time.Now().Add(ttl),
		HttpOnly: true,
		Secure:   cookieSecure(c),
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// clearCookie expires a cookie the browser may be holding.
func (b *bffHandler) clearCookie(c echo.Context, name string) {
	//nolint:gosec // Secure is assigned by cookieSecure, see setCookie.
	c.SetCookie(&http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
		HttpOnly: true,
		Secure:   cookieSecure(c),
		SameSite: http.SameSiteLaxMode,
	})
}

// readLogin returns the pending login transaction, if the cookie is intact
// and the transaction has not expired.
func (b *bffHandler) readLogin(c echo.Context) (loginClaims, bool) {
	var lc loginClaims
	if !b.readCookie(c, loginCookieName, &lc) {
		return loginClaims{}, false
	}
	if lc.Expires <= time.Now().Unix() || lc.State == "" || lc.Verifier == "" || lc.Nonce == "" {
		return loginClaims{}, false
	}
	return lc, true
}

// readSession returns the established session, if the cookie is intact and
// the session has not expired.
func (b *bffHandler) readSession(c echo.Context) (sessionClaims, bool) {
	var sess sessionClaims
	if !b.readCookie(c, sessionCookieName, &sess) {
		return sessionClaims{}, false
	}
	if sess.Expires <= time.Now().Unix() || sess.Subject == "" {
		return sessionClaims{}, false
	}
	return sess, true
}

// readCookie verifies the cookie's signature into dst.
func (b *bffHandler) readCookie(c echo.Context, name string, dst any) bool {
	ck, err := c.Cookie(name)
	if err != nil || ck == nil {
		return false
	}
	return verifyCookie(b.secret, ck.Value, dst) == nil
}

// signCookie serializes payload as base64url(JSON) and tags it with an
// HMAC-SHA256 keyed by secret. The cookies carry no secret material (the
// subject and scopes are the client's own identity), so they are signed but
// not encrypted.
func signCookie(secret []byte, payload any) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("bff: sign cookie: %w", err)
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body) // hash.Hash.Write never returns an error
	enc := base64.RawURLEncoding
	return enc.EncodeToString(body) + "." + enc.EncodeToString(mac.Sum(nil)), nil
}

// verifyCookie checks the tag and decodes the payload. A tampered, truncated
// or foreign cookie fails with errInvalidSignature.
func verifyCookie(secret []byte, raw string, dst any) error {
	body, tag, ok := strings.Cut(raw, ".")
	if !ok {
		return errInvalidSignature
	}
	enc := base64.RawURLEncoding
	got, err := enc.DecodeString(tag)
	if err != nil {
		return errInvalidSignature
	}
	want, err := enc.DecodeString(body)
	if err != nil {
		return errInvalidSignature
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(want)
	if !hmac.Equal(got, mac.Sum(nil)) {
		return errInvalidSignature
	}
	if err := json.Unmarshal(want, dst); err != nil {
		return fmt.Errorf("%w: %w", errInvalidSignature, err)
	}
	return nil
}

// randomToken returns 32 bytes of crypto/rand noise as an unpadded base64url
// string (43 characters), used for state, nonce and the PKCE verifier.
func randomToken() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("bff: random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf[:]), nil
}

// pkceChallenge is the S256 code challenge for verifier.
func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// idTokenNonce reads the `nonce` claim out of an id_token. Callers must run it
// only after sso.Verifier accepted the token: the signature is what makes the
// payload trustworthy.
func idTokenNonce(raw string) (string, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("%w: not a compact JWS", errInvalidSignature)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("%w: %w", errInvalidSignature, err)
	}
	var claims struct {
		Nonce string `json:"nonce"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("%w: %w", errInvalidSignature, err)
	}
	return claims.Nonce, nil
}

// safeReturnTo keeps the post-login redirect on this origin: only a
// same-origin path is accepted, everything else (absolute URLs, protocol-relative
// `//host`, backslashes, control characters) falls back to "/". The parameter
// is capped so the login cookie cannot grow past the browser's cookie limit.
func safeReturnTo(raw string) string {
	const home = "/"
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxReturnToLen {
		return home
	}
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.Contains(raw, "\\") {
		return home
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return home
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Scheme != "" || u.Host != "" || u.User != nil {
		return home
	}
	return raw
}

// cookieSecure marks cookies Secure on TLS, including the proxy-terminated
// TLS of a production deployment (X-Forwarded-Proto), so plain-HTTP dev
// logins keep working.
func cookieSecure(c echo.Context) bool {
	r := c.Request()
	if r.TLS != nil || r.URL.Scheme == "https" {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
