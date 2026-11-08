package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/petersonsalme/blockchain-with-go/client"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

const (
	adderHex   = "0100000000000000003001000000000000000040100100000000000000003100"
	counterHex = "01000000000000000030010000000000000001100100000000000000003100"
	guardHex   = "010000000000000000401501000000000000002a21010000000000000000400100000000000000013100ff"
)

// fakeNode serves a balance of three UTXOs for w, records every POST /tx body
// and counts all requests.
type fakeNode struct {
	url      string
	requests atomic.Int32
	body     []byte
}

func startFakeNode(t *testing.T, w *wallet.Wallet) *fakeNode {
	t.Helper()

	f := &fakeNode{}
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		rw.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/balance/"+w.Address():
			_, _ = rw.Write([]byte(`{"address":"` + w.Address() + `","balance":150,"utxos":[{"txid":"a","index":0,"amount":30},{"txid":"b","index":1,"amount":20},{"txid":"c","index":0,"amount":100}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/tx":
			f.body, _ = io.ReadAll(r.Body)

			rw.WriteHeader(http.StatusAccepted)

			var tx struct{ ID string }

			_ = json.Unmarshal(f.body, &tx)
			_, _ = rw.Write([]byte(`{"id":"` + tx.ID + `"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/contracts/gone":
			rw.WriteHeader(http.StatusNotFound)
			_, _ = rw.Write([]byte(`{"error":"contract not found","code":"contract_not_found"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/contracts/abc":
			_, _ = rw.Write([]byte("{\"id\":\"abc\",\"code\":\"00\",\"storage\":{\"0\":12}}\n"))
		default:
			http.NotFound(rw, r)
		}
	}))
	t.Cleanup(srv.Close)

	f.url = srv.URL

	return f
}

func (f *fakeNode) posted(t *testing.T) map[string]json.RawMessage {
	t.Helper()

	var m map[string]json.RawMessage

	err := json.Unmarshal(f.body, &m)
	if err != nil {
		t.Fatalf("posted body %q: %v", f.body, err)
	}

	return m
}

func (f *fakeNode) contract(t *testing.T) map[string]json.RawMessage {
	t.Helper()

	var c map[string]json.RawMessage

	err := json.Unmarshal(f.posted(t)["contract"], &c)
	if err != nil {
		t.Fatalf("contract key of %s: %v", f.body, err)
	}

	return c
}

func TestContractDeploySubmitsSignedDeploy(t *testing.T) {
	w, keyPath := savedWallet(t)
	f := startFakeNode(t, w)

	out, err := runCLI(t, context.Background(), "contract", "deploy", "--node", f.url, "--keystore", keyPath,
		"--code", strings.ToUpper(adderHex), "--gas-limit", "100", "--fee", "45", "--args", "1,2", "--args", "3")
	if err != nil {
		t.Fatal(err)
	}

	var tx domain.Transaction

	body := f.posted(t)

	err = json.Unmarshal(f.body, &tx)
	if err != nil {
		t.Fatal(err)
	}

	if strings.TrimSpace(out) != tx.ID || len(tx.ID) != 64 {
		t.Fatalf("printed %q, want the transaction ID %q on one line", out, tx.ID)
	}

	if !strings.HasSuffix(out, "\n") || strings.Count(out, "\n") != 1 {
		t.Fatalf("output %q is not one line", out)
	}

	var c map[string]any

	err = json.Unmarshal(body["contract"], &c)
	if err != nil {
		t.Fatal(err)
	}

	if len(c) != 4 || c["kind"] != "deploy" || c["code"] != adderHex || c["gas_limit"] != float64(100) || fmt.Sprint(c["args"]) != "[1 2 3]" {
		t.Fatalf("contract = %s", body["contract"])
	}

	if len(tx.Inputs) != 2 || tx.Inputs[0].PrevTxID != "a" || tx.Inputs[1].PrevTxID != "b" || tx.Inputs[1].OutIndex != 1 {
		t.Fatalf("inputs = %+v, want a:0 and b:1 (balance order until fee+1 is covered)", tx.Inputs)
	}

	if len(tx.Outputs) != 1 || tx.Outputs[0].Amount != 5 || tx.Outputs[0].Address != w.Address() {
		t.Fatalf("outputs = %+v, want one output of sum-fee=5 to the sender", tx.Outputs)
	}

	for _, in := range tx.Inputs {
		if in.PublicKey != hex.EncodeToString(w.PublicKey()) || in.Signature == "" {
			t.Fatalf("input not signed: %+v", in)
		}
	}
}

func TestContractDeployCodeFileTrimsWhitespace(t *testing.T) {
	w, keyPath := savedWallet(t)
	f := startFakeNode(t, w)

	file := filepath.Join(t.TempDir(), "c.hex")

	err := os.WriteFile(file, []byte("\n  "+strings.ToUpper(counterHex)+" \n\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	_, err = runCLI(t, context.Background(), "contract", "deploy", "--node", f.url, "--keystore", keyPath, "--code-file", file, "--gas-limit", "50")
	if err != nil {
		t.Fatal(err)
	}

	c := f.contract(t)
	if string(c["code"]) != `"`+counterHex+`"` {
		t.Fatalf("code = %s, want trimmed lower-case %s", c["code"], counterHex)
	}

	if _, ok := c["args"]; ok {
		t.Fatalf("no --args must send no args key: %s", f.body)
	}

	if _, ok := c["target"]; ok {
		t.Fatalf("deploy must not send target: %s", f.body)
	}
}

func TestContractDeployCodeFlagsAreExclusive(t *testing.T) {
	w, keyPath := savedWallet(t)
	f := startFakeNode(t, w)
	file := filepath.Join(t.TempDir(), "c.hex")

	err := os.WriteFile(file, []byte(counterHex), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	for name, args := range map[string][]string{
		"neither":      {"--gas-limit", "50"},
		"both":         {"--code", counterHex, "--code-file", file, "--gas-limit", "50"},
		"missing file": {"--code-file", file + ".missing", "--gas-limit", "50"},
	} {
		t.Run(name, func(t *testing.T) {
			full := append([]string{"contract", "deploy", "--node", f.url, "--keystore", keyPath}, args...)

			_, err := runCLI(t, context.Background(), full...)
			if err == nil {
				t.Fatal("expected a usage error")
			}

			if n := f.requests.Load(); n != 0 {
				t.Fatalf("contacted the node %d times", n)
			}
		})
	}
}

func TestContractArgsMustBeUint64(t *testing.T) {
	w, keyPath := savedWallet(t)
	f := startFakeNode(t, w)

	for _, bad := range []string{"x", "-1", "1,,2", "18446744073709551616", "1.5"} {
		for _, sub := range [][]string{
			{"deploy", "--code", counterHex},
			{"call", "--target", strings.Repeat("ab", 32)},
		} {
			full := append([]string{"contract"}, sub...)
			full = append(full, "--node", f.url, "--keystore", keyPath, "--gas-limit", "50", "--args", bad)

			_, err := runCLI(t, context.Background(), full...)
			if err == nil {
				t.Fatalf("%v with --args %q: expected a usage error", sub[0], bad)
			}
		}
	}

	if n := f.requests.Load(); n != 0 {
		t.Fatalf("contacted the node %d times", n)
	}
}

func TestContractCallSubmitsCall(t *testing.T) {
	w, keyPath := savedWallet(t)
	f := startFakeNode(t, w)
	target := strings.Repeat("ab", 32)

	out, err := runCLI(t, context.Background(), "contract", "call", "--node", f.url, "--keystore", keyPath,
		"--target", target, "--gas-limit", "70", "--args", "0,18446744073709551615")
	if err != nil {
		t.Fatal(err)
	}

	if len(strings.TrimSpace(out)) != 64 || strings.Count(out, "\n") != 1 {
		t.Fatalf("output %q is not a transaction ID on one line", out)
	}

	c := f.contract(t)
	if string(c["kind"]) != `"call"` || string(c["target"]) != `"`+target+`"` || string(c["gas_limit"]) != "70" || string(c["args"]) != "[0,18446744073709551615]" {
		t.Fatalf("contract = %v", c)
	}

	if _, ok := c["code"]; ok {
		t.Fatalf("call must not send code: %s", f.body)
	}

	var tx domain.Transaction

	err = json.Unmarshal(f.body, &tx)
	if err != nil {
		t.Fatal(err)
	}

	if len(tx.Inputs) != 1 || tx.Inputs[0].PrevTxID != "a" || len(tx.Outputs) != 1 || tx.Outputs[0].Amount != 30 {
		t.Fatalf("tx = %+v, want input a and one output of 30 (fee 0)", tx)
	}
}

func TestContractShowPrintsBodyUnchanged(t *testing.T) {
	w, _ := savedWallet(t)
	f := startFakeNode(t, w)

	out, err := runCLI(t, context.Background(), "contract", "show", "--node", f.url, "abc")
	if err != nil {
		t.Fatal(err)
	}

	if want := "{\"id\":\"abc\",\"code\":\"00\",\"storage\":{\"0\":12}}\n"; out != want {
		t.Fatalf("got %q, want %q", out, want)
	}

	_, err = runCLI(t, context.Background(), "contract", "show", "--node", f.url, "gone")
	if err == nil || err.Error() != "node answered 404: contract not found" {
		t.Fatalf("got %v", err)
	}

	_, err = runCLI(t, context.Background(), "contract", "show", "--node", f.url)
	if err == nil {
		t.Fatal("show without ID must be a usage error")
	}
}

func TestContractNodeRejections(t *testing.T) {
	w, keyPath := savedWallet(t)
	nodeURL := startNode(t, domain.TxOut{Amount: 100, Address: w.Address()}, domain.TxOut{Amount: 100, Address: w.Address()},
		domain.TxOut{Amount: 100, Address: w.Address()}, domain.TxOut{Amount: 100, Address: w.Address()},
		domain.TxOut{Amount: 100, Address: w.Address()}, domain.TxOut{Amount: 100, Address: w.Address()})

	mine := func() {
		t.Helper()

		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, nodeURL+"/mine", nil)
		if err != nil {
			t.Fatal(err)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
			t.Fatalf("mine: %d", resp.StatusCode)
		}
	}

	deploy := func(code string) string {
		t.Helper()

		out, err := runCLI(t, context.Background(), "contract", "deploy", "--node", nodeURL, "--keystore", keyPath, "--code", code, "--gas-limit", "100")
		if err != nil {
			t.Fatal(err)
		}

		mine()

		return strings.TrimSpace(out)
	}

	counter := deploy(counterHex)
	guard := deploy(guardHex)

	var many []string
	for range domain.MaxContractArgs + 1 {
		many = append(many, "1")
	}

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"out of gas", []string{"--target", counter, "--gas-limit", "28"}, "out of gas"},
		{"reverted", []string{"--target", guard, "--gas-limit", "100", "--args", "0"}, "revert"},
		{"unknown contract", []string{"--target", strings.Repeat("0", 64), "--gas-limit", "100"}, "contract"},
		{"too many args", []string{"--target", counter, "--gas-limit", "100", "--args", strings.Join(many, ",")}, "invalid contract operation"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			full := append([]string{"contract", "call", "--node", nodeURL, "--keystore", keyPath}, tc.args...)

			_, err := runCLI(t, context.Background(), full...)
			if err == nil || !strings.HasPrefix(err.Error(), "node answered 4") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want a node error containing %q", err, tc.want)
			}
		})
	}

	// The success path still works after the rejections: counter is 1.
	_, err := runCLI(t, context.Background(), "contract", "call", "--node", nodeURL, "--keystore", keyPath, "--target", counter, "--gas-limit", "29")
	if err != nil {
		t.Fatal(err)
	}
}

func TestToClientTxContract(t *testing.T) {
	plain := toClientTx(domain.Transaction{ID: "x"})
	if plain.Contract != nil {
		t.Fatalf("a transfer must carry no contract, got %+v", plain.Contract)
	}

	deploy := toClientTx(domain.Transaction{Contract: &domain.ContractOp{Kind: domain.ContractDeploy, Code: "00", GasLimit: 7}})
	if c := deploy.Contract; c == nil || c.Kind != "deploy" || c.GasLimit != 7 || c.Code == nil || *c.Code != "00" || c.Target != nil || c.Args != nil {
		t.Fatalf("deploy mapped to %+v", deploy.Contract)
	}

	call := toClientTx(domain.Transaction{Contract: &domain.ContractOp{Kind: domain.ContractCall, Target: "t", Args: []uint64{1}, GasLimit: 9}})
	if c := call.Contract; c == nil || c.Kind != "call" || c.GasLimit != 9 || c.Code != nil || c.Target == nil || *c.Target != "t" || c.Args == nil || len(*c.Args) != 1 {
		t.Fatalf("call mapped to %+v", call.Contract)
	}

	raw, err := json.Marshal(plain)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(raw), "contract") {
		t.Fatalf("transfer JSON has a contract key: %s", raw)
	}
}

func TestBuildContractTx(t *testing.T) {
	w, err := wallet.New()
	if err != nil {
		t.Fatal(err)
	}

	op := domain.ContractOp{Kind: domain.ContractCall, Target: strings.Repeat("a", 64), GasLimit: 5}
	now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	utxos := []client.Utxo{{Txid: "a", Amount: 10}, {Txid: "b", Amount: 10}}

	// fee+1 = 10 is covered by the first output alone; the change is sum-fee = 1.
	tx, err := buildContractTx(w, utxos, op, 9, now)
	if err != nil {
		t.Fatal(err)
	}

	if len(tx.Inputs) != 1 || len(tx.Outputs) != 1 || tx.Outputs[0].Amount != 1 || tx.Contract == nil || tx.ID != tx.CalculateHash() {
		t.Fatalf("tx = %+v", tx)
	}

	// fee+1 = 11 needs both outputs.
	tx, err = buildContractTx(w, utxos, op, 10, now)
	if err != nil || len(tx.Inputs) != 2 || tx.Outputs[0].Amount != 10 {
		t.Fatalf("tx = %+v, err = %v", tx, err)
	}

	// fee = sum leaves no coin for the output.
	_, err = buildContractTx(w, utxos, op, 20, now)
	if !errors.Is(err, errInsufficientFunds) {
		t.Fatalf("got %v, want errInsufficientFunds", err)
	}

	_, err = buildContractTx(w, utxos, op, math.MaxUint64, now)
	if !errors.Is(err, errAmountOverflow) {
		t.Fatalf("fee overflow: got %v", err)
	}

	_, err = buildContractTx(w, []client.Utxo{{Txid: "a", Amount: 5}, {Txid: "b", Amount: math.MaxUint64}}, op, 9, now)
	if !errors.Is(err, errAmountOverflow) {
		t.Fatalf("sum overflow: got %v", err)
	}
}
