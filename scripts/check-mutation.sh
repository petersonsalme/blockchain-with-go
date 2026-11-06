#!/usr/bin/env bash
set -euo pipefail

# Usage: scripts/check-mutation.sh [BASE]          run gremlins on the diff against BASE (default origin/master) and judge it
#        scripts/check-mutation.sh --report FILE   judge an existing gremlins JSON report (used by the test)

if command -v mise >/dev/null 2>&1; then run() { mise exec -- "$@"; }; else run() { "$@"; }; fi

judge_report() {
  local file="$1"
  if [ ! -f "$file" ] || [ ! -s "$file" ] || ! jq -e 'type == "object"' "$file" >/dev/null 2>&1; then
    echo "check-mutation: no readable gremlins report at $file"
    exit 1
  fi

  local failing
  failing="$(jq -r '.files[]? | .file_name as $f | .mutations[]? | select(.status=="LIVED" or .status=="NOT COVERED" or .status=="TIMED OUT") | "\(.status) \($f):\(.line):\(.column) \(.type)"' "$file")"

  local lived not_covered timed_out
  read -r lived not_covered timed_out < <(jq -r '
    [ .files[]?.mutations[]? ] as $all |
    [
      ($all | map(select(.status == "LIVED")) | length),
      ($all | map(select(.status == "NOT COVERED")) | length),
      ($all | map(select(.status == "TIMED OUT")) | length)
    ] | @tsv
  ' "$file")

  if [ "$lived" -gt 0 ] || [ "$not_covered" -gt 0 ] || [ "$timed_out" -gt 0 ]; then
    if [ -n "$failing" ]; then
      printf '%s\n' "$failing"
    fi
    echo "check-mutation: $lived LIVED, $not_covered NOT COVERED, $timed_out TIMED OUT on the changed lines"
    exit 1
  fi

  echo "check-mutation: OK"
  exit 0
}

if [ "${1:-}" = "--report" ]; then
  report_file="${2:?--report needs a file}"
  judge_report "$report_file"
fi

base="${1:-origin/master}"
cd "$(git rev-parse --show-toplevel)"
mb="$(git merge-base "$base" HEAD)"

go_changes="$( { git diff --name-only --diff-filter=ACMRT "$mb"; git ls-files --others --exclude-standard; } | grep '\.go$' || true )"
if [ -z "$go_changes" ]; then
  echo "check-mutation: no Go change against $base"
  exit 0
fi

out="$(mktemp)"
report="$(mktemp)"
trap 'rm -f "$out" "$report"' EXIT
rm -f "$report"

run go clean -testcache
run go run github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0 unleash --timeout-coefficient 20 -E 'storagetest|consensustest' --diff "$mb" -o "$report" > "$out" 2>&1 || true

grep -v '^ *SKIPPED ' "$out" || true

judge_report "$report"
