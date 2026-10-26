package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/bolt"
)

// Snapshot and pruning configuration.
const (
	snapshotIntervalEnvVar = "SNAPSHOT_INTERVAL"
	pruneKeepBlocksEnvVar  = "PRUNE_KEEP_BLOCKS"

	defaultSnapshotInterval = 1000
	// minPruneKeepBlocks is the smallest PRUNE_KEEP_BLOCKS that leaves room for a reorg.
	minPruneKeepBlocks = 100
)

// parsePruning reads SNAPSHOT_INTERVAL (default 1000, 0 = off) and
// PRUNE_KEEP_BLOCKS (default 0 = keep every body). Both must be non-negative
// integers, PRUNE_KEEP_BLOCKS is either 0 or at least 100, and keeping blocks
// needs a snapshot interval.
func parsePruning() (storage.Pruning, error) {
	interval, err := nonNegativeInt(snapshotIntervalEnvVar, defaultSnapshotInterval)
	if err != nil {
		return storage.Pruning{}, err
	}

	keep, err := nonNegativeInt(pruneKeepBlocksEnvVar, 0)
	if err != nil {
		return storage.Pruning{}, err
	}

	if keep > 0 && keep < minPruneKeepBlocks {
		return storage.Pruning{}, fmt.Errorf("%s: %d is too small: want 0 or at least %d", pruneKeepBlocksEnvVar, keep, minPruneKeepBlocks)
	}

	if keep > 0 && interval == 0 {
		return storage.Pruning{}, fmt.Errorf("%s: pruning needs %s above 0", pruneKeepBlocksEnvVar, snapshotIntervalEnvVar)
	}

	return storage.Pruning{SnapshotInterval: interval, KeepBlocks: keep}, nil
}

// nonNegativeInt reads the environment variable name as a non-negative integer; unset or empty is fallback.
func nonNegativeInt(name string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}

	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s: invalid value %q: want a non-negative integer", name, raw)
	}

	return n, nil
}

// snapshotDB returns DATA_DIR/chain.db, or an error naming DATA_DIR when it is unset.
func snapshotDB(what string) (string, error) {
	dataDir := os.Getenv(dataDirEnvVar)
	if strings.TrimSpace(dataDir) == "" {
		return "", fmt.Errorf("%s: %s needs a DATA_DIR", dataDirEnvVar, what)
	}

	return filepath.Join(dataDir, "chain.db"), nil
}

// exportSnapshot writes the snapshot stored in DATA_DIR/chain.db, with the
// headers 0..S, to file and prints its height and hash. It never creates
// DATA_DIR or chain.db and never opens a port.
func exportSnapshot(file string, stdout io.Writer) error {
	path, err := snapshotDB("--snapshot-export")
	if err != nil {
		return err
	}

	_, err = os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s: %w", dataDirEnvVar, err)
	}

	repo, err := bolt.Open(path)
	if err != nil {
		return err
	}

	defer func() { _ = repo.Close() }()

	snap, err := repo.Snapshot()
	if errors.Is(err, storage.ErrNotFound) {
		return fmt.Errorf("no snapshot stored in %s yet (see %s)", path, snapshotIntervalEnvVar)
	}

	if err != nil {
		return err
	}

	blocks, err := repo.Range(0, snap.Height+1)
	if err != nil {
		return err
	}

	headers := make([]*domain.Block, len(blocks))
	for i, b := range blocks {
		h := *b
		h.Transactions = []domain.Transaction{}
		headers[i] = &h
	}

	var buf bytes.Buffer

	err = storage.WriteSnapshotFile(&buf, headers, snap)
	if err != nil {
		return err
	}

	err = os.WriteFile(file, buf.Bytes(), 0o644)
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(stdout, "snapshot: height %d hash %s\n", snap.Height, snap.Hash)

	return err
}

// importSnapshot restores DATA_DIR/chain.db, which must be absent or empty,
// from file. It reads the file, compares its hash with wantHash, verifies every
// header against the consensus engine of CONSENSUS and only then restores, so a
// failure writes nothing. It never opens a port.
func importSnapshot(file, wantHash string, stdout io.Writer) error {
	path, err := snapshotDB("--snapshot-import")
	if err != nil {
		return err
	}

	if strings.TrimSpace(wantHash) == "" {
		return errors.New("--snapshot-hash is required with --snapshot-import")
	}

	pruning, err := parsePruning()
	if err != nil {
		return err
	}

	f, err := os.Open(file)
	if err != nil {
		return err
	}

	headers, snap, err := storage.ReadSnapshotFile(f)
	_ = f.Close() // read-only

	if err != nil {
		return err
	}

	if !strings.EqualFold(snap.Hash, strings.TrimSpace(wantHash)) {
		return fmt.Errorf("snapshot hash mismatch: file has %s, expected %s", snap.Hash, strings.TrimSpace(wantHash))
	}

	engine, err := newConsensus()
	if err != nil {
		return err
	}

	for i := 1; i < len(headers); i++ {
		err := engine.Verify(headers[:i], headers[i])
		if err != nil {
			return fmt.Errorf("header %d: %w", i, err)
		}
	}

	repo, closeRepo, err := openRepository(filepath.Dir(path), pruning)
	if err != nil {
		return err
	}
	defer func() { _ = closeRepo() }()

	err = repo.Restore(headers, snap.UTXOs)
	if err != nil {
		return fmt.Errorf("restore into %s: %w", path, err)
	}

	_, err = fmt.Fprintf(stdout, "restored: height %d\n", snap.Height)

	return err
}
