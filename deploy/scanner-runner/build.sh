#!/bin/sh
# Build the public scanner stub. This script does not clone or download a plugin.
set -eu
root=$(CDPATH='' cd -- "$(dirname "$0")/../.." && pwd)
cd "$root"
if [ -n "${SCANNER_PRIVATE_URL:-}" ]; then
  echo "scanner-runner: refusing to fetch a private scanner" >&2
  exit 1
fi
if [ "$#" -ne 1 ]; then
  echo "usage: deploy/scanner-runner/build.sh <output>" >&2
  exit 2
fi
GOOS=linux GOARCH=amd64 go build -trimpath -o "$1" ./deploy/scanner-runner/stub
