package main

import (
	"context"
	"errors"
	"fmt"
	"log"
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
	defer stop()

	fatalIf(bootstrap(ctx), log.Fatal)
}

// fatalIf calls fatal with err unless err is nil.
func fatalIf(err error, fatal func(v ...any)) {
	if err != nil {
		fatal(err)
	}
}

// bootstrap loads .env and runs the node until ctx is done.
func bootstrap(ctx context.Context) error {
	if err := godotenv.Load(); err != nil {
		return err
	}
	return run(ctx)
}

// shutdownContext bounds the graceful shutdown to 10 seconds.
func shutdownContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// genesisAllocationsEnvVar lists the genesis outputs as address:amount[,address:amount...].
const genesisAllocationsEnvVar = "GENESIS_ALLOCATIONS"

// newService builds the chain service, funding the genesis block of an empty
// repository from GENESIS_ALLOCATIONS.
func newService(repo storage.BlockRepository, engine consensus.Consensus) (*chain.Service, error) {
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
func openRepository(dataDir string) (storage.BlockRepository, func() error, error) {
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
func run(ctx context.Context) error {
	repo, closeRepo, err := openRepository(os.Getenv(dataDirEnvVar))
	if err != nil {
		return err
	}
	defer func() { _ = closeRepo() }()

	engine := pow.NewProofOfWorkFromEnv()

	svc, err := newService(repo, engine)
	if err != nil {
		return err
	}

	srv := server.NewServer(svc, server.Config{
		MiningTimeout: server.ParseMiningTimeout(os.Getenv(server.MiningTimeoutEnvVar)),
	})

	addr := ":" + os.Getenv("PORT")
	httpSrv := srv.HTTPServer(addr)

	errCh := make(chan error, 1)
	go func() {
		log.Println("Listening on ", addr)
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
