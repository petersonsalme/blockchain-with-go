# ADR-0006: Route HTTP requests with the standard library ServeMux

Status: Accepted (board, 2026-10-20, ENG-90)

## Context
- `internal/server` used gorilla/mux v1.8.0 only for method matching and single-segment path variables; there is no middleware.
- Go 1.22+ `ServeMux` supports method patterns, wildcards (`r.PathValue`) and exact-match `{$}`; the module targets Go 1.26.
- AGENTS.md forbids heavy dependencies without need.

## Options
A. stdlib `ServeMux`: zero dependencies, handlers unchanged. B. Chi v5: net/http-compatible, but swaps one dependency for another with no used feature. C. Gin: changes handler signatures, package-level mode state, ~34 module requirements, wrong method → 404 by default.

## Decision
Option A.
- `Server.Handler()` builds an `http.ServeMux` with method patterns; the root uses `/{$}`.
- Each path also registers a method-less fallback that answers 405 with an empty body and an `Allow` header.
- `gorilla/mux` is removed from `go.mod`.

## Consequences
- No third-party router; handlers, `Handler() http.Handler` and existing tests are unchanged.
- 405 responses now include `Allow` (RFC 9110 §15.5.6); `HEAD` on GET routes is served by the GET handler; an escaped slash in a path variable (`/block/a%2Fb`) reaches the handler and yields 400 instead of 404.
- Conflicting patterns panic at registration, caught by any test that builds `Handler()`.
