package server

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"go.pilab.hu/agenthub/internal/authz"
	"go.pilab.hu/agenthub/internal/config"
)

// testSessionSecret satisfies the 32-character minimum of config.Validate.
const testSessionSecret = "test-session-secret-0123456789abcdef"

const (
	testClientID     = "agenthub-web"
	testClientSecret = "test-client-secret"
	testSubject      = "user-123"
	testCallbackURL  = "http://localhost:8080/auth/callback"
	testPostLogout   = "http://localhost:8080/"
)

// RSA keys are shared by every test: generating them costs far more than
// using them, and they are immutable.
var (
	testKeysOnce sync.Once
	testIDPKey   *rsa.PrivateKey
	rogueIDPKey  *rsa.PrivateKey
)

func idpKeys() (good, rogue *rsa.PrivateKey) {
	testKeysOnce.Do(func() {
		var err error
		if testIDPKey, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(fmt.Errorf("generate idp key: %w", err))
		}
		if rogueIDPKey, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(fmt.Errorf("generate rogue key: %w", err))
		}
	})
	return testIDPKey, rogueIDPKey
}

// fakeIdP is a minimal OIDC provider: discovery, JWKS and a token endpoint
// that records what it was asked for and issues an RS256 id_token.
type fakeIdP struct {
	server *httptest.Server

	mu            sync.Mutex
	tokenStatus   int // 0 means 200
	discoveryFail bool
	tokenCalls    int
	tokenForm     url.Values
	tokenAuth     string
	nonce         string
	rogue         bool // sign the id_token with a key the JWKS does not publish
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	idpKeys()
	p := &fakeIdP{}
	p.server = httptest.NewServer(p)
	t.Cleanup(p.server.Close)
	return p
}

func (p *fakeIdP) jwksURL() string { return p.server.URL + "/jwks.json" }

// tokenRecord is what the token endpoint observed, captured before the
// handler returned.
func (p *fakeIdP) tokenRecord() (url.Values, string, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tokenForm, p.tokenAuth, p.tokenCalls
}

func (p *fakeIdP) setNonce(nonce string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nonce = nonce
}

func (p *fakeIdP) setTokenStatus(status int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tokenStatus = status
}

func (p *fakeIdP) setDiscoveryFail(fail bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.discoveryFail = fail
}

func (p *fakeIdP) setRogue(rogue bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rogue = rogue
}

func (p *fakeIdP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		p.mu.Lock()
		fail := p.discoveryFail
		p.mu.Unlock()
		if fail {
			http.Error(w, "discovery down", http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{
			"issuer":                 p.server.URL,
			"authorization_endpoint": p.server.URL + "/authorize",
			"token_endpoint":         p.server.URL + "/token",
			"jwks_uri":               p.jwksURL(),
			"end_session_endpoint":   p.server.URL + "/end_session",
		})
	case "/jwks.json":
		good, _ := idpKeys()
		writeJSON(w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key:       &good.PublicKey,
			KeyID:     "k1",
			Algorithm: string(jose.RS256),
			Use:       "sig",
		}}})
	case "/token":
		p.serveToken(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (p *fakeIdP) serveToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	good, rogue := idpKeys()

	p.mu.Lock()
	p.tokenCalls++
	p.tokenForm = r.PostForm
	p.tokenAuth = r.Header.Get(echo.HeaderAuthorization)
	status, nonce, useRogue := p.tokenStatus, p.nonce, p.rogue
	p.mu.Unlock()

	if status != 0 && status != http.StatusOK {
		http.Error(w, "token endpoint error", status)
		return
	}
	signKey := good
	if useRogue {
		signKey = rogue
	}
	token, err := signIDToken(signKey, p.server.URL, nonce)
	if err != nil {
		http.Error(w, "signing failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"access_token": "access-token",
		"token_type":   "Bearer",
		"expires_in":   3600,
		"id_token":     token,
		"scope":        "openid profile agenthub.read",
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	_ = json.NewEncoder(w).Encode(v)
}

// signIDToken issues an id_token for testClientID with the given nonce.
func signIDToken(key *rsa.PrivateKey, issuer, nonce string) (string, error) {
	now := time.Now()
	claims := map[string]any{
		"iss":   issuer,
		"aud":   testClientID,
		"sub":   testSubject,
		"nonce": nonce,
		"iat":   now.Unix(),
		"exp":   now.Add(5 * time.Minute).Unix(),
	}
	opts := (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1")
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, opts)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	tok, err := signer.Sign(payload)
	if err != nil {
		return "", err
	}
	return tok.CompactSerialize()
}

// baseConfigYAML is the smallest config that passes Validate with a BFF
// pointed at the fake IdP.
func baseConfigYAML(idp *fakeIdP) string {
	return fmt.Sprintf(`
env: dev
server:
  http_addr: ":8080"
  public_url: "http://localhost:8080"
database:
  dsn: "postgres://agenthub:agenthub@localhost:5432/agenthub"
storage:
  kind: filesystem
cache:
  kind: memory
auth:
  issuer: %q
  jwks_url: %q
  audience: agenthub
  client_id: %q
  client_secret: %q
  redirect_url: %q
  post_logout_redirect_url: %q
  session_secret: %q
  scopes: ["openid", "profile", "agenthub.read"]
`, idp.server.URL, idp.jwksURL(), testClientID, testClientSecret, testCallbackURL, testPostLogout, testSessionSecret)
}

func loadConfig(t *testing.T, yaml string) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o600))
	cfg, err := config.Load(t.Context(), path, nil, nil)
	require.NoError(t, err)
	return cfg
}

