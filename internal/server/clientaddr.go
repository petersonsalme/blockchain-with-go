package server

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
)

// TrustedProxiesEnvVar lists the proxies whose forwarded client address is trusted (ADR-0025).
const TrustedProxiesEnvVar = "TRUSTED_PROXIES"

// ErrInvalidTrustedProxy is returned by ParseTrustedProxies for an entry that is neither an address nor a prefix.
var ErrInvalidTrustedProxy = errors.New("server: invalid trusted proxy")

// ParseTrustedProxies reads a TRUSTED_PROXIES value: comma-separated IP addresses or CIDR prefixes (ADR-0025).
// Spaces around each entry and empty entries are skipped; a blank value yields nil.
// A prefix is masked; a bare address becomes a single-address prefix, with IPv4-mapped IPv6 unmapped.
func ParseTrustedProxies(raw string) ([]netip.Prefix, error) {
	var prefixes []netip.Prefix

	for field := range strings.SplitSeq(raw, ",") {
		entry := strings.TrimSpace(field)
		if entry == "" {
			continue
		}

		if strings.Contains(entry, "/") {
			prefix, err := netip.ParsePrefix(entry)
			if err != nil {
				return nil, fmt.Errorf("%w: %q", ErrInvalidTrustedProxy, entry)
			}

			prefixes = append(prefixes, prefix.Masked())

			continue
		}

		addr, err := netip.ParseAddr(entry)
		if err != nil {
			return nil, fmt.Errorf("%w: %q", ErrInvalidTrustedProxy, entry)
		}

		addr = addr.Unmap()
		prefixes = append(prefixes, netip.PrefixFrom(addr, addr.BitLen()))
	}

	return prefixes, nil
}

// clientAddr returns the address the rate limiter keys on (ADR-0025). Forwarded addresses are
// trusted only when the direct peer is inside a trusted prefix; the walk runs from the right and
// stops at the first untrusted entry, so a client cannot forge its address from the left.
func clientAddr(remoteAddr string, forwardedFor []string, trusted []netip.Prefix) string {
	if len(trusted) == 0 {
		return remoteAddr
	}

	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}

	peer, err := netip.ParseAddr(host)
	if err != nil || !isTrusted(peer, trusted) {
		return remoteAddr
	}

	var entries []string

	for _, value := range forwardedFor {
		for field := range strings.SplitSeq(value, ",") {
			if entry := strings.TrimSpace(field); entry != "" {
				entries = append(entries, entry)
			}
		}
	}

	if len(entries) == 0 {
		return remoteAddr
	}

	last := peer.WithZone("").Unmap()

	for _, entry := range slices.Backward(entries) {
		addr, err := netip.ParseAddr(entry)
		if err != nil {
			return last.String()
		}

		addr = addr.WithZone("").Unmap()
		if !isTrusted(addr, trusted) {
			return addr.String()
		}

		last = addr
	}

	return last.String()
}

// isTrusted reports whether addr, with IPv4-mapped IPv6 unmapped, lies inside one of the prefixes.
func isTrusted(addr netip.Addr, trusted []netip.Prefix) bool {
	addr = addr.WithZone("").Unmap()

	for _, prefix := range trusted {
		if prefix.Contains(addr) {
			return true
		}
	}

	return false
}
