package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := newRootCmd().ExecuteContext(ctx)
	stop() // os.Exit skips deferred calls
	if err != nil {
		os.Exit(1)
	}
}

// newRootCmd builds the bwg command tree; flag defaults read the environment.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{Use: "bwg", Short: "Blockchain node, wallet and miner", SilenceUsage: true}
	root.AddCommand(newWalletCmd(), newMinerCmd())
	return root
}

// envOr returns the value of key, or fallback when it is unset or empty.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
