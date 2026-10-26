package p2p

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

func nid(i int) string { return fmt.Sprintf("%032x", i) }

func hAt(i int, listen string) Hello {
	h := hello(nid(i))
	h.ListenAddr = listen

	return h
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func newClock() *clock { return &clock{t: time.Unix(1000, 0)} }

func TestLimitsDefaults(t *testing.T) {
	b := NewBook(Limits{}, nil)
	if b.limits != (Limits{16, 8, 1000}) {
		t.Fatalf("defaults = %+v", b.limits)
	}

	b = NewBook(Limits{-1, -5, -2}, nil)
	if b.limits != (Limits{16, 8, 1000}) {
		t.Fatalf("negative = %+v", b.limits)
	}

	b = NewBook(Limits{1, 2, 3}, nil)
	if b.limits != (Limits{1, 2, 3}) {
		t.Fatalf("explicit = %+v", b.limits)
	}

	if b.now == nil || time.Since(b.now()) > time.Minute {
		t.Fatal("nil clock must default to time.Now")
	}
}

func TestBookInboundLimit(t *testing.T) {
	b := NewBook(Limits{MaxInbound: 2, MaxOutbound: 1}, newClock().now)
	for i := 1; i <= 2; i++ {
		err := b.Connect(hAt(i, ""), true)
		if err != nil {
			t.Fatal(err)
		}
	}

	before := b.Peers()

	addrsBefore := b.Addrs()

	err := b.Connect(hAt(3, "http://c:1"), true)
	if !errors.Is(err, ErrPeerLimit) {
		t.Fatalf("err = %v", err)
	}

	if !reflect.DeepEqual(before, b.Peers()) || !reflect.DeepEqual(addrsBefore, b.Addrs()) {
		t.Fatal("book changed on refusal")
	}
	// the outbound slot is independent
	err = b.Connect(hAt(4, "http://d:1"), false)
	if err != nil {
		t.Fatal(err)
	}
}

func TestBookOutboundLimit(t *testing.T) {
	b := NewBook(Limits{MaxInbound: 1, MaxOutbound: 2}, newClock().now)
	_ = b.Connect(hAt(1, "http://a:1"), false)
	_ = b.Connect(hAt(2, "http://b:1"), false)

	before := b.Peers()

	err := b.Connect(hAt(3, "http://c:1"), false)
	if !errors.Is(err, ErrPeerLimit) {
		t.Fatalf("err = %v", err)
	}

	if !reflect.DeepEqual(before, b.Peers()) {
		t.Fatal("peers changed")
	}

	if got := b.Addrs(); !reflect.DeepEqual(got, []string{"http://a:1", "http://b:1"}) {
		t.Fatalf("addrs = %v", got)
	}

	err = b.Connect(hAt(5, ""), true)
	if err != nil {
		t.Fatal(err)
	}
}

func TestBookReconnectRefreshes(t *testing.T) {
	c := newClock()

	b := NewBook(Limits{MaxInbound: 1, MaxOutbound: 1}, c.now)

	err := b.Connect(hAt(1, "http://a:1"), true)
	if err != nil {
		t.Fatal(err)
	}

	c.add(time.Minute)

	h := hAt(1, "http://a:1")
	h.Height = 9
	h.TipHash = idB + idB
	// at the limit and even with the other direction: only refreshes
	err = b.Connect(h, false)
	if err != nil {
		t.Fatal(err)
	}

	ps := b.Peers()
	if len(ps) != 1 || !ps[0].Inbound || ps[0].Height != 9 || ps[0].TipHash != h.TipHash || !ps[0].LastSeen.Equal(c.now()) || ps[0].Addr != "http://a:1" || ps[0].NodeID != nid(1) {
		t.Fatalf("peers = %+v", ps)
	}

	err = b.Connect(hAt(2, ""), true)
	if !errors.Is(err, ErrPeerLimit) {
		t.Fatalf("second inbound: %v", err)
	}
}

func TestBookConnectAddsListenAddr(t *testing.T) {
	b := NewBook(Limits{}, nil)

	err := b.Connect(hAt(1, "http://A:1/"), true)
	if err != nil {
		t.Fatal(err)
	}

	if got := b.Addrs(); !reflect.DeepEqual(got, []string{"http://a:1"}) {
		t.Fatalf("addrs = %v", got)
	}

	if got := b.Peers()[0].Addr; got != "http://a:1" {
		t.Fatalf("addr = %q", got)
	}

	err = b.Connect(hAt(2, "bad"), true)
	if !errors.Is(err, ErrBadAddr) {
		t.Fatalf("err = %v", err)
	}

	if len(b.Peers()) != 1 {
		t.Fatal("bad addr connected")
	}
}

func TestBookAddAddrs(t *testing.T) {
	b := NewBook(Limits{MaxAddrs: 3}, nil)

	n := b.AddAddrs("http://B:1", "bad", "http://b:1/", "http://a:1")
	if n != 2 {
		t.Fatalf("added = %d", n)
	}

	if n := b.AddAddrs("http://a:1", "http://c:1", "http://d:1"); n != 1 {
		t.Fatalf("added = %d", n)
	}

	want := []string{"http://a:1", "http://b:1", "http://c:1"}
	if got := b.Addrs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("addrs = %v", got)
	}

	if n := b.AddAddrs(); n != 0 {
		t.Fatal(n)
	}
	// Addrs returns a copy
	b.Addrs()[0] = "x"
	if b.Addrs()[0] != "http://a:1" {
		t.Fatal("Addrs leaks internal state")
	}
	// a full book still lets a connected peer in, without its address
	err := b.Connect(hAt(1, "http://z:1"), true)
	if err != nil {
		t.Fatal(err)
	}

	if got := b.Addrs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("addrs = %v", got)
	}
}

