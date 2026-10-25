package p2p

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// maxResponseBytes caps what the dialer reads from a peer response.
const maxResponseBytes = 1 << 20

// Run does a Round at once and then one every interval, until ctx is done.
func (n *Node) Run(ctx context.Context) {
	ticker := time.NewTicker(n.interval)
	defer ticker.Stop()
	for {
		n.Round(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Round runs one dialer round of ADR-0013: it renews the hello with every
// outbound peer, dials known addresses up to MaxOutbound, collects the
// addresses of the outbound peers, expires stale peers and saves the addresses.
// Failures are logged; none stops the node.
func (n *Node) Round(ctx context.Context) {
	local, err := n.Hello()
	if err != nil {
		n.log.Error("p2p: cannot build the local hello", "error", err)
		return
	}
	n.renew(ctx, local)
	n.dial(ctx, local)
	n.collect(ctx)
	n.book.Expire(n.now().Add(-3 * n.interval))
	if n.addrFile != "" {
		if err := SaveAddrs(n.addrFile, n.book.Addrs()); err != nil {
			n.log.Error("p2p: cannot save peers", "path", n.addrFile, "error", err)
		}
	}
}

func (n *Node) outbound() []PeerInfo {
	var out []PeerInfo
	for _, p := range n.book.Peers() {
		if !p.Inbound {
			out = append(out, p)
		}
	}
	return out
}

// renew says hello again to every outbound peer; one that fails or no longer
// passes Check is disconnected.
func (n *Node) renew(ctx context.Context, local Hello) {
	for _, p := range n.outbound() {
		if ctx.Err() != nil {
			return
		}
		remote, err := n.handshake(ctx, p.Addr, local)
		if err != nil {
			n.log.Warn("p2p: peer dropped", "addr", p.Addr, "error", err)
			n.book.Disconnect(p.NodeID)
			continue
		}
		n.book.Disconnect(p.NodeID) // the peer may have restarted with a new identity
		if err := n.book.Connect(remote, false); err != nil {
			n.log.Warn("p2p: peer dropped", "addr", p.Addr, "error", err)
		}
	}
}

// dial says hello to known addresses that no peer uses, until MaxOutbound.
func (n *Node) dial(ctx context.Context, local Hello) {
	count := len(n.outbound())
	for _, addr := range n.book.Candidates() {
		if count >= n.book.MaxOutbound() || ctx.Err() != nil {
			return
		}
		if addr == n.listenAddr {
			continue
		}
		remote, err := n.handshake(ctx, addr, local)
		if err != nil {
			n.log.Warn("p2p: dial failed", "addr", addr, "error", err)
			continue
		}
		if err := n.book.Connect(remote, false); err != nil {
			n.log.Warn("p2p: dial failed", "addr", addr, "error", err)
			continue
		}
		count++
	}
}

// collect adds the addresses every outbound peer knows to the book.
func (n *Node) collect(ctx context.Context) {
	for _, p := range n.outbound() {
		if ctx.Err() != nil {
			return
		}
		addrs, err := n.fetchAddrs(ctx, p.Addr)
		if err != nil {
			n.log.Warn("p2p: cannot fetch peers", "addr", p.Addr, "error", err)
			continue
		}
		for _, a := range addrs {
			if norm, err := NormalizeAddr(a); err == nil && norm != n.listenAddr {
				n.book.AddAddrs(norm)
			}
		}
	}
}

// handshake posts the local hello to addr and returns the response, which must pass Check.
func (n *Node) handshake(ctx context.Context, addr string, local Hello) (Hello, error) {
	body, err := json.Marshal(local)
	if err != nil {
		return Hello{}, err
	}
	var remote Hello
	if err := n.do(ctx, http.MethodPost, addr+"/p2p/hello", body, &remote); err != nil {
		return Hello{}, err
	}
	if err := Check(local, remote); err != nil {
		return Hello{}, err
	}
	remote.ListenAddr = addr // an outbound peer is known by the address we dialled
	return remote, nil
}

func (n *Node) fetchAddrs(ctx context.Context, addr string) ([]string, error) {
	var resp struct {
		Addrs []string `json:"addrs"`
	}
	if err := n.do(ctx, http.MethodGet, addr+"/p2p/peers", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Addrs, nil
}

// do sends one request and decodes a 200 JSON response of at most maxResponseBytes into out.
func (n *Node) do(ctx context.Context, method, url string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }() // read-only response body
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s %s: status %d", method, url, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > maxResponseBytes {
		return errors.New("response too large")
	}
	return json.Unmarshal(raw, out)
}
