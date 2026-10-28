# ADR-0023: read-only network dashboard as `bwg dashboard`

Status: Accepted

## Context
Each node serves an explorer for itself only. Its page cannot read another node: the CSP is `connect-src 'self'` and nodes send no CORS headers. There is no view of the network as a whole (agreement, a stopped or lagging node, forks, peers). Options: (A) CORS on the nodes and a browser that reads every node, which changes node code and opens cross-origin reads; (B) a separate Go service that reads the nodes server-side and renders HTML; (C) a single-page app with a JavaScript build step. B was chosen in the roadmap design note. For B, a new binary (`cmd/dashboard`) and a `bwg` subcommand were compared.

## Decision
- The dashboard is the `bwg dashboard` subcommand; ADR-0016 holds (one binary, `cmd/bwg` is the only composition root). Its code lives in `internal/dashboard`, which imports only the standard library: it talks to nodes over HTTP like any outside client. A test parses the package's imports and fails on any other.
- It reads only the nodes named in `NODES`, with `GET`, on a fixed allowlist of paths with validated parameters; it follows no redirect, has a per-request timeout (1.5 s) and a body size limit (4 MiB). A request names a node, never a host. It never relays a node body: it renders its own HTML with `html/template`, which escapes every node value. It serves only `GET`/`HEAD`.
- Front end: htmx 2.0.11 (sha256 `d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717`, `dist/htmx.min.js` of the npm package `htmx.org@2.0.11`) copied into `internal/dashboard/static/`, hand-written CSS, no Node, no npm, no build step, no external request. htmx runs with `allowEval`, `allowScriptTags` and `includeIndicatorStyles` off, so the CSP (same as the explorer's) holds: no inline script, no inline style attribute, no `hx-on`. Charts are server-rendered SVG.
- Panels refresh by polling every 2 s (`hx-trigger`); node health is read by one background poller, so load on the nodes does not grow with browser tabs.
- It runs in compose under profile `dashboard` on host port 18080.

## Consequences
- The node image and every release of `bwg` carry the dashboard (templates, CSS and htmx, a few tens of KB); there is one Dockerfile and one goreleaser build.
- No node code changes; the per-node explorer stays.
- Upgrading htmx is replacing one file and updating this ADR.
- No live updates: `/events` is not read; switching to it is a later decision.
- Rejected: `cmd/dashboard`, a second composition root and a second build for an isolation the stdlib-only import rule already gives.
