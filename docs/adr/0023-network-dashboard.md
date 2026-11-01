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
- `static/app.css` is the design export, copied as is. Templates use only classes it defines: `TestTemplateClassesExistInCSS` parses the embedded templates and the stylesheet and fails on any other, checking a class built by a template action for every value the action can take. The only exemption is the state hook `banner-<state>`, which tests and `docs/playground.md` read.
- No copy-hash button: the dashboard ships no script of its own, htmx is the only one. A hash cut with "…" is cut by CSS (`text-overflow`), so its text is the full value and select and copy gives the whole hash; the full value is also in `title`, and the block, transaction, address and contract pages show it in a `.hash-full` element that one click selects whole.

## Consequences
- The node image and every release of `bwg` carry the dashboard (templates, CSS and htmx, a few tens of KB); there is one Dockerfile and one goreleaser build.
- No node code changes; the per-node explorer stays.
- Upgrading htmx is replacing one file and updating this ADR.
- No live updates: `/events` is not read; switching to it is a later decision.
- Rejected: `cmd/dashboard`, a second composition root and a second build for an isolation the stdlib-only import rule already gives.
- A design re-export that renames or drops a class the templates use fails `go test`; classes it defines that no template uses are only logged.
- Rejected: a copy button in a same-origin script under `/static`. `script-src 'self'` allows it, but `navigator.clipboard` works only in a secure context (localhost or the TLS stack of ADR-0024, not plain HTTP on a LAN host), CI has no browser to test it, and its style is not in the design export. Revisit if a later design ships the control.
