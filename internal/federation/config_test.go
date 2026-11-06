package federation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sampleUpstream(t *testing.T) Upstream {
	t.Helper()
	key := newTestKey(t)
	pub := pubOf(key)
	return Upstream{
		Name:        "pilab",
		HubID:       "hub.example",
		BaseURL:     "https://hub.example",
		Mode:        ModeReference,
		Trust:       TrustPinned,
		Fingerprint: Fingerprint(pub),
		PublicKey:   EncodePublicKey(pub),
		AddedAt:     time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC).Format(time.RFC3339),
	}
}

func TestReadConfigMissingFile(t *testing.T) {
	t.Parallel()

	cfg, err := ReadConfig(filepath.Join(t.TempDir(), FederationFileName))
	if err != nil {
		t.Fatalf("read missing config: %v", err)
	}
	if cfg.SchemaVersion != 1 || len(cfg.Upstreams) != 0 {
		t.Fatalf("config = %+v, want an empty schema-1 document", cfg)
	}
}

func TestWriteReadConfigRoundTrip(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), FederationFileName)
	want := sampleUpstream(t)
	cfg := Config{Upstreams: []Upstream{want}}
	if err := WriteConfig(path, cfg); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := ReadConfig(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.SchemaVersion != 1 || len(got.Upstreams) != 1 {
		t.Fatalf("config = %+v, want one upstream", got)
	}
	if got.Upstreams[0] != want {
		t.Fatalf("upstream = %+v, want %+v", got.Upstreams[0], want)
	}
	raw, err := os.ReadFile(path) //nolint:gosec // test reads the file it just wrote
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"fingerprint": "sha256:`) {
		t.Fatalf("config file is not indented JSON:\n%s", raw)
	}
}

func TestReadConfigRejectsFutureSchema(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), FederationFileName)
	if err := os.WriteFile(path, []byte(`{"schemaVersion":2,"upstreams":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadConfig(path); err == nil {
		t.Fatal("ReadConfig accepted a future schema version")
	}
}

func TestConfigUpsert(t *testing.T) {
	t.Parallel()

	up := sampleUpstream(t)
	cfg := Config{}

	replaced, err := cfg.Upsert(up)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if replaced {
		t.Fatal("first insert reported a replacement")
	}

	// The same URL replaces the record and keeps its original added_at.
	again := up
	again.Mode = ModeMirror
	again.AddedAt = time.Now().UTC().Format(time.RFC3339)
	replaced, err = cfg.Upsert(again)
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if !replaced {
		t.Fatal("second insert did not replace the existing record")
	}
	if len(cfg.Upstreams) != 1 {
		t.Fatalf("upstreams = %d, want 1", len(cfg.Upstreams))
	}
	if cfg.Upstreams[0].Mode != ModeMirror {
		t.Fatalf("mode = %q, want %q", cfg.Upstreams[0].Mode, ModeMirror)
	}
	if cfg.Upstreams[0].AddedAt != up.AddedAt {
		t.Fatalf("added_at = %q, want the original %q",
			cfg.Upstreams[0].AddedAt, up.AddedAt)
	}

	// A different URL under a taken name is rejected: names are aliases of
	// exactly one upstream.
	conflict := up
	conflict.BaseURL = "https://other.example"
	if _, err := cfg.Upsert(conflict); err == nil {
		t.Fatal("Upsert accepted a duplicate name for another hub")
	}
}

func TestConfigUpsertValidation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(*Upstream)
	}{
		{"missing name", func(u *Upstream) { u.Name = "" }},
		{"missing hub id", func(u *Upstream) { u.HubID = "" }},
		{"missing url", func(u *Upstream) { u.BaseURL = "" }},
		{"bad mode", func(u *Upstream) { u.Mode = "banana" }},
		{"bad trust", func(u *Upstream) { u.Trust = "blind" }},
		{"bad fingerprint", func(u *Upstream) { u.Fingerprint = "sha256:zz" }},
		{"bad public key", func(u *Upstream) { u.PublicKey = "not-a-key" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			up := sampleUpstream(t)
			tc.mutate(&up)
			if _, err := (&Config{}).Upsert(up); err == nil {
				t.Fatal("Upsert accepted an invalid record")
			}
		})
	}
}

func TestValidMode(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"reference", " Mirror ", "proxy"} {
		got, err := ValidMode(mode)
		if err != nil {
			t.Fatalf("ValidMode(%q): %v", mode, err)
		}
		if got != strings.ToLower(strings.TrimSpace(mode)) {
			t.Fatalf("ValidMode(%q) = %q", mode, got)
		}
	}
	if _, err := ValidMode("sync"); err == nil {
		t.Fatal("ValidMode accepted an unknown mode")
	}
}

func TestParsePublicKeyRoundTrip(t *testing.T) {
	t.Parallel()

	key := newTestKey(t)
	pub := pubOf(key)
	encodings := []string{
		EncodePublicKey(pub), // standard base64, padded
		strings.TrimRight(EncodePublicKey(pub), "="),
	}
	for _, s := range encodings {
		got, err := ParsePublicKey(s)
		if err != nil {
			t.Fatalf("ParsePublicKey(%q): %v", s, err)
		}
		if !got.Equal(pub) {
			t.Fatalf("ParsePublicKey(%q) did not round trip", s)
		}
	}
	if _, err := ParsePublicKey("nope"); err == nil {
		t.Fatal("ParsePublicKey accepted garbage")
	}
}
