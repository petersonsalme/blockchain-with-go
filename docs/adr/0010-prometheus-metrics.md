# ADR-0010: Prometheus metrics without a client library

Status: Superseded by [ADR-0022](0022-prometheus-client-golang.md) (ENG-221); accepted ENG-16, 2026-10-22.

## Context
- ENG-16 asks for `/metrics` in the Prometheus format with at least 3 metrics (blocks, mining time, requests).
- AGENTS.md forbids heavy dependencies without need; ADR-0006 removed gorilla/mux for the same reason.
- We only need counters, one gauge and a summary without quantiles (`_sum`/`_count`); no histograms, push or Go runtime metrics.

## Options
A. Hand-written text exposition in `internal/server` (text format 0.0.4, a mutex-guarded map for the labelled counter). Cost: ~100 lines and format tests; zero new modules.
B. `github.com/prometheus/client_golang`: industry standard, histograms and runtime metrics for free. Cost: ~10 indirect modules (`prometheus/common`, `client_model`, `procfs`, `protobuf`, `xxhash`, ...), more surface for govulncheck, and a global registry (`prometheus.DefaultRegisterer`) to avoid by hand.
C. `expvar` (stdlib): zero dependencies, but it exposes JSON at `/debug/vars`, not the Prometheus format, so it does not meet the requirement.

## Decision
Option A. The metrics live in a `metrics` type inside `server.Server` (no package state); `GET /metrics` serves `text/plain; version=0.0.4; charset=utf-8`.
- `http_requests_total{method,route,code}` (counter); `route` is the ServeMux pattern or `unmatched`, never the raw path.
- `blockchain_blocks` (gauge, `Tip().Index + 1` read at scrape time).
- `blockchain_mining_duration_seconds` (summary without quantiles: `_sum` and `_count` of the `Mine` calls that returned a block).

## Consequences
- `go.mod` does not change; nothing global, consistent with Ports & Adapters.
- No histograms/quantiles or process metrics; if they are needed, reopen this ADR and migrate to B (the metric names stay).
- The format is our responsibility: the tests check every line against the text format grammar (HELP/TYPE per family, label value escaping).
