package dashboard_test

import (
	"strings"
	"testing"

	"github.com/petersonsalme/blockchain-with-go/internal/dashboard"
)

func TestParseNodesKeepsOrder(t *testing.T) {
	t.Parallel()

	nodes, err := dashboard.ParseNodes("node2=http://node2:8080, node1=https://node1.example:9090/ ,n-3=http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("ParseNodes: %v", err)
	}

	want := []struct{ name, base string }{
		{"node2", "http://node2:8080"},
		{"node1", "https://node1.example:9090"},
		{"n-3", "http://127.0.0.1:1"},
	}
	if len(nodes) != len(want) {
		t.Fatalf("len = %d, want %d", len(nodes), len(want))
	}

	for i, w := range want {
		if nodes[i].Name != w.name || nodes[i].Base.String() != w.base {
			t.Errorf("nodes[%d] = %s %s, want %s %s", i, nodes[i].Name, nodes[i].Base, w.name, w.base)
		}
	}
}

func TestParseNodesRejectsBadEntries(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, in, mention string }{
		{"empty", "", "NODES"},
		{"blank", "  ", "NODES"},
		{"no equals", "node1", "node1"},
		{"no name", "=http://x", "=http://x"},
		{"no url", "node1=", "node1"},
		{"duplicate", "a=http://x,a=http://y", `"a"`},
		{"upper name", "Node1=http://x", "Node1"},
		{"underscore name", "n_1=http://x", "n_1"},
		{"ftp scheme", "a=ftp://x", "ftp://x"},
		{"no scheme", "a=x:8080", "x:8080"},
		{"no host", "a=http://", "http://"},
		{"path", "a=http://x/api", "http://x/api"},
		{"query", "a=http://x?y=1", "http://x?y=1"},
		{"empty query", "a=http://x/?", "http://x/?"},
		{"fragment", "a=http://x#f", "http://x#f"},
		{"empty fragment", "a=http://x#", "http://x#"},
		{"userinfo", "a=http://u:p@x", "http://u:p@x"},
		{"trailing comma", "a=http://x,", "NODES"},
		{"unparsable", "a=http://%zz", "http://%zz"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			nodes, err := dashboard.ParseNodes(tc.in)
			if err == nil {
				t.Fatalf("ParseNodes(%q) = %v, want error", tc.in, nodes)
			}

			if !strings.Contains(err.Error(), tc.mention) {
				t.Errorf("error %q does not mention %q", err, tc.mention)
			}
		})
	}
}
