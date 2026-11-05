package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadPrecedence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte("server:\n  http_addr: \":9090\"\ndatabase:\n  dsn: \"postgres://u:p@h/db\"\n"), 0o644))

	t.Setenv("AGENTHUB_SERVER_HTTP_ADDR", ":7070")

	cfg, err := Load(t.Context(), path, nil, nil)
	require.NoError(t, err)
	require.Equal(t, ":7070", cfg.Server.HTTPAddr) // env > file

	// default survives for unset key
	require.Equal(t, ":9100", cfg.Server.MetricsAddr)
}

func TestLoadEnvOverridesKeysWithoutDefaults(t *testing.T) {
	t.Setenv("AGENTHUB_DATABASE_DSN", "postgres://env:p@h/db")
	t.Setenv("AGENTHUB_AUTH_ISSUER", "https://sso.example")
	t.Setenv("AGENTHUB_AUTH_CLIENT_ID", "agenthub-web")
	t.Setenv("AGENTHUB_AUTH_SESSION_SECRET", strings.Repeat("s", 32))

	cfg, err := Load(t.Context(), "", nil, nil)
	require.NoError(t, err)
	require.Equal(t, "postgres://env:p@h/db", cfg.Database.DSN)
	require.Equal(t, "https://sso.example", cfg.Auth.Issuer)
	require.Equal(t, "agenthub-web", cfg.Auth.ClientID)
	require.Equal(t, strings.Repeat("s", 32), cfg.Auth.SessionSecret)
}

func TestLoadEnvWinsOverFileForKeysWithoutDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte("database:\n  dsn: \"postgres://file:p@h/db\"\n"), 0o644))
	t.Setenv("AGENTHUB_DATABASE_DSN", "postgres://env:p@h/db")

	cfg, err := Load(t.Context(), path, nil, nil)
	require.NoError(t, err)
	require.Equal(t, "postgres://env:p@h/db", cfg.Database.DSN)
}

func TestValidateProdRejectsFilesystemStorage(t *testing.T) {
	cfg := &Config{}
	cfg.Env = "prod"
	cfg.Server.HTTPAddr = ":8080"
	cfg.Server.PublicURL = "https://x"
	cfg.Database.DSN = "postgres://u:p@h/db"
	cfg.Storage.Kind = "filesystem"
	cfg.Cache.Kind = "valkey"
	cfg.Signing.RequirePublisherSignature = true
	err := cfg.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "filesystem")
}

func TestValidateExpandsEnvInDSN(t *testing.T) {
	t.Setenv("DB_PASSWORD", "secret")
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte("database:\n  dsn: \"postgres://u:${DB_PASSWORD}@h/db\"\n"), 0o644))

	cfg, err := Load(t.Context(), path, nil, nil)
	require.NoError(t, err)
	require.Equal(t, "postgres://u:secret@h/db", cfg.Database.DSN)
}

// bffConfig is the smallest config that validates with the OIDC BFF
// (auth.client_id) enabled.
func bffConfig() *Config {
	cfg := &Config{}
	cfg.Env = "dev"
	cfg.Server.HTTPAddr = ":8080"
	cfg.Server.PublicURL = "https://hub.example"
	cfg.Database.DSN = "postgres://u:p@h/db"
	cfg.Storage.Kind = "filesystem"
	cfg.Cache.Kind = "memory"
	cfg.Auth.ClientID = "agenthub-web"
	cfg.Auth.Issuer = "https://sso.example"
	cfg.Auth.Audience = "agenthub"
	cfg.Auth.RedirectURL = "https://hub.example/auth/callback"
	return cfg
}

func TestValidateBFFRequiresSessionSecret(t *testing.T) {
	cfg := bffConfig()
	err := cfg.Validate()
	require.ErrorContains(t, err, "auth.session_secret is required when auth.client_id is set")

	cfg.Auth.SessionSecret = "too-short"
	err = cfg.Validate()
	require.ErrorContains(t, err, "auth.session_secret must be at least 32 characters")

	cfg.Auth.SessionSecret = strings.Repeat("s", 32)
	require.NoError(t, cfg.Validate())
}

func TestValidateBFFRequiresIssuerAndRedirectURI(t *testing.T) {
	cfg := bffConfig()
	cfg.Auth.SessionSecret = strings.Repeat("s", 32)
	cfg.Auth.Issuer = ""
	err := cfg.Validate()
	require.ErrorContains(t, err, "auth.issuer is required when auth.client_id is set")

	// With an issuer the callback path is derived from server.public_url,
	// so no explicit redirect_url is needed...
	cfg.Auth.Issuer = "https://sso.example"
	cfg.Auth.RedirectURL = ""
	require.NoError(t, cfg.Validate())

	// ...but a deployment without public_url has to provide one.
	cfg.Server.PublicURL = ""
	cfg.Auth.RedirectURL = ""
	err = cfg.Validate()
	require.ErrorContains(t, err, "auth.redirect_url (or server.public_url) is required")

	cfg.Auth.RedirectURL = "https://hub.example/auth/callback"
	require.NoError(t, cfg.Validate())

	// A short session secret is rejected even outside BFF deployments.
	cfg.Auth.ClientID = ""
	cfg.Auth.SessionSecret = "short"
	require.ErrorContains(t, cfg.Validate(), "at least 32 characters")
}
