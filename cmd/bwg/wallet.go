package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/petersonsalme/blockchain-with-go/client"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
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
	cmd.AddCommand(newWalletNewCmd(), newWalletBalanceCmd(), newWalletSendCmd())

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

func newWalletSendCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "send",
		Short: "Send a transfer to an address",
		Args:  cobra.NoArgs,
		RunE:  runWalletSend,
	}
	cmd.Flags().String("to", "", "recipient address")
	_ = cmd.MarkFlagRequired("to")
	cmd.Flags().Uint64("amount", 0, "amount to send")
	_ = cmd.MarkFlagRequired("amount")
	cmd.Flags().Uint64("fee", 0, "transaction fee")
	cmd.Flags().String("keystore", envOr(walletKeystoreEnvVar, defaultKeystore), "path to keystore file")
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

	_, err = os.Stat(path)
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("keystore %q: %w", path, os.ErrExist)
	}

	w, err := wallet.New()
	if err != nil {
		return err
	}

	err = w.Save(path, password)
	if err != nil {
		return err
	}

	_, err = fmt.Fprintln(cmd.OutOrStdout(), w.Address())

	return err
}

func runWalletBalance(cmd *cobra.Command, args []string) error {
	err := wallet.ValidateAddress(args[0])
	if err != nil {
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

	_, err = fmt.Fprintln(cmd.OutOrStdout(), resp.JSON200.Balance)

	return err
}

func runWalletSend(cmd *cobra.Command, args []string) error {
	path, err := cmd.Flags().GetString("keystore")
	if err != nil {
		return err
	}

	w, err := wallet.Load(path, os.Getenv(walletPasswordEnvVar))
	if err != nil {
		return fmt.Errorf("keystore %q: %w", path, err)
	}

	c, err := newNodeClient(cmd)
	if err != nil {
		return err
	}

	resp, err := c.GetBalanceWithResponse(cmd.Context(), w.Address())
	if err != nil {
		return err
	}

	if resp.JSON200 == nil {
		return nodeError(resp.HTTPResponse, resp.Body)
	}

	to, err := cmd.Flags().GetString("to")
	if err != nil {
		return err
	}

	amount, err := cmd.Flags().GetUint64("amount")
	if err != nil {
		return err
	}

	fee, err := cmd.Flags().GetUint64("fee")
	if err != nil {
		return err
	}

	tx, err := buildTransfer(w, resp.JSON200.Utxos, to, amount, fee, time.Now())
	if err != nil {
		return err
	}

	sub, err := c.SubmitTxWithResponse(cmd.Context(), toClientTx(tx))
	if err != nil {
		return err
	}

	if sub.JSON202 == nil {
		return nodeError(sub.HTTPResponse, sub.Body)
	}

	_, err = fmt.Fprintln(cmd.OutOrStdout(), sub.JSON202.Id)

	return err
}

func toClientTx(tx domain.Transaction) client.Transaction {
	id := tx.ID

	ins := make([]client.TxIn, len(tx.Inputs))
	for i, in := range tx.Inputs {
		ins[i] = client.TxIn{
			OutIndex:  in.OutIndex,
			PrevTxID:  in.PrevTxID,
			PublicKey: in.PublicKey,
			Signature: in.Signature,
		}
	}

	outs := make([]client.TxOut, len(tx.Outputs))
	for i, out := range tx.Outputs {
		outs[i] = client.TxOut{
			Address: out.Address,
			Amount:  out.Amount,
		}
	}

	return client.Transaction{
		ID:        &id,
		Inputs:    &ins,
		Outputs:   outs,
		Timestamp: tx.Timestamp,
	}
}
