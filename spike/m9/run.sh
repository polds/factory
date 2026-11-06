#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
MODE="${MODE:-offline}"
if [[ "$MODE" != "offline" ]]; then
  echo "m9 supports offline only" >&2
  exit 1
fi
export MODE

echo "m9 offline native runtime"
python3 "$ROOT/docs/contracts/defined-factory/validate.py" | tee /tmp/m9-validate.out | grep -q 'CONTRACT FIXTURES PASS'
export FACTORY_TEST_DATABASE_URL="${FACTORY_TEST_DATABASE_URL:-postgres://postgres:t@127.0.0.1:5432/factory?sslmode=disable}"
go test -count=1 "$ROOT/internal/definition" -run 'TestNative|TestReportsTo'
go test -count=1 "$ROOT/internal/coordination/..."
go test -count=1 "$ROOT/internal/store" -run 'TestNative'
go test -count=1 "$ROOT/internal/server" -run 'TestNative'
echo "m9 offline PASS"
