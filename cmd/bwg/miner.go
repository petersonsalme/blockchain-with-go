package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/spf13/cobra"
)

func newMinerCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "miner", Short: "Ask the node to mine blocks from its mempool", Args: cobra.NoArgs, RunE: runMiner}
	addNodeFlag(cmd)
	cmd.Flags().Uint("count", 0, "blocks to mine; 0 mines until interrupted")
	cmd.Flags().Duration("interval", 0, "pause between mined blocks")
	return cmd
}

// runMiner mines --count blocks (0 = until the context is done), retrying on 409.
func runMiner(cmd *cobra.Command, _ []string) error {
	count, err := cmd.Flags().GetUint("count")
	if err != nil {
		return err
	}
	interval, err := cmd.Flags().GetDuration("interval")
	if err != nil {
		return err
	}
	if interval < 0 {
		return fmt.Errorf("--interval must not be negative, got %s", interval)
	}
	api, err := newNodeClient(cmd)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	for mined := uint(0); count == 0 || mined < count; {
		resp, err := api.MinePendingWithResponse(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return err
		}
		if resp.StatusCode() == http.StatusConflict {
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "chain changed while mining, retrying")
			continue
		}
		if resp.JSON201 == nil {
			return nodeError(resp.HTTPResponse, resp.Body)
		}
		mined++
		b := resp.JSON201
		txs := 0
		if b.Transactions != nil {
			txs = len(*b.Transactions)
		}
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "block %d %s txs=%d\n", b.Index, b.Hash, txs); err != nil {
			return err
		}
		if interval > 0 {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(interval):
			}
		}
	}
	return nil
}
