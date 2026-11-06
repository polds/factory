#!/usr/bin/env bash
# Synthetic egress matrix checker (sanitized output only).
set -euo pipefail

matrix="${1:-}"
if [[ -z "${matrix}" || ! -f "${matrix}" ]]; then
  echo "usage: $0 <matrix.env>" >&2
  exit 2
fi
# shellcheck disable=SC1090
source "${matrix}"
[[ "${metadata:-deny}" == "deny" ]] || exit 1
echo "egress-matrix: PASS"