// newBFFServer builds the echo server from a validated config.
func newBFFServer(t *testing.T, cfg *config.Config) *echo.Echo {
	t.Helper()
	require.NoError(t, cfg.Validate())
	return New(cfg, nil, nil, nil)
}

func doRequest(e *echo.Echo, method, target string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for _, ck := range cookies {
		req.AddCookie(ck)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// startLogin runs GET /auth/login and returns the authorization redirect plus
// the login cookie the browser would keep.
func startLogin(t *testing.T, e *echo.Echo, returnTo string) (*url.URL, *http.Cookie) {
	t.Helper()
	target := "/auth/login"
	if returnTo != "" {
		target += "?return_to=" + url.QueryEscape(returnTo)
	}
	rec := doRequest(e, http.MethodGet, target)
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	loc, err := url.Parse(rec.Header().Get(echo.HeaderLocation))
	require.NoError(t, err)
	ck := findCookie(rec.Result().Cookies(), loginCookieName)
	require.NotNil(t, ck, "login cookie missing")
	require.True(t, ck.HttpOnly)
	require.Equal(t, http.SameSiteLaxMode, ck.SameSite)
	return loc, ck
}

func findCookie(cookies []*http.Cookie, name string) *http.Cookie {
	for _, ck := range cookies {
		if ck.Name == name {
			return ck
		}
	}
	return nil
}

func decodeLogin(t *testing.T, ck *http.Cookie) loginClaims {
	t.Helper()
	var lc loginClaims
	require.NoError(t, verifyCookie([]byte(testSessionSecret), ck.Value, &lc))
	return lc
}

func TestBFFLoginRedirectsWithPKCE(t *testing.T) {
	idp := newFakeIdP(t)
	e := newBFFServer(t, loadConfig(t, baseConfigYAML(idp)))

	loc, ck := startLogin(t, e, "/dashboard")

	require.Equal(t, "/authorize", loc.Path)
	q := loc.Query()
	require.Equal(t, "code", q.Get("response_type"))
	require.Equal(t, testClientID, q.Get("client_id"))
	require.Equal(t, testCallbackURL, q.Get("redirect_uri"))
	require.Equal(t, "openid profile agenthub.read", q.Get("scope"))
	require.Equal(t, "S256", q.Get("code_challenge_method"))

	lc := decodeLogin(t, ck)
	require.Equal(t, q.Get("state"), lc.State)
	require.Equal(t, q.Get("nonce"), lc.Nonce)
	require.Equal(t, pkceChallenge(lc.Verifier), q.Get("code_challenge"))
	require.Equal(t, "/dashboard", lc.ReturnTo)
	require.NotEmpty(t, lc.Verifier)
	require.Greater(t, lc.Expires, time.Now().Unix())
	require.Equal(t, 600, ck.MaxAge)
	require.False(t, ck.Secure, "plain HTTP dev login must not set Secure")
}

func TestBFFLoginRejectsUnsafeReturnTo(t *testing.T) {
	idp := newFakeIdP(t)
	e := newBFFServer(t, loadConfig(t, baseConfigYAML(idp)))

	cases := map[string]string{
		"":                                    "/", // parameter absent
		"/dashboard":                          "/dashboard",
		"/store?q=agent":                      "/store?q=agent",
		"//evil.example":                      "/",
		"https://evil.example":                "/",
		"/\\evil.example":                     "/",
		"/\t/evil.example":                    "/",
		strings.Repeat("a", maxReturnToLen+1): "/",
	}
	for returnTo, want := range cases {
		t.Run(fmt.Sprintf("%q", returnTo), func(t *testing.T) {
			_, ck := startLogin(t, e, returnTo)
			require.Equal(t, want, decodeLogin(t, ck).ReturnTo)
		})
	}
}

func TestBFFLoginIsSecureBehindForwardedTLS(t *testing.T) {
	idp := newFakeIdP(t)
	e := newBFFServer(t, loadConfig(t, baseConfigYAML(idp)))

	req := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusFound, rec.Code)
	ck := findCookie(rec.Result().Cookies(), loginCookieName)
	require.NotNil(t, ck)
	require.True(t, ck.Secure)
}

func TestBFFLoginWhenProviderIsDown(t *testing.T) {
	idp := newFakeIdP(t)
	idp.setDiscoveryFail(true)
	e := newBFFServer(t, loadConfig(t, baseConfigYAML(idp)))

	rec := doRequest(e, http.MethodGet, "/auth/login")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Nil(t, findCookie(rec.Result().Cookies(), loginCookieName))
}

func TestBFFCallbackEstablishesSession(t *testing.T) {
	idp := newFakeIdP(t)
	e := newBFFServer(t, loadConfig(t, baseConfigYAML(idp)))

	loc, ck := startLogin(t, e, "/dashboard")
	require.Equal(t, "/authorize", loc.Path)
	lc := decodeLogin(t, ck)
	idp.setNonce(lc.Nonce)

	target := "/auth/callback?code=the-code&state=" + url.QueryEscape(lc.State)
	rec := doRequest(e, http.MethodGet, target, ck)

	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	require.Equal(t, "/dashboard", rec.Header().Get(echo.HeaderLocation))

	// The token endpoint saw a correct, PKCE-bound confidential exchange.
	form, auth, calls := idp.tokenRecord()
	require.Equal(t, 1, calls)
	require.Equal(t, "authorization_code", form.Get("grant_type"))
	require.Equal(t, "the-code", form.Get("code"))
	require.Equal(t, testCallbackURL, form.Get("redirect_uri"))
	require.Equal(t, lc.Verifier, form.Get("code_verifier"))
	require.Equal(t, testClientID, form.Get("client_id"))
	require.Equal(t, testClientSecret, form.Get("client_secret"))
	require.Empty(t, auth, "client credentials belong in the POST body, not Basic auth")

	// The session cookie carries the identity, the login cookie is spent.
	sess := findCookie(rec.Result().Cookies(), sessionCookieName)
	require.NotNil(t, sess, "session cookie missing")
	require.True(t, sess.HttpOnly)
	require.Equal(t, http.SameSiteLaxMode, sess.SameSite)
	var claims sessionClaims
	require.NoError(t, verifyCookie([]byte(testSessionSecret), sess.Value, &claims))
	require.Equal(t, testSubject, claims.Subject)
	require.Equal(t, []string{"openid", "profile", "agenthub.read"}, claims.Scopes)
	require.Greater(t, claims.Expires, time.Now().Unix())

	spent := findCookie(rec.Result().Cookies(), loginCookieName)
	require.NotNil(t, spent)
	require.Empty(t, spent.Value)
	require.True(t, spent.Expires.Before(time.Now()))
}

func TestBFFCallbackRejectsBadTransactions(t *testing.T) {
	cases := []struct {
		name       string
		state      string // when omitState is false and empty, the real state is sent
		omitState  bool   // do not send the state parameter at all
		skipCookie bool   // do not send the login cookie at all
	}{
		{name: "state mismatch", state: "not-the-state"},
		{name: "state missing", omitState: true},
		{name: "login cookie missing", skipCookie: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idp := newFakeIdP(t)
			e := newBFFServer(t, loadConfig(t, baseConfigYAML(idp)))

			_, ck := startLogin(t, e, "")
			lc := decodeLogin(t, ck)

			target := "/auth/callback?code=the-code"
			if !tc.omitState {
				state := tc.state
				if state == "" {
					state = lc.State
				}
				target += "&state=" + url.QueryEscape(state)
			}
			var cookies []*http.Cookie
			if !tc.skipCookie {
				cookies = append(cookies, ck)
			}
			rec := doRequest(e, http.MethodGet, target, cookies...)

			require.Equal(t, http.StatusBadRequest, rec.Code)
			_, _, calls := idp.tokenRecord()
			require.Zero(t, calls, "a rejected callback must not reach the token endpoint")
			require.Nil(t, findCookie(rec.Result().Cookies(), sessionCookieName))
		})
	}
}

