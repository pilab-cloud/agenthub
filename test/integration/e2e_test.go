package integration

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	catalogv1 "go.pilab.hu/agenthub/gen/go/agenthub/catalog/v1"
	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"
	registryv1 "go.pilab.hu/agenthub/gen/go/agenthub/registry/v1"
)

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url) //nolint:gosec,noctx // test URL
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestHubEndpoints(t *testing.T) {
	h := startHub(t)
	for _, p := range []string{"/healthz", "/.well-known/agenthub.json", "/"} {
		if code, _ := get(t, h.URL+p); code != http.StatusOK {
			t.Errorf("GET %s = %d", p, code)
		}
	}
}

func TestPublishSearchAndBundle(t *testing.T) {
	h := startHub(t)
	h.publish(t, "alice", reviewer)
	ctx := context.Background()

	res, err := h.Catalog.SearchAssets(ctx, connect.NewRequest(&catalogv1.SearchAssetsRequest{Query: "reviewer"}))
	if err != nil || len(res.Msg.Assets) != 1 {
		t.Fatalf("search: %v %v", err, res)
	}

	ref := &commonv1.AssetRef{Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "reviewer", Version: "1.0.0"}
	b, err := h.Catalog.GetBundle(ctx, connect.NewRequest(&catalogv1.GetBundleRequest{
		Selector: &catalogv1.GetBundleRequest_Ref{Ref: ref},
	}))
	if err != nil {
		t.Fatalf("GetBundle: %v", err)
	}
	if shaHex(b.Msg.Manifest) != b.Msg.ManifestDigest.Value || len(b.Msg.Files) != 1 {
		t.Fatalf("bundle: %+v", b.Msg)
	}
	code, body := get(t, b.Msg.Files[0].Url)
	if code != http.StatusOK || body != reviewer.Files["prompt.md"] {
		t.Fatalf("blob: %d %q", code, body)
	}

	// By manifest digest too.
	if _, err := h.Catalog.GetBundle(ctx, connect.NewRequest(&catalogv1.GetBundleRequest{
		Selector: &catalogv1.GetBundleRequest_ManifestDigest{ManifestDigest: b.Msg.ManifestDigest},
	})); err != nil {
		t.Fatalf("GetBundle by digest: %v", err)
	}

	// Unknown bundle.
	ref.Name = "ghost"
	_, err = h.Catalog.GetBundle(ctx, connect.NewRequest(&catalogv1.GetBundleRequest{
		Selector: &catalogv1.GetBundleRequest_Ref{Ref: ref},
	}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("want NotFound, got %v", err)
	}
}

func TestBlobUploadRules(t *testing.T) {
	h := startHub(t)
	data := []byte("hello")
	url := h.URL + "/blobs/" + shaHex(data)

	if code := putBlob(t, url, "", data); code != http.StatusUnauthorized {
		t.Errorf("anonymous upload = %d, want 401", code)
	}
	if code := putBlob(t, url, "alice", []byte("tampered")); code != http.StatusBadRequest {
		t.Errorf("digest mismatch = %d, want 400", code)
	}
	if code, _ := get(t, url); code != http.StatusNotFound {
		t.Errorf("missing blob = %d, want 404", code)
	}
	if code := putBlob(t, url, "alice", data); code != http.StatusNoContent {
		t.Errorf("upload = %d, want 204", code)
	}
	if code, body := get(t, url); code != http.StatusOK || body != "hello" {
		t.Errorf("download = %d %q", code, body)
	}
}

func TestPublishRequiresNamespaceMembership(t *testing.T) {
	h := startHub(t)
	h.publish(t, "alice", reviewer)
	ctx := context.Background()
	_, err := h.Registry.BeginPublish(ctx, as("mallory", &registryv1.BeginPublishRequest{
		Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "evil", Version: "1.0.0",
	}))
	if code := connect.CodeOf(err); code != connect.CodePermissionDenied && code != connect.CodeUnauthenticated {
		t.Fatalf("foreign publish: want denial, got %v", err)
	}
}

func TestWebPagesRenderPublishedAsset(t *testing.T) {
	h := startHub(t)
	h.publish(t, "alice", reviewer)
	for path, want := range map[string]string{
		"/":                             "reviewer",
		"/assets":                       "reviewer",
		"/ns/pilab":                     "pilab",
		"/a/pilab/agent/reviewer":       "1.0.0",
		"/a/pilab/agent/reviewer/1.0.0": "reviewer",
	} {
		code, body := get(t, h.URL+path)
		// A template error after the first write still reports 200, so
		// check the document was completed.
		if code != http.StatusOK || !strings.Contains(body, want) || !strings.Contains(body, "</html>") {
			t.Errorf("GET %s: status %d, has %q=%v, complete=%v", path, code, want,
				strings.Contains(body, want), strings.Contains(body, "</html>"))
		}
	}
}

func TestCLIAddInstallRemoveLifecycle(t *testing.T) {
	h := startHub(t)
	h.publish(t, "alice", reviewer)
	h.publish(t, "alice", tester)
	w := newWorkspace(t)

	out := w.mustRun(t, "add", "pilab/agent/reviewer", "--hub", h.URL)
	if !strings.Contains(out, "fetched") && !strings.Contains(out, "summary") {
		t.Fatalf("add output: %s", out)
	}
	w.mustRun(t, "add", "pilab/skill/tester@^2", "--hub", h.URL)
	if !strings.Contains(w.read(t, "agenthub.json"), "tester") || !strings.Contains(w.read(t, "agenthub.lock.json"), "reviewer@1.0.0") {
		t.Fatalf("desired/lock not written")
	}

	// Install into every harness, project-local.
	out = w.mustRun(t, "install", "--hub", h.URL, "-t", "all", "-l", "local")
	if !strings.Contains(out, "summary: total=2") {
		t.Fatalf("install output: %s", out)
	}
	for _, f := range []string{
		".claude/agents/pilab-reviewer.md", ".claude/skills/tester/SKILL.md",
		".opencode/agents/pilab-reviewer.md", ".github/agents/pilab-reviewer.agent.md",
		".github/skills/tester/SKILL.md", ".cursor/rules/pilab-reviewer.mdc", "AGENTS.md",
	} {
		if !w.exists(f) {
			t.Errorf("missing %s", f)
		}
	}
	if !strings.Contains(w.read(t, ".claude/agents/pilab-reviewer.md"), "meticulous code reviewer") {
		t.Error("agent prompt not materialized")
	}
	if got := w.read(t, ".claude/skills/tester/SKILL.md"); got != tester.Files["SKILL.md"] {
		t.Errorf("skill must be copied verbatim, got %q", got)
	}

	// Re-run is a no-op.
	out = w.mustRun(t, "install", "--hub", h.URL, "-t", "claude", "-l", "local")
	if !strings.Contains(out, "written=0") || !strings.Contains(out, "drift=0") {
		t.Fatalf("rerun should be idempotent: %s", out)
	}

	// Frozen + offline reinstall works purely from the cache.
	w.mustRun(t, "install", "--frozen", "--offline", "-t", "claude", "-l", "local")

	// Remove drops the asset from desired and lock.
	w.mustRun(t, "remove", "pilab/skill/tester")
	if strings.Contains(w.read(t, "agenthub.lock.json"), "tester") {
		t.Fatal("tester still locked after remove")
	}
	// A refresh prunes what the removed asset wrote.
	w.mustRun(t, "install", "--offline", "--refresh")
	if w.exists(".claude/skills/tester/SKILL.md") {
		t.Error("stale skill not pruned by --refresh")
	}
	if !w.exists(".claude/agents/pilab-reviewer.md") {
		t.Error("refresh removed a still-locked asset")
	}
}

func TestCLIGlobalInstall(t *testing.T) {
	h := startHub(t)
	h.publish(t, "alice", reviewer)
	w := newWorkspace(t)
	w.mustRun(t, "add", "pilab/agent/reviewer", "--hub", h.URL)
	out := w.mustRun(t, "install", "--hub", h.URL, "-t", "claude,cursor", "-l", "global")
	if !strings.Contains(out, "Cursor: skipped") {
		t.Errorf("cursor has no global location: %s", out)
	}
	if _, err := os.Stat(filepath.Join(w.Home, ".claude", "agents", "pilab-reviewer.md")); err != nil {
		t.Errorf("global agent missing: %v", err)
	}
	if w.exists(".claude") {
		t.Error("global install wrote into the project")
	}
}

func TestCLIDriftIsPreservedUntilForce(t *testing.T) {
	h := startHub(t)
	h.publish(t, "alice", reviewer)
	w := newWorkspace(t)
	w.mustRun(t, "add", "pilab/agent/reviewer", "--hub", h.URL)
	w.mustRun(t, "install", "--hub", h.URL, "-t", "claude", "-l", "local")

	path := filepath.Join(w.Project, ".claude", "agents", "pilab-reviewer.md")
	if err := os.WriteFile(path, []byte("my edits"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := w.mustRun(t, "install", "--hub", h.URL, "-t", "claude", "-l", "local")
	if !strings.Contains(out, "drift=1") || w.read(t, ".claude/agents/pilab-reviewer.md") != "my edits" {
		t.Fatalf("drift must be kept: %s", out)
	}
	w.mustRun(t, "install", "--hub", h.URL, "-t", "claude", "-l", "local", "--force")
	if !strings.Contains(w.read(t, ".claude/agents/pilab-reviewer.md"), "meticulous") {
		t.Fatal("--force must restore the managed file")
	}
}

func TestCLIPrintConfigIsReadOnly(t *testing.T) {
	h := startHub(t)
	h.publish(t, "alice", reviewer)
	w := newWorkspace(t)
	w.mustRun(t, "add", "pilab/agent/reviewer", "--hub", h.URL)
	out := w.mustRun(t, "install", "--offline", "--print-config", "claude")
	if !strings.Contains(out, "meticulous code reviewer") || w.exists(".claude") {
		t.Fatalf("print-config: %s", out)
	}
}

func TestCLIUnknownAssetFailsResolution(t *testing.T) {
	h := startHub(t)
	w := newWorkspace(t)
	out, code := w.run(t, "add", "pilab/agent/ghost", "--hub", h.URL)
	if code != 4 {
		t.Fatalf("exit = %d, want 4\n%s", code, out)
	}
	if w.exists("agenthub.json") && strings.Contains(w.read(t, "agenthub.json"), "ghost") {
		t.Fatal("failed add must not leave the desired entry behind")
	}
}

func TestCLIOfflineWithEmptyCacheIsIncomplete(t *testing.T) {
	h := startHub(t)
	h.publish(t, "alice", reviewer)
	w := newWorkspace(t)
	w.mustRun(t, "add", "pilab/agent/reviewer", "--hub", h.URL)
	if err := os.RemoveAll(w.Cache); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(w.Cache, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, code := w.run(t, "install", "--frozen", "--offline"); code != 6 {
		t.Fatalf("exit = %d, want 6\n%s", code, out)
	}
}

func TestCLIUpdatePicksUpNewVersion(t *testing.T) {
	h := startHub(t)
	h.publish(t, "alice", reviewer)
	w := newWorkspace(t)
	w.mustRun(t, "add", "pilab/agent/reviewer", "--hub", h.URL)

	v2 := reviewer
	v2.Version = "1.1.0"
	v2.Files = map[string]string{"prompt.md": "Reviewer, second edition.\n"}
	h.publish(t, "alice", v2)

	w.mustRun(t, "update", "--hub", h.URL)
	if !strings.Contains(w.read(t, "agenthub.lock.json"), "reviewer@1.1.0") {
		t.Fatalf("lock not advanced:\n%s", w.read(t, "agenthub.lock.json"))
	}
	w.mustRun(t, "install", "--hub", h.URL, "-t", "claude", "-l", "local")
	if !strings.Contains(w.read(t, ".claude/agents/pilab-reviewer.md"), "second edition") {
		t.Fatal("install did not materialize the new version")
	}
}
