#!/usr/bin/env bash
# Lists the fuzz targets of the module as a compact JSON array of {"pkg": "./<dir>", "target": "Fuzz..."},
# sorted by pkg then target. .github/workflows/fuzz.yml builds its nightly matrix from it, so a new
# Fuzz function is fuzzed without editing the workflow.
# Usage: scripts/fuzz-targets.sh
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
module="$(sed -n 's/^module //p' go.mod)"
if command -v mise >/dev/null 2>&1; then run() { mise exec -- "$@"; }; else run() { "$@"; }; fi
run go test -list '^Fuzz' ./... | awk -v mod="$module" '
  /^Fuzz/ { names[++n] = $1; next }
  /^ok[ \t]/ { pkg = $2; sub("^" mod, ".", pkg); for (i = 1; i <= n; i++) printf "%s\t%s\n", pkg, names[i]; n = 0 }
' | LC_ALL=C sort | jq -cRn '[inputs | split("\t") | {pkg: .[0], target: .[1]}]'
