#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
# Explicit services make PG tests fail rather than silently skip, and avoid
# launching/tearing down a container for each inherited upstream test.
vector=""
plain=""
cleanup() {
  if [[ -n "$vector" ]]; then docker rm -f "$vector" >/dev/null 2>&1 || true; fi
  if [[ -n "$plain" ]]; then docker rm -f "$plain" >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT
vector="$(docker run --rm -d -p 127.0.0.1::5432 -e POSTGRES_USER=kata -e POSTGRES_PASSWORD=forge-test-only -e POSTGRES_DB=kata_test pgvector/pgvector:pg17)"
plain="$(docker run --rm -d -p 127.0.0.1::5432 -e POSTGRES_USER=kata -e POSTGRES_PASSWORD=forge-test-only -e POSTGRES_DB=kata_test postgres:17)"
for container in "$vector" "$plain"; do
  ready=false
  for _ in {1..60}; do
    if docker exec "$container" pg_isready -U kata -d kata_test >/dev/null 2>&1; then ready=true; break; fi
    sleep 1
  done
  if [[ "$ready" != true ]]; then echo 'Test PostgreSQL did not become ready.' >&2; exit 1; fi
done
vectorAddress="$(docker port "$vector" 5432/tcp)"
plainAddress="$(docker port "$plain" 5432/tcp)"
export KATA_TEST_POSTGRES_DSN="postgres://kata:forge-test-only@$vectorAddress/kata_test?sslmode=disable"
export KATA_TEST_PLAIN_POSTGRES_DSN="postgres://kata:forge-test-only@$plainAddress/kata_test?sslmode=disable"
# macOS AF_UNIX paths have a short length limit. Long inherited TMPDIR paths
# cause unrelated upstream client/TUI tests to fail before reaching their logic.
export TMPDIR=/tmp
"$root/scripts/bootstrap-kata.sh"
(cd "$root/kata/web" && bun install --frozen-lockfile)
cd "$root/kata"
go test -v ./... -p 4 -timeout 15m
