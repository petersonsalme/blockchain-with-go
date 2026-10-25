package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/poa"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/server"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/bolt"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/memory"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
	"github.com/spf13/cobra"
)

// nodeStopped marks an error returned by the node command, so main logs it as
// "node stopped" instead of leaving it to cobra's text output.
type nodeStopped struct{ err error }

func (e nodeStopped) Error() string { return e.err.Error() }
func (e nodeStopped) Unwrap() error { return e.err }

// fatalIf logs err at error level and calls exit(1) unless err is nil.
func fatalIf(err error, logger *slog.Logger, exit func(code int)) {
	if err != nil {
		logger.Error("node stopped", "error", err)
		exit(1)
	}
}

// newNodeCmd builds `bwg node`: it runs the node, or, with --reindex, rebuilds
// the indexes of DATA_DIR and returns. Configuration comes from the environment
// and .env, never from flags.
func newNodeCmd(logger *slog.Logger) *cobra.Command {
	var (
		reindex                              bool
		snapshotExport, snapshotImport, hash string
	)
	cmd := &cobra.Command{
		Use:          "node",
		Short:        "Run the node (configured by environment and .env)",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		// main reports a node error as a JSON "node stopped" line, not as cobra text.
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var err error
			switch {
			case snapshotExport != "" || snapshotImport != "" || hash != "":
				err = runSnapshot(cmd, reindex, snapshotExport, snapshotImport, hash)
			default:
				err = runNode(cmd.Context(), logger, reindex, cmd.OutOrStdout())
			}
			if err != nil {
				return nodeStopped{err}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&reindex, "reindex", false, "rebuild the indexes of DATA_DIR/chain.db and exit")
	cmd.Flags().StringVar(&snapshotExport, "snapshot-export", "", "write the snapshot stored in DATA_DIR/chain.db to FILE and exit")
	cmd.Flags().StringVar(&snapshotImport, "snapshot-import", "", "restore an empty DATA_DIR/chain.db from the snapshot FILE and exit; needs --snapshot-hash")
	cmd.Flags().StringVar(&hash, "snapshot-hash", "", "hash the --snapshot-import file must carry (hex)")
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return nodeStopped{err} })
	return cmd
}

// runSnapshot runs --snapshot-export or --snapshot-import after loading .env when present.
// The flags exclude each other and --reindex; none of them opens a port.
func runSnapshot(cmd *cobra.Command, reindex bool, export, imp, hash string) error {
	if (export != "") == (imp != "") || reindex || (hash != "" && imp == "") {
		return errors.New("use exactly one of --snapshot-export or --snapshot-import (with --snapshot-hash), and not --reindex")
	}
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if export != "" {
		return exportSnapshot(export, cmd.OutOrStdout())
	}
	return importSnapshot(imp, hash, cmd.OutOrStdout())
}

// runNode runs the node, or, with reindex, rebuilds the indexes of DATA_DIR and returns.
func runNode(ctx context.Context, logger *slog.Logger, reindex bool, stdout io.Writer) error {
	if reindex {
		return reindexData(stdout)
	}
	return bootstrap(ctx, logger)
}

// reindexData loads .env when present, then rebuilds the indexes of DATA_DIR/chain.db
// and prints the tip height. It never creates DATA_DIR and never opens a port.
func reindexData(stdout io.Writer) error {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	dataDir := os.Getenv(dataDirEnvVar)
	if strings.TrimSpace(dataDir) == "" {
		return fmt.Errorf("%s: --reindex needs a DATA_DIR; the in-memory repository has no indexes to rebuild", dataDirEnvVar)
	}
	repo, err := bolt.Open(filepath.Join(dataDir, "chain.db"))
	if err != nil {
		return err
	}
	defer func() { _ = repo.Close() }()

	if err := repo.Reindex(); err != nil {
		return err
	}
	tip, err := repo.Last()
	if errors.Is(err, storage.ErrEmpty) {
		_, err = fmt.Fprintln(stdout, "reindexed: empty chain")
		return err
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "reindexed: tip height %d\n", tip.Index)
	return err
}