func TestBFFCallbackReportsProviderError(t *testing.T) {
	idp := newFakeIdP(t)
	e := newBFFServer(t, loadConfig(t, baseConfigYAML(idp)))

	_, ck := startLogin(t, e, "")
	rec := doRequest(e, http.MethodGet, "/auth/callback?error=access_denied&error_description=nope", ck)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	_, _, calls := idp.tokenRecord()
	require.Zero(t, calls)
}

func TestBFFCallbackTokenEndpointFailures(t *testing.T) {
	// A 4xx means the flow itself is broken (restart the login); anything
	// else is an upstream outage.
	for status, want := range map[int]int{
		http.StatusBadRequest:          http.StatusBadRequest,
		http.StatusUnauthorized:        http.StatusBadRequest,
		http.StatusInternalServerError: http.StatusBadGateway,
	} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			idp := newFakeIdP(t)
			e := newBFFServer(t, loadConfig(t, baseConfigYAML(idp)))

			_, ck := startLogin(t, e, "")
			lc := decodeLogin(t, ck)
			idp.setNonce(lc.Nonce)
			idp.setTokenStatus(status)

			target := "/auth/callback?code=the-code&state=" + url.QueryEscape(lc.State)
			rec := doRequest(e, http.MethodGet, target, ck)

			require.Equal(t, want, rec.Code)
			require.Nil(t, findCookie(rec.Result().Cookies(), sessionCookieName))
		})
	}
}

