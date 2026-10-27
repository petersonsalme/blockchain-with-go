#!/usr/bin/env bash
# Starts the playground (docs/playground.md) from an empty chain and funds a
# wallet by mining on node1. Usage: WALLET_PASSWORD=... scripts/playground.sh [BLOCKS]
set -euo pipefail

blocks="${1:-3}"
: "${WALLET_PASSWORD:?set WALLET_PASSWORD (it is never printed or stored)}"

cd "$(dirname "$0")/.."
dir=.playground
mkdir -p "$dir"

if [ -f "$dir/miner.key" ] && [ ! -f "$dir/miner.addr" ]; then
  echo "playground: $dir/miner.key exists but $dir/miner.addr is missing; restore miner.addr or delete $dir/miner.key to create a new miner wallet" >&2
  exit 1
fi

go build -o "$dir/bwg" ./cmd/bwg

if [ ! -f "$dir/miner.key" ]; then
  WALLET_KEYSTORE="$dir/miner.key" "$dir/bwg" wallet new >"$dir/miner.addr"
fi
addr="$(cat "$dir/miner.addr")"
printf 'MINER_ADDRESS=%s\n' "$addr" >"$dir/compose.env"

docker compose -p bwg-playground --env-file "$dir/compose.env" \
  -f docker-compose.yml -f compose.playground.yml up -d --build

for port in 18081 18082 18083; do
  for _ in $(seq 60); do
    curl -fsS "http://127.0.0.1:$port/health" >/dev/null 2>&1 && break
    sleep 1
  done
  curl -fsS "http://127.0.0.1:$port/health" >/dev/null
done

"$dir/bwg" miner --node http://127.0.0.1:18081 --count "$blocks"
sleep 3 # gossip reaches node2 and node3 (PEER_INTERVAL=1s)

echo "miner address: $addr"
for port in 18081 18082 18083; do
  echo "balance on 127.0.0.1:$port: $("$dir/bwg" wallet balance --node "http://127.0.0.1:$port" "$addr")"
done
