package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"
	"go.pilab.hu/agenthub/internal/federation"
)

func newSyncCmd() *cobra.Command {
	var configFile string
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync upstream hubs from agenthub.federation.json",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := federation.ReadConfig(configFile)
			if err != nil {
				return err
			}
			store := federation.NewMemUpstreamStore()
			for _, u := range cfg.Upstreams {
				pk, _ := federation.ParsePublicKey(u.PublicKey)
				client, err := federation.NewUpstreamClient(u.BaseURL, federation.WithPinnedHubID(u.HubID), federation.WithPinnedKey(pk))
				if err != nil {
					fmt.Fprintf(os.Stderr, "skip %s: %v\n", u.BaseURL, err)
					continue
				}
				res, err := client.Sync(context.Background(), nil)
				if err != nil {
					fmt.Fprintf(os.Stderr, "sync %s failed: %v\n", u.BaseURL, err)
					continue
				}
				for _, e := range res.Assets {
					ref := &commonv1.AssetRef{Namespace: e.Namespace, Kind: e.Kind, Name: e.Name, Version: e.Version}
					var md, bd *commonv1.Digest
					if e.ManifestDigest != nil {
						md = e.ManifestDigest
					}
					if e.BundleDigest != nil {
						bd = e.BundleDigest
					}
					_ = store.Upsert(federation.UpstreamAsset{Hub: u.HubID, Ref: ref, ManifestDigest: md, BundleDigest: bd})
				}
				fmt.Printf("synced %s: %d assets\n", u.BaseURL, len(res.Assets))
			}
			if err := federation.WriteConfig(configFile, cfg); err != nil {
				return err
			}
			_ = store
			return nil
		},
	}
	cmd.Flags().StringVar(&configFile, "config", "agenthub.federation.json", "upstream config file")
	return cmd
}
