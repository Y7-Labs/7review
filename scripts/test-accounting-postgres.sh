#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
name="7review-accounting-test-$$"
cleanup() { docker rm -f "$name" >/dev/null 2>&1 || true; }
trap cleanup EXIT
docker run --rm -d --name "$name" \
  -e POSTGRES_USER=budget_test -e POSTGRES_PASSWORD=budget_test_local_only \
  -e POSTGRES_DB=budget_test -p 127.0.0.1::5432 postgres:16-alpine >/dev/null
ready=false
for ((i=0; i<120; i++)); do
  if docker exec "$name" pg_isready -h 127.0.0.1 -U budget_test -d budget_test >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 0.25
done
if [[ "$ready" != true ]]; then
  printf 'PostgreSQL test container did not become ready\n' >&2
  docker logs "$name" >&2
  exit 1
fi
address="$(docker port "$name" 5432/tcp)"
export SEVENREVIEW_TEST_POSTGRES="postgres://budget_test:budget_test_local_only@${address}/budget_test?sslmode=disable"
GOCACHE="${GOCACHE:-/tmp/7review-go-build}" go test -count=1 "$@" ./agent/accounting ./agent/orchestrator
