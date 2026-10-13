#!/bin/sh
# The worker extracts this image's root onto a volume mounted at /agent, then
# execs /agent/entrypoint.sh inside the task container. Resolve the binary next
# to this script so the same file works at /entrypoint.sh and at /agent/entrypoint.sh.
here=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
exec "$here/factory-harness" "$@"
