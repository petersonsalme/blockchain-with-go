//go:build e2e

package e2e

import (
	"context"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

const (
	poaProject  = "bwg-e2e-poa"
	poaPassword = "e2e-poa-password"
)

// Fixed seeds, different from testdata/key.seed; the keystores are written at run time.
const (
	poaSeed1 = "bwg-e2e-poa-validator-seed-0001!"
	poaSeed2 = "bwg-e2e-poa-validator-seed-0002!"
)

// writeValidator saves the keystore of the wallet built from seed as dir/name and
// returns its public key in hex. The container runs as another uid, so the file is
// made world-readable (it is mounted read-only and holds a throwaway test key).
func writeValidator(t *testing.T, dir, name, seed string) string {
	t.Helper()

	w, err := wallet.FromSeed([]byte(seed))
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, name)

	err = w.Save(path, poaPassword)
	if err != nil {
		t.Fatal(err)
	}

	err = os.Chmod(path, 0o644)
	if err != nil {
		t.Fatal(err)
	}

	return hex.EncodeToString(w.PublicKey())
}

type consensusView struct {
	Engine string `json:"engine"`
	PoA    struct {
		Validators []string `json:"validators"`
	} `json:"poa"`
}

// TestPoATwoValidators runs two PoA nodes with their own keys: both seal, blocks
// cross, and after a partition the node that sealed more blocks wins.
func TestPoATwoValidators(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioBudget)
	defer cancel()

	dir := t.TempDir()
	key1 := writeValidator(t, dir, "node1.key", poaSeed1)
	key2 := writeValidator(t, dir, "node2.key", poaSeed2)

	s := stack{
		project: poaProject,
		files:   []string{"testdata/compose.poa.yml"},
		env: []string{
			"BWG_E2E_KEYS=" + dir,
			"BWG_E2E_POA_VALIDATORS=" + key1 + "," + key2,
			"BWG_E2E_POA_PASSWORD=" + poaPassword,
		},
	}
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

	for _, n := range both {
		var c consensusView

		err := call(ctx, http.MethodGet, nodeURLs[n]+"/consensus", &c)
		if err != nil {
			t.Fatal(err)
		}

		if c.Engine != "poa" || len(c.PoA.Validators) != 2 ||
			!slices.Contains(c.PoA.Validators, key1) || !slices.Contains(c.PoA.Validators, key2) {
			t.Fatalf("%s: consensus %+v, want poa with validators %s and %s", n, c, key1, key2)
		}
	}

	mine(ctx, t, "node1", 1)
	waitFor(ctx, t, "block sealed on node1 reaches node2", converged(1, both...))
	mine(ctx, t, "node2", 1)
	waitFor(ctx, t, "block sealed on node2 reaches node1", converged(2, both...))

	for ref, want := range map[string]string{"1": key1, "2": key2} {
		for _, n := range both {
			b, err := getBlock(ctx, n, ref)
			if err != nil {
				t.Fatal(err)
			}

			if b.Validator != want {
				t.Fatalf("%s: block %s validator %q, want %q", n, ref, b.Validator, want)
			}
		}
	}

	// Partition: node2 is cut off; node1 seals 3 blocks and node2 seals 1.
	cid := s.containerID(ctx, t, "node2")
	network := poaProject + "_default"

	mustDocker(ctx, t, "network", "disconnect", network, cid)

	mine(ctx, t, "node1", 3)
	mine(ctx, t, "node2", 1)

	waitFor(ctx, t, "node1 at height 5", converged(5, "node1"))
	waitFor(ctx, t, "isolated node2 at height 3", converged(3, "node2"))

	winner := tipOf(ctx, t, "node1")
	loser := tipOf(ctx, t, "node2")

	// compose gave the container its service name as a DNS alias; connect must restore it.
	mustDocker(ctx, t, "network", "connect", "--alias", "node2", network, cid)

	waitFor(ctx, t, "both nodes at height 5", converged(5, both...))

	for _, n := range both {
		got := tipOf(ctx, t, n)
		if got != winner {
			t.Fatalf("%s tip %s, want node1's tip %s", n, got, winner)
		}

		if got == loser {
			t.Fatalf("%s kept the isolated tip %s", n, loser)
		}
	}
}
