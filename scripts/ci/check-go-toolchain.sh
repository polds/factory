#!/usr/bin/env bash
# The runner's Go must be the toolchain line, and golangci-lint's build Go
# must be at least the module language. A newer laptop toolchain hides both.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${root}"

toolchain="$(awk '/^toolchain / { print $2; exit }' go.mod)"
language="$(awk '/^go / { print $2; exit }' go.mod)"
if [[ -z "${toolchain}" || -z "${language}" ]]; then
  echo "go.mod is missing a go or toolchain directive" >&2
  exit 1
fi

got="$(go version | awk '{ print $3 }')"
if [[ "${got}" != "${toolchain}" ]]; then
  echo "go version ${got} does not match ${toolchain}" >&2
  exit 1
fi

built="$(golangci-lint version | sed -n 's/.*built with go\([0-9][0-9.]*\).*/\1/p')"
if [[ -z "${built}" ]]; then
  echo "could not read the Go version that built golangci-lint" >&2
  exit 1
fi

python3 - "${language}" "${built}" <<'PY'
import sys

def parse(raw):
    parts = [int(piece) for piece in raw.split(".") if piece != ""]
    return (parts + [0, 0, 0])[:3]

language, built = sys.argv[1:]
if parse(built) < parse(language):
    sys.exit(f"golangci-lint was built with go{built}, older than module go {language}")
PY

echo "go ${got} matches ${toolchain}; golangci-lint built with go${built}"
