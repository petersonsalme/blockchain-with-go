package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/indexer/postgres/pgtest"
)

func TestMain(m *testing.M) {
	code := m.Run()

	pgtest.Stop()
	os.Exit(code)
}

func quietLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// indexerEnv points the indexer at a new database and returns its URL.
func indexerEnv(t *testing.T) string {
	t.Helper()

	url := pgtest.NewDatabase(t)
	t.Setenv(indexerDatabaseURLEnvVar, url)
	t.Setenv(indexerPollIntervalEnvVar, "20ms")

	return url
}

func blockRows(t *testing.T, url string) int {
	t.Helper()

	conn, err := pgx.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()

	var n int

	err = conn.QueryRow(context.Background(), "SELECT count(*) FROM blocks").Scan(&n)
	if err != nil { // the schema may not exist yet
		return 0
	}

	return n
}

func waitIndexer(t *testing.T, what string, ok func() bool) {
	t.Helper()

	deadline := time.Now().Add(20 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(10 * time.Millisecond)
	}
}

func runCmd(ctx context.Context, args ...string) error {
	var out bytes.Buffer

	return execute(ctx, quietLogger(), args, &out)
}

func TestIndexerRunFollowsTheNodeLocksAndResyncRebuilds(t *testing.T) {
	url := indexerEnv(t)
	node := startNode(t, domain.TxOut{Amount: 100, Address: newAddress(t)}, domain.TxOut{Amount: 5, Address: newAddress(t)})

	err := runCmd(t.Context(), "miner", "--count", "3", "--node", node)
	if err != nil {
		t.Fatal(err)
	}

	ctx, stop := context.WithCancel(t.Context())
	done := make(chan error, 1)

	go func() { done <- runCmd(ctx, "indexer", "run", "--node", node) }()

	waitIndexer(t, "4 blocks indexed", func() bool { return blockRows(t, url) == 4 })

	// A second run on the same database refuses while the first holds the lock.
	err = runCmd(t.Context(), "indexer", "run", "--node", node)
	if err == nil || !strings.Contains(err.Error(), "lock") {
		t.Fatalf("second run: %v, want an error naming the lock", err)
	}

	err = runCmd(t.Context(), "indexer", "resync", "--node", node)
	if err == nil || !strings.Contains(err.Error(), "lock") {
		t.Fatalf("resync while running: %v, want an error naming the lock", err)
	}

	// New blocks reach the database while the indexer runs.
	err = runCmd(t.Context(), "miner", "--count", "2", "--node", node)
	if err != nil {
		t.Fatal(err)
	}

	waitIndexer(t, "6 blocks indexed", func() bool { return blockRows(t, url) == 6 })

	stop()

	err = <-done
	if err != nil {
		t.Fatalf("run stopped with %v", err)
	}

	pgtest.CheckInvariants(t, url)
	before := pgtest.Dump(t, url)

	err = runCmd(t.Context(), "indexer", "resync", "--node", node)
	if err != nil {
		t.Fatal(err)
	}

	if got := pgtest.Dump(t, url); got != before {
		t.Fatalf("resync changed the data:\n%s\nwant\n%s", got, before)
	}
}

func TestIndexerStopsOnAnotherChain(t *testing.T) {
	url := indexerEnv(t)
	a := startNode(t, domain.TxOut{Amount: 1, Address: newAddress(t)})
	b := startNode(t, domain.TxOut{Amount: 2, Address: newAddress(t)})

	for _, n := range []string{a, b} {
		err := runCmd(t.Context(), "miner", "--count", "2", "--node", n)
		if err != nil {
			t.Fatal(err)
		}
	}

	err := runCmd(t.Context(), "indexer", "resync", "--node", a)
	if err != nil {
		t.Fatal(err)
	}

	before := pgtest.Dump(t, url)

	err = runCmd(t.Context(), "indexer", "run", "--node", b)
	if err == nil || err.Error() != "different chain; run bwg indexer resync" {
		t.Fatalf("err %v", err)
	}

	if pgtest.Dump(t, url) != before {
		t.Fatal("wrote although the chain differs")
	}

	// resync adopts the other chain.
	err = runCmd(t.Context(), "indexer", "resync", "--node", b)
	if err != nil {
		t.Fatal(err)
	}

	if blockRows(t, url) != 3 {
		t.Fatalf("%d rows after resync, want 3", blockRows(t, url))
	}
}

func TestIndexerConfigErrors(t *testing.T) {
	node := startNode(t)
	t.Setenv(indexerDatabaseURLEnvVar, "")

	err := runCmd(t.Context(), "indexer", "run", "--node", node)
	if err == nil || !strings.Contains(err.Error(), indexerDatabaseURLEnvVar) {
		t.Fatalf("missing URL: %v", err)
	}

	url := pgtest.NewDatabase(t)
	t.Setenv(indexerDatabaseURLEnvVar, url)

	for _, bad := range []string{"soon", "0s", "-1s"} {
		t.Setenv(indexerPollIntervalEnvVar, bad)

		err := runCmd(t.Context(), "indexer", "run", "--node", node)
		if err == nil || !strings.Contains(err.Error(), indexerPollIntervalEnvVar) {
			t.Fatalf("interval %q: %v", bad, err)
		}
	}

	t.Setenv(indexerPollIntervalEnvVar, "")
	t.Setenv(indexerDatabaseURLEnvVar, "postgres://nobody@127.0.0.1:1/x?connect_timeout=1")

	err = runCmd(t.Context(), "indexer", "run", "--node", node)
	if err == nil {
		t.Fatal("unreachable database accepted")
	}

	t.Setenv(indexerDatabaseURLEnvVar, url)

	err = runCmd(t.Context(), "indexer", "resync", "--node", "://bad")
	if err == nil {
		t.Fatal("bad node URL accepted")
	}

	err = runCmd(t.Context(), "indexer", "resync", "--node", "http://127.0.0.1:1")
	if err == nil {
		t.Fatal("unreachable node accepted by resync")
	}
}
