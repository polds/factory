#!/usr/bin/env bash
# Validate a G14 synthetic matrix file (sanitized key=value lines only).
set -euo pipefail

matrix="${1:-}"
if [[ -z "${matrix}" || ! -f "${matrix}" ]]; then
  echo "usage: $0 <matrix.env>" >&2
  exit 2
fi
# shellcheck disable=SC1090
source "${matrix}"

required=(
  deny_by_default
  direct_ip
  dns_connect_tricks
  metadata
  cross_engagement
)
for key in "${required[@]}"; do
  if [[ -z "${!key:-}" ]]; then
    echo "g14-matrix: FAIL missing ${key}" >&2
    exit 1
  fi
done

if [[ "${deny_by_default}" != "true" ]]; then
  echo "g14-matrix: FAIL deny_by_default" >&2
  exit 1
fi
for key in direct_ip dns_connect_tricks metadata cross_engagement; do
  if [[ "${!key}" != "deny" ]]; then
    echo "g14-matrix: FAIL ${key}=${!key}" >&2
    exit 1
  fi
done
echo "g14-matrix: PASS"