// bootstrap loads .env and runs the node until ctx is done.
func bootstrap(ctx context.Context, logger *slog.Logger) error {
	if err := godotenv.Load(); err != nil {
		return err
	}
	return run(ctx, logger)
}

// shutdownContext bounds the graceful shutdown to 10 seconds.
func shutdownContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// Consensus selection: CONSENSUS is "pow" (default) or "poa"; the POA_* variables configure poa.
const (
	consensusEnvVar           = "CONSENSUS"
	poaValidatorsEnvVar       = "POA_VALIDATORS"
	poaKeystoreEnvVar         = "POA_KEYSTORE"
	poaKeystorePasswordEnvVar = "POA_KEYSTORE_PASSWORD"
)

// newConsensus picks the consensus engine from CONSENSUS. Absent, empty and
// "pow" select Proof of Work; "poa" selects Proof of Authority; anything else fails.
func newConsensus() (consensus.Consensus, error) {
	switch v := os.Getenv(consensusEnvVar); v {
	case "", "pow":
		return pow.NewProofOfWorkFromEnv(), nil
	case "poa":
		return newProofOfAuthority()
	default:
		return nil, fmt.Errorf("%s: unknown value %q: want pow or poa", consensusEnvVar, v)
	}
}

// newProofOfAuthority builds poa from POA_VALIDATORS and the POA_KEYSTORE signer.
func newProofOfAuthority() (consensus.Consensus, error) {
	validators, err := parseValidators(os.Getenv(poaValidatorsEnvVar))
	if err != nil {
		return nil, err
	}

	path := os.Getenv(poaKeystoreEnvVar)
	signer, err := wallet.Load(path, os.Getenv(poaKeystorePasswordEnvVar))
	if errors.Is(err, wallet.ErrWrongPassword) || errors.Is(err, wallet.ErrEmptyPassword) {
		return nil, fmt.Errorf("%s: cannot unlock %q: %w", poaKeystorePasswordEnvVar, path, err)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: cannot load %q: %w", poaKeystoreEnvVar, path, err)
	}

	engine, err := poa.New(validators, signer)
	if err != nil {
		return nil, fmt.Errorf("%s: key %s is not in %s: %w", poaKeystoreEnvVar, hex.EncodeToString(signer.PublicKey()), poaValidatorsEnvVar, err)
	}
	return engine, nil
}

// parseValidators parses comma-separated hex Ed25519 public keys. The set must
// be non-empty and every item well formed and unique; any bad item fails the list.
func parseValidators(raw string) ([]ed25519.PublicKey, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("%s: at least one validator public key is required", poaValidatorsEnvVar)
	}
	var keys []ed25519.PublicKey
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		key, err := hex.DecodeString(item)
		if err != nil {
			return nil, fmt.Errorf("%s: invalid item %q: not hex", poaValidatorsEnvVar, item)
		}
		if len(key) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("%s: invalid item %q: want %d bytes", poaValidatorsEnvVar, item, ed25519.PublicKeySize)
		}
		for _, seen := range keys {
			if bytes.Equal(seen, key) {
				return nil, fmt.Errorf("%s: duplicate item %q", poaValidatorsEnvVar, item)
			}
		}
		keys = append(keys, key)
	}
	return keys, nil
}

// genesisAllocationsEnvVar lists the genesis outputs as address:amount[,address:amount...].
const genesisAllocationsEnvVar = "GENESIS_ALLOCATIONS"

// newService builds the chain service, funding the genesis block of an empty
// repository from GENESIS_ALLOCATIONS.
func newService(repo storage.IndexedRepository, engine consensus.Consensus) (*chain.Service, error) {
	outs, err := parseGenesisAllocations(os.Getenv(genesisAllocationsEnvVar))
	if err != nil {
		return nil, err
	}
	opts := []chain.Option{chain.WithGenesisAllocations(outs)}
	miner, err := parseMinerAddress(os.Getenv(minerAddressEnvVar))
	if err != nil {
		return nil, err
	}
	if miner != "" {
		opts = append(opts, chain.WithMinerAddress(miner))
	}
	return chain.NewService(repo, engine, opts...)
}

