package server

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestHealthzIntegration verifies the /healthz endpoint is accessible.
func TestHealthzIntegration(t *testing.T) {
	e := New(nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "/healthz should return OK")
}

// TestWellKnownIntegration verifies the /.well-known/agenthub.json endpoint.
func TestWellKnownIntegration(t *testing.T) {
	e := New(nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/.well-known/agenthub.json", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "/.well-known/agenthub.json should return OK")

	var doc map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc), "response should be valid JSON")
	require.Equal(t, "ed25519", doc["signing_algorithm"], "signing_algorithm should be ed25519")
	key, ok := doc["public_key"].(string)
	require.True(t, ok, "public_key must be a base64 string")
	pub, err := base64.StdEncoding.DecodeString(key)
	require.NoError(t, err, "public_key should be valid base64")
	require.Equal(t, ed25519.PublicKeySize, len(pub), "public key should be 32 bytes")
}

// TestFullCycleIntegration verifies the core endpoints that work with nil config.
func TestFullCycleIntegration(t *testing.T) {
	t.Helper()

	// Step 1: Health check
	e := New(nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "step 1: /healthz should return OK")

	// Step 2: Well-known config
	req2 := httptest.NewRequest(http.MethodGet, "/.well-known/agenthub.json", nil)
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)
	require.Equal(t, http.StatusOK, rec2.Code, "step 2: /.well-known/agenthub.json should return OK")

	t.Log("✅ Full cycle integration test passed (health + well-known)")
}
