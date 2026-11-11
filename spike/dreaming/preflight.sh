#!/usr/bin/env bash
# Preflight for the Dreaming #454 live run.
# Usage: spike/dreaming/preflight.sh offline|preflight|live
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${root}"

mode="${1:-}"
case "${mode}" in
  offline | preflight | live | chain | publish) ;;
  *)
    echo "usage: spike/dreaming/preflight.sh offline|preflight|live|chain|publish" >&2
    exit 2
    ;;
esac

bin="$(mktemp)"
trap 'rm -f "${bin}"' EXIT
go build -o "${bin}" ./deploy/dreaming-live/cmd/dreamingpreflight
exec "${bin}" "${mode}"
