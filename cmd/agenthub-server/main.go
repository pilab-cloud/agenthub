// Command agenthub-server runs the AgentHub server.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/uptrace/bun"

	"go.pilab.hu/agenthub/internal/config"
	"go.pilab.hu/agenthub/internal/db"
	"go.pilab.hu/agenthub/internal/logging"
	"go.pilab.hu/agenthub/internal/metrics"
	"go.pilab.hu/agenthub/internal/server"
	"go.pilab.hu/agenthub/internal/version"
	"go.pilab.hu/agenthub/internal/web"
	"go.pilab.hu/agenthub/migrations"
)

func main() {
	if err := newRoot().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "agenthub-server",
		Short:         "AgentHub server",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newServeCmd(), newMigrateCmd(), newVersionCmd())
	return root
}

func newServeCmd() *cobra.Command {
	var configPath string
	var checkConfig bool
	var httpAddr string

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the HTTP/Connect server",
		RunE: func(cmd *cobra.Command, args []string) error {
			bindings := map[string]string{}
			if httpAddr != "" {
				bindings["server.http_addr"] = "http-addr"
			}
			cfg, err := config.Load(cmd.Context(), configPath, cmd.Flags(), bindings)
			if err != nil {
				return err
			}
			if checkConfig {
				fmt.Println("configuration ok")
				return nil
			}
			logger, err := logging.New(cfg.Observability.LogLevel, cfg.Observability.LogFormat)
			if err != nil {
				return err
			}
			if cfg.Env == "dev" && !cfg.Signing.RequirePublisherSignature {
				logger.Warn().Msg("DEV MODE: publisher signatures are not required")
			}

			var database *bun.DB
			if cfg.Database.DSN != "" {
				database, err = db.Open(cmd.Context(), cfg.Database)
				if err != nil {
					logger.Warn().Err(err).Msg("database not reachable; readyz will report unavailable")
					database = nil
				}
			}
			if cfg.Database.MigrateOnStart && database != nil {
				if err := migrations.Up(cmd.Context(), database.DB); err != nil {
					return fmt.Errorf("migrate on start: %w", err)
				}
			}

			m, err := metrics.New(cfg.Observability.MetricsEnabled)
			if err != nil {
				return err
			}
			defer func() { _ = m.Shutdown(cmd.Context()) }()

			webH, err := web.New(version.String())
			if err != nil {
				return err
			}

			e := server.New(cfg, database, m, webH)

			// metrics server on separate listener
			metricsErr := make(chan error, 1)
			if cfg.Observability.MetricsEnabled && cfg.Server.MetricsAddr != "" {
				go func() {
					srv := &http.Server{Addr: cfg.Server.MetricsAddr, Handler: m.Handler(), ReadHeaderTimeout: 5 * time.Second}
					if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
						metricsErr <- err
					}
				}()
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			serverErr := make(chan error, 1)
			go func() {
				if err := e.Start(cfg.Server.HTTPAddr); err != nil && err != http.ErrServerClosed {
					serverErr <- err
				}
			}()

			select {
			case <-ctx.Done():
				shutCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownGrace)
				defer cancel()
				_ = e.Shutdown(shutCtx)
				if database != nil {
					_ = db.Close(database)
				}
				return nil
			case err := <-serverErr:
				return err
			case err := <-metricsErr:
				return err
			}
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "path to config file")
	cmd.Flags().BoolVar(&checkConfig, "check-config", false, "validate config and exit")
	cmd.Flags().StringVar(&httpAddr, "http-addr", "", "override server.http_addr")
	return cmd
}

func newMigrateCmd() *cobra.Command {
	var configPath string
	var direction string
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Run goose migrations",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(cmd.Context(), configPath, nil, nil)
			if err != nil {
				return err
			}
			database, err := db.Open(cmd.Context(), cfg.Database)
			if err != nil {
				return err
			}
			defer func() { _ = db.Close(database) }()
			switch direction {
			case "up", "":
				return migrations.Up(cmd.Context(), database.DB)
			case "status":
				out, err := migrations.Status(cmd.Context(), database.DB)
				if err != nil {
					return err
				}
				fmt.Print(out)
				return nil
			case "down":
				return migrations.Down(cmd.Context(), database.DB)
			default:
				return fmt.Errorf("unknown direction %q", direction)
			}
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "path to config file")
	cmd.Flags().StringVar(&direction, "direction", "up", "up | status | down")
	return cmd
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
