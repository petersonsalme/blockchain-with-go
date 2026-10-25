package main

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/petersonsalme/blockchain-with-go/client"
	"github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/server"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/memory"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

// startNode serves a PoW (difficulty 1) in-memory node whose genesis pays outs.
func startNode(t *testing.T, outs ...domain.TxOut) string {
	t.Helper()
	svc, err := chain.NewService(memory.New(), pow.New(1), chain.WithGenesisAllocations(outs))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := server.NewServer(svc, server.Config{})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts.URL
}

// savedWallet stores a new wallet under t.TempDir() with password "pw" and sets WALLET_PASSWORD.
func savedWallet(t *testing.T) (*wallet.Wallet, string) {
	t.Helper()
	w, err := wallet.New()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "wallet.key")
	if err := w.Save(path, "pw"); err != nil {
		t.Fatal(err)
	}
	t.Setenv(walletPasswordEnvVar, "pw")
	return w, path
}

func TestWalletSendSubmitsSignedTransaction(t *testing.T) {
	w, keyPath := savedWallet(t)
	wTo, err := wallet.New()
	if err != nil {
		t.Fatal(err)
	}
	to := wTo.Address()

	nodeURL := startNode(t, domain.TxOut{Amount: 100, Address: w.Address()})

	out, err := runCLI(t, context.Background(), "wallet", "send", "--node", nodeURL, "--keystore", keyPath, "--to", to, "--amount", "30", "--fee", "2")
	if err != nil {
		t.Fatalf("runCLI: %v", err)
	}

	txID := strings.TrimSpace(out)
	if len(txID) != 64 {
		t.Fatalf("expected 64 hex chars, got %q (len %d)", txID, len(txID))
	}
	if _, err := hex.DecodeString(txID); err != nil {
		t.Fatalf("invalid hex string %q: %v", txID, err)
	}

	c, err := client.NewClientWithResponses(nodeURL)
	if err != nil {
		t.Fatal(err)
	}
	mp, err := c.GetMempoolWithResponse(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if mp.JSON200 == nil {
		t.Fatalf("expected 200 from mempool, got %v", mp.HTTPResponse)
	}
	if len(mp.JSON200.Transactions) != 1 {
		t.Fatalf("expected 1 transaction in mempool, got %d", len(mp.JSON200.Transactions))
	}
	mTx := mp.JSON200.Transactions[0]
	if mTx.ID == nil || *mTx.ID != txID {
		t.Fatalf("expected tx ID %s, got %v", txID, mTx.ID)
	}
	if mTx.Inputs == nil || len(*mTx.Inputs) != 1 {
		t.Fatalf("expected 1 input, got %v", mTx.Inputs)
	}
	expectedPK := hex.EncodeToString(w.PublicKey())
	if (*mTx.Inputs)[0].PublicKey != expectedPK {
		t.Fatalf("expected public key %s, got %s", expectedPK, (*mTx.Inputs)[0].PublicKey)
	}
	if len(mTx.Outputs) != 2 {
		t.Fatalf("expected 2 outputs, got %d", len(mTx.Outputs))
	}
	if mTx.Outputs[0].Address != to || mTx.Outputs[0].Amount != 30 {
		t.Fatalf("unexpected output 0: %+v", mTx.Outputs[0])
	}
	if mTx.Outputs[1].Address != w.Address() || mTx.Outputs[1].Amount != 68 {
		t.Fatalf("unexpected output 1: %+v", mTx.Outputs[1])
	}
}

func TestWalletSendInsufficientFundsSubmitsNothing(t *testing.T) {
	w, keyPath := savedWallet(t)
	wTo, err := wallet.New()
	if err != nil {
		t.Fatal(err)
	}

	nodeURL := startNode(t, domain.TxOut{Amount: 10, Address: w.Address()})

	_, err = runCLI(t, context.Background(), "wallet", "send", "--node", nodeURL, "--keystore", keyPath, "--to", wTo.Address(), "--amount", "10", "--fee", "1")
	if !errors.Is(err, errInsufficientFunds) {
		t.Fatalf("expected errInsufficientFunds, got %v", err)
	}

	c, err := client.NewClientWithResponses(nodeURL)
	if err != nil {
		t.Fatal(err)
	}
	mp, err := c.GetMempoolWithResponse(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if mp.JSON200 == nil {
		t.Fatalf("expected 200 from mempool, got %v", mp.HTTPResponse)
	}
	if len(mp.JSON200.Transactions) != 0 {
		t.Fatalf("expected empty mempool, got %d transactions", len(mp.JSON200.Transactions))
	}
}

func TestWalletSendZeroAmountSubmitsNothing(t *testing.T) {
	w, keyPath := savedWallet(t)
	wTo, err := wallet.New()
	if err != nil {
		t.Fatal(err)
	}

	nodeURL := startNode(t, domain.TxOut{Amount: 100, Address: w.Address()})

	_, err = runCLI(t, context.Background(), "wallet", "send", "--node", nodeURL, "--keystore", keyPath, "--to", wTo.Address(), "--amount", "0", "--fee", "1")
	if !errors.Is(err, errZeroAmount) {
		t.Fatalf("expected errZeroAmount, got %v", err)
	}

	c, err := client.NewClientWithResponses(nodeURL)
	if err != nil {
		t.Fatal(err)
	}
	mp, err := c.GetMempoolWithResponse(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if mp.JSON200 == nil {
		t.Fatalf("expected 200 from mempool, got %v", mp.HTTPResponse)
	}
	if len(mp.JSON200.Transactions) != 0 {
		t.Fatalf("expected empty mempool, got %d transactions", len(mp.JSON200.Transactions))
	}
}

func TestWalletSendWrongPassword(t *testing.T) {
	_, keyPath := savedWallet(t)
	t.Setenv(walletPasswordEnvVar, "other")
	wTo, err := wallet.New()
	if err != nil {
		t.Fatal(err)
	}

	_, err = runCLI(t, context.Background(), "wallet", "send", "--keystore", keyPath, "--to", wTo.Address(), "--amount", "10")
	if !errors.Is(err, wallet.ErrWrongPassword) {
		t.Fatalf("expected ErrWrongPassword, got %v", err)
	}
}

func TestWalletSendReportsNodeRejection(t *testing.T) {
	w, keyPath := savedWallet(t)
	wTo, err := wallet.New()
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/balance/"+w.Address() {
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(http.StatusOK)
			_, _ = rw.Write([]byte(`{"address":"` + w.Address() + `","balance":50,"utxos":[{"txid":"aa","index":0,"amount":50}]}`))
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/tx" {
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(http.StatusConflict)
			_, _ = rw.Write([]byte(`{"error":"conflict","code":"mempool_conflict"}`))
			return
		}
		http.NotFound(rw, r)
	}))
	defer srv.Close()

	_, err = runCLI(t, context.Background(), "wallet", "send", "--node", srv.URL, "--keystore", keyPath, "--to", wTo.Address(), "--amount", "10")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "409") || !strings.Contains(msg, "conflict") {
		t.Fatalf("error %q should contain '409' and 'conflict'", msg)
	}
}
