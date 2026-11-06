#!/usr/bin/env bash
# Shared helpers for synthetic scan-security probes.
# A missing input is a failed check. Never return success for an absent file or tool.
set -euo pipefail

run_check() {
  local name="$1"
  if "$name"; then
    echo "PASS ${name}"
    return 0
  fi
  echo "FAIL ${name}"
  return 1
}

require_matrix() {
  [[ -n "${SCAN_SECURITY_MATRIX:-}" && -f "${SCAN_SECURITY_MATRIX}" ]]
}

g14_deny_default() {
  require_matrix || return 1
  grep -q 'deny_by_default=true' "${SCAN_SECURITY_MATRIX}"
}

g14_no_docker_socket_mount() {
  local sample="${SCAN_SECURITY_SAMPLE_TASK:-}"
  [[ -n "${sample}" && -f "${sample}" ]] || return 1
  if grep -q '/var/run/docker.sock' "${sample}"; then
    return 1
  fi
  if grep -q 'hostPath:' "${sample}"; then
    return 1
  fi
  if grep -q 'all-factory-secrets' "${sample}"; then
    return 1
  fi
  return 0
}

g14_metadata_denied() {
  require_matrix || return 1
  grep -q 'metadata=deny' "${SCAN_SECURITY_MATRIX}"
}

g14_matrix_probe() {
  require_matrix || return 1
  bash "${REPO_ROOT}/deploy/scan-security/probes/g14-matrix.sh" "${SCAN_SECURITY_MATRIX}" >/dev/null
}

g14_direct_ip_denied() {
  require_matrix || return 1
  grep -q 'direct_ip=deny' "${SCAN_SECURITY_MATRIX}"
}

g14_dns_connect_denied() {
  require_matrix || return 1
  grep -q 'dns_connect_tricks=deny' "${SCAN_SECURITY_MATRIX}"
}

g18_resolver_matrix() {
  command -v go >/dev/null 2>&1 || return 1
  (cd "${REPO_ROOT:-.}" && go test ./internal/secrets/... -run 'TestResolve' -count=1 >/dev/null)
}

g18_rotation_redaction() {
  command -v go >/dev/null 2>&1 || return 1
  (cd "${REPO_ROOT:-.}" && go test ./internal/secrets/... -run 'TestRedactionRotation' -count=1 >/dev/null)
}
