package p2p

import (
	"errors"
	"strings"
	"testing"
)

var (
	idA = strings.Repeat("a", 32)
	idB = strings.Repeat("b", 32)
	gen = strings.Repeat("0", 64)
	tip = strings.Repeat("f", 64)
)

func hello(id string) Hello {
	return Hello{Version: ProtocolVersion, NodeID: id, GenesisHash: gen, Height: 3, TipHash: tip, ListenAddr: "http://10.0.0.2:8080"}
}

func TestCheck(t *testing.T) {
	local := hello(idA)

	tests := []struct {
		name string
		mod  func(*Hello)
		want error
	}{
		{"ok", func(h *Hello) {}, nil},
		{"ok empty listen addr", func(h *Hello) { h.ListenAddr = "" }, nil},
		{"ok height zero", func(h *Hello) { h.Height = 0 }, nil},
		{"negative height", func(h *Hello) { h.Height = -1 }, ErrBadHello},
		{"short node id", func(h *Hello) { h.NodeID = "abc" }, ErrBadHello},
		{"upper node id", func(h *Hello) { h.NodeID = strings.Repeat("A", 32) }, ErrBadHello},
		{"non hex node id", func(h *Hello) { h.NodeID = strings.Repeat("g", 32) }, ErrBadHello},
		{"short genesis", func(h *Hello) { h.GenesisHash = "00" }, ErrBadHello},
		{"upper genesis", func(h *Hello) { h.GenesisHash = strings.Repeat("A", 64) }, ErrBadHello},
		{"short tip", func(h *Hello) { h.TipHash = "00" }, ErrBadHello},
		{"non hex tip", func(h *Hello) { h.TipHash = strings.Repeat("z", 64) }, ErrBadHello},
		{"bad listen addr", func(h *Hello) { h.ListenAddr = "ftp://h:1" }, ErrBadHello},
		{"version 0", func(h *Hello) { h.Version = 0 }, ErrIncompatibleVersion},
		{"version 2", func(h *Hello) { h.Version = 2 }, ErrIncompatibleVersion},
		{"genesis mismatch", func(h *Hello) { h.GenesisHash = strings.Repeat("1", 64) }, ErrGenesisMismatch},
		{"self", func(h *Hello) { h.NodeID = idA }, ErrSelf},
		{"bad hello before version", func(h *Hello) { h.Height = -1; h.Version = 2 }, ErrBadHello},
		{"version before genesis", func(h *Hello) { h.Version = 2; h.GenesisHash = strings.Repeat("1", 64) }, ErrIncompatibleVersion},
		{"genesis before self", func(h *Hello) { h.NodeID = idA; h.GenesisHash = strings.Repeat("1", 64) }, ErrGenesisMismatch},
		{"version before self", func(h *Hello) { h.NodeID = idA; h.Version = 2 }, ErrIncompatibleVersion},
		{"bad hello before self", func(h *Hello) { h.NodeID = idA; h.TipHash = "x" }, ErrBadHello},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			remote := hello(idB)
			tt.mod(&remote)

			err := Check(local, remote)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Check = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestNormalizeAddr(t *testing.T) {
	ok := map[string]string{
		"http://Host:8080":     "http://host:8080",
		"https://h:443/":       "https://h:443",
		"HTTP://H.Example:80":  "http://h.example:80",
		"http://[::1]:8080":    "http://[::1]:8080",
		"http://[FE80::1]:9/":  "http://[fe80::1]:9",
		"http://127.0.0.1:008": "http://127.0.0.1:8",
		"http://z-9.a0:65535":  "http://z-9.a0:65535",
		"http://h:1":           "http://h:1",
	}
	for in, want := range ok {
		got, err := NormalizeAddr(in)
		if err != nil || got != want {
			t.Errorf("NormalizeAddr(%q) = %q, %v; want %q", in, got, err, want)
		}

		again, err := NormalizeAddr(got)
		if err != nil || again != got {
			t.Errorf("not idempotent for %q: %q, %v", got, again, err)
		}
	}

	bad := []string{
		"", "ftp://h:21", "h:8080", "//h:80", "http://h", "http://h:", "http://u@h:80", "http://u:p@h:80",
		"http://h:80/x", "http://h:80/x/", "http://h:80?a=1", "http://h:80/?", "http://h:80#f", "http://h:80/#",
		"http://:80", "http://h:abc", "http://h:0", "http://h:65536", "http://h:-1", "http://h:+1", "http://h :80",
		"http://h%41:80", "http://[::1%25eth0]:80", "http://[zz]:80", "http://h_x:80", " http://h:80",
	}
	for _, in := range bad {
		got, err := NormalizeAddr(in)
		if !errors.Is(err, ErrBadAddr) {
			t.Errorf("NormalizeAddr(%q) = %q, %v; want ErrBadAddr", in, got, err)
		}
	}
}

func TestNewNodeID(t *testing.T) {
	a, err := NewNodeID()
	if err != nil || !isHex(a, 32) {
		t.Fatalf("NewNodeID = %q, %v", a, err)
	}

	b, _ := NewNodeID()
	if a == b {
		t.Fatal("ids repeat")
	}
}
