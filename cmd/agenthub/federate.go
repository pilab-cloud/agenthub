package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"go.pilab.hu/agenthub/internal/federation"
)

// newFederateCmd is the federation CLI surface (M4.2): record and list the
// upstream hubs this project federates with. Syncing them is the server's
// job; the client half lives in internal/federation.
func newFederateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "federate",
		Short: "Manage federated upstream hubs",
	}
	cmd.AddCommand(newFederateAddCmd(), newFederateListCmd())
	return cmd
}

// federateAddOptions are the flags of `federate add`.
type federateAddOptions struct {
	Name        string
	Mode        string
	Fingerprint string
	File        string
}

func newFederateAddCmd() *cobra.Command {
	var opts federateAddOptions
	cmd := &cobra.Command{
		Use:   "add <hub-url>",
		Short: "Record a federated upstream hub",
		Long: strings.TrimSpace(`
Records a hub in agenthub.federation.json after fetching its identity
(GetHubInfo) and checking it against the pinned trust anchors.

Without --fingerprint the hub key is trusted on first use (TOFU): the
fingerprint is printed and must be verified out of band before the
upstream is trusted. With --fingerprint the key is pinned: a hub that
advertises any other key is rejected.`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFederateAdd(cmd, args[0], opts)
		},
	}
	cmd.Flags().StringVar(&opts.Name, "name", "", "local alias for the upstream (default: hub host)")
	cmd.Flags().StringVar(&opts.Mode, "mode", federation.ModeReference, "sync mode: reference, mirror or proxy")
	cmd.Flags().StringVar(&opts.Fingerprint, "fingerprint", "", "expected sha256 fingerprint of the hub key (default: trust on first use)")
	cmd.Flags().StringVar(&opts.File, "file", federation.FederationFileName, "upstream config file")
	return cmd
}

// runFederateAdd verifies the hub's identity, then records it. Nothing is
// written when verification fails: a bad key must not leave a usable
// upstream record behind.
func runFederateAdd(cmd *cobra.Command, hubURL string, opts federateAddOptions) error {
	ctx := cmd.Context()
	mode, err := federation.ValidMode(opts.Mode)
	if err != nil {
		return err
	}
	client, err := federation.NewUpstreamClient(hubURL)
	if err != nil {
		return err
	}
	hub, err := client.HubInfo(ctx)
	if err != nil {
		return err
	}

	trust := federation.TrustTOFU
	if pinned := strings.TrimSpace(opts.Fingerprint); pinned != "" {
		want, err := federation.ParseFingerprint(pinned)
		if err != nil {
			return err
		}
		if want != hub.Fingerprint {
			return fmt.Errorf("%w: hub %q advertises %s, --fingerprint pinned %s",
				federation.ErrKeyMismatch, hub.ID, hub.Fingerprint, want)
		}
		trust = federation.TrustPinned
	}

	name := strings.TrimSpace(opts.Name)
	if name == "" {
		name = hubHost(client.BaseURL())
	}
	cfg, err := federation.ReadConfig(opts.File)
	if err != nil {
		return err
	}
	replaced, err := cfg.Upsert(federation.Upstream{
		Name:        name,
		HubID:       hub.ID,
		BaseURL:     client.BaseURL(),
		Mode:        mode,
		Trust:       trust,
		Fingerprint: hub.Fingerprint,
		PublicKey:   federation.EncodePublicKey(hub.PublicKey),
		AddedAt:     time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	if err := federation.WriteConfig(opts.File, cfg); err != nil {
		return err
	}

	verb := "added"
	if replaced {
		verb = "updated"
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "%s upstream %q in %s\n", verb, name, opts.File)
	fmt.Fprintf(out, "  hub:         %s\n", hub.ID)
	fmt.Fprintf(out, "  url:         %s\n", client.BaseURL())
	fmt.Fprintf(out, "  mode:        %s\n", mode)
	if trust == federation.TrustTOFU {
		fmt.Fprintln(out, "  trust:       tofu; verify the fingerprint out of band before trusting this hub")
	} else {
		fmt.Fprintln(out, "  trust:       pinned")
	}
	fmt.Fprintf(out, "  fingerprint: %s\n", hub.Fingerprint)
	return nil
}

func newFederateListCmd() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List recorded federated upstream hubs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := federation.ReadConfig(file)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(cfg.Upstreams) == 0 {
				fmt.Fprintf(out, "no upstreams recorded in %s\n", file)
				return nil
			}
			for _, u := range cfg.Upstreams {
				fmt.Fprintf(out, "%s\thub=%s\t%s\tmode=%s\ttrust=%s\t%s\n",
					u.Name, u.HubID, u.BaseURL, u.Mode, u.Trust, u.Fingerprint)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", federation.FederationFileName, "upstream config file")
	return cmd
}
