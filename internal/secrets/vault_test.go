package secrets

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSplitKVPath(t *testing.T) {
	cases := []struct{ in, mount, sub string }{
		{"secret/data/AgentHub", "secret", "AgentHub"},
		{"secret/AgentHub", "secret", "AgentHub"},
		{"", "", ""},
	}
	for _, c := range cases {
		m, s := SplitKVPath(c.in)
		require.Equal(t, c.mount, m)
		require.Equal(t, c.sub, s)
	}
}

func TestLoadKVv2(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/secret/data/AgentHub" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"data": map[string]any{"DB_PASSWORD": "p@ss"},
			},
		})
	}))
	defer ts.Close()

	cfg := VaultConfig{Address: ts.URL, Path: "secret/data/AgentHub", Token: "t", Timeout: 5e9}
	vals, err := cfg.Load(t.Context())
	require.NoError(t, err)
	require.Equal(t, "p@ss", vals["DB_PASSWORD"])
}
