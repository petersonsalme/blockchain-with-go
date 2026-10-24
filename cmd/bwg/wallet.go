package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"

	"github.com/petersonsalme/blockchain-with-go/client"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

const (
	nodeURLEnvVar        = "NODE_URL"
	walletKeystoreEnvVar = "WALLET_KEYSTORE"
	walletPasswordEnvVar = "WALLET_PASSWORD"
	defaultNodeURL       = "http://localhost:8080"
	defaultKeystore      = "wallet.key"
)

func newWalletCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "wallet", Short: "Manage a wallet and talk to a node"}
	cmd.AddCommand(newWalletNewCmd(), newWalletBalanceCmd())
	return cmd
}

func newWalletNewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "new",
		Short: "Generate a new wallet and save it to a keystore",
		Args:  cobra.NoArgs,
		RunE:  runWalletNew,
	}
	cmd.Flags().String("keystore", envOr(walletKeystoreEnvVar, defaultKeystore), "path to keystore file")
	return cmd
}

func newWalletBalanceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "balance ADDRESS",
		Short: "Fetch the balance of an address from a node",
		Args:  cobra.ExactArgs(1),
		RunE:  runWalletBalance,
	}
	addNodeFlag(cmd)
	return cmd
}

// addNodeFlag adds --node, defaulting to NODE_URL or http://localhost:8080.
func addNodeFlag(cmd *cobra.Command) {
	cmd.Flags().String("node", envOr(nodeURLEnvVar, defaultNodeURL), "base URL of the node HTTP API")
}

// newNodeClient builds the API client for the --node flag of cmd.
func newNodeClient(cmd *cobra.Command) (*client.ClientWithResponses, error) {
	url, err := cmd.Flags().GetString("node")
	if err != nil {
		return nil, err
	}
	return client.NewClientWithResponses(url)
}

// nodeError reports a non-success answer of the node with its status and message.
func nodeError(resp *http.Response, body []byte) error {
	var e client.ErrorResponse
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return fmt.Errorf("node answered %d: %s", resp.StatusCode, e.Error)
	}
	return fmt.Errorf("node answered %d", resp.StatusCode)
}

func runWalletNew(cmd *cobra.Command, args []string) error {
	password := os.Getenv(walletPasswordEnvVar)
	if password == "" {
		return fmt.Errorf("%s: %w", walletPasswordEnvVar, wallet.ErrEmptyPassword)
	}
	path, err := cmd.Flags().GetString("keystore")
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("keystore %q: %w", path, os.ErrExist)
	}
	w, err := wallet.New()
	if err != nil {
		return err
	}
	if err := w.Save(path, password); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), w.Address())
	return nil
}

func runWalletBalance(cmd *cobra.Command, args []string) error {
	if err := wallet.ValidateAddress(args[0]); err != nil {
		return err
	}
	c, err := newNodeClient(cmd)
	if err != nil {
		return err
	}
	resp, err := c.GetBalanceWithResponse(cmd.Context(), args[0])
	if err != nil {
		return err
	}
	if resp.JSON200 == nil {
		return nodeError(resp.HTTPResponse, resp.Body)
	}
	fmt.Fprintln(cmd.OutOrStdout(), resp.JSON200.Balance)
	return nil
}