func TestBookEmptyListsAreNotNil(t *testing.T) {
	b := NewBook(Limits{}, nil)
	if b.Addrs() == nil || b.Peers() == nil || b.Candidates() == nil {
		t.Fatal("want non-nil empty slices")
	}
}

func TestBookCandidates(t *testing.T) {
	b := NewBook(Limits{}, nil)
	b.AddAddrs("http://c:1", "http://a:1", "http://b:1")

	_ = b.Connect(hAt(1, "http://b:1"), false)
	if got := b.Candidates(); !reflect.DeepEqual(got, []string{"http://a:1", "http://c:1"}) {
		t.Fatalf("candidates = %v", got)
	}

	b.Disconnect(nid(1))

	if got := b.Candidates(); len(got) != 3 {
		t.Fatalf("candidates = %v", got)
	}

	b.Disconnect("unknown")

	if len(b.Addrs()) != 3 {
		t.Fatal("disconnect dropped addresses")
	}
}

func TestBookPeersSorted(t *testing.T) {
	b := NewBook(Limits{}, nil)
	for _, i := range []int{3, 1, 2} {
		_ = b.Connect(hAt(i, ""), true)
	}

	ps := b.Peers()
	if len(ps) != 3 || ps[0].NodeID != nid(1) || ps[1].NodeID != nid(2) || ps[2].NodeID != nid(3) {
		t.Fatalf("peers = %+v", ps)
	}
}

func TestBookExpire(t *testing.T) {
	c := newClock()
	b := NewBook(Limits{}, c.now)
	_ = b.Connect(hAt(1, "http://a:1"), true)

	c.add(time.Minute)

	_ = b.Connect(hAt(2, "http://b:1"), false)

	cut := c.now()
	if n := b.Expire(cut); n != 1 {
		t.Fatalf("expired = %d", n)
	}

	ps := b.Peers()
	if len(ps) != 1 || ps[0].NodeID != nid(2) {
		t.Fatalf("peers = %+v", ps)
	}

	if len(b.Addrs()) != 2 {
		t.Fatal("addresses lost")
	}
	// LastSeen equal to the cutoff is kept
	if n := b.Expire(cut); n != 0 {
		t.Fatalf("expired = %d", n)
	}

	if n := b.Expire(cut.Add(time.Nanosecond)); n != 1 {
		t.Fatalf("expired = %d", n)
	}
	// slot freed
	err := b.Connect(hAt(1, ""), true)
	if err != nil {
		t.Fatal(err)
	}
}

