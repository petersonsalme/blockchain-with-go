package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/consensus/poa"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/server"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/memory"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

const testPassword = "correct horse"

func hexKey(w *wallet.Wallet) string { return hex.EncodeToString(w.PublicKey()) }

// poaEnv saves a fresh keystore and sets the PoA variables; it returns the signer.
func poaEnv(t *testing.T, validators func(self *wallet.Wallet) string) (*wallet.Wallet, string) {
	t.Helper()

	w, err := wallet.New()
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "validator.key")
	if err := w.Save(path, testPassword); err != nil {
		t.Fatal(err)
	}

	t.Setenv(consensusEnvVar, "poa")
	t.Setenv(poaValidatorsEnvVar, validators(w))
	t.Setenv(poaKeystoreEnvVar, path)
	t.Setenv(poaKeystorePasswordEnvVar, testPassword)

	return w, path
}

func TestNewConsensusPoW(t *testing.T) {
	t.Setenv("POW_DIFFICULTY", "1")

	for name, set := range map[string]func(t *testing.T){
		"absent": func(t *testing.T) {},
		"empty":  func(t *testing.T) { t.Setenv(consensusEnvVar, "") },
		"pow":    func(t *testing.T) { t.Setenv(consensusEnvVar, "pow") },
	} {
		t.Run(name, func(t *testing.T) {
			set(t)

			eng, err := newConsensus()
			if err != nil {
				t.Fatal(err)
			}

			if _, ok := eng.(*pow.ProofOfWork); !ok {
				t.Fatalf("engine = %T, want *pow.ProofOfWork", eng)
			}
		})
	}
}

func TestNewConsensusPoWReadsRetargetSettings(t *testing.T) {
	t.Setenv("POW_DIFFICULTY", "3")
	t.Setenv("POW_RETARGET_INTERVAL", "7")
	t.Setenv("TARGET_BLOCK_TIME", "3s")

	eng, err := newConsensus()
	if err != nil {
		t.Fatal(err)
	}

	p, ok := eng.(*pow.ProofOfWork)
	if !ok {
		t.Fatalf("engine = %T, want *pow.ProofOfWork", eng)
	}

	if p.Difficulty != 3 || p.RetargetInterval != 7 || p.TargetBlockTime != 3*time.Second {
		t.Fatalf("engine = %+v, want difficulty 3, interval 7, target 3s", *p)
	}
}

func TestNewConsensusRejectsUnknownValue(t *testing.T) {
	for _, v := range []string{"POA", "Pow", "pos", " poa", "poa ", "x"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv(consensusEnvVar, v)

			_, err := newConsensus()
			if err == nil || !strings.Contains(err.Error(), consensusEnvVar) {
				t.Fatalf("err = %v, want error citing %s", err, consensusEnvVar)
			}
		})
	}
}

func TestNewConsensusPoA(t *testing.T) {
	other, _ := wallet.New()
	w, _ := poaEnv(t, func(self *wallet.Wallet) string {
		return "  " + hexKey(other) + " , " + hexKey(self) + "  "
	})

	eng, err := newConsensus()
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := eng.(*poa.ProofOfAuthority); !ok {
		t.Fatalf("engine = %T, want *poa.ProofOfAuthority", eng)
	}

	b := &domain.Block{Index: 1, Timestamp: time.Unix(1, 0)}
	if err := eng.Seal(context.Background(), nil, b); err != nil {
		t.Fatal(err)
	}

	if b.Validator != hexKey(w) {
		t.Fatalf("Validator = %s, want %s", b.Validator, hexKey(w))
	}
}

