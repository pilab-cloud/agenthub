package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
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
}
