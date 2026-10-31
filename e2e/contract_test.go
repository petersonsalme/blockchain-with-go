//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/vm"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

const contractProject = "bwg-e2e-contract"

// composeContract runs docker compose on the contract project: the stock file plus
// the funding override under testdata.
func composeContract(ctx context.Context, args ...string) ([]byte, error) {
	full := append([]string{
		"compose", "-p", contractProject,
		"-f", "../docker-compose.yml", "-f", "testdata/compose.contract.yml",
	}, args...)

	return exec.CommandContext(ctx, "docker", full...).CombinedOutput()
}

func mustDocker(ctx context.Context, t *testing.T, args ...string) []byte {
	t.Helper()

	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %v: %v\n%s", args, err, out)
	}

	return out
}

type utxo struct {
	TxID   string `json:"txid"`
	Index  uint32 `json:"index"`
	Amount uint64 `json:"amount"`
}

type balance struct {
	Balance uint64 `json:"balance"`
	UTXOs   []utxo `json:"utxos"`
}

type contractBody struct {
	ID      string            `json:"id"`
	Storage map[string]uint64 `json:"storage"`
}

// funded is the test key and the genesis outputs it has not spent yet.
type funded struct {
	key  *wallet.Wallet
	outs []utxo
}

func loadKey(t *testing.T) *wallet.Wallet {
	t.Helper()

	raw, err := os.ReadFile("testdata/key.seed")
	if err != nil {
		t.Fatal(err)
	}

	seed, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}

	w, err := wallet.FromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}

	return w
}

// next hands out the next unspent genesis output, so no two transactions conflict.
func (f *funded) next(t *testing.T) utxo {
	t.Helper()

	if len(f.outs) == 0 {
		t.Fatal("no funded output left")
	}

	u := f.outs[0]
	f.outs = f.outs[1:]

	return u
}

// submit signs a transaction carrying op, spending one funded output, and posts it to node.
func (f *funded) submit(ctx context.Context, t *testing.T, node string, op domain.ContractOp) string {
	t.Helper()

	in := f.next(t)
	tx := domain.Transaction{
		Inputs:    []domain.TxIn{{PrevTxID: in.TxID, OutIndex: in.Index}},
		Outputs:   []domain.TxOut{{Amount: 1, Address: f.key.Address()}},
		Timestamp: time.Now(),
		Contract:  &op,
	}
	tx.ID = tx.CalculateHash()

	err := domain.SignInput(&tx, 0, f.key)
	if err != nil {
		t.Fatal(err)
	}

	// The API spells the contract object in lower case.
	raw, err := json.Marshal(tx)
	if err != nil {
		t.Fatal(err)
	}

	var m map[string]json.RawMessage

	err = json.Unmarshal(raw, &m)
	if err != nil {
		t.Fatal(err)
	}

	args := op.Args
	if args == nil {
		args = []uint64{}
	}

	argsJSON, _ := json.Marshal(args)

	delete(m, "Contract")
	m["contract"] = json.RawMessage(fmt.Sprintf(`{"kind":%q,"code":%q,"target":%q,"args":%s,"gas_limit":%d}`,
		op.Kind, op.Code, op.Target, argsJSON, op.GasLimit))

	body, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, nodeURLs[node]+"/tx", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /tx on %s: status %d", node, resp.StatusCode)
	}

	return tx.ID
}

func (f *funded) call(ctx context.Context, t *testing.T, node, id string) {
	t.Helper()
	f.submit(ctx, t, node, domain.ContractOp{Kind: domain.ContractCall, Target: id, GasLimit: 100})
}

// counterIs requires every listed node to answer the same body, with storage {"0": want}.
func counterIs(id string, want uint64, nodes ...string) func(context.Context) error {
	return func(ctx context.Context) error {
		var first []byte

		for _, n := range nodes {
			var raw json.RawMessage

			err := call(ctx, http.MethodGet, nodeURLs[n]+"/contracts/"+id, &raw)
			if err != nil {
				return err
			}

			var c contractBody

			err = json.Unmarshal(raw, &c)
			if err != nil {
				return err
			}

			if len(c.Storage) != 1 || c.Storage["0"] != want {
				return fmt.Errorf("%s storage %v, want {0: %d}", n, c.Storage, want)
			}

			if first == nil {
				first = raw
			} else if !bytes.Equal(first, raw) {
				return fmt.Errorf("%s answers a different body than the first node", n)
			}
		}

		return nil
	}
}

