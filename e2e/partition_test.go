//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"
)

const (
	partitionProject = "bwg-e2e-partition"
	// scenarioBudget bounds one partition/PoA scenario; the package timeout is 20m.
	scenarioBudget = 5 * time.Minute
	holdTime       = 5 * time.Second
)

// TestPartitionReorgToMoreWork cuts node3 off, lets node1 mine 2 blocks and node3
// mine 3, and checks that after reconnecting every node follows node3's branch: it
// carries more work although only one node mined it.
func TestPartitionReorgToMoreWork(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioBudget)
	defer cancel()

	s := stack{project: partitionProject, files: []string{"testdata/compose.partition.yml"}}
	s.cleanup(t)
	s.must(ctx, t, "up", "-d", "--build")

	all := []string{"node1", "node2", "node3"}
	majority := []string{"node1", "node2"}

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

	mine(ctx, t, "node1", 2)
	waitFor(ctx, t, "2 common blocks reach all nodes", converged(2, all...))

	cid := s.containerID(ctx, t, "node3")
	network := partitionProject + "_default"

	mustDocker(ctx, t, "network", "disconnect", network, cid)

	mine(ctx, t, "node1", 2)
	mine(ctx, t, "node3", 3)

	waitFor(ctx, t, "node1 and node2 at height 4 with one tip", converged(4, majority...))
	waitFor(ctx, t, "isolated node3 at height 5", converged(5, "node3"))

	majorityTip := tipOf(ctx, t, "node1")
	minorityTip := tipOf(ctx, t, "node3")

	if majorityTip == minorityTip {
		t.Fatalf("both branches share tip %s", majorityTip)
	}

	// The partition is real: nothing crosses for holdTime.
	time.Sleep(holdTime)

	waitFor(ctx, t, "partition holds: node1 and node2 still at height 4", converged(4, majority...))
	waitFor(ctx, t, "partition holds: node3 still at height 5", converged(5, "node3"))

	if got := tipOf(ctx, t, "node1"); got != majorityTip {
		t.Fatalf("node1 tip moved during the partition: %s -> %s", majorityTip, got)
	}

	if got := tipOf(ctx, t, "node3"); got != minorityTip {
		t.Fatalf("node3 tip moved during the partition: %s -> %s", minorityTip, got)
	}

	// compose gave the container its service name as a DNS alias; connect must restore it.
	mustDocker(ctx, t, "network", "connect", "--alias", "node3", network, cid)

	waitFor(ctx, t, "all nodes at height 5", converged(5, all...))

	for _, n := range all {
		if got := tipOf(ctx, t, n); got != minorityTip {
			t.Fatalf("%s tip %s, want node3's isolated tip %s", n, got, minorityTip)
		}
	}
}

func tipOf(ctx context.Context, t *testing.T, node string) string {
	t.Helper()

	h, err := getHealth(ctx, node)
	if err != nil {
		t.Fatal(fmt.Errorf("%s: %w", node, err))
	}

	return h.Tip
}
