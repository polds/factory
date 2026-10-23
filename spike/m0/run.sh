#!/usr/bin/env bash
# M0 protocol spike — end-to-end.
#
# Proves the central assumption of this project: that the unmodified,
# MIT-licensed oz-agent-worker can be pointed at a control plane we run.
#
#   ./spike/m0/run.sh
#
# Exits 0 only if a real worker authenticated to our server, claimed our task,
# launched our harness sidecar inside a task container, and reported
# task_completed. Anything else is a failure with the reason printed.
#
# Requires: docker (running), go, git. Pulls busybox and alpine on first run.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
WORK="$HERE/.work"
# shellcheck source=spike/lib/upstream-worker.sh
. "$ROOT/spike/lib/upstream-worker.sh"

HARNESS_IMAGE="factory-m0-harness:dev"
TASK_IMAGE="${TASK_IMAGE:-alpine:3.20}"
API_KEY="m0-spike-key"
PORT="${PORT:-8080}"
WORKER_ID="m0-spike"

SERVER_PID=""
WORKER_PID=""

# shellcheck disable=SC2329 # invoked by the EXIT/INT/TERM trap
cleanup() {
  local code=$?
  if [[ -n "${WORKER_PID}" ]]; then
    kill "${WORKER_PID}" 2>/dev/null || true
  fi
  if [[ -n "${SERVER_PID}" ]]; then
    kill "${SERVER_PID}" 2>/dev/null || true
  fi
  wait 2>/dev/null || true
  exit "${code}"
}
trap cleanup EXIT INT TERM

say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }

# ---------------------------------------------------------------------------
say "Preflight"
command -v docker >/dev/null || {
  echo "docker not found"
  exit 2
}
command -v go >/dev/null || {
  echo "go not found"
  exit 2
}
command -v git >/dev/null || {
  echo "git not found"
  exit 2
}
docker info >/dev/null 2>&1 || {
  echo "docker daemon is not reachable"
  exit 2
}

# The worker only supports linux/amd64 and linux/arm64 task platforms.
platform="$(docker info --format '{{.OSType}}/{{.Architecture}}' 2>/dev/null || true)"
case "$platform" in
  linux/aarch64 | linux/arm64 | linux/x86_64 | linux/amd64) ;;
  *)
    echo "unsupported docker platform: $platform"
    exit 2
    ;;
esac
echo "docker platform: $platform"
echo "go: $(go version)"

mkdir -p "$WORK"

# ---------------------------------------------------------------------------
say "Build the stub harness sidecar ($HARNESS_IMAGE)"
docker build -q -t "$HARNESS_IMAGE" "$HERE/harness" >/dev/null
echo "built $HARNESS_IMAGE"

say "Ensure the task image is present locally ($TASK_IMAGE)"
# The worker's docker backend is configured IfNotPresent below, so it will not
# reach a registry for either image. Both must already be local.
docker image inspect "$TASK_IMAGE" >/dev/null 2>&1 || docker pull -q "$TASK_IMAGE"
echo "task image ready"

# ---------------------------------------------------------------------------
worker_ref="$(upstream_worker_ref "$ROOT")"
say "Build the unmodified upstream worker (ref: $worker_ref)"
if [[ ! -d "$WORK/oz-agent-worker/.git" ]]; then
  bash "$ROOT/spike/lib/clone-upstream-worker.sh" "$WORK/oz-agent-worker"
fi
WORKER_SHA="$(git -C "$WORK/oz-agent-worker" rev-parse --short HEAD)"
(cd "$WORK/oz-agent-worker" && go build -o "$WORK/oz-agent-worker-bin" .)
echo "built worker from $WORKER_SHA — source unmodified:"
git -C "$WORK/oz-agent-worker" status --porcelain | sed 's/^/  /' || true
[[ -z "$(git -C "$WORK/oz-agent-worker" status --porcelain)" ]] &&
  echo "  (clean tree — no local modifications)"

# ---------------------------------------------------------------------------
say "Build the M0 control-plane spike"
(cd "$ROOT" && go build -o "$WORK/m0-spike" ./cmd/m0-spike)
echo "built m0-spike"

# IfNotPresent keeps the worker off the registry for our locally-built images.
# The docker backend defaults to Always, which would fail on factory-m0-harness.
cat >"$WORK/worker.yaml" <<YAML
cleanup: true
max_concurrent_tasks: 1
backend:
  docker:
    image_pull_policy: IfNotPresent
YAML

# ---------------------------------------------------------------------------
say "Start the spike server"
# The worker pings every 30s, so a 4-second task never exercises the liveness
# half of the protocol. Hold the connection open past one interval to verify
# ping/pong too. FAST=1 skips the wait.
HOLD="35s"
if [[ -n "${FAST:-}" ]]; then
  HOLD="0s"
  echo "FAST=1 — skipping heartbeat verification"
fi

"$WORK/m0-spike" \
  -addr ":$PORT" \
  -api-key "$API_KEY" \
  -docker-image "$TASK_IMAGE" \
  -sidecar-image "$HARNESS_IMAGE" \
  -hold-for-heartbeat "$HOLD" \
  -timeout 3m \
  >"$WORK/server.log" 2>&1 &
SERVER_PID=$!

for _ in $(seq 1 50); do
  if curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then break; fi
  sleep 0.2
done
curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null || {
  echo "server did not start"
  cat "$WORK/server.log"
  exit 1
}
echo "server healthy on :$PORT"

# ---------------------------------------------------------------------------
say "Run the worker against it"
echo "the two hidden flags are the whole point of this spike:"
echo "  --web-socket-url  ws://127.0.0.1:$PORT/api/v1/selfhosted/worker/ws"
echo "  --server-root-url http://127.0.0.1:$PORT"
echo "(note the kebab: kong derives --web-socket-url from the WebSocketURL field)"

WARP_API_KEY="$API_KEY" "$WORK/oz-agent-worker-bin" \
  --worker-id "$WORKER_ID" \
  --backend docker \
  --config-file "$WORK/worker.yaml" \
  --log-level debug \
  --web-socket-url "ws://127.0.0.1:$PORT/api/v1/selfhosted/worker/ws" \
  --server-root-url "http://127.0.0.1:$PORT" \
  >"$WORK/worker.log" 2>&1 &
WORKER_PID=$!

# ---------------------------------------------------------------------------
say "Waiting for a terminal message"
set +e
wait "$SERVER_PID"
VERDICT=$?
set -e
SERVER_PID=""

kill "$WORKER_PID" 2>/dev/null || true
WORKER_PID=""

echo
echo "── server log ────────────────────────────────────────────────"
cat "$WORK/server.log"
echo "── harness output (from the task container) ──────────────────"
grep -a "\[harness\]" "$WORK/worker.log" || echo "(no harness output captured)"

if [[ $VERDICT -ne 0 ]]; then
  echo
  echo "── worker log (tail) ─────────────────────────────────────────"
  tail -n 60 "$WORK/worker.log"
fi

echo
if [[ $VERDICT -eq 0 ]]; then
  echo "M0 PASS — upstream worker $WORKER_SHA ran our harness against our server."
else
  echo "M0 FAIL — see logs in $WORK"
fi
exit $VERDICT
