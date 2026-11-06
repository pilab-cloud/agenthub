// Package integration runs the real agenthub binary against a real
// in-process hub (server.New behind an httptest server) over HTTP.
package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"connectrpc.com/connect"

	catalogv1connect "go.pilab.hu/agenthub/gen/go/agenthub/catalog/v1/catalogv1connect"
	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"
	registryv1 "go.pilab.hu/agenthub/gen/go/agenthub/registry/v1"
	registryv1connect "go.pilab.hu/agenthub/gen/go/agenthub/registry/v1/registryv1connect"
	"go.pilab.hu/agenthub/internal/config"
	"go.pilab.hu/agenthub/internal/server"
	"go.pilab.hu/agenthub/internal/web"
)

var cliBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "agenthub-it")
	if err != nil {
		panic(err)
	}
	cliBinary = filepath.Join(dir, "agenthub")
	_, self, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(self), "..", "..")
	build := exec.Command("go", "build", "-o", cliBinary, "./cmd/agenthub")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build agenthub: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// hub is a running in-process registry.
type hub struct {
	URL      string
	Registry registryv1connect.RegistryServiceClient
	Catalog  catalogv1connect.CatalogServiceClient
}

func startHub(t *testing.T) *hub {
	t.Helper()
	cfg := &config.Config{}
	cfg.Server.MaxRequestBytes = 4 << 20
	cfg.Server.RateLimitPerSec = 1000
	cfg.Server.MetricsAddr = ":0"

	// The public URL must be known before the handler is built, so bind the
	// listener first.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	url := "http://" + l.Addr().String()
	cfg.Server.PublicURL = url
	cfg.Server.HTTPAddr = l.Addr().String()
	w, err := web.New("integration")
	if err != nil {
		t.Fatal(err)
	}
	ts := &httptest.Server{Listener: l, Config: &http.Server{Handler: server.New(cfg, nil, nil, w)}} //nolint:gosec // test server
	ts.Start()
	t.Cleanup(ts.Close)

	return &hub{
		URL:      url,
		Registry: registryv1connect.NewRegistryServiceClient(http.DefaultClient, url),
		Catalog:  catalogv1connect.NewCatalogServiceClient(http.DefaultClient, url),
	}
}

// as marks a request as coming from subject (the dev header authenticator).
func as[T any](subject string, msg *T) *connect.Request[T] {
	r := connect.NewRequest(msg)
	r.Header().Set(server.SubjectHeader, subject)
	return r
}

// bundle is a publishable asset: a manifest plus its file blobs.
type bundle struct {
	Namespace, Name, Version, Kind string
	Files                          map[string]string // bundle path -> content
	Spec                           map[string]any
}

func shaHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func (b bundle) manifest() []byte {
	var files []map[string]any
	for p, c := range b.Files {
		files = append(files, map[string]any{"path": p, "sha256": "sha256:" + shaHex([]byte(c)), "size": len(c)})
	}
	m := map[string]any{
		"kind": b.Kind, "namespace": b.Namespace, "name": b.Name, "version": b.Version,
		"summary": "integration " + b.Name, "files": files, "spec": b.Spec,
	}
	raw, _ := json.Marshal(m)
	return raw
}

func (b bundle) assetKind() commonv1.AssetKind {
	switch b.Kind {
	case "skill":
		return commonv1.AssetKind_ASSET_KIND_SKILL
	case "prompt":
		return commonv1.AssetKind_ASSET_KIND_PROMPT
	}
	return commonv1.AssetKind_ASSET_KIND_AGENT
}

// publish uploads and commits b as subject, creating the namespace if needed.
func (h *hub) publish(t *testing.T, subject string, b bundle) {
	t.Helper()
	ctx := context.Background()
	if _, err := h.Registry.CreateNamespace(ctx, as(subject, &registryv1.CreateNamespaceRequest{Slug: b.Namespace})); err != nil &&
		connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("create namespace: %v", err)
	}
	var descs []*commonv1.FileDescriptor
	for p, c := range b.Files {
		descs = append(descs, &commonv1.FileDescriptor{Path: p, Size: uint64(len(c)), Sha256: shaHex([]byte(c))})
	}
	manifest := b.manifest()
	begin, err := h.Registry.BeginPublish(ctx, as(subject, &registryv1.BeginPublishRequest{
		Namespace: b.Namespace, Kind: b.assetKind(), Name: b.Name, Version: b.Version,
		BundleDigest: &commonv1.Digest{Algorithm: "sha256", Value: shaHex(manifest)}, Files: descs,
	}))
	if err != nil {
		t.Fatalf("begin publish: %v", err)
	}
	if len(begin.Msg.Uploads) != len(b.Files) {
		t.Fatalf("uploads = %d, want %d", len(begin.Msg.Uploads), len(b.Files))
	}
	for _, u := range begin.Msg.Uploads {
		if code := putBlob(t, u.Url, subject, []byte(b.Files[u.Path])); code != http.StatusNoContent {
			t.Fatalf("upload %s: status %d", u.Path, code)
		}
	}
	if _, err := h.Registry.CommitPublish(ctx, as(subject, &registryv1.CommitPublishRequest{
		PublishId: begin.Msg.PublishId, Manifest: manifest,
		ManifestDigest: &commonv1.Digest{Algorithm: "sha256", Value: shaHex(manifest)},
	})); err != nil {
		t.Fatalf("commit publish: %v", err)
	}
}

func putBlob(t *testing.T, url, subject string, data []byte) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if subject != "" {
		req.Header.Set(server.SubjectHeader, subject)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// workspace is an isolated user: its own HOME, project dir and cache.
type workspace struct {
	Home, Project, Cache string
}

func newWorkspace(t *testing.T) workspace {
	t.Helper()
	return workspace{Home: t.TempDir(), Project: t.TempDir(), Cache: t.TempDir()}
}

// run executes the CLI in the workspace and returns combined output and the
// process exit code.
func (w workspace) run(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(cliBinary, args...)
	cmd.Dir = w.Project
	cmd.Env = []string{
		"HOME=" + w.Home, "PATH=" + os.Getenv("PATH"),
		"AGENTHUB_CACHE_DIR=" + w.Cache, "NO_COLOR=1",
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run %v: %v", args, err)
	}
	return out.String(), code
}

func (w workspace) mustRun(t *testing.T, args ...string) string {
	t.Helper()
	out, code := w.run(t, args...)
	if code != 0 {
		t.Fatalf("agenthub %v: exit %d\n%s", args, code, out)
	}
	return out
}

func (w workspace) read(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(w.Project, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func (w workspace) exists(rel string) bool {
	_, err := os.Stat(filepath.Join(w.Project, rel))
	return err == nil
}

var reviewer = bundle{
	Namespace: "pilab", Name: "reviewer", Version: "1.0.0", Kind: "agent",
	Files: map[string]string{"prompt.md": "You are a meticulous code reviewer.\n"},
	Spec:  map[string]any{"agent": map[string]any{"prompt": "prompt.md"}},
}

var tester = bundle{
	Namespace: "pilab", Name: "tester", Version: "2.1.0", Kind: "skill",
	Files: map[string]string{"SKILL.md": "---\nname: tester\ndescription: Writes tests\n---\nWrite tests first.\n"},
	Spec:  map[string]any{"skill": map[string]any{"entry": "SKILL.md"}},
}
