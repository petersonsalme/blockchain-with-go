package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	err := execute(ctx, logger, os.Args[1:], os.Stdout)
	stop() // os.Exit skips deferred calls
	fatalIf(err, logger, os.Exit)
}

// fatalIf logs err at error level and calls exit(1) unless err is nil.
func fatalIf(err error, logger *slog.Logger, exit func(code int)) {
	if err != nil {
		logger.Error("node stopped", "error", err)
		exit(1)
	}
}

// execute runs the node, or, with -reindex, rebuilds the indexes of DATA_DIR and returns.
func execute(ctx context.Context, logger *slog.Logger, args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("node", flag.ContinueOnError)
	reindex := flags.Bool("reindex", false, "rebuild the indexes of DATA_DIR/chain.db and exit")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *reindex {
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
		return fmt.Errorf("%s: -reindex needs a DATA_DIR; the in-memory repository has no indexes to rebuild", dataDirEnvVar)
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
	return chain.NewService(repo, engine, chain.WithGenesisAllocations(outs))
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
func openRepository(dataDir string) (storage.IndexedRepository, func() error, error) {
	if strings.TrimSpace(dataDir) == "" {
		return memory.New(), func() error { return nil }, nil
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, nil, err
	}
	repo, err := bolt.Open(filepath.Join(dataDir, "chain.db"))
	if err != nil {
		return nil, nil, err
	}
	return repo, repo.Close, nil
}

// run wires repository, consensus, service and server, then serves until ctx is done.
func run(ctx context.Context, logger *slog.Logger) error {
	repo, closeRepo, err := openRepository(os.Getenv(dataDirEnvVar))
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

	srv, err := server.NewServer(svc, server.Config{
		MiningTimeout: server.ParseMiningTimeout(os.Getenv(server.MiningTimeoutEnvVar)),
		RateLimit:     server.ParseRateLimit(os.Getenv(server.RateLimitEnvVar)),
		Logger:        logger,
	})
	if err != nil {
		return err
	}

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