func TestBFFCallbackRejectsBadIDToken(t *testing.T) {
	cases := map[string]func(*fakeIdP, loginClaims){
		"nonce mismatch": func(idp *fakeIdP, lc loginClaims) {
			idp.setNonce("some-other-nonce")
		},
		"unknown signing key": func(idp *fakeIdP, _ loginClaims) {
			idp.setRogue(true)
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			idp := newFakeIdP(t)
			e := newBFFServer(t, loadConfig(t, baseConfigYAML(idp)))

			_, ck := startLogin(t, e, "")
			lc := decodeLogin(t, ck)
			setup(idp, lc)

			target := "/auth/callback?code=the-code&state=" + url.QueryEscape(lc.State)
			rec := doRequest(e, http.MethodGet, target, ck)

			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Nil(t, findCookie(rec.Result().Cookies(), sessionCookieName))
		})
	}
}

func TestBFFLogoutClearsSessionAndEndsSession(t *testing.T) {
	idp := newFakeIdP(t)
	e := newBFFServer(t, loadConfig(t, baseConfigYAML(idp)))

	sess := signedSessionCookie(t, sessionClaims{
		Subject: testSubject,
		Scopes:  []string{"agenthub.read"},
		Expires: time.Now().Add(time.Hour).Unix(),
	})

	rec := doRequest(e, http.MethodGet, "/auth/logout", sess)

	require.Equal(t, http.StatusFound, rec.Code)
	loc, err := url.Parse(rec.Header().Get(echo.HeaderLocation))
	require.NoError(t, err)
	require.Equal(t, "/end_session", loc.Path)
	require.Equal(t, testClientID, loc.Query().Get("client_id"))
	require.Equal(t, testPostLogout, loc.Query().Get("post_logout_redirect_uri"))

	cleared := findCookie(rec.Result().Cookies(), sessionCookieName)
	require.NotNil(t, cleared)
	require.Empty(t, cleared.Value)
	require.True(t, cleared.Expires.Before(time.Now()))
}

