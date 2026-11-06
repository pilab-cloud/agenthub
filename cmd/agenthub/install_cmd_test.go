package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"go.pilab.hu/agenthub/internal/cache"
	"go.pilab.hu/agenthub/internal/lockfile"
)

func seedAgent(t *testing.T, prompt string) (cacheDir string) {
	t.Helper()
	cacheDir = t.TempDir()
	c, err := cache.New(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(prompt)
	bk := cache.Key(body)
	if err := c.Put(bk, body); err != nil {
		t.Fatal(err)
	}
	manifest := []byte(fmt.Sprintf(`{"kind":"agent","namespace":"pilab","name":"reviewer","summary":"Reviews code",`+
		`"files":[{"path":"prompt.md","sha256":"sha256:%s"}],"spec":{"agent":{"prompt":"prompt.md"}}}`, bk))
	mk := cache.Key(manifest)
	if err := c.Put(mk, manifest); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.WriteLocked(lockfile.LockFileName, lockfile.LockedState{
		Assets: []lockfile.LockedAsset{{Ref: "pilab/agent/reviewer@1.0.0", ManifestDigest: "sha256:" + mk}},
	}); err != nil {
		t.Fatal(err)
	}
	return cacheDir
}

func runInstallArgs(t *testing.T, cacheDir string, args ...string) (string, error) {
	t.Helper()
	root := newRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"install", "--offline", "--cache-dir", cacheDir}, args...))
	err := root.Execute()
	return out.String(), err
}

func TestInstallMaterializesAndDetectsDrift(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", t.TempDir())
	cacheDir := seedAgent(t, "Review carefully.\n")

	out, err := runInstallArgs(t, cacheDir, "-t", "claude,codex", "-l", "local")
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	agent := filepath.Join(dir, ".claude", "agents", "pilab-reviewer.md")
	b, err := os.ReadFile(agent)
	if err != nil || !strings.Contains(string(b), "Review carefully.") {
		t.Fatalf("agent file: %v %q", err, b)
	}
	agents, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil || !strings.Contains(string(agents), "agenthub:begin") {
		t.Fatalf("AGENTS.md: %v %q", err, agents)
	}

	// Rerun is idempotent; a user edit is drift and survives without --force.
	if err := os.WriteFile(agent, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = runInstallArgs(t, cacheDir, "-t", "claude", "-l", "local")
	if err != nil || !strings.Contains(out, "drift=1") {
		t.Fatalf("want drift: %v\n%s", err, out)
	}
	if b, _ := os.ReadFile(agent); string(b) != "mine" {
		t.Fatalf("drifted file overwritten: %q", b)
	}
	if _, err = runInstallArgs(t, cacheDir, "-t", "claude", "-l", "local", "--force"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(agent); !strings.Contains(string(b), "Review carefully.") {
		t.Fatalf("--force did not restore: %q", b)
	}

	out, err = runInstallArgs(t, cacheDir, "--refresh")
	if err != nil || !strings.Contains(out, "Claude") {
		t.Fatalf("refresh: %v\n%s", err, out)
	}
}

func TestInstallTargetNeedsLocationWithoutTTY(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())
	cacheDir := seedAgent(t, "x")
	if _, err := runInstallArgs(t, cacheDir, "-t", "claude"); err == nil {
		t.Fatal("expected location error")
	}
	if _, err := runInstallArgs(t, cacheDir, "-t", "nope", "-l", "local"); err == nil {
		t.Fatal("expected unknown target error")
	}
}

func TestInstallPrintConfigWritesNothing(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", t.TempDir())
	cacheDir := seedAgent(t, "Review carefully.\n")
	out, err := runInstallArgs(t, cacheDir, "--print-config", "cursor")
	if err != nil || !strings.Contains(out, "Review carefully.") {
		t.Fatalf("print-config: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".cursor")); err == nil {
		t.Fatal("print-config wrote files")
	}
}

func press(m wizardModel, keys ...tea.KeyMsg) wizardModel {
	for _, k := range keys {
		next, _ := m.Update(k)
		m = next.(wizardModel)
	}
	return m
}

func TestWizardFlow(t *testing.T) {
	m := newWizardModel(wizardInput{
		Harnesses: []wizardHarness{{ID: "claude", Name: "Claude", Global: true}, {ID: "cursor", Name: "Cursor"}},
		Preselect: map[string]bool{"claude": true},
	})
	enter := tea.KeyMsg{Type: tea.KeyEnter}
	down := tea.KeyMsg{Type: tea.KeyDown}
	space := tea.KeyMsg{Type: tea.KeySpace}
	m = press(m, down, space, enter, down, enter, enter) // add cursor, pick local, confirm
	if !m.done || m.result.Cancelled || m.result.Location != locLocal ||
		strings.Join(m.result.Targets, ",") != "claude,cursor" {
		t.Fatalf("result: %+v", m.result)
	}

	empty := press(newWizardModel(wizardInput{Harnesses: []wizardHarness{{ID: "claude", Name: "Claude"}}}), enter)
	if empty.stage != stageTargets || empty.errMsg == "" {
		t.Fatalf("empty selection must be rejected: %+v", empty)
	}
	if c := press(m, tea.KeyMsg{Type: tea.KeyEsc}); !c.result.Cancelled {
		t.Fatal("esc must cancel")
	}
}
