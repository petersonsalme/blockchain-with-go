package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

type fakeMinerNode struct {
	url   string
	calls *atomic.Int32
}

func newFakeMinerNode(t *testing.T, script []int, errorBody string, onCall func(n int)) *fakeMinerNode {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/mine" {
			http.NotFound(w, r)
			return
		}
		n := int(calls.Add(1))
		if onCall != nil {
			onCall(n)
		}
		status := http.StatusCreated
		if n <= len(script) {
			status = script[n-1]
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status == http.StatusCreated {
			body := fmt.Sprintf(`{"Index":%d,"Hash":"h%d","PrevHash":"p","Timestamp":"2026-10-24T12:00:00Z","Transactions":null,"Nonce":0,"Difficulty":1,"Validator":"","Signature":""}`, n, n)
			_, _ = w.Write([]byte(body))
		} else {
			if errorBody != "" {
				_, _ = w.Write([]byte(errorBody))
			} else {
				_, _ = w.Write([]byte(`{"error":"error"}`))
			}
		}
	}))
	t.Cleanup(srv.Close)
	return &fakeMinerNode{
		url:   srv.URL,
		calls: &calls,
	}
}

func TestMinerStopsAfterCount(t *testing.T) {
	node := newFakeMinerNode(t, []int{http.StatusCreated, http.StatusCreated}, "", nil)

	out, err := runCLI(t, context.Background(), "miner", "--node", node.url, "--count", "2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := int(node.calls.Load()); got != 2 {
		t.Fatalf("expected 2 calls, got %d", got)
	}
	want := "block 1 h1 txs=0\nblock 2 h2 txs=0\n"
	if out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}

func TestMinerRetriesOnConflict(t *testing.T) {
	node := newFakeMinerNode(t, []int{http.StatusConflict, http.StatusCreated}, `{"error":"conflict"}`, nil)

	out, err := runCLI(t, context.Background(), "miner", "--node", node.url, "--count", "1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := int(node.calls.Load()); got != 2 {
		t.Fatalf("expected 2 calls, got %d", got)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 || lines[0] == "" {
		t.Fatalf("expected 1 line in output, got %q", out)
	}
}

func TestMinerReportsRejection(t *testing.T) {
	node := newFakeMinerNode(t, []int{http.StatusUnprocessableEntity}, `{"error":"invalid block"}`, nil)

	_, err := runCLI(t, context.Background(), "miner", "--node", node.url, "--count", "1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "422") || !strings.Contains(errMsg, "invalid block") {
		t.Fatalf("expected error containing 422 and invalid block, got %q", errMsg)
	}
}

func TestMinerStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var node *fakeMinerNode
	node = newFakeMinerNode(t, nil, "", func(n int) {
		if n == 3 {
			cancel()
		}
	})

	_, err := runCLI(t, ctx, "miner", "--node", node.url, "--count", "0")
	if err != nil {
		t.Fatalf("expected nil error on cancel, got: %v", err)
	}
	if got := int(node.calls.Load()); got != 3 {
		t.Fatalf("expected exactly 3 calls, got %d", got)
	}
}

func TestMinerRejectsNegativeInterval(t *testing.T) {
	node := newFakeMinerNode(t, nil, "", nil)

	_, err := runCLI(t, context.Background(), "miner", "--node", node.url, "--interval", "-1s")
	if err == nil {
		t.Fatal("expected error for negative interval, got nil")
	}
	if got := int(node.calls.Load()); got != 0 {
		t.Fatalf("expected 0 calls, got %d", got)
	}
}

func TestSendThenMineMovesFunds(t *testing.T) {
	w, keyPath := savedWallet(t)
	wTo, err := wallet.New()
	if err != nil {
		t.Fatal(err)
	}
	to := wTo.Address()

	nodeURL := startNode(t, domain.TxOut{Amount: 100, Address: w.Address()})

	_, err = runCLI(t, context.Background(), "wallet", "send", "--node", nodeURL, "--keystore", keyPath, "--to", to, "--amount", "30", "--fee", "2")
	if err != nil {
		t.Fatalf("wallet send: %v", err)
	}

	out, err := runCLI(t, context.Background(), "miner", "--node", nodeURL, "--count", "1")
	if err != nil {
		t.Fatalf("miner: %v", err)
	}
	matched, err := regexp.MatchString(`^block 1 [0-9a-f]{64} txs=1\n$`, out)
	if err != nil {
		t.Fatalf("regex match: %v", err)
	}
	if !matched {
		t.Fatalf("miner output %q did not match expected regex", out)
	}

	balTo, err := runCLI(t, context.Background(), "wallet", "balance", to, "--node", nodeURL)
	if err != nil {
		t.Fatalf("wallet balance to: %v", err)
	}
	if balTo != "30\n" {
		t.Fatalf("wallet balance to: got %q, want %q", balTo, "30\n")
	}

	balFrom, err := runCLI(t, context.Background(), "wallet", "balance", w.Address(), "--node", nodeURL)
	if err != nil {
		t.Fatalf("wallet balance from: %v", err)
	}
	if balFrom != "68\n" {
		t.Fatalf("wallet balance from: got %q, want %q", balFrom, "68\n")
	}
}
