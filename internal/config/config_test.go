package config

import (
	"os"
	"path/filepath"
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
