#!/usr/bin/env bash
# M1 harness run — end to end.
#
# One unmodified oz-agent-worker, driven by cmd/m0-spike, runs the
# generic-openai harness inside factory-m1-task. The agent clones a repo,
# changes greet.Hello, runs go test, and publishes the result.
#
#   MODE=offline ./spike/m1/run.sh    # default; ends with "M1 OFFLINE PASS"
#   MODE=live    ./spike/m1/run.sh    # real model + GitHub; prints the PR URL
#
# Offline serves spike/m1/fixture as a bare repo on the host. The worker's
# docker backend does not set host networking or host.docker.internal, and
# 127.0.0.1 inside the task container is not the host. A listener bound to
# the docker bridge gateway is reachable from that container, so git daemon
# and the model stub listen there. See spike/m1/README.md.
#
# The harness writes /var/run/factory/events.ndjson inside the task
# container. The worker removes that container, so this script bind-mounts
# spike/m1/.work/runtime there and copies the file to
# spike/m1/.work/evidence/events.ndjson after the run.
#
# Requires: docker (running), go, git, curl. Live also requires gh and
# OPENAI_BASE_URL, M1_MODEL, GH_TOKEN. OPENAI_API_KEY is optional.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
WORK="$HERE/.work"

WORKER_REPO="https://github.com/warpdotdev/oz-agent-worker.git"
WORKER_REF="${WORKER_REF:-main}"
HARNESS_IMAGE="factory-harness:dev"
TASK_IMAGE="factory-m1-task:dev"
API_KEY="m1-spike-key"
PORT="${PORT:-8080}"
WORKER_ID="m1-spike"
MODE="${MODE:-offline}"
# Long enough that a leaked value cannot be a substring of ordinary log text.
OFFLINE_DUMMY_KEY="m1-offline-dummy-7f3c9a"

SERVER_PID=""
WORKER_PID=""
MODEL_PID=""
GITD_PID=""
GITD_PIDFILE=""

# shellcheck disable=SC2329 # invoked by the EXIT/INT/TERM trap
cleanup() {
  local code=$?
  if [[ -n "${WORKER_PID}" ]]; then
    kill "${WORKER_PID}" 2>/dev/null || true
  fi
  if [[ -n "${SERVER_PID}" ]]; then
    kill "${SERVER_PID}" 2>/dev/null || true
  fi
  if [[ -n "${MODEL_PID}" ]]; then
    kill "${MODEL_PID}" 2>/dev/null || true
  fi
  if [[ -n "${GITD_PID}" ]]; then
    kill "${GITD_PID}" 2>/dev/null || true
  fi
  if [[ -n "${GITD_PIDFILE}" && -f "${GITD_PIDFILE}" ]]; then
    kill "$(cat "${GITD_PIDFILE}")" 2>/dev/null || true
  fi
  wait 2>/dev/null || true
  exit "${code}"
}
trap cleanup EXIT INT TERM

say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }

die() {
  printf '%s\n' "$*" >&2
  exit 1
}

