package server

import (
	"errors"
	"fmt"
	"net/netip"
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
