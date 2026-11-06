package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"go.pilab.hu/agenthub/internal/cache"
	"go.pilab.hu/agenthub/internal/lockfile"
)

// installFlags are the flags of `agenthub install`.
type installFlags struct {
	Targets     string
	Location    string
	PrintConfig string
	Yes         bool
	Refresh     bool
	Force       bool
	Frozen      bool
	Offline     bool
	Hub         string
	CacheDir    string
	Config      string
}

func newInstallCmd() *cobra.Command {
	var f installFlags
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the locked assets into your agents (Claude Code, Cursor, Codex CLI, opencode, Copilot)",
		Long: strings.TrimSpace(`
Installs the assets pinned in agenthub.lock.json: first into the local cache
(verifying digests and signatures), then into the agents you pick, as each
agent's own files (.claude/agents, .opencode/agents, .cursor/rules, AGENTS.md,
...).

Without --target the interactive wizard asks which agents and which location
(global or local). Without a terminal and without --target, assets are only
cached. Files agenthub wrote are tracked; a file you edited since is reported
as drift and left alone unless --force is given.`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInstallCommand(cmd, f)
		},
	}
	fl := cmd.Flags()
	fl.StringVarP(&f.Targets, "target", "t", "", `target agent(s): comma-separated ids (claude, cursor, codex, opencode, copilot), or "auto"|"all"|"none" (default: prompt)`)
	fl.StringVarP(&f.Location, "location", "l", "", `install location: "global" or "local" (default: prompt)`)
	fl.BoolVarP(&f.Yes, "yes", "y", false, "non-interactive: defaults to --location=global --target=auto")
	fl.StringVar(&f.PrintConfig, "print-config", "", "print the files that would be written for the named agent and exit (no writes)")
	fl.BoolVar(&f.Refresh, "refresh", false, "rewrite what previous installs wrote, for already-configured agents only")
	fl.BoolVar(&f.Force, "force", false, "overwrite files that were modified since agenthub wrote them")
	fl.BoolVar(&f.Frozen, "frozen", false, "fail unless every pinned asset is fully installed")
	fl.BoolVar(&f.Offline, "offline", false, "never touch the network; use the cache only")
	fl.StringVar(&f.Hub, "hub", "", "hub URL")
	fl.StringVar(&f.CacheDir, "cache-dir", "", "cache directory (default: $AGENTHUB_CACHE_DIR or the user cache)")
	fl.StringVar(&f.Config, "config", "", "path to config file")
	return cmd
}

func isTerminal(v any) bool {
	f, ok := v.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func parseLocation(s string) (location, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return "", nil
	case "global":
		return locGlobal, nil
	case "local":
		return locLocal, nil
	}
	return "", fmt.Errorf("invalid --location %q: want global or local", s)
}

// parseTargets resolves --target. set is false when the flag was not given.
func parseTargets(flag string, e env) (targets []harness, set bool, err error) {
	flag = strings.ToLower(strings.TrimSpace(flag))
	switch flag {
	case "":
		return nil, false, nil
	case "none":
		return nil, true, nil
	case "all":
		return harnesses(), true, nil
	case "auto":
		return detected(e), true, nil
	}
	for _, id := range strings.Split(flag, ",") {
		h, ok := findHarness(strings.TrimSpace(id))
		if !ok {
			return nil, false, fmt.Errorf("unknown target %q (known: %s)", id, harnessIDs())
		}
		targets = append(targets, h)
	}
	return targets, true, nil
}

func harnessIDs() string {
	var ids []string
	for _, h := range harnesses() {
		ids = append(ids, h.ID)
	}
	return strings.Join(ids, ", ")
}

func detected(e env) []harness {
	var out []harness
	for _, h := range harnesses() {
		if h.Detect(e.Home) {
			out = append(out, h)
		}
	}
	return out
}

func runInstallCommand(cmd *cobra.Command, f installFlags) error {
	ctx, out := cmd.Context(), cmd.OutOrStdout()
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	e := env{Home: home, Cwd: cwd}
	loc, err := parseLocation(f.Location)
	if err != nil {
		return err
	}
	targets, targetsSet, err := parseTargets(f.Targets, e)
	if err != nil {
		return err
	}

	cacheDir := f.CacheDir
	if cacheDir == "" {
		cacheDir = cache.DefaultDir()
	}
	c, err := cache.New(cacheDir)
	if err != nil {
		return fmt.Errorf("open cache: %w", err)
	}

	if f.PrintConfig != "" {
		return printConfig(ctx, out, c, e, f.PrintConfig, loc)
	}

	opts := installOptions{Hub: resolveHub(ctx, f.Hub, f.Config), CacheDir: cacheDir, Offline: f.Offline}
	if err := runInstall(ctx, out, lockfile.LockFileName, f.Frozen, opts); err != nil {
		return err
	}
	locked, err := lockfile.ReadLocked(lockfile.LockFileName)
	if err != nil {
		return err
	}

	if f.Refresh {
		return refreshInstalls(out, c, locked.Assets, e, f.Force)
	}

	interactive := !f.Yes && isTerminal(cmd.InOrStdin()) && isTerminal(out)
	if f.Yes {
		if !targetsSet {
			targets, targetsSet = detected(e), true
		}
		if loc == "" {
			loc = locGlobal
		}
	}
	switch {
	case !targetsSet && !interactive:
		return nil
	case targetsSet && loc == "" && !interactive:
		return fmt.Errorf("--location global|local is required without a terminal")
	}

	if interactive && (!targetsSet || loc == "") {
		res, err := runWizard(cmd.InOrStdin(), out, wizardInputFor(e, targets, targetsSet, loc, len(locked.Assets)))
		if err != nil {
			return err
		}
		if res.Cancelled {
			fmt.Fprintln(out, "cancelled; nothing written to agents")
			return nil
		}
		targets, loc = nil, res.Location
		for _, id := range res.Targets {
			if h, ok := findHarness(id); ok {
				targets = append(targets, h)
			}
		}
	}
	if len(targets) == 0 {
		fmt.Fprintln(out, "no target agents selected; assets are cached only")
		return nil
	}
	return materializeAll(out, c, locked.Assets, targets, loc, e, f.Force)
}

