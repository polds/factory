#!/usr/bin/env bash
# Offline scan-security checks for CI (no real credentials, no live backend claim).
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
export REPO_ROOT="${root}"
export SCAN_SECURITY_MATRIX="${root}/deploy/scan-security/matrices/g14-default.env"
export SCAN_SECURITY_SAMPLE_TASK="${root}/deploy/scan-security/fixtures/sample-scanner-task.yaml"

echo "scan-security-check: go test internal/secrets"
go test "${root}/internal/secrets/..." -race -count=1

echo "scan-security-check: preflight"
SCAN_SECURITY_MATRIX="${SCAN_SECURITY_MATRIX}" \
  SCAN_SECURITY_SAMPLE_TASK="${SCAN_SECURITY_SAMPLE_TASK}" \
  REPO_ROOT="${REPO_ROOT}" \
  bash "${root}/deploy/scan-security/preflight.sh" all

echo "scan-security-check: PASS"
