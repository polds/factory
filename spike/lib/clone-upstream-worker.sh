#!/usr/bin/env bash
# Clone the unmodified oz-agent-worker.
# The default commit is ARG WORKER_REF in deploy/worker/Dockerfile.
# WORKER_REF overrides it (branch, tag, or commit). WORKER_REPO overrides the URL.
# An existing DEST/.git checkout is reused as-is.
set -euo pipefail

if [[ $# -ne 1 || -z "${1}" ]]; then
  printf 'usage: clone-upstream-worker.sh DEST\n' >&2
  exit 2
fi

dest="$1"
if [[ -d "$dest/.git" ]]; then
  exit 0
fi
if [[ -e "$dest" ]]; then
  printf 'refusing to clone into existing %s\n' "$dest" >&2
  exit 1
fi

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
# shellcheck source=spike/lib/upstream-worker.sh
. "$HERE/upstream-worker.sh"

repo="${WORKER_REPO:-https://github.com/warpdotdev/oz-agent-worker.git}"
ref="$(upstream_worker_ref "$ROOT")"
if [[ -z "$ref" || "$ref" == -* ]]; then
  printf 'refusing upstream worker ref\n' >&2
  exit 2
fi

tmp="$(mktemp -d)"
# shellcheck disable=SC2329 # invoked by the EXIT trap
cleanup_tmp() {
  rm -rf "$tmp"
}
trap cleanup_tmp EXIT

export GIT_TERMINAL_PROMPT=0
git -C "$tmp" init -q
git -C "$tmp" remote add origin "$repo"
git -C "$tmp" fetch --depth 1 origin "$ref"
git -C "$tmp" checkout --detach --quiet FETCH_HEAD

mkdir -p "$(dirname "$dest")"
mv "$tmp" "$dest"
trap - EXIT
