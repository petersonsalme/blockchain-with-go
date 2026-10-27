//go:build e2e

// Package e2e runs a real three-node network with docker compose and checks
// that blocks propagate, a restarted node syncs and all nodes converge.
package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"testing"
	"time"
)

const (
	project      = "bwg-e2e"
	phaseTimeout = 60 * time.Second
	// testBudget stays below the 10m `go test -timeout` so that t.Cleanup runs.
	testBudget = 9 * time.Minute
)

var nodeURLs = map[string]string{
	"node1": "http://127.0.0.1:18081",
	"node2": "http://127.0.0.1:18082",
	"node3": "http://127.0.0.1:18083",
}

type health struct {
	Status string `json:"status"`
	Height uint64 `json:"height"`
	Tip    string `json:"tip"`
}

type peers struct {
	Peers []json.RawMessage `json:"peers"`
}

func compose(ctx context.Context, args ...string) ([]byte, error) {
	full := append([]string{"compose", "-p", project, "-f", "../docker-compose.yml"}, args...)

	return exec.CommandContext(ctx, "docker", full...).CombinedOutput()
}

func mustCompose(ctx context.Context, t *testing.T, args ...string) {
	t.Helper()

	out, err := compose(ctx, args...)
	if err != nil {
		t.Fatalf("docker compose %v: %v\n%s", args, err, out)
	}
}

func call(ctx context.Context, method, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("%s %s: status %d", method, url, resp.StatusCode)
	}

	return json.NewDecoder(resp.Body).Decode(out)
}

func getHealth(ctx context.Context, node string) (health, error) {
	var h health

	err := call(ctx, http.MethodGet, nodeURLs[node]+"/health", &h)
	if err != nil {
		return h, err
	}

	if h.Status != "ok" {
		return h, fmt.Errorf("%s: status %q", node, h.Status)
	}

	return h, nil
}

func mine(ctx context.Context, t *testing.T, node string, n int) {
	t.Helper()

	for range n {
		var discard json.RawMessage

		err := call(ctx, http.MethodPost, nodeURLs[node]+"/mine", &discard)
		if err != nil {
			t.Fatalf("mine on %s: %v", node, err)
		}
	}
}

// waitFor polls check every 500ms until it returns nil or the phase deadline passes.
func waitFor(ctx context.Context, t *testing.T, what string, check func(context.Context) error) {
	t.Helper()

	ctx, cancel := context.WithTimeout(ctx, phaseTimeout)
	defer cancel()

	var last error

	for {
		last = check(ctx)
		if last == nil {
			return
		}

		select {
		case <-ctx.Done():
			t.Fatalf("%s: not reached within %s: %v", what, phaseTimeout, last)
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// converged requires every listed node to report the given height and one common tip.
func converged(height uint64, nodes ...string) func(context.Context) error {
	return func(ctx context.Context) error {
		tip := ""

		for _, n := range nodes {
			h, err := getHealth(ctx, n)
			if err != nil {
				return err
			}

			if h.Height != height {
				return fmt.Errorf("%s at height %d, want %d", n, h.Height, height)
			}

			if tip == "" {
				tip = h.Tip
			} else if h.Tip != tip {
				return fmt.Errorf("%s tip %s differs from %s", n, h.Tip, tip)
			}
		}

		return nil
	}
}

func hasPeer(ctx context.Context, node string) error {
	var p peers

	err := call(ctx, http.MethodGet, nodeURLs[node]+"/p2p/peers", &p)
	if err != nil {
		return err
	}

	if len(p.Peers) == 0 {
		return errors.New(node + " lists no peers")
	}

	return nil
}

func TestThreeNodeNetwork(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testBudget)
	defer cancel()

	t.Cleanup(func() {
		// Fresh context: the test one may already be done.
		cctx, ccancel := context.WithTimeout(context.Background(), time.Minute)
		defer ccancel()

		if t.Failed() {
			logs, _ := compose(cctx, "logs", "--no-color")
			t.Logf("docker compose logs:\n%s", logs)
		}

		out, err := compose(cctx, "down", "-v", "--remove-orphans")
		if err != nil {
			t.Errorf("docker compose down -v: %v\n%s", err, out)
		}
	})

	mustCompose(ctx, t, "up", "-d", "--build")

	all := []string{"node1", "node2", "node3"}

	waitFor(ctx, t, "genesis: three nodes at height 0 with one tip", converged(0, all...))
	waitFor(ctx, t, "every node lists a peer", func(c context.Context) error {
		for _, n := range all {
			err := hasPeer(c, n)
			if err != nil {
				return err
			}
		}

		return nil
	})

	mine(ctx, t, "node1", 3)
	waitFor(ctx, t, "3 blocks propagate to all nodes", converged(3, all...))

	mustCompose(ctx, t, "stop", "node3")
	mine(ctx, t, "node1", 2)
	waitFor(ctx, t, "node1 and node2 at height 5", converged(5, "node1", "node2"))

	mustCompose(ctx, t, "start", "node3")
	waitFor(ctx, t, "restarted node3 syncs to height 5", converged(5, all...))

	mine(ctx, t, "node3", 1)
	waitFor(ctx, t, "block mined on node3 reaches all nodes", converged(6, all...))
}
