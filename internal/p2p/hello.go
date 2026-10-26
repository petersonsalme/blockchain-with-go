// Package p2p holds the peer-to-peer building blocks of ADR-0013: the Hello
// message, the handshake rules, the peer book and the persisted addresses.
// It depends on internal/domain and the standard library only.
package p2p

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// ProtocolVersion is the only protocol version this node speaks.
const ProtocolVersion = 1

var (
	ErrBadHello            = errors.New("p2p: malformed hello")
	ErrIncompatibleVersion = errors.New("p2p: incompatible protocol version")
	ErrGenesisMismatch     = errors.New("p2p: genesis mismatch")
	ErrSelf                = errors.New("p2p: connection to self")
	ErrPeerLimit           = errors.New("p2p: peer limit reached")
	ErrBadAddr             = errors.New("p2p: invalid peer address")
)

// Hello is the handshake message a node sends to introduce itself.
type Hello struct {
	Version     int    `json:"version"`
	NodeID      string `json:"node_id"`
	GenesisHash string `json:"genesis_hash"`
	Height      int    `json:"height"`
	TipHash     string `json:"tip_hash"`
	ListenAddr  string `json:"listen_addr"`
}

// Check applies the handshake checks of ADR-0013 to the remote hello, in
// order: malformed, version, genesis, self.
func Check(local, remote Hello) error {
	if remote.Height < 0 || !isHex(remote.NodeID, 32) || !isHex(remote.GenesisHash, 64) || !isHex(remote.TipHash, 64) {
		return ErrBadHello
	}

	if remote.ListenAddr != "" {
		if _, err := NormalizeAddr(remote.ListenAddr); err != nil {
			return ErrBadHello
		}
	}

	if remote.Version != local.Version {
		return ErrIncompatibleVersion
	}

	if remote.GenesisHash != local.GenesisHash {
		return ErrGenesisMismatch
	}

	if remote.NodeID == local.NodeID {
		return ErrSelf
	}

	return nil
}

// isHex reports whether s is exactly n lowercase hexadecimal characters.
func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}

	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}

	return true
}

// NewNodeID returns 16 random bytes as 32 lowercase hex characters.
func NewNodeID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}

	return hex.EncodeToString(b[:]), nil
}

// NormalizeAddr validates a peer base URL and returns it as
// scheme://host:port with scheme and host in lower case.
func NormalizeAddr(raw string) (string, error) {
	if strings.ContainsAny(raw, "?#") {
		return "", ErrBadAddr
	}

	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || (u.Path != "" && u.Path != "/") {
		return "", ErrBadAddr
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", ErrBadAddr
	}

	host := strings.ToLower(u.Hostname())
	if !validHost(host) {
		return "", ErrBadAddr
	}

	for _, c := range u.Port() {
		if c < '0' || c > '9' {
			return "", ErrBadAddr
		}
	}

	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return "", ErrBadAddr
	}

	return scheme + "://" + net.JoinHostPort(host, strconv.Itoa(port)), nil
}

// validHost accepts an IP literal without zone or a DNS-style name.
func validHost(h string) bool {
	if h == "" {
		return false
	}

	if strings.Contains(h, ":") {
		return !strings.Contains(h, "%") && net.ParseIP(h) != nil
	}

	for i := 0; i < len(h); i++ {
		c := h[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '.' {
			return false
		}
	}

	return true
}
