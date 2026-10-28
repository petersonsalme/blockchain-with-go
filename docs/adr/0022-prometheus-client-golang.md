# ADR-0022: Prometheus metrics with client_golang and an observability compose profile

Status: Accepted (ENG-221). Supersedes [ADR-0010](0010-prometheus-metrics.md).

## Context
- [ADR-0010](0010-prometheus-metrics.md) writes the text exposition by hand to avoid about ten modules and a global registry, and says to migrate to `client_golang`, keeping the metric names, once histograms or runtime metrics are needed.
- [ADR-0021](0021-professional-grade-study-reference.md) puts observability on the side of market tools: the chain core is written by hand, the tooling around it uses industry standards.
- `/metrics` exposes `http_requests_total{method,route,code}`, `blockchain_blocks` and `blockchain_mining_duration_seconds`; there are no Go runtime or process metrics, and nothing in the repository scrapes or graphs them.

## Options
A. **Keep the hand-written exposition.** Zero modules. Cost: runtime and process metrics, and any future histogram, are written and format-tested by hand; it teaches nothing about blockchains.
B. **`github.com/prometheus/client_golang` with a custom registry (chosen).** The reference Go client, used by Prometheus itself, Kubernetes and Geth-style dashboards. Cost: eight new modules (below) and care to avoid its global registry.
C. **OpenTelemetry metrics SDK with the Prometheus exporter.** Vendor-neutral. Cost: many more modules, a second abstraction over the same exposition, and it only pays off with tracing, which is out of scope.

## Decision
- `internal/server` builds one `*prometheus.Registry` per `Server`, creates the families with `promauto.With(reg)` and serves it with `promhttp.HandlerFor(reg, promhttp.HandlerOpts{})` on `GET /metrics`. The global registry, `promhttp.Handler()` and the package-level `promauto` constructors are not used; no package state is added.
- The names and labels of ADR-0010 stay: `http_requests_total{method,route,code}` (counter; `route` is the ServeMux pattern or `unmatched`, unknown methods are `OTHER`), `blockchain_blocks` (gauge from a collector that reads `Tip()` at scrape time and omits the family when it fails) and `blockchain_mining_duration_seconds` (summary without quantiles).
- The registry also carries the Go runtime (`go_*`) and process (`process_*`) collectors. No histogram is added now: a mining histogram would need a new name next to the summary, and none is needed by the dashboard.
- The response is the library's text format 0.0.4 (`text/plain; version=0.0.4; charset=utf-8; escaping=underscores`); labels and families come out sorted by name. OpenMetrics negotiation stays off.
- How the criteria of ADR-0021 hold: (1) it observes the HTTP adapter, never the core; (2) it is the reference Prometheus client, actively maintained, Apache-2.0; (3) it enters through `internal/server` (the HTTP adapter) with no global state; (4) `govulncheck` is clean; (5) a correct exposition of runtime and process metrics by hand teaches nothing about blockchains.
- Modules added to `go.mod` (client_golang v1.25.0): direct `github.com/prometheus/client_golang`; indirect `github.com/prometheus/client_model` v0.6.3, `github.com/prometheus/common` v0.72.0, `github.com/prometheus/procfs` v0.22.0, `github.com/beorn7/perks` v1.0.1, `github.com/cespare/xxhash/v2` v2.3.0, `github.com/munnerz/goautoneg` v0.0.0-20191010083416-a7dc8b61c822, `google.golang.org/protobuf` v1.36.12; `github.com/google/go-cmp` moves from v0.5.8 to v0.7.0.
- An opt-in `observability` profile in `compose.playground.yml` runs Prometheus (`prom/prometheus:v3.15.1`, scraping `node1..node3:8080` every 5 s) and Grafana (`grafana/grafana:12.4.14`, anonymous Viewer, a provisioned data source and the committed `bwg-network` dashboard), published on `127.0.0.1:19090` and `127.0.0.1:13000` only. Both run as separate services; neither is linked into `bwg`.

## Consequences
- `go.mod` grows by the modules above; govulncheck covers them on every CI run.
- Clients that compared the `Content-Type` exactly or relied on the label order see a change; names, types and label sets do not change. `openapi.yaml` and the generated client follow the new media type.
- Histograms, alert rules, tracing and a separate metrics port can be added later on the same registry without reopening this decision; a new family must not reuse an existing name.
- The base `docker-compose.yml` and the e2e test do not start Prometheus or Grafana.
