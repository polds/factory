#!/bin/sh
# factoryd loads the definition before defsync runs, so an empty directory
# never reaches the git poller. Seed that directory from the same remote.
# defsync then replaces it. The parent has to be a writable mount so the
# replace can rename the directory.
set -eu
dest="${FACTORY_DEFINITION_PATH:-/var/lib/factory/definition}"
url="${FACTORY_DEFINITION_GIT_URL:-}"
ref="${FACTORY_DEFINITION_GIT_REF:-main}"
if [ ! -f "${dest}/factory.yaml" ]; then
  if [ -z "$url" ]; then
    echo "FACTORY_DEFINITION_GIT_URL is required to seed the definition" >&2
    exit 1
  fi
  rm -rf "$dest"
  git clone --depth 1 --branch "$ref" "$url" "$dest"
fi
exec /factoryd "$@"
