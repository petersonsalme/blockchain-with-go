//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const contractCLIProject = "bwg-e2e-contract-cli"

// bwgCLI runs the built bwg binary with the test keystore.
type bwgCLI struct {
	bin, keystore string
}

// run executes bwg and returns trimmed stdout, and stderr apart.
func (b bwgCLI) run(ctx context.Context, args ...string) (stdout, stderr string, err error) {
	cmd := exec.CommandContext(ctx, b.bin, args...)
	cmd.Env = append(os.Environ(), "WALLET_PASSWORD=pw", "WALLET_KEYSTORE="+b.keystore)

	var out, errOut strings.Builder

	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()

	return strings.TrimSpace(out.String()), strings.TrimSpace(errOut.String()), err
}

func (b bwgCLI) must(ctx context.Context, t *testing.T, args ...string) string {
	t.Helper()

	out, errOut, err := b.run(ctx, args...)
	if err != nil {
		t.Fatalf("bwg %v: %v\n%s", args, err, errOut)
	}

	return out
}

// TestContractCLI drives deploy, call and show through the bwg CLI only.
func TestContractCLI(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testBudget)
	defer cancel()

	dir := t.TempDir()
	bin := filepath.Join(dir, "bwg")

	build := exec.CommandContext(ctx, "go", "build", "-o", bin, "../cmd/bwg")

	out, err := build.CombinedOutput()
	if err != nil {
		t.Fatalf("go build bwg: %v\n%s", err, out)
	}

	keystore := filepath.Join(dir, "wallet.key")

	err = loadKey(t).Save(keystore, "pw")
	if err != nil {
		t.Fatal(err)
	}

	cli := bwgCLI{bin: bin, keystore: keystore}

	s := stack{project: contractCLIProject, files: []string{"testdata/compose.contract.yml"}}
	s.cleanup(t)
	s.must(ctx, t, "up", "-d", "--build", "node1", "node2")

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

	node1, node2 := "--node="+nodeURLs["node1"], "--node="+nodeURLs["node2"]
	file := func(name string) string { return "--code-file=../examples/contracts/" + name + ".hex" }

	// Deploy adder, then two calls, one block each: storage 7, then 12.
	adder := cli.must(ctx, t, "contract", "deploy", node1, file("adder"), "--gas-limit=100")
	mine(ctx, t, "node1", 1)

	cli.must(ctx, t, "contract", "call", node1, "--target="+adder, "--gas-limit=100", "--args=7")
	mine(ctx, t, "node1", 1)
	cli.must(ctx, t, "contract", "call", node1, "--target="+adder, "--gas-limit=100", "--args=5")
	mine(ctx, t, "node1", 1)

	waitFor(ctx, t, "node2 reaches height 3 with node1", converged(3, both...))
	waitFor(ctx, t, "bwg contract show on node2 reports {0: 12}", func(c context.Context) error {
		body, errOut, err := cli.run(c, "contract", "show", node2, adder)
		if err != nil {
			return fmt.Errorf("%w: %s", err, errOut)
		}

		var got contractBody

		err = json.Unmarshal([]byte(body), &got)
		if err != nil {
			return err
		}

		if got.ID != adder || len(got.Storage) != 1 || got.Storage["0"] != 12 {
			return fmt.Errorf("show answered %s, want storage {0: 12}", body)
		}

		return nil
	})

	counter := cli.must(ctx, t, "contract", "deploy", node1, file("counter"), "--gas-limit=100")
	mine(ctx, t, "node1", 1)

	guard := cli.must(ctx, t, "contract", "deploy", node1, file("guard"), "--gas-limit=100")
	mine(ctx, t, "node1", 1)

	many := strings.TrimSuffix(strings.Repeat("1,", 257), ",")
	failures := []struct {
		name, want string
		args       []string
	}{
		{"out of gas", "out of gas", []string{"--target=" + counter, "--gas-limit=28"}},
		{"reverted", "revert", []string{"--target=" + guard, "--gas-limit=100", "--args=0"}},
		{"unknown contract", "contract", []string{"--target=" + strings.Repeat("0", 64), "--gas-limit=100"}},
		{"257 args", "invalid contract operation", []string{"--target=" + counter, "--gas-limit=100", "--args=" + many}},
	}

	for _, f := range failures {
		_, errOut, err := cli.run(ctx, append([]string{"contract", "call", node1}, f.args...)...)

		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			t.Errorf("%s: err = %v, want exit status 1", f.name, err)
		}

		if !strings.Contains(errOut, "node answered 4") || !strings.Contains(errOut, f.want) {
			t.Errorf("%s: stderr %q, want a node error naming %q", f.name, errOut, f.want)
		}
	}

	_, errOut, err := cli.run(ctx, "contract", "show", node1, strings.Repeat("0", 64))
	if err == nil || !strings.Contains(errOut, "node answered 404: contract not found") {
		t.Errorf("show of an unknown contract: %v %q", err, errOut)
	}
}