func TestNewConsensusPoAInvalidConfig(t *testing.T) {
	dup := func(self *wallet.Wallet) string { return hexKey(self) + "," + hexKey(self) }

	cases := map[string]struct {
		validators func(self *wallet.Wallet) string
		mutate     func(t *testing.T, path string)
		wantVar    string
		wantItem   func(self *wallet.Wallet) string
	}{
		"validators empty":      {validators: func(*wallet.Wallet) string { return "" }, wantVar: poaValidatorsEnvVar},
		"validators blank":      {validators: func(*wallet.Wallet) string { return "  " }, wantVar: poaValidatorsEnvVar},
		"not hex":               {validators: func(s *wallet.Wallet) string { return hexKey(s) + ",zz" }, wantVar: poaValidatorsEnvVar, wantItem: func(*wallet.Wallet) string { return "zz" }},
		"wrong size":            {validators: func(s *wallet.Wallet) string { return hexKey(s) + ",abcd" }, wantVar: poaValidatorsEnvVar, wantItem: func(*wallet.Wallet) string { return "abcd" }},
		"empty item":            {validators: func(s *wallet.Wallet) string { return hexKey(s) + "," }, wantVar: poaValidatorsEnvVar, wantItem: func(*wallet.Wallet) string { return `""` }},
		"duplicate":             {validators: dup, wantVar: poaValidatorsEnvVar, wantItem: hexKey},
		"signer not in set":     {validators: func(*wallet.Wallet) string { o, _ := wallet.New(); return hexKey(o) }, wantVar: poaKeystoreEnvVar},
		"keystore missing":      {validators: hexKey, mutate: func(t *testing.T, p string) { t.Setenv(poaKeystoreEnvVar, p+".missing") }, wantVar: poaKeystoreEnvVar},
		"wrong password":        {validators: hexKey, mutate: func(t *testing.T, p string) { t.Setenv(poaKeystorePasswordEnvVar, "nope") }, wantVar: poaKeystorePasswordEnvVar},
		"invalid keystore file": {validators: hexKey, mutate: func(t *testing.T, p string) { writeFile(t, p, "garbage") }, wantVar: poaKeystoreEnvVar},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			w, path := poaEnv(t, c.validators)
			if c.mutate != nil {
				c.mutate(t, path)
			}

			_, err := newConsensus()
			if err == nil {
				t.Fatal("newConsensus succeeded, want error")
			}

			if !strings.Contains(err.Error(), c.wantVar) {
				t.Fatalf("err = %v, want it to cite %s", err, c.wantVar)
			}

			if c.wantItem != nil && !strings.Contains(err.Error(), c.wantItem(w)) {
				t.Fatalf("err = %v, want it to cite item %s", err, c.wantItem(w))
			}

			if strings.Contains(err.Error(), testPassword) {
				t.Fatalf("err leaks the password: %v", err)
			}
		})
	}
}

func TestRunFailsOnInvalidConsensus(t *testing.T) {
	t.Setenv("PORT", freePort(t))
	t.Setenv(consensusEnvVar, "pos")

	err := run(context.Background(), discardLogger())
	if err == nil || !strings.Contains(err.Error(), consensusEnvVar) {
		t.Fatalf("run = %v, want error citing %s", err, consensusEnvVar)
	}
}

// mineFirstBlock serves the node over httptest, POSTs one transaction spending
// the genesis allocation and returns the chain from GET /.
func mineFirstBlock(t *testing.T) []domain.Block {
	t.Helper()

	funder, _ := wallet.New()
	dest, _ := wallet.New()

	t.Setenv(genesisAllocationsEnvVar, funder.Address()+":10")

	eng, err := newConsensus()
	if err != nil {
		t.Fatal(err)
	}

	repo := memory.New()

	svc, err := newService(repo, eng)
	if err != nil {
		t.Fatal(err)
	}

	srv, err := server.NewServer(svc, server.Config{MiningTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	genesis, _ := repo.All()

	tx := domain.Transaction{
		Inputs:    []domain.TxIn{{PrevTxID: genesis[0].Transactions[0].ID, OutIndex: 0}},
		Outputs:   []domain.TxOut{{Amount: 10, Address: dest.Address()}},
		Timestamp: time.Unix(1700000000, 0).UTC(),
	}
	if err := domain.SignInput(&tx, 0, funder); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]any{"Transactions": []domain.Transaction{tx}})

	resp, err := http.Post(ts.URL+"/", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close() // response body of a test request; close error is irrelevant
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST / = %d, want 201", resp.StatusCode)
	}

	resp, err = http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }() // response body of a test request; close error is irrelevant

	var got struct{ Blocks []domain.Block }
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}

	blocks := got.Blocks
	if len(blocks) != 2 {
		t.Fatalf("chain has %d blocks, want 2", len(blocks))
	}

	return blocks
}

func TestEndToEndPoA(t *testing.T) {
	w, _ := poaEnv(t, hexKey)
	blocks := mineFirstBlock(t)

	b := blocks[1]
	if b.Validator != hexKey(w) || b.Signature == "" {
		t.Fatalf("block 1 validator=%q signature=%q, want validator %s and a signature", b.Validator, b.Signature, hexKey(w))
	}

	verifier, err := poa.New([]ed25519.PublicKey{w.PublicKey()}, w)
	if err != nil {
		t.Fatal(err)
	}

	if err := verifier.Verify([]*domain.Block{&blocks[0]}, &blocks[1]); err != nil {
		t.Fatalf("poa.Verify rejected block 1: %v", err)
	}
}

func TestEndToEndPoWLeavesSealFieldsEmpty(t *testing.T) {
	t.Setenv("POW_DIFFICULTY", "1")

	blocks := mineFirstBlock(t)
	if b := blocks[1]; b.Validator != "" || b.Signature != "" {
		t.Fatalf("PoW block 1 validator=%q signature=%q, want both empty", b.Validator, b.Signature)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
