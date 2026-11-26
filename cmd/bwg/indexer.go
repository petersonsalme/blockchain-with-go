package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/petersonsalme/blockchain-with-go/internal/indexer"
	"github.com/petersonsalme/blockchain-with-go/internal/indexer/postgres"
)

const (
	indexerDatabaseURLEnvVar  = "INDEXER_DATABASE_URL"
	indexerPollIntervalEnvVar = "INDEXER_POLL_INTERVAL"
	indexerPortEnvVar         = "INDEXER_PORT"
	defaultIndexerPort        = "8090"
)

// newIndexerCmd builds `bwg indexer run|resync`: the reorg-safe Postgres chain indexer (ADR-0038).
// INDEXER_DATABASE_URL, NODE_URL (or --node), INDEXER_POLL_INTERVAL and INDEXER_PORT are read here only.
func newIndexerCmd(logger *slog.Logger) *cobra.Command {
	root := &cobra.Command{Use: "indexer", Short: "Copy the chain of a node into Postgres (INDEXER_DATABASE_URL)"}

	run := &cobra.Command{
		Use:   "run",
		Short: "Follow the node until interrupted, staying equal to its canonical chain",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return runIndexer(cmd, logger, false) },
	}
	resync := &cobra.Command{
		Use:   "resync",
		Short: "Drop the indexer's tables, re-ingest the node's chain from height 0 and exit when level",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return runIndexer(cmd, logger, true) },
	}

	addNodeFlag(run)
	addNodeFlag(resync)
	root.AddCommand(run, resync)

	return root
}

// runIndexer holds the database lock for its whole life. With rebuild it drops the tables
// first and returns once the database is level with the node; otherwise it follows the node.
func runIndexer(cmd *cobra.Command, logger *slog.Logger, rebuild bool) error {
	url := os.Getenv(indexerDatabaseURLEnvVar)
	if url == "" {
		return fmt.Errorf("%s is not set", indexerDatabaseURLEnvVar)
	}

	interval := indexer.DefaultPollInterval

	if raw := os.Getenv(indexerPollIntervalEnvVar); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return fmt.Errorf("%s must be a positive duration such as 1s, got %q", indexerPollIntervalEnvVar, raw)
		}

		interval = d
	}

	nodeURL, err := cmd.Flags().GetString("node")
	if err != nil {
		return err
	}

	node, err := indexer.NewNodeClient(nodeURL)
	if err != nil {
		return err
	}

	ctx := cmd.Context()

	store, err := postgres.Open(ctx, url)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close(context.WithoutCancel(ctx)) }()

	f := indexer.NewFollower(node, store, logger, indexer.WithPollInterval(interval))

	if !rebuild {
		return runIndexerWithAPI(ctx, logger, f, nodeURL, url)
	}

	err = store.Reset(ctx)
	if err != nil {
		return err
	}

	err = f.Sync(ctx)
	if errors.Is(err, context.Canceled) {
		return nil
	}

	return err
}

// runIndexerWithAPI binds the read API listener and runs the HTTP server alongside the follower until either stops.
func runIndexerWithAPI(ctx context.Context, logger *slog.Logger, f *indexer.Follower, nodeURL, dbURL string) error {
	port := envOr(indexerPortEnvVar, defaultIndexerPort)

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", ":"+port)
	if err != nil {
		return err
	}
	defer func() { _ = ln.Close() }()

	httpSrv := &http.Server{
		Handler:           indexer.NewAPI(postgres.NewReader(dbURL)),
		ReadHeaderTimeout: 5 * time.Second,
	}

	runCtx, stopFollower := context.WithCancel(ctx)
	defer stopFollower()

	httpErrCh := make(chan error, 1)

	go func() {
		logger.Info("indexer API listening", "addr", ln.Addr().String())

		httpErrCh <- httpSrv.Serve(ln)
	}()

	followErrCh := make(chan error, 1)

	go func() {
		logger.Info("indexer following", "node", nodeURL)

		followErrCh <- f.Run(runCtx)
	}()

	select {
	case err := <-followErrCh:
		shutdownCtx, cancel := shutdownContext()
		defer cancel()

		shutdownErr := httpSrv.Shutdown(shutdownCtx) //nolint:contextcheck // caller ctx may already be cancelled; graceful shutdown needs a fresh deadline
		httpErr := <-httpErrCh

		if err != nil {
			return err
		}

		if shutdownErr != nil {
			return shutdownErr
		}

		if !errors.Is(httpErr, http.ErrServerClosed) {
			return httpErr
		}

		return nil
	case httpErr := <-httpErrCh:
		stopFollower()

		followErr := <-followErrCh

		if !errors.Is(httpErr, http.ErrServerClosed) {
			return httpErr
		}

		return followErr
	}
}
