package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

// runCLI runs bwg with args and returns what it printed on stdout.
func runCLI(t *testing.T, ctx context.Context, args ...string) (string, error) {
	t.Helper()

	cmd := newRootCmd()

	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(ctx)

	return out.String(), err
}

func TestWalletNewWritesKeystoreAndPrintsAddress(t *testing.T) {
	t.Setenv("WALLET_PASSWORD", "pw")
	dir := t.TempDir()
	path := filepath.Join(dir, "w.key")

	out, err := runCLI(t, context.Background(), "wallet", "new", "--keystore", path)
	if err != nil {
		t.Fatalf("runCLI: %v", err)
	}

	w, err := wallet.Load(path, "pw")
	if err != nil {
		t.Fatalf("wallet.Load: %v", err)
	}

	expected := w.Address() + "\n"
	if out != expected {
		t.Fatalf("got %q, want %q", out, expected)
	}
}

func TestWalletNewReadsKeystoreFromEnv(t *testing.T) {
	t.Setenv("WALLET_PASSWORD", "pw")
	dir := t.TempDir()
	path := filepath.Join(dir, "env.key")
	t.Setenv("WALLET_KEYSTORE", path)

	out, err := runCLI(t, context.Background(), "wallet", "new")
	if err != nil {
		t.Fatalf("runCLI: %v", err)
	}

	_, err = os.Stat(path)
	if err != nil {
		t.Fatalf("stat keystore: %v", err)
	}

	w, err := wallet.Load(path, "pw")
	if err != nil {
		t.Fatalf("wallet.Load: %v", err)
	}

	if out != w.Address()+"\n" {
		t.Fatalf("got %q, want %q", out, w.Address()+"\n")
	}
}

func TestWalletNewRefusesExistingKeystore(t *testing.T) {
	t.Setenv("WALLET_PASSWORD", "pw")
	dir := t.TempDir()

	path := filepath.Join(dir, "existing.key")

	err := os.WriteFile(path, []byte("x"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	_, err = runCLI(t, context.Background(), "wallet", "new", "--keystore", path)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected os.ErrExist, got %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if string(content) != "x" {
		t.Fatalf("expected file content 'x', got %q", string(content))
	}
}

func TestWalletNewRequiresPassword(t *testing.T) {
	t.Setenv("WALLET_PASSWORD", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "nopw.key")

	_, err := runCLI(t, context.Background(), "wallet", "new", "--keystore", path)
	if !errors.Is(err, wallet.ErrEmptyPassword) {
		t.Fatalf("expected wallet.ErrEmptyPassword, got %v", err)
	}

	_, err = os.Stat(path)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected file to not exist, got err: %v", err)
	}
}

func TestWalletBalancePrintsBalance(t *testing.T) {
	w, err := wallet.New()
	if err != nil {
		t.Fatal(err)
	}

	addr := w.Address()

	called := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++

		if r.Method != http.MethodGet {
			t.Errorf("got method %s, want GET", r.Method)
		}

		if r.URL.Path != "/balance/"+addr {
			t.Errorf("got path %s, want /balance/%s", r.URL.Path, addr)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"address":"` + addr + `","balance":42,"utxos":[]}`))
	}))
	defer srv.Close()

	out, err := runCLI(t, context.Background(), "wallet", "balance", addr, "--node", srv.URL)
	if err != nil {
		t.Fatalf("runCLI: %v", err)
	}

	if called != 1 {
		t.Fatalf("server called %d times, want 1", called)
	}

	if out != "42\n" {
		t.Fatalf("got %q, want \"42\\n\"", out)
	}
}

func TestWalletBalanceUsesNodeURLFromEnv(t *testing.T) {
	w, err := wallet.New()
	if err != nil {
		t.Fatal(err)
	}

	addr := w.Address()

	called := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++

		if r.Method != http.MethodGet || r.URL.Path != "/balance/"+addr {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"address":"` + addr + `","balance":42,"utxos":[]}`))
	}))
	defer srv.Close()

	t.Setenv("NODE_URL", srv.URL)

	out, err := runCLI(t, context.Background(), "wallet", "balance", addr)
	if err != nil {
		t.Fatalf("runCLI: %v", err)
	}

	if called != 1 {
		t.Fatalf("server called %d times, want 1", called)
	}

	if out != "42\n" {
		t.Fatalf("got %q, want \"42\\n\"", out)
	}
}

func TestWalletBalanceRejectsInvalidAddress(t *testing.T) {
	called := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
	}))
	defer srv.Close()

	t.Setenv("NODE_URL", srv.URL)

	_, err := runCLI(t, context.Background(), "wallet", "balance", "nope")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if called != 0 {
		t.Fatalf("server called %d times, want 0", called)
	}
}

func TestWalletBalanceReportsNodeError(t *testing.T) {
	w, err := wallet.New()
	if err != nil {
		t.Fatal(err)
	}

	addr := w.Address()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad address"}`))
	}))
	defer srv.Close()

	_, err = runCLI(t, context.Background(), "wallet", "balance", addr, "--node", srv.URL)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	msg := err.Error()
	if !strings.Contains(msg, "400") || !strings.Contains(msg, "bad address") {
		t.Fatalf("error %q should contain '400' and 'bad address'", msg)
	}
}
