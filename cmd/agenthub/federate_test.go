package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"connectrpc.com/connect"

	federationv1 "go.pilab.hu/agenthub/gen/go/agenthub/federation/v1"
	federationv1connect "go.pilab.hu/agenthub/gen/go/agenthub/federation/v1/federationv1connect"

	"go.pilab.hu/agenthub/internal/federation"
)

// stubFederation serves the identity RPC only: everything `federate add`
// needs from a remote hub.
type stubFederation struct {
	federationv1connect.UnimplementedFederationServiceHandler
	hubID string
	pub   ed25519.PublicKey
}

func (s stubFederation) GetHubInfo(_ context.Context, _ *connect.Request[federationv1.GetHubInfoRequest]) (*connect.Response[federationv1.GetHubInfoResponse], error) {
	return connect.NewResponse(&federationv1.GetHubInfoResponse{
		Hub: &federationv1.HubInfo{
			Id:               s.hubID,
			BaseUrl:          "https://" + s.hubID,
			PublicKey:        s.pub,
			Protocol:         1,
			SigningAlgorithm: "ed25519",
		},
	}), nil
}

// newHubPub returns a fresh Ed25519 public key for a stub hub.
func newHubPub(t *testing.T) ed25519.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

// newHubInfoStub runs a stub federation server and returns its URL.
func newHubInfoStub(t *testing.T, hubID string, pub ed25519.PublicKey) string {
	t.Helper()
	path, handler := federationv1connect.NewFederationServiceHandler(
		stubFederation{hubID: hubID, pub: pub})
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// runFederate executes the CLI in a fresh temp dir and returns its output.
func runFederate(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return runFederateIn(t, t.TempDir(), args...)
}

// runFederateIn executes the CLI with dir as the working directory.
func runFederateIn(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	t.Chdir(dir)
	root := newRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestFederateAddCmd(t *testing.T) {
	// Not parallel: changes the process working directory.
	pub := newHubPub(t)
	hub := newHubInfoStub(t, "hub.example", pub)

	out, err := runFederate(t, "federate", "add", hub)
	if err != nil {
		t.Fatalf("federate add: %v (output: %s)", err, out)
	}
	for _, want := range []string{
		"added upstream",
		"hub.example",
		"tofu",
		federation.Fingerprint(pub),
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}

	cfg, err := federation.ReadConfig(federation.FederationFileName)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if len(cfg.Upstreams) != 1 {
		t.Fatalf("upstreams = %d, want 1", len(cfg.Upstreams))
	}
	got := cfg.Upstreams[0]
	if got.HubID != "hub.example" || got.BaseURL != hub || got.Mode != federation.ModeReference {
		t.Fatalf("upstream = %+v", got)
	}
	if got.Trust != federation.TrustTOFU || got.Fingerprint != federation.Fingerprint(pub) {
		t.Fatalf("trust = %q fingerprint = %q", got.Trust, got.Fingerprint)
	}
	if got.PublicKey == "" || got.AddedAt == "" {
		t.Fatalf("upstream missing key or timestamp: %+v", got)
	}
	// The default alias is the hub host of the dialed URL.
	if got.Name == "" || strings.Contains(got.Name, "://") {
		t.Fatalf("name = %q, want a bare host", got.Name)
	}
}

func TestFederateAddCmdWithFingerprint(t *testing.T) {
	// Not parallel: changes the process working directory.
	pub := newHubPub(t)
	hub := newHubInfoStub(t, "hub.example", pub)

	out, err := runFederate(t, "federate", "add", hub,
		"--fingerprint", federation.Fingerprint(pub), "--name", "pinned-hub")
	if err != nil {
		t.Fatalf("federate add: %v (output: %s)", err, out)
	}
	if !strings.Contains(out, "pinned") || strings.Contains(out, "tofu") {
		t.Fatalf("output should report a pinned trust:\n%s", out)
	}
	cfg, err := federation.ReadConfig(federation.FederationFileName)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Upstreams) != 1 || cfg.Upstreams[0].Trust != federation.TrustPinned ||
		cfg.Upstreams[0].Name != "pinned-hub" {
		t.Fatalf("upstreams = %+v", cfg.Upstreams)
	}
}

func TestFederateAddCmdRejectsWrongFingerprint(t *testing.T) {
	// Not parallel: changes the process working directory.
	pub := newHubPub(t)
	hub := newHubInfoStub(t, "hub.example", pub)

	out, err := runFederate(t, "federate", "add", hub,
		"--fingerprint", "sha256:"+strings.Repeat("0", 64))
	if err == nil {
		t.Fatalf("federate add accepted a foreign key (output: %s)", out)
	}
	if !strings.Contains(err.Error(), federation.ErrKeyMismatch.Error()) {
		t.Fatalf("error = %v, want %v", err, federation.ErrKeyMismatch)
	}
	if code := exitCode(err); code != exitVerification {
		t.Fatalf("exit code = %d, want %d", code, exitVerification)
	}
	if _, statErr := os.Stat(federation.FederationFileName); !os.IsNotExist(statErr) {
		t.Fatalf("config written despite the failed verification: %v", statErr)
	}
}

func TestFederateAddCmdIdempotent(t *testing.T) {
	// Not parallel: changes the process working directory.
	pub := newHubPub(t)
	hub := newHubInfoStub(t, "hub.example", pub)

	dir := t.TempDir()
	if out, err := runFederateIn(t, dir, "federate", "add", hub, "--name", "up"); err != nil {
		t.Fatalf("first add: %v (output: %s)", err, out)
	}
	// The second add must update the record in place instead of appending a
	// duplicate.
	out, err := runFederateIn(t, dir, "federate", "add", hub, "--name", "up")
	if err != nil {
		t.Fatalf("second add: %v (output: %s)", err, out)
	}
	if !strings.Contains(out, "updated upstream") {
		t.Fatalf("second add output:\n%s", out)
	}
	cfg, err := federation.ReadConfig(federation.FederationFileName)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Upstreams) != 1 {
		t.Fatalf("upstreams = %d, want 1 after the re-add", len(cfg.Upstreams))
	}
}

func TestFederateListCmd(t *testing.T) {
	// Not parallel: changes the process working directory.
	dir := t.TempDir()
	out, err := runFederateIn(t, dir, "federate", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "no upstreams recorded") {
		t.Fatalf("empty list output:\n%s", out)
	}

	pub := newHubPub(t)
	hub := newHubInfoStub(t, "hub.example", pub)
	if out, err := runFederateIn(t, dir, "federate", "add", hub, "--name", "up"); err != nil {
		t.Fatalf("add: %v (output: %s)", err, out)
	}
	out, err = runFederateIn(t, dir, "federate", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, want := range []string{"up", "hub.example", "mode=reference", "trust=tofu"} {
		if !strings.Contains(out, want) {
			t.Fatalf("list output missing %q:\n%s", want, out)
		}
	}
}

func TestFederateAddCmdRejectsBadMode(t *testing.T) {
	// Not parallel: changes the process working directory.
	pub := newHubPub(t)
	hub := newHubInfoStub(t, "hub.example", pub)

	if _, err := runFederate(t, "federate", "add", hub, "--mode", "banana"); err == nil {
		t.Fatal("federate add accepted an unknown mode")
	}
}
