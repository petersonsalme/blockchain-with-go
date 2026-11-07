package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Set at link time with -X (goreleaser); a plain go build keeps the defaults.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// newVersionCmd prints the tag, commit and build date the binary was built from.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the bwg version, commit and build date",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "bwg %s (commit %s, built %s)\n", version, commit, date)

			return err
		},
	}
}
