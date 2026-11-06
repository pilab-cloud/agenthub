package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"go.pilab.hu/agenthub/internal/lockfile"
)

func newMaterializeCmd() *cobra.Command {
	var outDir string
	cmd := &cobra.Command{
		Use:   "materialize",
		Short: "Materialize locked assets into .opencode/ (portable mode)",
		RunE: func(cmd *cobra.Command, args []string) error {
			locked, err := lockfile.ReadLocked("agenthub.lock.json")
			if err != nil {
				return err
			}
			if outDir == "" {
				outDir = ".opencode"
			}
			if err := os.MkdirAll(outDir, 0o755); err != nil {
				return err
			}
			for _, la := range locked.Assets {
				ref := la.Ref
				safe := la.Ref
				for i := range safe {
					if safe[i] == '/' || safe[i] == '\\' {
						safe = safe[:i] + "_" + safe[i+1:]
					}
				}
				dir := filepath.Join(outDir, safe)
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return err
				}
				meta := map[string]any{"ref": ref, "manifest_digest": la.ManifestDigest}
				b, _ := json.MarshalIndent(meta, "", "  ")
				if err := os.WriteFile(filepath.Join(dir, "asset.json"), b, 0o644); err != nil {
					return err
				}
				fmt.Printf("materialized %s -> %s\n", ref, dir)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&outDir, "out", "", "output directory (default .opencode)")
	return cmd
}
