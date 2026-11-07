package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	bbolt "go.etcd.io/bbolt"

	"github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/bolt"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

// writeMinedChain mines two blocks onto dir/chain.db through the service, as a
// node would, and returns the tip height.
func writeMinedChain(t *testing.T, dir string) int {
	t.Helper()

	w, err := wallet.New()
	if err != nil {
		t.Fatal(err)
	}

	repo, err := bolt.Open(filepath.Join(dir, "chain.db"))
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = repo.Close() }() // test repository; close error is irrelevant

	svc, err := chain.NewService(repo, pow.New(1), chain.WithGenesisAllocations([]domain.TxOut{
		{Amount: 10, Address: w.Address()}, {Amount: 10, Address: w.Address()},
	}))
	if err != nil {
		t.Fatal(err)
	}

	genesis, err := svc.BlockByHeight(0)
	if err != nil {
		t.Fatal(err)
	}

	txid := genesis.Transactions[0].CalculateHash()

	for i := range uint32(2) {
		tx := domain.Transaction{
			Inputs:    []domain.TxIn{{PrevTxID: txid, OutIndex: i}},
			Outputs:   []domain.TxOut{{Amount: 9, Address: w.Address()}},
			Timestamp: time.Now(),
		}
		tx.ID = tx.CalculateHash()

		err = domain.SignInput(&tx, 0, w)
		if err != nil {
			t.Fatal(err)
		}

		_, err = svc.Mine(context.Background(), []domain.Transaction{tx})
		if err != nil {
			t.Fatal(err)
		}
	}

	return 2
}

// tamperAmount raises the first output amount of the first transaction of the
// stored block at height h, directly in the bbolt file.
func tamperAmount(t *testing.T, dir string, h int) {
	t.Helper()

	db, err := bbolt.Open(filepath.Join(dir, "chain.db"), 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = db.Close() }() // test database; close error is irrelevant

	err = db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte("blocks"))

		return bucket.ForEach(func(k, v []byte) error {
			var b domain.Block

			err := json.Unmarshal(v, &b)
			if err != nil || b.Index != h {
				return err
			}

			b.Transactions[0].Outputs[0].Amount++

			data, err := json.Marshal(&b)
			if err != nil {
				return err
			}

			return bucket.Put(k, data)
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}

// lockedBuffer is a log sink that the node goroutine and the test can share.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.buf.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.buf.String()
}

// verifyEnv prepares a cwd with an empty .env, DATA_DIR=dir and a free PORT.
func verifyEnv(t *testing.T, dir string) string {
	t.Helper()

	cwd := t.TempDir()

	err := os.WriteFile(filepath.Join(cwd, ".env"), nil, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	t.Chdir(cwd)

	port := freePort(t)
	t.Setenv("PORT", port)
	t.Setenv("POW_DIFFICULTY", "1")
	t.Setenv(dataDirEnvVar, dir)

	return port
}

func portOpen(port string) bool {
	conn, err := (&net.Dialer{Timeout: 200 * time.Millisecond}).DialContext(context.Background(), "tcp", "127.0.0.1:"+port)
	if err != nil {
		return false
	}

	_ = conn.Close() // probe connection; close error is irrelevant

	return true
}

// serveUntilHealthy runs `bwg node args...`, waits for GET /health to answer 200
// and stops the node. It returns the log lines.
func serveUntilHealthy(t *testing.T, port string, args ...string) string {
	t.Helper()

	logs := &lockedBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		done <- execute(ctx, slog.New(slog.NewJSONHandler(logs, nil)), append([]string{"node"}, args...), &bytes.Buffer{})
	}()

	healthy := false

	for range 100 {
		resp, err := getURL("http://127.0.0.1:" + port + "/health")
		if err == nil {
			_ = resp.Body.Close() // response body of a test request; close error is irrelevant
			healthy = resp.StatusCode == http.StatusOK
		}

		if healthy {
			break
		}

		time.Sleep(50 * time.Millisecond)
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("node returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("node did not stop")
	}

	if !healthy {
		t.Fatalf("GET /health never answered 200; logs: %s", logs.String())
	}

	return logs.String()
}

func TestVerifyServesAnUntouchedChain(t *testing.T) {
	dir := t.TempDir()
	tip := writeMinedChain(t, dir)
	port := verifyEnv(t, dir)

	logs := serveUntilHealthy(t, port, "--verify")

	if !strings.Contains(logs, `"msg":"verified stored chain"`) || !strings.Contains(logs, fmt.Sprintf(`"height":%d`, tip)) {
		t.Fatalf("logs = %s, want \"verified stored chain\" with height %d", logs, tip)
	}
}

func TestWithoutVerifyATamperedChainStillStarts(t *testing.T) {
	dir := t.TempDir()
	writeMinedChain(t, dir)
	tamperAmount(t, dir, 1)
	port := verifyEnv(t, dir)

	logs := serveUntilHealthy(t, port)

	if strings.Contains(logs, "verified stored chain") {
		t.Fatalf("logs = %s, the default boot must not verify", logs)
	}
}

func TestVerifyRefusesATamperedChainAndOpensNoPort(t *testing.T) {
	dir := t.TempDir()
	writeMinedChain(t, dir)
	tamperAmount(t, dir, 1)
	port := verifyEnv(t, dir)
	p2pPort := freePort(t)
	t.Setenv("P2P_LISTEN_ADDR", "127.0.0.1:"+p2pPort)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := execute(ctx, discardLogger(), []string{"node", "--verify"}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("--verify accepted a tampered chain, want error")
	}

	for _, want := range []string{"stored block 1", "merkle_root_mismatch"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to contain %q", err, want)
		}
	}

	if portOpen(port) || portOpen(p2pPort) {
		t.Fatal("a listener is open after a failed verification")
	}
}

func TestVerifyExcludesReindexAndSnapshotFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--verify", "--reindex"},
		{"--verify", "--snapshot-export", "x.snap"},
		{"--verify", "--snapshot-import", "x.snap", "--snapshot-hash", "ab"},
		{"--verify", "--snapshot-hash", "ab"},
	} {
		dir := filepath.Join(t.TempDir(), "data")
		verifyEnv(t, dir)

		err := execute(context.Background(), discardLogger(), append([]string{"node"}, args...), &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "--verify cannot be combined") {
			t.Fatalf("%v: error = %v, want a usage error", args, err)
		}

		_, statErr := os.Stat(dir)
		if !os.IsNotExist(statErr) {
			t.Fatalf("%v created %s, want nothing touched", args, dir)
		}
	}
}
