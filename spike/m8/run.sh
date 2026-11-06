#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
export MODE="${MODE:-offline}"

echo "m8 offline invocation smoke"
python3 "$ROOT/docs/contracts/defined-factory/validate.py" | tee /tmp/m8-validate.out | grep -q 'CONTRACT FIXTURES PASS'
export FACTORY_TEST_DATABASE_URL="${FACTORY_TEST_DATABASE_URL:-postgres://postgres:t@127.0.0.1:5432/factory?sslmode=disable}"
go test -count=1 "$ROOT/internal/invocation/..." "$ROOT/internal/result/..." "$ROOT/internal/store/..." -run 'TestInsertInvocation|TestINV'
go test -count=1 "$ROOT/internal/server/..." -run 'TestM8'
echo "m8 offline PASS"
