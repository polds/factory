#!/usr/bin/env bash
# Validate a G14 synthetic matrix file (sanitized key=value lines only).
set -euo pipefail

matrix="${1:-}"
if [[ -z "${matrix}" || ! -f "${matrix}" ]]; then
  echo "usage: $0 <matrix.env>" >&2
  exit 2
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
expect direct_ip deny
expect dns_connect_tricks deny
expect metadata deny
expect cross_engagement deny
echo "g14-matrix: PASS"
