#!/usr/bin/env bash
# Brings up compose.tls.yml (project bwg-tls) and checks the proxy setup of ADR-0024:
# TLS with Caddy's local CA, the route allowlist, the response headers and that the
# nodes publish no port. Always runs `down -v` on exit.
# Usage: scripts/tls-smoke.sh   (needs docker compose, curl; ports 18443 and 18444 free)
set -euo pipefail

cd "$(dirname "$0")/.."

project="bwg-tls"
dc() { docker compose -p "$project" -f compose.tls.yml "$@"; }
certdir="$(mktemp -d)"
cacert="$certdir/root.crt"
failed=0

cleanup() {
  dc down -v >/dev/null 2>&1 || true
  rm -rf "$certdir"
}
trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  failed=1
}

# expect_status LABEL METHOD URL STATUS
expect_status() {
  local got
  got="$(curl -sS --cacert "$cacert" --path-as-is -o /dev/null -w '%{http_code}' -X "$3" "$4" 2>&1 || true)"
  if [ "$got" = "$2" ]; then echo "ok   $1 -> $got"; else fail "$1: want $2, got $got"; fi
}

# expect_blocked METHOD URL: the proxy answers 403 with its plain-text body.
expect_blocked() {
  local out status body
  out="$(curl -sS --cacert "$cacert" --path-as-is -X "$1" -w '\n%{http_code}' "$2" 2>&1 || true)"
  status="${out##*$'\n'}"
  body="${out%$'\n'*}"
  if [ "$status" = "403" ] && [ "$body" = "blocked by proxy" ]; then
    echo "ok   $1 $2 -> 403 blocked by proxy"
  else
    fail "$1 $2: want 403 'blocked by proxy', got $status '$body'"
  fi
}

dc up -d --build

# Caddy creates its local CA on first start; wait for the root certificate.
for _ in $(seq 1 30); do
  if dc cp caddy:/data/caddy/pki/authorities/local/root.crt "$cacert" >/dev/null 2>&1; then break; fi
  sleep 1
done
[ -s "$cacert" ] || { echo "caddy root certificate not found" >&2; dc logs caddy >&2 || true; exit 1; }

# Wait for the node behind the proxy.
for _ in $(seq 1 30); do
  if curl -fsS --cacert "$cacert" -o /dev/null https://localhost:18443/health 2>/dev/null; then break; fi
  sleep 1
done

echo "== exposed routes (TLS verified with the Caddy root certificate)"
expect_status "node /health" 200 GET https://localhost:18443/health
expect_status "explorer" 200 GET https://localhost:18443/explorer/
expect_status "dashboard /" 200 GET https://localhost:18444/
head_status="$(curl -sS --cacert "$cacert" -I -o /dev/null -w '%{http_code}' https://localhost:18443/health)"
if [ "$head_status" = "200" ]; then echo "ok   HEAD /health -> 200"; else fail "HEAD /health: want 200, got $head_status"; fi

echo "== response headers"
for url in https://localhost:18443/health https://localhost:18444/; do
  headers="$(curl -sS --cacert "$cacert" -D - -o /dev/null "$url" | tr -d '\r')"
  grep -qi '^strict-transport-security: max-age=31536000$' <<<"$headers" || fail "$url: missing Strict-Transport-Security"
  grep -qi '^x-content-type-options: nosniff$' <<<"$headers" || fail "$url: missing X-Content-Type-Options: nosniff"
  grep -qi '^referrer-policy: no-referrer$' <<<"$headers" || fail "$url: missing Referrer-Policy"
  if grep -qi '^server:' <<<"$headers"; then fail "$url: Server header present"; fi
  echo "ok   headers $url"
done

echo "== internal routes blocked at the proxy"
expect_blocked GET https://localhost:18443/p2p/peers
expect_blocked POST https://localhost:18443/p2p/hello
expect_blocked POST https://localhost:18443/tx
expect_blocked POST https://localhost:18443/mine
expect_blocked GET https://localhost:18443/metrics
expect_blocked GET https://localhost:18443/
expect_blocked POST https://localhost:18443/health
expect_blocked GET https://localhost:18443/block/../p2p/peers
expect_blocked GET "https://localhost:18443/block/%2e%2e/p2p/peers"
expect_blocked POST https://localhost:18444/
expect_blocked PUT https://localhost:18444/

echo "== nodes are not reachable from outside"
for svc in node1 node2 dashboard; do
  out="$(dc port "$svc" 8080 2>/dev/null || true)"
  case "$out" in
    "" | ":0") echo "ok   $svc publishes no port" ;;
    *) fail "$svc publishes $out" ;;
  esac
done
published="$(dc ps --format '{{.Publishers}}' | grep -o '[0-9.:]* *-> *[0-9a-f.:]*:[0-9]*' | grep -v -- '-> :0' || true)"
echo "published: $(tr '\n' ' ' <<<"$published")"
if [ "$(grep -c . <<<"$published")" -ne 2 ] || ! grep -q '127.0.0.1:18443' <<<"$published" || ! grep -q '127.0.0.1:18444' <<<"$published"; then
  fail "only 127.0.0.1:18443 and 127.0.0.1:18444 may be published"
fi

echo "== node2 sees node1 as a peer (read through the dashboard)"
peers=""
for _ in $(seq 1 30); do
  peers="$(curl -sS --cacert "$cacert" https://localhost:18444/peers | awk '/id="node2"/ { on = 1 } on' || true)"
  if grep -q 'http://node1:8080' <<<"$peers"; then break; fi
  sleep 1
done
if grep -q 'http://node1:8080' <<<"$peers"; then echo "ok   node2 lists http://node1:8080 as a peer"; else fail "node2 does not list node1 as a peer"; fi
health="$(curl -sS --cacert "$cacert" https://localhost:18443/health)"
echo "node1 /health: $health"

if [ "$failed" -ne 0 ]; then
  echo "tls-smoke: FAILED" >&2
  exit 1
fi
echo "tls-smoke: OK"
