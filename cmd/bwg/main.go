package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	err := newRootCmdWith(logger).ExecuteContext(ctx)

	stop() // os.Exit skips deferred calls
	finish(err, logger, os.Exit)
}

// finish exits 1 on err. A node error is logged as a JSON "node stopped" line;
// other commands have already printed theirs through cobra.
func finish(err error, logger *slog.Logger, exit func(code int)) {
	if stopped, ok := errors.AsType[nodeStopped](err); ok {
		fatalIf(stopped.err, logger, exit)
	} else if err != nil {
		exit(1)
	}
}

// execute runs bwg with args and returns the command's error; stdout receives its output.
func execute(ctx context.Context, logger *slog.Logger, args []string, stdout io.Writer) error {
	root := newRootCmdWith(logger)
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stdout)

	return root.ExecuteContext(ctx)
}

// newRootCmd builds the bwg command tree; flag defaults read the environment.
func newRootCmd() *cobra.Command {
	return newRootCmdWith(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
}

// newRootCmdWith is newRootCmd with the logger the node command uses.
func newRootCmdWith(logger *slog.Logger) *cobra.Command {
	root := &cobra.Command{Use: "bwg", Short: "Blockchain node, wallet and miner", SilenceUsage: true}
	root.AddCommand(newWalletCmd(), newMinerCmd(), newNodeCmd(logger))

	return root
}

// envOr returns the value of key, or fallback when it is unset or empty.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return fallback
}
