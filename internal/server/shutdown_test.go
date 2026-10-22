package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

// shutdownChain blocks Mine until its ctx is cancelled and holds Tip until released.
type shutdownChain struct {
	brokenChain
	mining      chan struct{}
	healthSeen  chan struct{}
	releaseTip  chan struct{}
	mineCtxDone chan struct{}
}

func (c *shutdownChain) Mine(ctx context.Context, _ []domain.Transaction) (*domain.Block, error) {
	close(c.mining)
	<-ctx.Done()
	close(c.mineCtxDone)
	return nil, ctx.Err()
}

func (c *shutdownChain) Tip() (*domain.Block, error) {
	close(c.healthSeen)
	<-c.releaseTip
	return &domain.Block{Index: 3, Hash: strings.Repeat("a", 64)}, nil
}

func TestShutdownCancelsMiningAndDrainsRequests(t *testing.T) {
	fake := &shutdownChain{
		mining:      make(chan struct{}),
		healthSeen:  make(chan struct{}),
		releaseTip:  make(chan struct{}),
		mineCtxDone: make(chan struct{}),
	}
	srv, err := NewServer(fake, Config{MiningTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	nodeCtx, cancelNode := context.WithCancel(context.Background())
	defer cancelNode()
	hs := srv.HTTPServer(nodeCtx, l.Addr().String())
	serveErr := make(chan error, 1)
	go func() { serveErr <- hs.Serve(l) }()
	base := "http://" + l.Addr().String()

	type result struct {
		status int
		body   string
		err    error
	}
	do := func(method, path, body string) <-chan result {
		ch := make(chan result, 1)
		go func() {
			req, err := http.NewRequest(method, base+path, strings.NewReader(body))
			if err != nil {
				ch <- result{err: err}
				return
			}
			resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
			if err != nil {
				ch <- result{err: err}
				return
			}
			defer func() { _ = resp.Body.Close() }() // test client; close error is irrelevant
			b, err := io.ReadAll(resp.Body)
			ch <- result{status: resp.StatusCode, body: string(b), err: err}
		}()
		return ch
	}
	wait := func(ch <-chan struct{}, what string) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s", what)
		}
	}

	tx := domain.Transaction{
		Timestamp: time.Unix(1, 0).UTC(),
		Outputs:   []domain.TxOut{{Amount: 1, Address: "x"}},
	}
	payload, err := json.Marshal(Message{Transactions: []domain.Transaction{tx}})
	if err != nil {
		t.Fatal(err)
	}
	mine := do(http.MethodPost, "/", string(payload))
	wait(fake.mining, "Mine to start")
	health := do(http.MethodGet, "/health", "")
	wait(fake.healthSeen, "GET /health to reach the chain")

	start := time.Now()
	cancelNode()
	wait(fake.mineCtxDone, "Mine ctx to be cancelled")

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- hs.Shutdown(shutdownCtx) }()

	// The in-flight health request must still be pending: Shutdown drains, it does not cut.
	select {
	case err := <-shutdownDone:
		t.Fatalf("Shutdown returned %v while GET /health was in flight", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(fake.releaseTip)

	if err := <-shutdownDone; err != nil {
		t.Fatalf("Shutdown = %v, want nil", err)
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("Serve = %v, want ErrServerClosed", err)
	}
	if elapsed := time.Since(start); elapsed >= 2*time.Second {
		t.Fatalf("cancel to shutdown took %v, want < 2s", elapsed)
	}

	m := <-mine
	if m.err != nil || m.status != http.StatusRequestTimeout {
		t.Fatalf("POST / = %d, err %v; want 408", m.status, m.err)
	}
	var e struct{ Error string }
	if err := json.Unmarshal([]byte(m.body), &e); err != nil || e.Error != "mining cancelled" {
		t.Fatalf("POST / body = %q, want error \"mining cancelled\"", m.body)
	}

	h := <-health
	if h.err != nil || h.status != http.StatusOK {
		t.Fatalf("GET /health = %d, err %v; want 200", h.status, h.err)
	}
	var hr struct {
		Status string
		Height int
	}
	if err := json.Unmarshal([]byte(h.body), &hr); err != nil || hr.Status != "ok" || hr.Height != 3 {
		t.Fatalf("GET /health body = %q (%v), want a complete ok response", h.body, err)
	}
}