// minerAddressEnvVar holds the address that receives the coinbase of every mined block.
const minerAddressEnvVar = "MINER_ADDRESS"

// parseMinerAddress trims raw and validates it as a wallet address. A blank
// value means no coinbase and yields "".
func parseMinerAddress(raw string) (string, error) {
	addr := strings.TrimSpace(raw)
	if addr == "" {
		return "", nil
	}
	if err := wallet.ValidateAddress(addr); err != nil {
		return "", fmt.Errorf("%s: invalid address %q: %w", minerAddressEnvVar, addr, err)
	}
	return addr, nil
}

// parseGenesisAllocations parses address:amount items separated by commas. A
// blank value yields no outputs; any invalid item fails the whole list.
func parseGenesisAllocations(raw string) ([]domain.TxOut, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var outs []domain.TxOut
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		addr, value, ok := strings.Cut(item, ":")
		if !ok {
			return nil, fmt.Errorf("%s: invalid item %q: want address:amount", genesisAllocationsEnvVar, item)
		}
		addr, value = strings.TrimSpace(addr), strings.TrimSpace(value)
		if err := wallet.ValidateAddress(addr); err != nil {
			return nil, fmt.Errorf("%s: invalid item %q: %w", genesisAllocationsEnvVar, item, err)
		}
		amount, err := strconv.ParseUint(value, 10, 64)
		if err != nil || amount == 0 {
			return nil, fmt.Errorf("%s: invalid item %q: amount must be an integer in [1, 2^64-1]", genesisAllocationsEnvVar, item)
		}
		outs = append(outs, domain.TxOut{Amount: amount, Address: addr})
	}
	return outs, nil
}

// dataDirEnvVar holds the optional directory where chain.db is stored.
const dataDirEnvVar = "DATA_DIR"

// openRepository returns an in-memory repository when dataDir is empty, or opens
// a bbolt repository at dataDir/chain.db, creating dataDir with 0o700 if needed.
// Either one snapshots and prunes as p says.
func openRepository(dataDir string, p storage.Pruning) (storage.IndexedRepository, func() error, error) {
	if strings.TrimSpace(dataDir) == "" {
		repo, err := memory.NewPruned(p)
		if err != nil {
			return nil, nil, err
		}
		return repo, func() error { return nil }, nil
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, nil, err
	}
	repo, err := bolt.OpenPruned(filepath.Join(dataDir, "chain.db"), p)
	if err != nil {
		return nil, nil, err
	}
	return repo, repo.Close, nil
}

// run wires repository, consensus, service and server, then serves until ctx is done.
func run(ctx context.Context, logger *slog.Logger) error {
	pruning, err := parsePruning()
	if err != nil {
		return err
	}
	repo, closeRepo, err := openRepository(os.Getenv(dataDirEnvVar), pruning)
	if err != nil {
		return err
	}
	defer func() { _ = closeRepo() }()

	engine, err := newConsensus()
	if err != nil {
		return err
	}

	svc, err := newService(repo, engine)
	if err != nil {
		return err
	}

	peers, err := newP2PNode(svc, os.Getenv(dataDirEnvVar), logger)
	if err != nil {
		return err
	}

	srv, err := server.NewServer(svc, server.Config{
		P2P:           peers,
		MiningTimeout: server.ParseMiningTimeout(os.Getenv(server.MiningTimeoutEnvVar)),
		RateLimit:     server.ParseRateLimit(os.Getenv(server.RateLimitEnvVar)),
		Logger:        logger,
	})
	if err != nil {
		return err
	}

	stopDialer := startDialer(ctx, peers)
	defer stopDialer()

	addr := ":" + os.Getenv("PORT")
	httpSrv := srv.HTTPServer(ctx, addr)

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", addr)
		errCh <- httpSrv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := shutdownContext()
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
