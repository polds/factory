#!/bin/sh
# Stub harness for the M0 spike — the minimum that satisfies the invocation half
# of Harness Contract v1 (docs/05-harness-contract.md).
#
# It runs inside the *task* container (not the sidecar image), mounted at
# /agent. It does no agent work. Its job is to prove that the control plane's
# instructions actually arrive at the place where an agent would run:
#
#   - the recovered oz flag vocabulary reaches argv
#   - env_vars from task_assignment reach the process environment
#   - the working directory is /workspace
#   - NDJSON events can be emitted on the contract's path
#
# Exit codes follow the contract: 0 success, 20 configuration invalid.

set -eu

EVENTS_DIR=/var/run/factory
EVENTS="$EVENTS_DIR/events.ndjson"
mkdir -p "$EVENTS_DIR"

emit() {
  # ts, type, then caller-supplied JSON fields
  printf '{"ts":"%s","type":"%s"%s}\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$1" "${2:-}" >>"$EVENTS"
}

echo "[harness] M0 stub harness starting"
echo "[harness] argv: $*"
echo "[harness] cwd: $(pwd)"

emit "turn.started" ',"turn":1,"model_id":"m0-stub-model"'

# --- the control plane's instructions arrived in argv -----------------------
task_id=""
server_root=""
sandboxed="no"
while [ $# -gt 0 ]; do
  case "$1" in
    --task-id)
      task_id="${2:-}"
      shift 2
      ;;
    --server-root-url)
      server_root="${2:-}"
      shift 2
      ;;
    --sandboxed)
      sandboxed="yes"
      shift
      ;;
    # Harness Contract v1: unknown flags are ignored with a warning, never
    # rejected, so a newer control plane cannot deadlock an older harness.
    --*)
      if [ $# -ge 2 ] && case "${2:-}" in --*) false ;; *) [ -n "${2:-}" ] ;; esac then
        echo "[harness] warning: ignoring unknown flag $1 ${2}"
        shift 2
      else
        echo "[harness] warning: ignoring unknown flag $1"
        shift
      fi
      ;;
    *)
      echo "[harness] warning: ignoring positional argument $1"
      shift
      ;;
  esac
done

if [ -z "$task_id" ]; then
  echo "[harness] FATAL: --task-id was not supplied" >&2
  emit "terminal" ',"status":"failed","summary":"missing --task-id"'
  exit 20
fi
echo "[harness] parsed task_id=$task_id server_root=$server_root sandboxed=$sandboxed"

# --- env injection ----------------------------------------------------------
if [ -z "${FACTORY_M0_MARKER:-}" ]; then
  echo "[harness] FATAL: FACTORY_M0_MARKER missing — env_vars did not reach the task" >&2
  emit "terminal" ',"status":"failed","summary":"env injection failed"'
  exit 20
fi
if [ "$FACTORY_M0_MARKER" != "$task_id" ]; then
  echo "[harness] FATAL: marker $FACTORY_M0_MARKER does not match task $task_id" >&2
  emit "terminal" ',"status":"failed","summary":"env/task mismatch"'
  exit 20
fi
echo "[harness] env injection confirmed: FACTORY_M0_MARKER matches task_id"

# --- working directory ------------------------------------------------------
if [ "$(pwd)" != "/workspace" ]; then
  echo "[harness] warning: expected cwd /workspace, got $(pwd)"
fi

# Stand in for the one real tool call an agent would make.
emit "tool.call" ',"turn":1,"tool":"bash","redacted_input":"uname -sm"'
uname_out="$(uname -sm)"
emit "tool.result" ',"turn":1,"tool":"bash","ok":true'
echo "[harness] task container reports: $uname_out"

emit "terminal" ',"status":"succeeded","summary":"M0 stub harness completed"'
echo "[harness] events written to $EVENTS:"
sed 's/^/[harness]   /' "$EVENTS"

echo "[harness] done"
exit 0
