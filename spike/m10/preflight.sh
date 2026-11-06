#!/usr/bin/env bash
# No-network preflight for offline and live. Live exits 1.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
mode="${1:-}"
if [[ "${mode}" != "offline" && "${mode}" != "live" ]]; then
  echo "usage: spike/m10/preflight.sh offline|live" >&2
  exit 2
fi

bin="$(mktemp)"
trap 'rm -f "${bin}"' EXIT
go build -o "${bin}" "${ROOT}/deploy/native-scan/cmd/m10preflight"
chmod 0755 "${bin}"
"${bin}" --mode "${mode}" --root "${ROOT}"
