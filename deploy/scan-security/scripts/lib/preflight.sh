#!/usr/bin/env bash
# Shared helpers for synthetic scan-security probes.
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

g14_deny_default() {
  # Default synthetic matrix expects deny-by-default firewall posture documented in probes README.
  [[ -f "${SCAN_SECURITY_MATRIX:-}" ]] || return 0
  grep -q 'deny_by_default=true' "${SCAN_SECURITY_MATRIX}"
}

g14_no_docker_socket_mount() {
  local sample="${SCAN_SECURITY_SAMPLE_TASK:-}"
  [[ -z "${sample}" ]] && return 0
  ! grep -q '/var/run/docker.sock' "${sample}"
}

g14_metadata_denied() {
  local matrix="${SCAN_SECURITY_MATRIX:-}"
  [[ -z "${matrix}" ]] && return 0
  grep -q 'metadata=deny' "${matrix}"
}

g14_matrix_probe() {
  local matrix="${SCAN_SECURITY_MATRIX:-}"
  [[ -z "${matrix}" ]] && return 0
  bash "${REPO_ROOT}/deploy/scan-security/probes/g14-matrix.sh" "${matrix}" >/dev/null
}

g14_direct_ip_denied() {
  local matrix="${SCAN_SECURITY_MATRIX:-}"
  [[ -z "${matrix}" ]] && return 0
  grep -q 'direct_ip=deny' "${matrix}"
}

g14_dns_connect_denied() {
  local matrix="${SCAN_SECURITY_MATRIX:-}"
  [[ -z "${matrix}" ]] && return 0
  grep -q 'dns_connect_tricks=deny' "${matrix}"
}

g18_resolver_matrix() {
  command -v go >/dev/null 2>&1 || return 0
  (cd "${REPO_ROOT:-.}" && go test ./internal/secrets/... -run 'TestResolve' -count=1 >/dev/null)
}

g18_rotation_redaction() {
  command -v go >/dev/null 2>&1 || return 0
  (cd "${REPO_ROOT:-.}" && go test ./internal/secrets/... -run 'TestRedactionRotation' -count=1 >/dev/null)
}