func TestBFFLogoutWithoutProviderStillSignsOut(t *testing.T) {
	idp := newFakeIdP(t)
	idp.setDiscoveryFail(true)
	e := newBFFServer(t, loadConfig(t, baseConfigYAML(idp)))

	sess := signedSessionCookie(t, sessionClaims{Subject: testSubject, Expires: time.Now().Add(time.Hour).Unix()})
	rec := doRequest(e, http.MethodGet, "/auth/logout", sess)

	require.Equal(t, http.StatusFound, rec.Code)
	require.Equal(t, testPostLogout, rec.Header().Get(echo.HeaderLocation))
	require.Empty(t, findCookie(rec.Result().Cookies(), sessionCookieName).Value)
}

func TestBFFRoutesAbsentWithoutClientID(t *testing.T) {
	yaml := strings.Replace(baseConfigYAML(newFakeIdP(t)), fmt.Sprintf("  client_id: %q\n", testClientID), "", 1)
	e := newBFFServer(t, loadConfig(t, yaml))

	for _, path := range []string{"/auth/login", "/auth/callback", "/auth/logout"} {
		require.Equal(t, http.StatusNotFound, doRequest(e, http.MethodGet, path).Code, path)
	}
}

// sessionEcho wires only the session middleware, so a request can be scored
// on the identity it produced.
func sessionEcho(t *testing.T, cfg *config.Config, presetSubject string) *echo.Echo {
	t.Helper()
	bff := newBFF(cfg)
	require.NotNil(t, bff)
	e := echo.New()
	if presetSubject != "" {
		e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c echo.Context) error {
				setIdentity(c, authz.Identity{Subject: presetSubject})
				return next(c)
			}
		})
	}
	e.Use(bff.sessionMiddleware())
	e.GET("/whoami", func(c echo.Context) error {
		sub, _ := c.Get(SubjectKey).(string)
		scopes, _ := c.Get(ScopesKey).([]string)
		return c.String(http.StatusOK, sub+"|"+strings.Join(scopes, ","))
	})
	return e
}

func signedSessionCookie(t *testing.T, sess sessionClaims) *http.Cookie {
	t.Helper()
	value, err := signCookie([]byte(testSessionSecret), sess)
	require.NoError(t, err)
	return &http.Cookie{Name: sessionCookieName, Value: value}
}

func TestBFFSessionCookieIsTheIdentity(t *testing.T) {
	cfg := loadConfig(t, baseConfigYAML(newFakeIdP(t)))
	valid := sessionClaims{
		Subject: testSubject,
		Scopes:  []string{"agenthub.read", "agenthub.publish"},
		Expires: time.Now().Add(time.Hour).Unix(),
	}

	t.Run("valid cookie", func(t *testing.T) {
		e := sessionEcho(t, cfg, "")
		rec := doRequest(e, http.MethodGet, "/whoami", signedSessionCookie(t, valid))
		require.Equal(t, testSubject+"|agenthub.read,agenthub.publish", rec.Body.String())
	})

	t.Run("no cookie", func(t *testing.T) {
		e := sessionEcho(t, cfg, "")
		rec := doRequest(e, http.MethodGet, "/whoami")
		require.Equal(t, "|", rec.Body.String())
	})

	t.Run("tampered cookie", func(t *testing.T) {
		ck := signedSessionCookie(t, valid)
		ck.Value = tamper(ck.Value)
		e := sessionEcho(t, cfg, "")
		rec := doRequest(e, http.MethodGet, "/whoami", ck)
		require.Equal(t, "|", rec.Body.String())
	})

	t.Run("foreign key", func(t *testing.T) {
		value, err := signCookie([]byte("another-secret-another-secret-32"), valid)
		require.NoError(t, err)
		e := sessionEcho(t, cfg, "")
		rec := doRequest(e, http.MethodGet, "/whoami", &http.Cookie{Name: sessionCookieName, Value: value})
		require.Equal(t, "|", rec.Body.String())
	})

	t.Run("expired cookie", func(t *testing.T) {
		expired := valid
		expired.Expires = time.Now().Add(-time.Minute).Unix()
		e := sessionEcho(t, cfg, "")
		rec := doRequest(e, http.MethodGet, "/whoami", signedSessionCookie(t, expired))
		require.Equal(t, "|", rec.Body.String())
	})

	t.Run("empty subject", func(t *testing.T) {
		empty := valid
		empty.Subject = ""
		e := sessionEcho(t, cfg, "")
		rec := doRequest(e, http.MethodGet, "/whoami", signedSessionCookie(t, empty))
		require.Equal(t, "|", rec.Body.String())
	})

	t.Run("bearer identity wins", func(t *testing.T) {
		e := sessionEcho(t, cfg, "bearer-subject")
		rec := doRequest(e, http.MethodGet, "/whoami", signedSessionCookie(t, valid))
		require.Equal(t, "bearer-subject|", rec.Body.String())
	})
}

