#!/usr/bin/env bash
# Fails when total statement coverage is below the floor.
# Usage: scripts/check-coverage.sh [profile] [floor-percent]
# Test-helper packages (*test) hold shared contract suites that are only run
# from other packages, so their own profile is 0% and would skew the total.
set -euo pipefail

profile="${1:-coverage.out}"
floor="${2:-90.0}"

filtered="$(mktemp)"
trap 'rm -f "$filtered"' EXIT
grep -Ev '/(consensustest|storagetest)/' "$profile" > "$filtered"

total="$(go tool cover -func="$filtered" | awk '/^total:/ { sub(/%/, "", $NF); print $NF }')"
if [ -z "$total" ]; then
  echo "could not read total coverage from $profile" >&2
  exit 2
fi

if awk -v t="$total" -v f="$floor" 'BEGIN { exit !(t < f) }'; then
  echo "FAIL: total coverage ${total}% is below the floor of ${floor}%" >&2
  exit 1
fi
echo "OK: total coverage ${total}% meets the floor of ${floor}%"
