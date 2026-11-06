// Command agenthub is the AgentHub CLI.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"go.pilab.hu/agenthub/internal/config"
	"go.pilab.hu/agenthub/internal/version"
)

func main() {
	if err := newRoot().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitCode(err))
	}
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "agenthub",
		Short:         "AgentHub CLI",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(
		newVersionCmd(), newConfigCmd(),
		newAddCmd(), newInstallCmd(), newUpdateCmd(), newRemoveCmd(),
		newExportCmd(), newImportCmd(),
		newFederateCmd(), newSyncCmd(), newMaterializeCmd(),
	)
	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(version.String())
		},
	}
}

func newConfigCmd() *cobra.Command {
	var configPath string
	var agentFlag string
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Print effective configuration (secrets redacted)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(cmd.Context(), configPath, nil, nil)
			if err != nil {
				return err
			}
			out, err := cfg.RedactedYAML()
			if err != nil {
				return err
			}
			fmt.Print(string(out))
			fmt.Fprintln(cmd.OutOrStderr(), "Agent flag:", agentFlag)
			return nil
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "path to config file")
	cmd.Flags().StringVar(&agentFlag, "agent", "", "target harness/agent (e.g., opencode, claude, copilot)")
	return cmd
}