func TestSafeReturnTo(t *testing.T) {
	require.Equal(t, "/", safeReturnTo(""))
	require.Equal(t, "/dashboard", safeReturnTo("  /dashboard  "))
	require.Equal(t, "/", safeReturnTo("//evil.example"))
	require.Equal(t, "/", safeReturnTo("https://evil.example"))
	require.Equal(t, "/", safeReturnTo(`/\evil.example`))
	require.Equal(t, "/", safeReturnTo("/ok\nLocation: https://evil.example"))
}

func TestCookieCodecRoundTripAndTamper(t *testing.T) {
	secret := []byte(testSessionSecret)
	orig := sessionClaims{Subject: "alice", Scopes: []string{"a", "b"}, Expires: time.Now().Unix()}
	raw, err := signCookie(secret, orig)
	require.NoError(t, err)

	var got sessionClaims
	require.NoError(t, verifyCookie(secret, raw, &got))
	require.Equal(t, orig, got)

	// A tampered payload, a tampered tag and a truncated value all fail.
	body, tag, ok := strings.Cut(raw, ".")
	require.True(t, ok)
	require.Error(t, verifyCookie(secret, tamper(body)+"."+tag, &got))
	require.Error(t, verifyCookie(secret, body+"."+tamper(tag), &got))
	require.Error(t, verifyCookie(secret, body, &got))
	require.Error(t, verifyCookie(secret, "", &got))
	// A different key must not verify either.
	require.Error(t, verifyCookie([]byte("other-secret-other-secret-other-1"), raw, &got))
}

// tamper flips one character in the middle of a base64 segment. The last
// character of a segment can carry only base64 padding bits, which decode to
// the very same bytes, so tampering there may leave the signature intact.
func tamper(s string) string {
	pos := len(s) / 2
	if strings.IndexByte(s, '.') == pos {
		pos--
	}
	replacement := "A"
	if s[pos] == 'A' {
		replacement = "B"
	}
	return s[:pos] + replacement + s[pos+1:]
}

func TestNewBFFRedirectURIDerivation(t *testing.T) {
	// No callback URI at all: nowhere to send the browser back to, so the
	// BFF stays out of the way.
	cfg := &config.Config{}
	cfg.Auth.ClientID = testClientID
	cfg.Auth.Issuer = "https://sso.example"
	require.Nil(t, newBFF(cfg))

	// An explicit redirect_url wins over the derived one.
	cfg.Auth.RedirectURL = "https://hub.example/custom-callback"
	require.Equal(t, "https://hub.example/custom-callback", newBFF(cfg).redirectURL)

	// Otherwise the conventional path is derived from public_url.
	cfg.Auth.RedirectURL = ""
	cfg.Server.PublicURL = "https://hub.example/"
	require.Equal(t, "https://hub.example/auth/callback", newBFF(cfg).redirectURL)

	// No client configured at all.
	cfg.Auth.ClientID = ""
	require.Nil(t, newBFF(cfg))
}
