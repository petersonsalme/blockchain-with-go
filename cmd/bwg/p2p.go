package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/p2p"
)

// Peer-to-peer configuration (ADR-0013).
const (
	seedPeersEnvVar        = "SEED_PEERS"
	p2pListenAddrEnvVar    = "P2P_LISTEN_ADDR"
	maxInboundPeersEnvVar  = "MAX_INBOUND_PEERS"
	maxOutboundPeersEnvVar = "MAX_OUTBOUND_PEERS"
	peerIntervalEnvVar     = "PEER_INTERVAL"

	// peersFileName is the file of known peer addresses inside DATA_DIR.
	peersFileName = "peers.json"
)

// newP2PNode builds the peer-to-peer node of this process from the environment.
// Its book starts with the addresses in DATA_DIR/peers.json plus SEED_PEERS; a
// corrupt file, a bad seed or a bad listen address fails the start.
func newP2PNode(c p2p.ChainView, dataDir string, logger *slog.Logger) (*p2p.Node, error) {
	seeds, err := parseSeedPeers(os.Getenv(seedPeersEnvVar))
	if err != nil {
		return nil, err
	}
	book := p2p.NewBook(p2p.Limits{
		MaxInbound:  parsePositiveInt(os.Getenv(maxInboundPeersEnvVar)),
		MaxOutbound: parsePositiveInt(os.Getenv(maxOutboundPeersEnvVar)),
	}, nil)

	opts := []p2p.Option{
		p2p.WithInterval(parsePeerInterval(os.Getenv(peerIntervalEnvVar))),
		p2p.WithLogger(logger),
	}
	if strings.TrimSpace(dataDir) != "" {
		path := filepath.Join(dataDir, peersFileName)
		saved, err := p2p.LoadAddrs(path)
		if err != nil {
			return nil, fmt.Errorf("%s: cannot load %q: %w", dataDirEnvVar, path, err)
		}
		book.AddAddrs(saved...)
		opts = append(opts, p2p.WithAddrFile(path))
	}
	book.AddAddrs(seeds...)

	id, err := p2p.NewNodeID()
	if err != nil {
		return nil, err
	}
	node, err := p2p.NewNode(c, book, id, strings.TrimSpace(os.Getenv(p2pListenAddrEnvVar)), opts...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p2pListenAddrEnvVar, err)
	}
	return node, nil
}

// parseSeedPeers parses comma-separated peer base URLs. A blank value yields
// none; any invalid item fails the whole list.
func parseSeedPeers(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var seeds []string
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		addr, err := p2p.NormalizeAddr(item)
		if err != nil {
			return nil, fmt.Errorf("%s: invalid item %q: want http(s)://host:port", seedPeersEnvVar, item)
		}
		seeds = append(seeds, addr)
	}
	return seeds, nil
}

// parsePositiveInt returns the positive integer in raw, or 0 (the default) when it is blank, invalid or not positive.
func parsePositiveInt(raw string) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// parsePeerInterval returns the duration in raw, or 0 (the default) when it is blank, invalid or not positive.
func parsePeerInterval(raw string) time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

// startDialer runs the dialer of node until ctx is done or the returned stop is called; stop waits for it.
func startDialer(ctx context.Context, node *p2p.Node) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		node.Run(ctx)
	}()
	return func() {
		cancel()
		wg.Wait()
	}
}