# scan_logs_for_secrets fails when any non-empty secret is present in the
# worker or server logs, the offline helper logs, or the archived
# events.ndjson. It prints nothing, so the secret is not copied into this
# script's output.
scan_logs_for_secrets() {
  local files=()
  local f secret
  for f in "$WORK/server.log" "$WORK/worker.log" "$WORK/model-stub.log" "$WORK/git-daemon.log" "$WORK/evidence/events.ndjson"; do
    if [[ -f "$f" ]]; then
      files+=("$f")
    fi
  done
  if [[ ${#files[@]} -eq 0 ]]; then
    return 0
  fi
  for secret in "$@"; do
    if [[ -z "$secret" ]]; then
      continue
    fi
    if grep -F -q -e "$secret" "${files[@]}"; then
      return 1
    fi
  done
  return 0
}

# archive_events copies the harness NDJSON from the bind-mounted runtime
# directory. The task container may have created it as mode 0600 owned by
# another uid, so an unreadable file is copied through the task image.
archive_events() {
  local dest="$1"
  local src="$WORK/runtime/events.ndjson"
  mkdir -p "$(dirname "$dest")"
  rm -f "$dest"
  if [[ ! -e "$src" ]]; then
    return 1
  fi
  if [[ -r "$src" ]]; then
    cp "$src" "$dest" || return 1
  elif ! docker run --rm --network none --user 0 --entrypoint cat \
    -v "$WORK/runtime:/src:ro" \
    "$TASK_IMAGE" /src/events.ndjson >"$dest"; then
    rm -f "$dest"
    return 1
  fi
  if [[ ! -s "$dest" ]] || ! grep -q '"type"' "$dest"; then
    rm -f "$dest"
    return 1
  fi
  return 0
}

dump_logs() {
  echo
  echo "── server log ────────────────────────────────────────────────"
  if [[ -f "$WORK/server.log" ]]; then
    cat "$WORK/server.log"
  else
    echo "(no server log)"
  fi
  echo "── harness output (from the task container) ──────────────────"
  if [[ -f "$WORK/worker.log" ]]; then
    grep -a -E '\[harness\]|factory-harness:' "$WORK/worker.log" || echo "(no harness output captured)"
  else
    echo "(no worker log)"
  fi
  echo "── archived events ───────────────────────────────────────────"
  if [[ -s "$WORK/evidence/events.ndjson" ]]; then
    echo "spike/m1/.work/evidence/events.ndjson ($(wc -l <"$WORK/evidence/events.ndjson" | tr -d ' ') lines)"
  else
    echo "(events.ndjson was not archived)"
  fi
}

dump_failure() {
  dump_logs
  echo
  echo "── worker log (tail) ─────────────────────────────────────────"
  if [[ -f "$WORK/worker.log" ]]; then
    tail -n 80 "$WORK/worker.log"
  fi
  if [[ -f "$WORK/model-stub.log" ]]; then
    echo "── model stub (tail) ─────────────────────────────────────────"
    tail -n 40 "$WORK/model-stub.log"
  fi
  if [[ -f "$WORK/git-daemon.log" ]]; then
    echo "── git daemon (tail) ─────────────────────────────────────────"
    tail -n 40 "$WORK/git-daemon.log"
  fi
}

# ---------------------------------------------------------------------------
say "Preflight"
case "$MODE" in
  offline | live) ;;
  *)
    die "MODE must be offline or live (got ${MODE})"
    ;;
esac

command -v docker >/dev/null || die "docker not found"
command -v go >/dev/null || die "go not found"
command -v git >/dev/null || die "git not found"
command -v curl >/dev/null || die "curl not found"
docker info >/dev/null 2>&1 || die "docker daemon is not reachable"

platform="$(docker info --format '{{.OSType}}/{{.Architecture}}' 2>/dev/null || true)"
case "$platform" in
  linux/aarch64 | linux/arm64 | linux/x86_64 | linux/amd64) ;;
  *)
    die "unsupported docker platform: $platform"
    ;;
esac
echo "docker platform: $platform"
echo "go: $(go version)"
echo "mode: $MODE"

if [[ "$MODE" == live ]]; then
  command -v gh >/dev/null || die "gh not found (required for MODE=live)"
  [[ -n "${OPENAI_BASE_URL:-}" ]] || die "OPENAI_BASE_URL is required for MODE=live"
  [[ -n "${M1_MODEL:-}" ]] || die "M1_MODEL is required for MODE=live"
  [[ -n "${GH_TOKEN:-}" ]] || die "GH_TOKEN is required for MODE=live"
  M1_REPO="${M1_REPO:-polds/factory}"
  echo "live repo: ${M1_REPO} (edits spike/m1/fixture)"
fi

mkdir -p "$WORK"

# ---------------------------------------------------------------------------
say "Build the generic-openai sidecar ($HARNESS_IMAGE)"
(cd "$ROOT" && CGO_ENABLED=0 go build -o harness/generic-openai/factory-harness ./cmd/factory-harness)
docker build -q -t "$HARNESS_IMAGE" "$ROOT/harness/generic-openai" >/dev/null
echo "built $HARNESS_IMAGE"

