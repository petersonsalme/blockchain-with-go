package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/indexer"
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
	t.Setenv(indexerPortEnvVar, "0")

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

	t.Setenv(indexerPortEnvVar, "invalid-port")

	err = runCmd(t.Context(), "indexer", "run", "--node", node)
	if err == nil {
		t.Fatal("invalid port accepted")
	}
}

func freeTCPPort(t *testing.T) string {
	t.Helper()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		_ = ln.Close()

		t.Fatal(err)
	}

	_ = ln.Close()

	return port
}

func getIndexerJSON[T any](t *testing.T, url string) (int, T) {
	t.Helper()

	var out T

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, out
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusOK {
		decErr := json.NewDecoder(resp.Body).Decode(&out)
		if decErr != nil {
			t.Fatalf("decode %s: %v", url, decErr)
		}
	}

	return resp.StatusCode, out
}

func mustGetIndexerJSON[T any](t *testing.T, url string) T {
	t.Helper()

	status, out := getIndexerJSON[T](t, url)
	if status != http.StatusOK {
		t.Fatalf("GET %s status %d, want 200", url, status)
	}

	return out
}

func assertSupplyEndpoints(t *testing.T, baseURL, tipHash string) indexer.SupplyRow {
	t.Helper()

	supplyTip := mustGetIndexerJSON[indexer.SupplyRow](t, baseURL+"/supply")
	if supplyTip.Height != 3 || supplyTip.Hash != tipHash || supplyTip.Total != "105" {
		t.Fatalf("unexpected /supply tip: %+v (hash=%s)", supplyTip, tipHash)
	}

	supply0 := mustGetIndexerJSON[indexer.SupplyRow](t, baseURL+"/supply?height=0")
	if supply0.Height != 0 || supply0.Hash == "" || supply0.Hash == tipHash {
		t.Fatalf("unexpected /supply?height=0 header: %+v", supply0)
	}

	if supply0.Total != "105" || supply0.Created != "105" || supply0.Spent != "0" || supply0.Coinbase != "105" {
		t.Fatalf("unexpected /supply?height=0 amounts: %+v", supply0)
	}

	return supplyTip
}

func assertRichestAndStats(t *testing.T, baseURL, tipHash, total, addr1, addr2 string) {
	t.Helper()

	richest := mustGetIndexerJSON[indexer.RichestSnapshot](t, baseURL+"/richest?limit=10")
	if richest.Height != 3 || richest.Hash != tipHash || richest.Total != total {
		t.Fatalf("unexpected /richest header: %+v", richest)
	}

	balances := make(map[string]string, len(richest.Addresses))
	for _, row := range richest.Addresses {
		balances[row.Address] = row.Balance
	}

	if balances[addr1] != "100" || balances[addr2] != "5" {
		t.Fatalf("unexpected richest addresses: %+v", richest.Addresses)
	}

	stats := mustGetIndexerJSON[indexer.RangeStats](t, baseURL+"/stats?from=0&to=3")
	if stats.From != 0 || stats.To != 3 || stats.Blocks != 4 || stats.Txs != 1 {
		t.Fatalf("unexpected /stats counts: %+v", stats)
	}

	if stats.Created != "105" || stats.Spent != "0" || stats.Coinbase != "105" {
		t.Fatalf("unexpected /stats amounts: %+v", stats)
	}
}

func TestIndexerRunServesReadAPI(t *testing.T) {
	indexerEnv(t)

	port := freeTCPPort(t)
	t.Setenv(indexerPortEnvVar, port)

	addr1 := newAddress(t)
	addr2 := newAddress(t)
	node := startNode(t, domain.TxOut{Amount: 100, Address: addr1}, domain.TxOut{Amount: 5, Address: addr2})

	err := runCmd(t.Context(), "miner", "--count", "3", "--node", node)
	if err != nil {
		t.Fatal(err)
	}

	ctx, stop := context.WithCancel(t.Context())
	done := make(chan error, 1)

	go func() { done <- runCmd(ctx, "indexer", "run", "--node", node) }()

	baseURL := "http://127.0.0.1:" + port

	type healthPayload struct {
		Height int    `json:"height"`
		Hash   string `json:"hash"`
	}

	var health healthPayload

	waitIndexer(t, "API /health at height 3", func() bool {
		status, got := getIndexerJSON[healthPayload](t, baseURL+"/health")
		if status != http.StatusOK || got.Height != 3 {
			return false
		}

		health = got

		return true
	})

	if health.Hash == "" {
		t.Fatal("health hash is empty")
	}

	supplyTip := assertSupplyEndpoints(t, baseURL, health.Hash)
	assertRichestAndStats(t, baseURL, health.Hash, supplyTip.Total, addr1, addr2)

	stop()

	err = <-done
	if err != nil {
		t.Fatalf("run stopped with %v", err)
	}
}
