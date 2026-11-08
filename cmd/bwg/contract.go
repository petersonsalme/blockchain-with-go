package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

var errCodeFlags = errors.New("exactly one of --code and --code-file is required")

func newContractCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "contract", Short: "Deploy, call and inspect contracts (ADR-0037)"}
	cmd.AddCommand(newContractDeployCmd(), newContractCallCmd(), newContractShowCmd())

	return cmd
}

func addContractTxFlags(cmd *cobra.Command) {
	cmd.Flags().Uint64("gas-limit", 0, "gas limit of the execution")
	_ = cmd.MarkFlagRequired("gas-limit")
	cmd.Flags().StringSlice("args", nil, "uint64 arguments, comma separated or repeated")
	cmd.Flags().Uint64("fee", 0, "transaction fee")
	cmd.Flags().String("keystore", envOr(walletKeystoreEnvVar, defaultKeystore), "path to keystore file")
	addNodeFlag(cmd)
}

func newContractDeployCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy a contract; prints the contract ID (the transaction ID)",
		Args:  cobra.NoArgs,
		RunE:  runContractDeploy,
	}
	cmd.Flags().String("code", "", "contract bytecode as hex")
	cmd.Flags().String("code-file", "", "file holding the contract bytecode as hex text")
	addContractTxFlags(cmd)

	return cmd
}

func newContractCallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "call",
		Short: "Call a deployed contract; prints the transaction ID",
		Args:  cobra.NoArgs,
		RunE:  runContractCall,
	}
	cmd.Flags().String("target", "", "contract ID")
	_ = cmd.MarkFlagRequired("target")
	addContractTxFlags(cmd)

	return cmd
}

func newContractShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show ID",
		Short: "Print the code and storage of a deployed contract",
		Args:  cobra.ExactArgs(1),
		RunE:  runContractShow,
	}
	addNodeFlag(cmd)

	return cmd
}

// contractCode returns the code of deploy, lower-case hex, from --code or --code-file.
func contractCode(cmd *cobra.Command) (string, error) {
	flags := cmd.Flags()
	if flags.Changed("code") == flags.Changed("code-file") {
		return "", errCodeFlags
	}

	code, err := flags.GetString("code")
	if err != nil {
		return "", err
	}

	if flags.Changed("code-file") {
		path, err := flags.GetString("code-file")
		if err != nil {
			return "", err
		}

		raw, err := os.ReadFile(path) //nolint:gosec // G304: the path is the user's own --code-file argument
		if err != nil {
			return "", fmt.Errorf("--code-file: %w", err)
		}

		code = string(raw)
	}

	return strings.ToLower(strings.TrimSpace(code)), nil
}

// contractArgs parses --args as uint64 values, nil when the flag is absent.
func contractArgs(cmd *cobra.Command) ([]uint64, error) {
	raw, err := cmd.Flags().GetStringSlice("args")
	if err != nil {
		return nil, err
	}

	var args []uint64

	for _, s := range raw {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("--args: %q is not a uint64", s)
		}

		args = append(args, v)
	}

	return args, nil
}

func runContractDeploy(cmd *cobra.Command, _ []string) error {
	code, err := contractCode(cmd)
	if err != nil {
		return err
	}

	return submitContract(cmd, domain.ContractOp{Kind: domain.ContractDeploy, Code: code})
}

func runContractCall(cmd *cobra.Command, _ []string) error {
	target, err := cmd.Flags().GetString("target")
	if err != nil {
		return err
	}

	return submitContract(cmd, domain.ContractOp{Kind: domain.ContractCall, Target: target})
}

// submitContract completes op with --gas-limit and --args, then builds, signs and
// submits the transaction that carries it and prints its ID.
func submitContract(cmd *cobra.Command, op domain.ContractOp) error {
	var err error

	op.Args, err = contractArgs(cmd)
	if err != nil {
		return err
	}

	op.GasLimit, err = cmd.Flags().GetUint64("gas-limit")
	if err != nil {
		return err
	}

	fee, err := cmd.Flags().GetUint64("fee")
	if err != nil {
		return err
	}

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

	bal, err := c.GetBalanceWithResponse(cmd.Context(), w.Address())
	if err != nil {
		return err
	}

	if bal.JSON200 == nil {
		return nodeError(bal.HTTPResponse, bal.Body)
	}

	tx, err := buildContractTx(w, bal.JSON200.Utxos, op, fee, time.Now())
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

func runContractShow(cmd *cobra.Command, args []string) error {
	c, err := newNodeClient(cmd)
	if err != nil {
		return err
	}

	resp, err := c.GetContractWithResponse(cmd.Context(), args[0])
	if err != nil {
		return err
	}

	if resp.JSON200 == nil {
		return nodeError(resp.HTTPResponse, resp.Body)
	}

	_, err = cmd.OutOrStdout().Write(resp.Body)

	return err
}
