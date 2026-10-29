#!/usr/bin/env bash
# Starts the image's default command and checks the controller boots and serves
# the dashboard and the auth routes. Catches startup panics (for example
# ServeMux route conflicts) that unit tests miss.
set -euo pipefail

image=${1:?usage: smoke.sh IMAGE}
name=mqtitan-smoke-$$
port=${SMOKE_PORT:-18080}

cleanup() {
  status=$?
  if [ "$status" -ne 0 ]; then docker logs "$name" 2>&1 | tail -n 30 || true; fi
  docker rm -f "$name" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker run -d --name "$name" -e MQTITAN_TOKEN=smoke-token -p "127.0.0.1:$port:8080" "$image" >/dev/null

for _ in $(seq 1 30); do
  if curl -fsS "http://127.0.0.1:$port/readyz" >/dev/null 2>&1; then ready=1; break; fi
  if [ "$(docker inspect -f '{{.State.Running}}' "$name")" != true ]; then
    echo "controller exited during startup" >&2; exit 1
  fi
  sleep 1
done
[ "${ready:-}" = 1 ] || { echo "controller not ready after 30s" >&2; exit 1; }

check() { # path expected-status [curl args...]
  local path=$1 want=$2 got
  shift 2
  got=$(curl -sS -o /dev/null -w '%{http_code}' "$@" "http://127.0.0.1:$port$path")
  [ "$got" = "$want" ] || { echo "$path returned $got, want $want" >&2; exit 1; }
}
check / 200
check /auth/session 200
check /api/v1/tests 401
check /api/v1/tests 200 -H "Authorization: Bearer smoke-token"
echo "smoke test passed"
