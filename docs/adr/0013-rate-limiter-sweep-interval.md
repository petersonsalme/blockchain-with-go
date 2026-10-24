# ADR-0013: Rate limiter evicts idle buckets at most once per refill window

Status: Accepted

## Context

The per-IP token bucket in `internal/server/ratelimit.go` (ENG-36) evicts idle buckets lazily: on every request, under the limiter's single mutex, `allow` walks the whole `buckets` map and deletes every other bucket whose refilled tokens reached `maxTokens`. The cost is O(n) per request in the number of tracked IPs. The second Risk core review of ENG-36 (ENG-93) measured it: 1,000 IPs ⇒ 14.7 µs/op, 10,000 IPs ⇒ 164 µs/op. Since the mutex is global, that cost also serializes every concurrent request.

Facts that bound the design:
- With `N` requests per minute, `maxTokens = N` and `refillRate = N/60` per second, so any bucket refills from empty to full in exactly 60 s, whatever `N` is (the refill window).
- Evicting a full bucket is safe: a new bucket starts at `maxTokens - 1` after its first request, which is exactly what a full bucket gives. Evicting a bucket that is not full would let that IP bypass the limit.

## Options

1. **Keep the per-request scan.** Cost: none in code; 164 µs of mutex time per request at 10,000 IPs, growing linearly.
2. **Throttle the scan to once per refill window (chosen).** Add `lastSweep time.Time`; `allow` runs the existing scan only when `now - lastSweep >= 60s` (or when the clock went back past `lastSweep`) and then sets `lastSweep = now`. Cost: ~10 lines; one O(n) scan per minute; an idle bucket can live up to 2 refill windows (≤ 120 s) instead of 60 s.
3. **Throttle to once every `60s/N`** (the original F1 proposal). Same code as 2, but the period shrinks as `N` grows: at `RATE_LIMIT=6000` it scans every 10 ms, so the cost under load still grows with `N`, while keeping idle buckets for at most one extra token time.
4. **Ordered eviction (list or heap by `lastCheck`).** Evict from the front while the oldest bucket has been idle ≥ 60 s: O(1) amortized and exact. Cost: a second structure kept consistent with the map on every request, more code and more invariants to test for a learning project.

## Decision

Option 2.

- The eviction predicate does not change: a bucket other than the requesting host is deleted when `tokens + elapsed*refillRate >= maxTokens`.
- The scan runs when `now.Sub(lastSweep) >= sweepInterval || now.Before(lastSweep)`, with `sweepInterval = time.Minute` (a package-level `const`); after the scan, `lastSweep = now`. The zero `lastSweep` makes the first request scan (an empty map).
- A clock that goes back never blocks eviction: the scan runs again and `lastSweep` follows the clock.

## Consequences

- Between scans `allow` is O(1); the O(n) scan happens at most once per minute of the limiter's clock (plus once per backward clock jump).
- The map holds at most the IPs seen in the last two refill windows (~120 s) instead of one; memory per bucket is unchanged.
- The rate guarantee does not change: only full buckets are evicted, so for any IP the number of non-429 answers in any interval T stays ≤ `N + floor(T·N/60s)`.
- Tests that assumed a scan on every request must advance the clock by at least `sweepInterval` before expecting an eviction.
