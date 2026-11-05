#!/usr/bin/env bash
# Working gate: tests (race) and lint only on the Go packages the change touches.
# Usage: scripts/gate-diff.sh [--base REF] [--dependents] [--mutation] [--list]
#   --base REF     compare against REF (default: origin/master); committed, staged, unstaged and untracked files count
#   --dependents   also test the packages that import a changed package (use it when a shared package changes)
#   --mutation     also run gremlins on the diff (root, no package path), capped at 6G of memory when systemd-run exists
#   --list         print the packages and stop
# This is the loop gate for implementers. It does NOT replace the full gate (go test -race ./... with the
# coverage floor, govulncheck, build): run those once, at the end (AGENTS.md, "CI quality gates").
set -euo pipefail

base="origin/master"
dependents=0
mutation=0
list_only=0
while [ $# -gt 0 ]; do
  case "$1" in
    --base) base="${2:?--base needs a ref}"; shift 2 ;;
    --dependents) dependents=1; shift ;;
    --mutation) mutation=1; shift ;;
    --list) list_only=1; shift ;;
    *) echo "usage: scripts/gate-diff.sh [--base REF] [--dependents] [--mutation] [--list]" >&2; exit 2 ;;
  esac
done

cd "$(git rev-parse --show-toplevel)"
module="$(sed -n 's/^module //p' go.mod)"

# go, golangci-lint and gofmt come from mise and are not on PATH (AGENTS.md).
if command -v mise >/dev/null 2>&1; then run() { mise exec -- "$@"; }; else run() { "$@"; }; fi

mb="$(git merge-base "$base" HEAD)"
files="$( { git diff --name-only --diff-filter=ACMRT "$mb"; git ls-files --others --exclude-standard; } | sort -u )"
if [ -z "$files" ]; then echo "gate-diff: no changes against $base"; exit 0; fi

# go.mod and go.sum change every package; openapi.yaml and client/ change the contract tests in internal/server.
all=0
pkgs=""
while IFS= read -r f; do
  case "$f" in
    go.mod|go.sum) all=1 ;;
    openapi.yaml|client/*) pkgs="$pkgs ./internal/server ./client" ;;
    *.go) d="$(dirname "$f")"; [ -d "$d" ] && pkgs="$pkgs ./$d" ;;
  esac
done <<EOF
$files
EOF

if [ "$all" = 1 ]; then
  pkgs="./..."
else
  valid=""
  for p in $(printf '%s\n' $pkgs | sed 's#^\./\./#./#' | sort -u); do
    if run go list "$p" >/dev/null 2>&1; then valid="$valid $p"; fi
  done
  pkgs="${valid# }"
  if [ "$dependents" = 1 ] && [ -n "$pkgs" ]; then
    changed="$(for p in $pkgs; do run go list "$p"; done | sort -u)"
    more="$(run go list -f '{{.ImportPath}} {{join .Deps " "}}' ./... | awk -v changed="$changed" '
      BEGIN { n = split(changed, c, "\n"); for (i = 1; i <= n; i++) want[c[i]] = 1 }
      { for (i = 2; i <= NF; i++) if ($i in want) { print $1; break } }' | sed "s#^$module#.#")"
    pkgs="$(printf '%s\n%s\n' "$(printf '%s\n' $pkgs)" "$more" | sed '/^$/d' | sort -u | tr '\n' ' ')"
  fi
fi

if [ -z "$pkgs" ]; then echo "gate-diff: changes touch no Go package (docs only); nothing to run"; exit 0; fi
echo "gate-diff: base $(git rev-parse --short "$mb") | packages:" $pkgs
[ "$list_only" = 1 ] && exit 0

fail=0
echo "--- go test -race"
run go test -race -count=1 $pkgs || fail=1
echo "--- golangci-lint run"
run golangci-lint run $pkgs || fail=1

if [ "$mutation" = 1 ]; then
  echo "--- gremlins --diff $mb (zero LIVED or NOT COVERED on changed lines)"
  g=(go run github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0 unleash --timeout-coefficient 20 -E 'storagetest|consensustest' --diff "$mb")
  out="$(mktemp)"
  if command -v systemd-run >/dev/null 2>&1; then
    systemd-run --user --scope --quiet -p MemoryMax=6G mise exec -- "${g[@]}" >"$out" 2>&1 || fail=1
  else
    run "${g[@]}" >"$out" 2>&1 || fail=1
  fi
  # SKIPPED lines are every mutant outside the diff (thousands); keep the rest.
  grep -v '^ *SKIPPED ' "$out" || true
  killed="$(sed -n 's/^Killed: \([0-9]*\), Lived: \([0-9]*\), Not covered: \([0-9]*\).*/\1 \2 \3/p' "$out")"
  rm -f "$out"
  if [ -z "$killed" ]; then echo "gate-diff: could not read the gremlins summary"; fail=1
  else
    set -- $killed
    if [ "$2" != 0 ] || [ "$3" != 0 ]; then echo "gate-diff: $2 LIVED and $3 NOT COVERED mutants on the changed lines"; fail=1; fi
    if [ "$1" = 0 ] && [ "$2" = 0 ] && [ "$3" = 0 ]; then echo "gate-diff: no mutant on the changed lines (only comments or non-mutable code?)"; fi
  fi
fi

if [ "$fail" = 0 ]; then echo "gate-diff: OK (working gate only: run the full gate once before delivery)"; else echo "gate-diff: FAIL"; fi
exit "$fail"