say "Build the task image ($TASK_IMAGE)"
docker build -q -t "$TASK_IMAGE" "$HERE/task-image" >/dev/null
echo "built $TASK_IMAGE"

# ---------------------------------------------------------------------------
say "Build the unmodified upstream worker (ref: $WORKER_REF)"
if [[ ! -d "$WORK/oz-agent-worker/.git" ]]; then
  git clone --depth 1 --branch "$WORKER_REF" "$WORKER_REPO" "$WORK/oz-agent-worker"
fi
WORKER_SHA="$(git -C "$WORK/oz-agent-worker" rev-parse --short HEAD)"
(cd "$WORK/oz-agent-worker" && go build -o "$WORK/oz-agent-worker-bin" .)
dirty="$(git -C "$WORK/oz-agent-worker" status --porcelain)"
if [[ -n "$dirty" ]]; then
  printf '%s\n' "$dirty"
  die "worker tree is dirty; refusing to run a modified upstream worker"
fi
echo "built worker from $WORKER_SHA — clean tree, no local modifications"

# ---------------------------------------------------------------------------
say "Build m0-spike and the offline model stub"
(cd "$ROOT" && go build -o "$WORK/m0-spike" ./cmd/m0-spike)
(cd "$ROOT" && CGO_ENABLED=0 go build -o "$WORK/model-stub" ./spike/m1/model-stub)
echo "built m0-spike and model-stub"

# The harness writes /var/run/factory/events.ndjson inside the task
# container. cleanup: true removes that container, so bind-mount a host
# directory there and copy the file out after the run.
cat >"$WORK/worker.yaml" <<YAML
cleanup: true
max_concurrent_tasks: 1
backend:
  docker:
    image_pull_policy: IfNotPresent
    volumes:
      - ${WORK}/runtime:/var/run/factory
YAML

