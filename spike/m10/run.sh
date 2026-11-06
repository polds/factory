#!/usr/bin/env bash
# Offline native-scanner acceptance. MODE=live is refused.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT}"

if [[ "${MODE:-offline}" != "offline" ]]; then
  echo "m10 supports offline only" >&2
  exit 1
fi

if [[ -n "${LINEAR_API_KEY:-}" || -n "${LINEAR_API_TOKEN:-}" || -n "${LINEAR_WEBHOOK_SECRET:-}" || -n "${LINEAR_TEAM_ID:-}" ]]; then
  echo "m10 linear-off: refusing Linear config" >&2
  exit 1
fi

echo "m10 offline acceptance"
python3 "${ROOT}/docs/contracts/defined-factory/validate.py" | tee /tmp/m10-validate.out | grep -q 'CONTRACT FIXTURES PASS'

bash "${ROOT}/spike/m10/preflight.sh" offline

echo "m10 synthetic-security separate"
bash "${ROOT}/scripts/ci/scan-security-check.sh"

run_pkg() {
  local pkg="$1"
  local run="$2"
  local out
  local code
  set +e
  out="$(go test -count=1 "${pkg}" -run "${run}")"
  code=$?
  set -e
  printf '%s\n' "${out}"
  if [[ "${code}" -ne 0 || "${out}" == *"no tests to run"* ]]; then
    echo "m10: ${pkg} ${run} failed" >&2
    exit 1
  fi
}

run_pkg ./deploy/native-scan 'TestM10'
run_pkg ./internal/outbound 'TestBridge'
run_pkg ./internal/normalize/linear 'TestSyntheticNativeBrief'
run_pkg ./internal/server 'TestRouteBridgeEcho|TestReportThroughBridge'

bin="$(mktemp)"
trap 'rm -f "${bin}"' EXIT
go build -o "${bin}" ./deploy/native-scan/cmd/m10accept
chmod 0755 "${bin}"
"${bin}"
echo "m10 offline PASS"
