#!/usr/bin/env bash
set -euo pipefail

dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
checker="$dir/check-mutation.sh"

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

failed=0

assert_case() {
  local name="$1"
  local expected_exit="$2"
  local match_text="${3:-}"
  local file="$4"

  local output
  local status=0
  output="$("$checker" --report "$file" 2>&1)" || status=$?

  if [ "$status" -ne "$expected_exit" ]; then
    echo "FAIL $name"
    failed=1
    return
  fi

  if [ -n "$match_text" ] && [[ "$output" != *"$match_text"* ]]; then
    echo "FAIL $name"
    failed=1
    return
  fi

  echo "ok $name"
}

# 1. clean: KILLED, NOT VIABLE and SKIPPED mutations -> 0
cat <<'EOF' > "$tmpdir/clean.json"
{
  "go_module": "m",
  "files": [
    {
      "file_name": "a.go",
      "mutations": [
        {"type": "CONDITIONALS_NEGATION", "status": "KILLED", "line": 1, "column": 1},
        {"type": "CONDITIONALS_NEGATION", "status": "NOT VIABLE", "line": 2, "column": 2},
        {"type": "CONDITIONALS_NEGATION", "status": "SKIPPED", "line": 3, "column": 3}
      ]
    }
  ]
}
EOF
assert_case "clean" 0 "" "$tmpdir/clean.json"

# 2. lived: one LIVED plus the clean ones -> 1, and the output contains LIVED a.go:3:7
cat <<'EOF' > "$tmpdir/lived.json"
{
  "go_module": "m",
  "files": [
    {
      "file_name": "a.go",
      "mutations": [
        {"type": "CONDITIONALS_NEGATION", "status": "KILLED", "line": 1, "column": 1},
        {"type": "CONDITIONALS_NEGATION", "status": "NOT VIABLE", "line": 2, "column": 2},
        {"type": "CONDITIONALS_NEGATION", "status": "SKIPPED", "line": 3, "column": 3},
        {"type": "CONDITIONALS_NEGATION", "status": "LIVED", "line": 3, "column": 7}
      ]
    }
  ]
}
EOF
assert_case "lived" 1 "LIVED a.go:3:7" "$tmpdir/lived.json"

# 3. not covered: one NOT COVERED -> 1
cat <<'EOF' > "$tmpdir/not_covered.json"
{
  "go_module": "m",
  "files": [
    {
      "file_name": "a.go",
      "mutations": [
        {"type": "CONDITIONALS_NEGATION", "status": "NOT COVERED", "line": 4, "column": 5}
      ]
    }
  ]
}
EOF
assert_case "not covered" 1 "" "$tmpdir/not_covered.json"

# 4. timed out: one TIMED OUT -> 1
cat <<'EOF' > "$tmpdir/timed_out.json"
{
  "go_module": "m",
  "files": [
    {
      "file_name": "a.go",
      "mutations": [
        {"type": "CONDITIONALS_NEGATION", "status": "TIMED OUT", "line": 5, "column": 6}
      ]
    }
  ]
}
EOF
assert_case "timed out" 1 "" "$tmpdir/timed_out.json"

# 5. missing: path that does not exist -> 1
assert_case "missing" 1 "" "$tmpdir/nonexistent.json"

# 6. empty: empty file -> 1
touch "$tmpdir/empty.json"
assert_case "empty" 1 "" "$tmpdir/empty.json"

# 7. not json: file with hello -> 1
echo "hello" > "$tmpdir/not_json.json"
assert_case "not json" 1 "" "$tmpdir/not_json.json"

if [ "$failed" -ne 0 ]; then
  exit 1
fi