func TestTwoNodeCounterContract(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testBudget)
	defer cancel()

	t.Cleanup(func() {
		// Fresh context: the test one may already be done.
		cctx, ccancel := context.WithTimeout(context.Background(), time.Minute)
		defer ccancel()

		if t.Failed() {
			logs, _ := composeContract(cctx, "logs", "--no-color")
			t.Logf("docker compose logs:\n%s", logs)
		}

		out, err := composeContract(cctx, "down", "-v", "--remove-orphans")
		if err != nil {
			t.Errorf("docker compose down -v: %v\n%s", err, out)
		}
	})

	run := func(args ...string) {
		t.Helper()

		out, err := composeContract(ctx, args...)
		if err != nil {
			t.Fatalf("docker compose %v: %v\n%s", args, err, out)
		}
	}

	run("up", "-d", "--build", "node1", "node2")

	both := []string{"node1", "node2"}

	waitFor(ctx, t, "genesis: two nodes at height 0 with one tip", converged(0, both...))
	waitFor(ctx, t, "every node lists a peer", func(c context.Context) error {
		for _, n := range both {
			err := hasPeer(c, n)
			if err != nil {
				return err
			}
		}

		return nil
	})

	key := loadKey(t)

	var bal balance

	for _, n := range both {
		bal = balance{}

		err := call(ctx, http.MethodGet, nodeURLs[n]+"/balance/"+key.Address(), &bal)
		if err != nil {
			t.Fatal(err)
		}

		if bal.Balance == 0 {
			t.Fatalf("%s: test key %s is not funded; does testdata/compose.contract.yml match testdata/key.seed?", n, key.Address())
		}
	}

	f := &funded{key: key, outs: bal.UTXOs}

	// Deploy the counter and call it three times; each step is mined on node1.
	id := f.submit(ctx, t, "node1", domain.ContractOp{Kind: domain.ContractDeploy, Code: hex.EncodeToString(vm.CounterCode), GasLimit: 100})
	mine(ctx, t, "node1", 1)

	for range 3 {
		f.call(ctx, t, "node1", id)
	}

	mine(ctx, t, "node1", 1)
	waitFor(ctx, t, "node2 reaches height 2 with node1", converged(2, both...))
	waitFor(ctx, t, "counter is 3 on both nodes", counterIs(id, 3, both...))

	// Isolate node2 from the compose network, then let each side mine its own branch.
	cid := strings.TrimSpace(string(mustDocker(ctx, t, "compose", "-p", contractProject, "-f", "../docker-compose.yml", "ps", "-q", "node2")))
	network := contractProject + "_default"

	mustDocker(ctx, t, "network", "disconnect", network, cid)

	// node1 mines two blocks with one call each: height 4, counter 5.
	for range 2 {
		f.call(ctx, t, "node1", id)
		mine(ctx, t, "node1", 1)
	}

	// node2 mines one block with one call: height 3, counter 4.
	f.call(ctx, t, "node2", id)
	mine(ctx, t, "node2", 1)

	waitFor(ctx, t, "node1 at height 4, counter 5", func(c context.Context) error {
		err := converged(4, "node1")(c)
		if err != nil {
			return err
		}

		return counterIs(id, 5, "node1")(c)
	})
	waitFor(ctx, t, "isolated node2 at height 3, counter 4", func(c context.Context) error {
		err := converged(3, "node2")(c)
		if err != nil {
			return err
		}

		return counterIs(id, 4, "node2")(c)
	})

	loser, err := getHealth(ctx, "node2")
	if err != nil {
		t.Fatal(err)
	}

	// Reconnect: node2 syncs to the branch with more work and drops its own block.
	mustDocker(ctx, t, "network", "connect", network, cid)

	waitFor(ctx, t, "both nodes agree on height 4 after the reorg", converged(4, both...))
	waitFor(ctx, t, "counter is the winner's 5 on both nodes", counterIs(id, 5, both...))

	h, err := getHealth(ctx, "node2")
	if err != nil {
		t.Fatal(err)
	}

	if h.Tip == loser.Tip {
		t.Fatalf("node2 kept its isolated tip %s", loser.Tip)
	}
}
