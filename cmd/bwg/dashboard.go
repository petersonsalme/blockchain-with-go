package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/petersonsalme/blockchain-with-go/internal/dashboard"
)

// newDashboardCmd builds `bwg dashboard`: the read-only network dashboard (ADR-0023).
// NODES and PORT come from the environment only.
func newDashboardCmd(logger *slog.Logger) *cobra.Command {
	return &cobra.Command{
		Use:   "dashboard",
		Short: "Run the read-only network dashboard (NODES=name=http://host:port,...; PORT)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			nodes := os.Getenv("NODES")

			// Check NODES before listening, so a bad entry fails fast.
			_, err := dashboard.ParseNodes(nodes)
			if err != nil {
				return err
			}

			ln, err := (&net.ListenConfig{}).Listen(cmd.Context(), "tcp", ":"+envOr("PORT", "8080"))
			if err != nil {
				return err
			}

			return serveDashboard(cmd.Context(), logger, ln, nodes)
		},
	}
}

// serveDashboard serves the dashboard for the NODES value on ln until ctx is done.
func serveDashboard(ctx context.Context, logger *slog.Logger, ln net.Listener, nodesSpec string) error {
	defer func() { _ = ln.Close() }()

	nodes, err := dashboard.ParseNodes(nodesSpec)
	if err != nil {
		return err
	}

	handler, err := dashboard.NewServer(nodes, dashboard.NewFetcher(dashboard.DefaultTimeout, dashboard.DefaultMaxBody))
	if err != nil {
		return err
	}

	httpSrv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	errCh := make(chan error, 1)

	go func() {
		logger.Info("dashboard listening", "addr", ln.Addr().String(), "nodes", len(nodes))

		errCh <- httpSrv.Serve(ln)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := shutdownContext()
	defer cancel()

	err = httpSrv.Shutdown(shutdownCtx) //nolint:contextcheck // ctx is already cancelled here; graceful shutdown needs a fresh deadline
	if err != nil {
		return err
	}

	err = <-errCh
	if !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	return nil
}
