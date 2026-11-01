# ADR-0025: Client address from X-Forwarded-For behind trusted proxies

Status: Accepted (ENG-294)

## Context
- The rate limiter keys buckets on `r.RemoteAddr` and ignores `X-Forwarded-For`, `Forwarded` and `X-Real-IP` (`internal/server/ratelimit.go`, pinned by `ratelimit_test.go`).
- Behind the Caddy of `compose.tls.yml` ([ADR-0024](0024-caddy-tls-reverse-proxy.md)), every public client arrives from Caddy's address, so `RATE_LIMIT` is one cap shared by all public traffic and one noisy client can throttle everyone.
- Forwarded headers are written by the client unless a proxy you trust has rewritten them. Trusting them from any peer lets every client choose its own bucket and bypass the limit.
- Caddy's `reverse_proxy` without its own `trusted_proxies` ignores an incoming `X-Forwarded-For` and sets it to the connecting client's address. It does not send `Forwarded`. nginx and most load balancers append to `X-Forwarded-For` in the same way.

## Options
1. **Right-to-left `X-Forwarded-For` with a CIDR allowlist (chosen).** A `TRUSTED_PROXIES` list parsed with stdlib `net/netip`. The header is read only when the connecting peer is inside the list. The walk goes right to left, skips trusted hops and stops at the first untrusted address. This is Caddy's `trusted_proxies` and `client_ip_headers` rule and the usual "rightmost untrusted" rule. Cost: about 60 lines and a table of tests. No module.
2. **RFC 7239 `Forwarded` only.** Standard and structured (`for=`, quoted IPv6, obfuscated identifiers). Cost: a quoted-string parser, and Caddy and nginx do not send it by default, so the shipped stack would need extra proxy config to benefit.
3. **Keep `RemoteAddr` and rate limit at the proxy.** Cost: Caddy needs a third-party plugin (`caddy-ratelimit`) and a custom image build. The node limiter stays a global cap behind the proxy, and direct clients and peers keep the node limiter anyway, so there would be two limiters with different rules.

## Decision
Option 1.
- `TRUSTED_PROXIES`: comma-separated CIDRs or bare IPs (a bare IP is a single-address prefix), read in `cmd/bwg` and passed as `server.Config.TrustedProxies []netip.Prefix`. Blank means no trusted proxy. That is the default and keeps today's behaviour exactly. An invalid entry stops the node at startup.
- Key rule: if the `RemoteAddr` host is not inside a trusted prefix, the key is today's key. Otherwise all `X-Forwarded-For` lines are joined into one list and walked right to left. The first entry that is not trusted is the key. A malformed entry stops the walk, and the key is the last trusted address checked. If every entry is trusted, the key is the leftmost one. IPv4-mapped IPv6 addresses are unmapped before the check and in the key.
- `Forwarded` and `X-Real-IP` stay ignored.
- `compose.tls.yml` pins Caddy to a fixed address on `backend` and trusts only that `/32`. Trusting the whole subnet would also trust node2 and the dashboard.
- ADR-0021 criteria: hand-written on the stdlib, no module is added to `go.mod`, and no global state is added.

## Consequences
- Behind a trusted proxy, `RATE_LIMIT` is per public client again. Clients that reach the node directly (dashboard, peers) keep their `RemoteAddr` buckets.
- A wrong `TRUSTED_PROXIES` is a security setting. Trusting a peer that does not overwrite `X-Forwarded-For` lets its clients choose their bucket. README and SECURITY.md say so.
- The access log has no client address field, so nothing changes there.
- The tests that pin "forwarded headers are ignored" keep pinning the default (empty list).
