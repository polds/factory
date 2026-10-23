#!/usr/bin/env bash
# Smoke test for the unmodified upstream worker image.
#
# Builds factory-upstream-worker:dev from this directory and checks:
#   * the entrypoint is oz-agent-worker and accepts --web-socket-url
#   * go version -m stamps vcs.revision to the Dockerfile pin and vcs.modified=false
#
# Requires: docker (running), go.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
# shellcheck source=spike/lib/upstream-worker.sh
. "$ROOT/spike/lib/upstream-worker.sh"
IMAGE="factory-upstream-worker:dev"
REV="$(upstream_worker_pin "$ROOT")"
BIN_PATH="/oz-agent-worker"

say() { printf '\n==> %s\n' "$*"; }

command -v docker >/dev/null 2>&1 || {
  echo "docker not found" >&2
  exit 2
}
command -v go >/dev/null 2>&1 || {
  echo "go not found" >&2
  exit 2
}
docker info >/dev/null 2>&1 || {
  echo "docker daemon is not reachable" >&2
  exit 2
}

say "Build ${IMAGE}"
docker build -t "$IMAGE" "$HERE"

say "CLI help and --web-socket-url"
# WebSocketURL is a kong hidden flag, so `oz-agent-worker --help`
# omits --web-socket-url. Passing the flag with no value still names it in
# the error (`--web-socket-url: expected string value`). That line is how
# this smoke asserts the binary exposes the flag.
help="$(docker run --rm "$IMAGE" --help 2>&1)"
printf '%s\n' "$help" | grep -F 'oz-agent-worker' >/dev/null
flag_out="$(docker run --rm "$IMAGE" --web-socket-url 2>&1 || true)"
printf '%s\n' "$flag_out" | grep -F -- '--web-socket-url' >/dev/null
if printf '%s\n' "$flag_out" | grep -F 'unknown flag --web-socket-url' >/dev/null; then
  echo "worker rejected --web-socket-url" >&2
  printf '%s\n' "$flag_out" >&2
  exit 1
fi
echo "cli accepts --web-socket-url"

say "Embedded VCS revision"
cid=""
tmp=""
# shellcheck disable=SC2329 # invoked by the EXIT trap
cleanup() {
  if [[ -n "${cid}" ]]; then
    docker rm -f "$cid" >/dev/null 2>&1 || true
  fi
  if [[ -n "${tmp}" ]]; then
    rm -rf "$tmp"
  fi
}
trap cleanup EXIT
cid="$(docker create "$IMAGE")"
tmp="$(mktemp -d)"
docker cp "${cid}:${BIN_PATH}" "$tmp/oz-agent-worker"
info="$(go version -m "$tmp/oz-agent-worker")"
printf '%s\n' "$info" | grep -F "vcs.revision=${REV}" >/dev/null
printf '%s\n' "$info" | grep -F 'vcs.modified=false' >/dev/null
echo "vcs.revision ${REV} vcs.modified=false"
