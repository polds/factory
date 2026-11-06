#!/usr/bin/env bash
# Synthetic egress matrix checker (sanitized output only).
# Same closed set as g14-matrix.sh: a broad gateway or missing denial fails.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
matrix="${1:-}"
if [[ -z "${matrix}" || ! -f "${matrix}" ]]; then
  echo "egress-matrix: FAIL missing matrix" >&2
  exit 1
fi
bash "${root}/probes/g14-matrix.sh" "${matrix}"
