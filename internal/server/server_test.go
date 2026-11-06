package server

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"go.pilab.hu/agenthub/internal/web"
)

func TestHealthz(t *testing.T) {
	e := New(nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestReadyzNoDB(t *testing.T) {
	e := New(nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	// no db configured -> ready (db not configured is not fatal for M0)
	require.True(t, rec.Code == http.StatusOK || rec.Code == http.StatusServiceUnavailable)
}

func TestWellKnown(t *testing.T) {
	e := New(nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/.well-known/agenthub.json", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	// M4.1: the document publishes the Ed25519 key that signs catalog
	// snapshots and sync pages.
	var doc map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	require.Equal(t, "ed25519", doc["signing_algorithm"])
	key, ok := doc["public_key"].(string)
	require.True(t, ok, "public_key must be a base64 string, got %T", doc["public_key"])
	pub, err := base64.StdEncoding.DecodeString(key)
	require.NoError(t, err)
	require.Len(t, pub, ed25519.PublicKeySize)
}

func TestStorePages(t *testing.T) {
	w, err := web.New("dev")
	require.NoError(t, err)
	e := New(nil, nil, nil, w)
	for _, p := range []string{"/ns/pilab", "/a/pilab/agent/foo", "/a/pilab/agent/foo/1.0.0"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, p)
	}
}
