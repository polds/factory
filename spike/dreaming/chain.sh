#!/usr/bin/env bash
# Chain job entry. The model key is a 0600 file, never an environment variable.
# This process execs the driver, so it does not remain as an ancestor.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
file="${FACTORY_MODEL_KEY_FILE:?}"
if [[ ! -f "${file}" ]]; then
  echo "model key file missing" >&2
  exit 1
fi
exec 3<"${file}"
rm -f "${file}"
export FACTORY_MODEL_KEY_FD=3
export FACTORY_CHAIN_OUT="${FACTORY_CHAIN_OUT:-${root}/spike/dreaming/.work/chain-out}"
cd "${root}"
exec "${root}/spike/dreaming/preflight.sh" chain