func TestBookConcurrent(t *testing.T) {
	b := NewBook(Limits{MaxInbound: 4, MaxOutbound: 3, MaxAddrs: 20}, nil)

	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()

			for i := range 200 {
				id := (g*7 + i) % 15
				_ = b.Connect(hAt(id, fmt.Sprintf("http://h%d:1", id)), id%2 == 0)
				b.AddAddrs(fmt.Sprintf("http://x%d:1", i))
				b.Peers()
				b.Candidates()
				b.Expire(time.Unix(0, 0))

				if i%3 == 0 {
					b.Disconnect(nid(id))
				}

				var in, out int

				for _, p := range b.Peers() {
					if p.Inbound {
						in++
					} else {
						out++
					}
				}

				if in > 4 || out > 3 || len(b.Addrs()) > 20 {
					t.Errorf("limits exceeded: %d %d %d", in, out, len(b.Addrs()))
					return
				}
			}
		}(g)
	}

	wg.Wait()
}

func TestBanRefusesNodeAndAddressUntilItExpires(t *testing.T) {
	c := newClock()
	b := NewBook(Limits{}, c.now)

	err := b.Connect(hAt(1, "http://a:1"), false)
	if err != nil {
		t.Fatal(err)
	}

	b.Ban(nid(1), "http://a:1", c.now().Add(time.Minute))

	if len(b.Peers()) != 0 {
		t.Fatal("Ban must disconnect the peer")
	}

	if !b.Banned(nid(1), "") || !b.Banned("", "http://A:1/") || b.Banned(nid(2), "http://b:1") {
		t.Fatal("Banned must match the NodeID or the normalised address, and only those")
	}

	err = b.Connect(hAt(1, "http://z:1"), false)
	if !errors.Is(err, ErrBanned) {
		t.Fatalf("banned NodeID: %v", err)
	}

	err = b.Connect(hAt(2, "http://a:1"), false)
	if !errors.Is(err, ErrBanned) {
		t.Fatalf("banned address: %v", err)
	}

	err = b.Connect(hAt(2, ""), false)
	if err != nil {
		t.Fatalf("an unrelated peer without address: %v", err)
	}

	b.AddAddrs("http://a:1", "http://b:1")

	if got := b.Candidates(); !reflect.DeepEqual(got, []string{"http://b:1"}) {
		t.Fatalf("Candidates = %v", got)
	}

	c.add(time.Minute - time.Nanosecond)

	if !b.Banned(nid(1), "") {
		t.Fatal("the ban ended early")
	}

	c.add(time.Nanosecond)

	if b.Banned(nid(1), "http://a:1") {
		t.Fatal("the ban did not end at until")
	}

	err = b.Connect(hAt(1, "http://a:1"), false)
	if err != nil {
		t.Fatalf("after the ban: %v", err)
	}

	if got := b.Candidates(); !reflect.DeepEqual(got, []string{"http://b:1"}) {
		t.Fatalf("Candidates after the ban = %v", got)
	}
}

func TestBanWithoutAddressBansTheNodeAlone(t *testing.T) {
	b := NewBook(Limits{}, nil)
	b.Ban(nid(1), "", time.Now().Add(time.Hour))
	b.Ban(nid(2), "not an address", time.Now().Add(time.Hour))

	err := b.Connect(hAt(3, ""), false)
	if err != nil {
		t.Fatalf("a peer without address must not match the empty key: %v", err)
	}

	if b.Banned(nid(3), "") {
		t.Fatal("an empty address is never banned")
	}
}

func TestBanRefusesAnAlreadyConnectedNodeThatReturns(t *testing.T) {
	b := NewBook(Limits{}, nil)

	err := b.Connect(hAt(1, ""), false)
	if err != nil {
		t.Fatal(err)
	}

	b.Ban(nid(1), "", time.Now().Add(time.Hour))

	err = b.Connect(hAt(1, ""), true)
	if !errors.Is(err, ErrBanned) {
		t.Fatalf("err = %v", err)
	}
}
