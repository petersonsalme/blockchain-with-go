#!/usr/bin/env bash
# Builds the image and checks that `bwg node` runs as a non-root user and
# answers GET /health, in memory and with DATA_DIR on a volume.
# Usage: scripts/docker-smoke.sh [image-tag]   (SMOKE_PORT overrides 18080)
set -euo pipefail

image="${1:-bwg:smoke}"
port="${SMOKE_PORT:-18080}"
volume="bwg-smoke-$$"
cid=""

cleanup() {
  if [ -n "$cid" ]; then docker rm -f "$cid" >/dev/null 2>&1 || true; fi
  docker volume rm "$volume" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker build -t "$image" .

user="$(docker image inspect -f '{{.Config.User}}' "$image")"
case "$user" in
  "" | 0 | 0:* | root | root:*)
    echo "image runs as root (User=\"$user\")" >&2
    exit 1
    ;;
esac

# check_health NAME [docker run args...] starts the image and waits for GET /health.
check_health() {
  local name="$1" body=""
  shift
  cid="$(docker run -d -p "127.0.0.1:${port}:8080" "$@" "$image")"
  for _ in $(seq 1 30); do
    if body="$(curl -fsS "http://127.0.0.1:${port}/health" 2>/dev/null)"; then
      case "$body" in
        *'"status": "ok"'*)
          echo "$name: $body"
          docker rm -f "$cid" >/dev/null
          cid=""
          return 0
          ;;
      esac
    fi
    sleep 1
  done
  echo "$name: GET /health did not answer ok within 30s" >&2
  docker logs "$cid" >&2 || true
  return 1
}

check_health memory
check_health data-dir -e DATA_DIR=/data -v "${volume}:/data"