# ---------------------------------------------------------------------------
SECRETS=()
if [[ "$MODE" == offline ]]; then
  say "Serve the fixture on the docker bridge gateway"
  GW="$(docker network inspect bridge --format '{{(index .IPAM.Config 0).Gateway}}')"
  if [[ ! "$GW" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    die "could not determine an IPv4 gateway for the docker bridge (got ${GW:-empty})"
  fi
  echo "docker bridge gateway: $GW"
  echo "task containers reach host listeners on this address; 127.0.0.1 and host.docker.internal do not work"

  rm -rf "$WORK/git"
  mkdir -p "$WORK/git"
  seed="$(mktemp -d)"
  git init -b main "$seed" >/dev/null
  cp -a "$HERE/fixture/." "$seed/"
  git -C "$seed" -c user.email=fixture@example.com -c user.name=fixture add -A
  git -C "$seed" -c user.email=fixture@example.com -c user.name=fixture commit -m "fixture baseline" >/dev/null
  git init --bare -b main "$WORK/git/remote.git" >/dev/null
  git -C "$seed" remote add origin "$WORK/git/remote.git"
  git -C "$seed" push -q origin main
  git --git-dir="$WORK/git/remote.git" symbolic-ref HEAD refs/heads/main
  rm -rf "$seed"
  git --git-dir="$WORK/git/remote.git" show main:greet/greet.go | grep -F '"Hello, " + name' >/dev/null

  sed "s#__M1_GIT_HOST__#${GW}#g" "$HERE/offline-script.json" >"$WORK/offline-script.json"
  if grep -q '__M1_GIT_HOST__' "$WORK/offline-script.json"; then
    die "git host was not substituted into the offline script"
  fi

  GITD_PIDFILE="$WORK/git-daemon.pid"
  rm -f "$GITD_PIDFILE"
  git daemon --reuseaddr --export-all --enable=receive-pack \
    --listen="$GW" --port=9418 --pid-file="$GITD_PIDFILE" \
    --base-path="$WORK/git" "$WORK/git" \
    >"$WORK/git-daemon.log" 2>&1 &
  GITD_PID=$!

  "$WORK/model-stub" -addr "${GW}:9090" -script "$WORK/offline-script.json" \
    >"$WORK/model-stub.log" 2>&1 &
  MODEL_PID=$!

  git_ready=0
  model_ready=0
  for _ in $(seq 1 50); do
    if [[ "$git_ready" -eq 0 ]] && git ls-remote "git://${GW}/remote.git" >/dev/null 2>&1; then
      git_ready=1
    fi
    if [[ "$model_ready" -eq 0 ]] && curl -fsS "http://${GW}:9090/healthz" >/dev/null 2>&1; then
      model_ready=1
    fi
    if [[ "$git_ready" -eq 1 && "$model_ready" -eq 1 ]]; then
      break
    fi
    sleep 0.2
  done
  if [[ "$git_ready" -ne 1 || "$model_ready" -ne 1 ]]; then
    dump_failure
    die "git daemon or model stub did not become reachable on ${GW}"
  fi
  # Same path the task container will use.
  docker run --rm --network bridge --entrypoint git "$TASK_IMAGE" \
    ls-remote "git://${GW}/remote.git" >/dev/null
  docker run --rm --network bridge --entrypoint curl "$TASK_IMAGE" \
    -fsS "http://${GW}:9090/healthz" >/dev/null
  echo "fixture and model stub reachable from a bridge container"

  SECRETS+=("$OFFLINE_DUMMY_KEY")
fi

# ---------------------------------------------------------------------------
say "Start m0-spike"
SPIKE_ARGS=(
  -addr ":${PORT}"
  -api-key "$API_KEY"
  -docker-image "$TASK_IMAGE"
  -sidecar-image "$HARNESS_IMAGE"
  -task-id-prefix run_m1_
  -hold-for-heartbeat 0s
)
if [[ "$MODE" == offline ]]; then
  SPIKE_ARGS+=(
    -model m1-offline-stub
    -title "M1 offline agent run"
    -prompt "M1 offline: clone the fixture, make Hello add an exclamation mark, test, and push branch m1-run."
    -timeout 15m
    -env "OPENAI_BASE_URL=http://${GW}:9090"
    -env "OPENAI_API_KEY=${OFFLINE_DUMMY_KEY}"
    -env "FACTORY_SECRET_VALUES=${OFFLINE_DUMMY_KEY}"
    -env "FACTORY_MAX_TURNS=8"
  )
else
  TS="$(date -u +%Y%m%dT%H%M%S)"
  BRANCH="m1/run-${TS}"
  prompt="Clone https://github.com/${M1_REPO} into repo. Change spike/m1/fixture/greet/greet.go so Hello returns the concatenation of \"Hello, \", name, and \"!\". Update spike/m1/fixture/greet/greet_test.go so the expected greeting for M1 is Hello, M1 with an exclamation mark. Run go test ./... in spike/m1/fixture. Create and push branch ${BRANCH}. GH_TOKEN is already exported; run gh auth setup-git before git push, and do not print GH_TOKEN or OPENAI_API_KEY. Open a draft pull request with gh pr create --draft --title 'M1: emphasize Hello' --body 'Update the fixture greeting.' Reply with the pull request URL."
  (
    umask 077
    {
      printf 'OPENAI_BASE_URL=%s\n' "$OPENAI_BASE_URL"
      if [[ -n "${OPENAI_API_KEY:-}" ]]; then
        printf 'OPENAI_API_KEY=%s\n' "$OPENAI_API_KEY"
      fi
      printf 'GH_TOKEN=%s\n' "$GH_TOKEN"
      printf 'FACTORY_MAX_TURNS=%s\n' "40"
      printf 'FACTORY_TASK_PROMPT=%s\n' "$prompt"
    } >"$WORK/m1.env"
  )
  chmod 600 "$WORK/m1.env"
  secret_values="$GH_TOKEN"
  SECRETS+=("$GH_TOKEN")
  if [[ -n "${OPENAI_API_KEY:-}" ]]; then
    secret_values="${GH_TOKEN}"$'\n'"${OPENAI_API_KEY}"
    SECRETS+=("$OPENAI_API_KEY")
  fi
  SPIKE_ARGS+=(
    -model "$M1_MODEL"
    -title "M1 real agent run"
    -prompt "$prompt"
    -timeout 20m
    -env-file "$WORK/m1.env"
    -env "FACTORY_SECRET_VALUES=${secret_values}"
  )
fi

"$WORK/m0-spike" "${SPIKE_ARGS[@]}" >"$WORK/server.log" 2>&1 &
SERVER_PID=$!

for _ in $(seq 1 50); do
  if curl -fsS "http://127.0.0.1:${PORT}/healthz" >/dev/null 2>&1; then
    break
  fi
  sleep 0.2
done
if ! curl -fsS "http://127.0.0.1:${PORT}/healthz" >/dev/null 2>&1; then
  dump_failure
  die "server did not start"
fi
echo "server healthy on :$PORT"

# ---------------------------------------------------------------------------
say "Run the worker against it"
rm -rf "$WORK/runtime" "$WORK/evidence"
mkdir -p "$WORK/runtime" "$WORK/evidence"
# The worker may start the task container as an arbitrary uid.
chmod 1777 "$WORK/runtime"
WARP_API_KEY="$API_KEY" "$WORK/oz-agent-worker-bin" \
  --worker-id "$WORKER_ID" \
  --backend docker \
  --config-file "$WORK/worker.yaml" \
  --log-level debug \
  --web-socket-url "ws://127.0.0.1:${PORT}/api/v1/selfhosted/worker/ws" \
  --server-root-url "http://127.0.0.1:${PORT}" \
  >"$WORK/worker.log" 2>&1 &
WORKER_PID=$!

say "Waiting for a terminal message"
set +e
wait "$SERVER_PID"
VERDICT=$?
set -e
SERVER_PID=""

kill "$WORKER_PID" 2>/dev/null || true
WORKER_PID=""

archived=0
if archive_events "$WORK/evidence/events.ndjson"; then
  archived=1
fi

if ! scan_logs_for_secrets "${SECRETS[@]}"; then
  rm -rf "$WORK/evidence" "$WORK/runtime"
  echo "M1 FAIL — a secret value appeared in the logs or archived events; output withheld"
  exit 1
fi

if [[ "$VERDICT" -ne 0 ]]; then
  dump_failure
  echo
  echo "M1 FAIL — m0-spike did not report task_completed (see $WORK)"
  exit "$VERDICT"
fi

dump_logs

if [[ "$archived" -ne 1 ]]; then
  echo "M1 FAIL — events.ndjson was not archived to spike/m1/.work/evidence/events.ndjson"
  exit 1
fi

if [[ "$MODE" == offline ]]; then
  greet="$(git --git-dir="$WORK/git/remote.git" show m1-run:greet/greet.go 2>/dev/null || true)"
  testf="$(git --git-dir="$WORK/git/remote.git" show m1-run:greet/greet_test.go 2>/dev/null || true)"
  if ! printf '%s\n' "$greet" | grep -F 'name + "!"' >/dev/null; then
    echo "M1 FAIL — branch m1-run is missing the updated Hello line"
    exit 1
  fi
  if ! printf '%s\n' "$testf" | grep -F 'Hello, M1!' >/dev/null; then
    echo "M1 FAIL — branch m1-run is missing the updated test"
    exit 1
  fi
  echo
  echo "M1 OFFLINE PASS — unmodified worker ${WORKER_SHA} pushed m1-run via m0-spike."
  exit 0
fi

PR_URL="$(gh pr list -R "$M1_REPO" --head "$BRANCH" --json url -q '.[0].url')"
if [[ -z "$PR_URL" || "$PR_URL" == "null" ]]; then
  echo "M1 FAIL — no pull request for ${BRANCH} on ${M1_REPO}"
  exit 1
fi
echo
echo "$PR_URL"
echo "M1 LIVE PASS — unmodified worker ${WORKER_SHA} opened ${PR_URL}."
exit 0