func wizardInputFor(e env, preset []harness, presetSet bool, loc location, assets int) wizardInput {
	in := wizardInput{Preselect: map[string]bool{}, Location: loc, Assets: assets}
	for _, h := range harnesses() {
		in.Harnesses = append(in.Harnesses, wizardHarness{ID: h.ID, Name: h.Name, Detected: h.Detect(e.Home), Global: h.Global})
		in.Preselect[h.ID] = h.Detect(e.Home)
	}
	if presetSet {
		in.Preselect = map[string]bool{}
		for _, h := range preset {
			in.Preselect[h.ID] = true
		}
	}
	return in
}

// loadAssets loads every pinned asset from the cache; assets that are not
// installed are reported and skipped.
func loadAssets(out io.Writer, c *cache.Cache, pins []lockfile.LockedAsset) []*materialAsset {
	var assets []*materialAsset
	for _, pin := range pins {
		a, err := loadAsset(c, pin)
		if err != nil {
			fmt.Fprintf(out, "skip %v\n", err)
			continue
		}
		assets = append(assets, a)
	}
	return assets
}

// materializeAll writes the assets for every target into loc.
func materializeAll(out io.Writer, c *cache.Cache, pins []lockfile.LockedAsset, targets []harness, loc location, e env, force bool) error {
	assets := loadAssets(out, c, pins)
	st, err := loadState(e.stateFile(loc))
	if err != nil {
		return err
	}
	for _, h := range targets {
		if loc == locGlobal && !h.Global {
			fmt.Fprintf(out, "%s: skipped, no global location (use --location local)\n", h.Name)
			continue
		}
		res, err := applyPlan(h, loc, e, buildPlan(h, loc, e, assets), &st, force)
		if err != nil {
			return fmt.Errorf("%s: %w", h.Name, err)
		}
		if !containsString(st.Harnesses, h.ID) {
			st.Harnesses = append(st.Harnesses, h.ID)
		}
		fmt.Fprintf(out, "%s (%s): written=%d unchanged=%d removed=%d drift=%d\n",
			h.Name, loc, res.Written, res.Unchanged, res.Removed, res.Drift)
		for _, n := range res.Notes {
			fmt.Fprintf(out, "  %s\n", n)
		}
	}
	return saveState(e.stateFile(loc), st)
}

// refreshInstalls rewrites what earlier installs configured; it never adds a
// target.
func refreshInstalls(out io.Writer, c *cache.Cache, pins []lockfile.LockedAsset, e env, force bool) error {
	any := false
	for _, loc := range []location{locGlobal, locLocal} {
		st, err := loadState(e.stateFile(loc))
		if err != nil {
			return err
		}
		var targets []harness
		for _, id := range st.Harnesses {
			if h, ok := findHarness(id); ok {
				targets = append(targets, h)
			}
		}
		if len(targets) == 0 {
			continue
		}
		any = true
		if err := materializeAll(out, c, pins, targets, loc, e, force); err != nil {
			return err
		}
	}
	if !any {
		fmt.Fprintln(out, "nothing to refresh: no agents were configured by a previous install")
	}
	return nil
}

// printConfig shows what an install would write for one agent, without
// touching the disk or the network.
func printConfig(ctx context.Context, out io.Writer, c *cache.Cache, e env, id string, loc location) error {
	_ = ctx
	h, ok := findHarness(id)
	if !ok {
		return fmt.Errorf("unknown target %q (known: %s)", id, harnessIDs())
	}
	if loc == "" {
		loc = locLocal
	}
	if loc == locGlobal && !h.Global {
		return fmt.Errorf("%s has no global location", h.Name)
	}
	locked, err := lockfile.ReadLocked(lockfile.LockFileName)
	if err != nil {
		return err
	}
	p := buildPlan(h, loc, e, loadAssets(io.Discard, c, locked.Assets))
	for _, f := range p.Files {
		fmt.Fprintf(out, "# %s\n%s\n", f.Path, f.Content)
	}
	for _, s := range p.Sections {
		fmt.Fprintf(out, "# %s (section %s)\n%s\n\n", s.Path, s.ID, s.Body)
	}
	for _, n := range p.Notes {
		fmt.Fprintf(out, "# note: %s\n", n)
	}
	return nil
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
