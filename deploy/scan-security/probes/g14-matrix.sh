#!/usr/bin/env bash
# Validate a G14 synthetic matrix file (sanitized key=value lines only).
set -euo pipefail

matrix="${1:-}"
if [[ -z "${matrix}" || ! -f "${matrix}" ]]; then
  echo "g14-matrix: FAIL missing matrix" >&2
  exit 1
fi

expect() {
  local key="$1"
  local want="$2"
  local got
  got="$(grep -E "^${key}=" "${matrix}" | tail -1 | cut -d= -f2-)"
  if [[ "${got}" != "${want}" ]]; then
    echo "g14-matrix: FAIL ${key}=${got:-<missing>} want ${want}" >&2
    exit 1
  fi
}

expect deny_by_default true
expect gateway_routes forge,control-plane
expect direct_ip deny
expect dns_connect_tricks deny
expect metadata deny
expect cross_engagement deny
expect attribution task_identity
echo "g14-matrix: PASS"
