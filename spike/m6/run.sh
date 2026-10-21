#!/usr/bin/env bash
# M6 exit loop.
#
#   MODE=offline ./spike/m6/run.sh
#   MODE=live ./spike/m6/run.sh
#   MODE=live-ci ./spike/m6/run.sh
#   KEEP_UP=1 ./spike/m6/run.sh
#   ./spike/m6/run.sh --manual
#
# offline plays the agent, the judge, and the improver. live and live-ci
# use the same spec with structural assertions. A missing live variable
# prints "missing <NAME>" and exits before any container starts.

set -euo pipefail
# shellcheck source=spike/lib/common.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/../lib/common.sh"
# shellcheck source=spike/lib/driver.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/../lib/driver.sh"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="$ROOT/spike/m6/.work"
SIGNAL="$WORK/signal"
EVIDENCE="$WORK/evidence"
PLAYWRIGHT_IMAGE="${PLAYWRIGHT_IMAGE:-mcr.microsoft.com/playwright:v1.58.2-noble@sha256:6446946a1d9fd62d9ae501312a2d76a43ee688542b21622056a372959b65d63d}"
PINNED_SHA="c0ffeec0ffeec0ffeec0ffeec0ffeec0ffeec0ff"
MODE="${MODE:-offline}"
MANUAL=0
COMPOSE_PROJECT="factory-m6"
COMPOSE_OVERLAY="$ROOT/spike/m6/compose.yml"
COMPOSE_PROFILE="m6"
PHASE_PREFIX="m6"
E2E_SRC="$ROOT/spike/m6/e2e"
PLAYWRIGHT_NETWORK="factory-m6_default"
PLAYWRIGHT_FACTORY_URL="http://factoryd-m6:8080"
PLAYWRIGHT_HEADED_URL="http://127.0.0.1:8082"
EVIDENCE_HAR="loop.har"
EVIDENCE_TRACE="loop-trace.zip"
EVIDENCE_HOSTS="factoryd-m6 127.0.0.1 localhost"

die() {
  printf 'M6 FAIL — %s\n' "$*" >&2
  exit 1
}

say() {
  printf '%s\n' "$*"
}

require_var() {
  local name="$1"
  if [[ -z "${!name:-}" ]]; then
    echo "missing ${name}" >&2
    return 1
  fi
}

require_key_file() {
  if [[ -z "${FACTORY_GITHUB_APP_PRIVATE_KEY_FILE:-}" || ! -f "$FACTORY_GITHUB_APP_PRIVATE_KEY_FILE" ]]; then
    echo "missing FACTORY_GITHUB_APP_PRIVATE_KEY_FILE" >&2
    return 1
  fi
}

require_mode_secrets() {
  case "${MODE:-}" in
    offline) return 0 ;;
    live)
      require_var FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL || return 1
      require_var M6_LIVE_MODEL || return 1
      if [[ -z "${M6_DEFINITION_GIT_URL:-}" ]]; then
        echo "missing M6_DEFINITION_GIT_URL" >&2
        return 1
      fi
      require_var FACTORY_GITHUB_APP_ID || return 1
      require_key_file || return 1
      ;;
    live-ci)
      require_var M1_OPENAI_BASE_URL || return 1
      require_var M1_OPENAI_API_KEY || return 1
      require_var M6_LIVE_MODEL || return 1
      if [[ -z "${M6_DEFINITION_GIT_URL:-}" ]]; then
        echo "missing M6_DEFINITION_GIT_URL" >&2
        return 1
      fi
      require_var FACTORY_GITHUB_APP_ID || return 1
      require_key_file || return 1
      ;;
    *)
      echo "MODE must be offline, live, or live-ci" >&2
      return 1
      ;;
  esac
}

reject_loopback() {
  local host=""
  host="$(url_hostname "$1")"
  case "$host" in
    localhost | 127.0.0.1 | ::1)
      echo "inference URL points at a loopback host. Task containers cannot reach it. On Docker Desktop use host.docker.internal. On Linux use the bridge gateway. This script does not rewrite that URL." >&2
      return 1
      ;;
  esac
}

pick_port() {
  local env_name="$1" configured="$2"
  python3 - "$ROOT/spike/lib/ports.py" "$env_name" "$configured" <<'PY'
import importlib.util
import sys

path, env_name, configured = sys.argv[1:]
spec = importlib.util.spec_from_file_location("spike_lib_ports", path)
if spec is None or spec.loader is None:
    raise SystemExit("ports import failed")
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)
print(mod.resolve_port(configured, env_name))
PY
}

wait_port() {
  python3 - "$1" <<'PY'
import socket
import sys
import time

port = int(sys.argv[1])
for _ in range(50):
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.settimeout(0.2)
    try:
        sock.connect(("127.0.0.1", port))
    except OSError:
        time.sleep(0.1)
    else:
        raise SystemExit(0)
    finally:
        sock.close()
raise SystemExit("port did not accept")
PY
}

rewrite_models() {
  local dest="$1" agent="$2" judge="$3"
  python3 - "$dest" "$agent" "$judge" <<'PY'
import sys
from pathlib import Path

dest, agent, judge = sys.argv[1:]
root = Path(dest)
for path in root.rglob("*"):
    if not path.is_file():
        continue
    text = path.read_text(encoding="utf-8")
    path.write_text(text.replace("factory-default", agent).replace("factory-judge", judge), encoding="utf-8")
PY
}

prepare_definition() {
  local dest="$WORK/definition" agent="factory-default" judge="factory-judge"
  rm -rf "$dest"
  mkdir -p "$dest"
  cp -R "$ROOT/deploy/m6-factory/." "$dest/"
  if [[ "$MODE" == offline ]]; then
    agent="m6-agent"
    judge="m6-judge"
  else
    agent="${M6_LIVE_MODEL}"
    judge="${M6_LIVE_MODEL_JUDGE:-$M6_LIVE_MODEL}"
  fi
  rewrite_models "$dest" "$agent" "$judge"
}

seed_git() {
  local bare="$WORK/git/m6/factory.git"
  rm -rf "$WORK/git" "$WORK/seed"
  mkdir -p "$WORK/seed"
  cp -R "$WORK/definition/." "$WORK/seed/"
  git -C "$WORK/seed" init -b main
  git -C "$WORK/seed" add .
  git -C "$WORK/seed" -c user.email=factory@example.com -c user.name=factory commit -m "m6 factory"
  git clone --bare "$WORK/seed" "$bare"
  git --git-dir="$bare" config uploadpack.allowReachableSHA1InWant true
  git --git-dir="$bare" update-ref refs/heads/candidate "$(git --git-dir="$bare" rev-parse refs/heads/main)"
}

wait_factory_name() {
  local url="$1" name="$2" i
  for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30 \
    31 32 33 34 35 36 37 38 39 40 41 42 43 44 45 46 47 48 49 50 51 52 53 54 55 56 57 58 59 60 \
    61 62 63 64 65 66 67 68 69 70 71 72 73 74 75 76 77 78 79 80 81 82 83 84 85 86 87 88 89 90; do
    if curl -fsS --connect-timeout 2 -m 5 \
      -H "Authorization: Bearer ${FACTORY_API_KEY}" \
      "$url" |
      python3 -c 'import json,sys
name=sys.argv[1]
try:
    rows=json.load(sys.stdin)
except Exception:
    raise SystemExit(1)
names=set()
if isinstance(rows, list):
    for row in rows:
        if isinstance(row, dict) and row.get("name"):
            names.add(row["name"])
raise SystemExit(0 if name in names else 1)' "$name"; then
      return 0
    fi
    sleep 1
  done
  return 1
}

distinct_models() {
  printf '%s\n' "$M6_LIVE_MODEL"
  if [[ -n "${M6_LIVE_MODEL_JUDGE:-}" && "$M6_LIVE_MODEL_JUDGE" != "$M6_LIVE_MODEL" ]]; then
    printf '%s\n' "$M6_LIVE_MODEL_JUDGE"
  fi
}

token_probe() {
  local base="$1" model="$2" dest="" hdr="" line="" code=""
  dest="$(mktemp)"
  hdr="$(mktemp)"
  if ! write_catalog_headers "$hdr"; then
    rm -f "$dest" "$hdr"
    return 1
  fi
  local -a args=(-sS --connect-timeout 2 -m 15 -o "$dest" -w '%{http_code}')
  while IFS= read -r line || [[ -n "$line" ]]; do
    if [[ -n "$line" ]]; then
      args+=(-H "$line")
    fi
  done <"$hdr"
  rm -f "$hdr"
  args+=(-H "Content-Type: application/json")
  args+=(-d "{\"model\":\"${model}\",\"max_tokens\":1,\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}")
  args+=("${base%/}/chat/completions")
  code="$("${TOKEN_CURL:-curl}" "${args[@]}" || true)"
  python3 - "$dest" "$code" "$model" <<'PY'
import os
import sys

dest, code, model = sys.argv[1:]
key = os.environ.get("FACTORY_INFERENCE_OPENAI_API_KEY", "")
body = open(dest, encoding="utf-8", errors="replace").read(200)
if key:
    body = body.replace(key, "[REDACTED]")
sys.stdout.write("token probe %s status %s body %s\n" % (model, code, body.replace("\n", " ")))
raise SystemExit(0 if code.startswith("2") else 1)
PY
  rm -f "$dest"
}

preflight_live() {
  local probe="" catalog="" id=""
  reject_loopback "$FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL"
  probe="$(host_catalog_probe_url "$FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL" "${TASK_HOST:-}")"
  catalog="$WORK/catalog.json"
  mkdir -p "$WORK"
  if ! catalog_probe "$probe" "$catalog"; then
    echo "catalog probe failed" >&2
    return 1
  fi
  while IFS= read -r id; do
    [[ -z "$id" ]] && continue
    if ! reject_unlisted_model "$catalog" "$id"; then
      return 1
    fi
    if ! token_probe "$probe" "$id"; then
      return 1
    fi
  done < <(distinct_models)
}

build_images() {
  say "Build images"
  local phase_start="$SECONDS"
  docker build --platform "$OCI_PLATFORM" -t factoryd:dev \
    -f "$ROOT/deploy/factoryd/Dockerfile" "$ROOT"
  docker build --platform "$OCI_PLATFORM" -t factory-upstream-worker:dev \
    "$ROOT/deploy/worker"
  (cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH_TARGET" \
    go build -o harness/generic-openai/factory-harness ./cmd/factory-harness)
  assert_linux_elf "$ROOT/harness/generic-openai/factory-harness" "$GOARCH_TARGET"
  docker build --platform "$OCI_PLATFORM" -t factory-harness:dev \
    "$ROOT/harness/generic-openai"
  docker build --platform "$OCI_PLATFORM" -t factory-m1-task:dev \
    "$ROOT/spike/m1/task-image"
  docker build --platform "$OCI_PLATFORM" -t factoryd-git:dev \
    -f "$ROOT/spike/m5/factoryd-git.Dockerfile" "$ROOT/spike/m5"
  phase_report "image build" "$phase_start"
}

start_git() {
  git daemon --reuseaddr --export-all --enable=receive-pack \
    --listen=0.0.0.0 --port="$GIT_PORT" \
    --base-path="$WORK/git" "$WORK/git" \
    >"$WORK/git-daemon.log" 2>&1 &
  GIT_PID=$!
  wait_port "$GIT_PORT"
}

start_forge() {
  local fake=""
  fake="$(pick_port M6_FAKE_PORT "")"
  (cd "$ROOT" && CGO_ENABLED=0 GOOS="$(uname -s | tr '[:upper:]' '[:lower:]')" GOARCH="$(go env GOARCH)" \
    go build -o "$WORK/fake-github" ./spike/m4/fakes/github)
  "$WORK/fake-github" -addr "127.0.0.1:${fake}" >"$WORK/fake-github.log" 2>&1 &
  FAKE_PID=$!
  if ! wait_http "http://127.0.0.1:${fake}/healthz"; then
    echo "fake-github did not become reachable" >&2
    return 1
  fi
  M6_UPSTREAM_GITHUB="http://127.0.0.1:${fake}" \
    M6_TOKEN_FILE="$WORK/install-token" \
    python3 "$ROOT/spike/m6/forge.py" --addr "0.0.0.0:${FORGE_PORT}" \
    >"$WORK/forge.log" 2>&1 &
  FORGE_PID=$!
  if ! wait_http "http://127.0.0.1:${FORGE_PORT}/_spike/m6"; then
    echo "forge did not become reachable" >&2
    return 1
  fi
}

start_router() {
  M6_DEFINITION_GIT_URL="$M6_TASK_GIT_URL" \
    M6_GITHUB_API_BASE="$M6_TASK_GITHUB_API" \
    python3 "$ROOT/spike/m6/router.py" --addr "0.0.0.0:${ROUTER_PORT}" \
    >"$WORK/router.log" 2>&1 &
  ROUTER_PID=$!
  if ! wait_http "http://127.0.0.1:${ROUTER_PORT}/healthz"; then
    echo "model router did not become reachable" >&2
    return 1
  fi
}

watch_candidate() {
  local bare="$WORK/git/m6/factory.git" ref="" sha="" id="" cmd=""
  while [[ ! -f "$SIGNAL/stop" ]]; do
    # improve/<batch id> is refs/heads/improve/<id>, not refs/heads/improve.
    ref="$(git --git-dir="$bare" for-each-ref --format='%(refname)' 'refs/heads/improve/*' 2>/dev/null | head -n 1 || true)"
    if [[ -n "$ref" ]]; then
      sha="$(git --git-dir="$bare" rev-parse "$ref")"
      git --git-dir="$bare" update-ref refs/heads/candidate "$sha"
      mkdir -p "$EVIDENCE" "$SIGNAL"
      git --git-dir="$bare" diff main "$ref" >"$EVIDENCE/skill.diff" || true
      printf '%s\n' "$sha" >"$SIGNAL/candidate-ready"
    fi
    if [[ -n "${FORGE_PORT:-}" ]]; then
      mkdir -p "$EVIDENCE"
      curl -fsS --connect-timeout 2 -m 5 "http://127.0.0.1:${FORGE_PORT}/_spike/m6" \
        -o "$EVIDENCE/forge.json" || true
    fi
    # Benchmark trials put the pinned SHA on the task argv. The worker
    # removes the container when the trial exits, so poll while it runs.
    if [[ -n "${PINNED_SHA:-}" && ! -f "$EVIDENCE/head-override.txt" ]]; then
      while read -r id; do
        [[ -n "$id" ]] || continue
        cmd="$(docker inspect --format '{{json .Config.Cmd}}' "$id" 2>/dev/null || true)"
        if [[ "$cmd" == *"$PINNED_SHA"* ]]; then
          mkdir -p "$EVIDENCE"
          printf 'task argv contains %s\n' "$PINNED_SHA" >"$EVIDENCE/head-override.txt"
          break
        fi
      done < <(docker ps -q --filter ancestor=factory-m1-task:dev 2>/dev/null || true)
    fi
    sleep 0.2
  done
}

copy_shots() {
  local name=""
  mkdir -p /opt/cursor/artifacts
  for name in scorers inbox improvement benchmarks; do
    if [[ -f "$EVIDENCE/${name}.png" ]]; then
      cp "$EVIDENCE/${name}.png" "/opt/cursor/artifacts/${name}.png"
    fi
  done
}

cleanup() {
  local code=$?
  touch "$SIGNAL/stop" 2>/dev/null || true
  if [[ -n "${WATCH_PID:-}" ]]; then
    kill "$WATCH_PID" 2>/dev/null || true
    wait "$WATCH_PID" 2>/dev/null || true
  fi
  local pid=""
  for pid in ${ROUTER_PID:-} ${FORGE_PID:-} ${FAKE_PID:-} ${GIT_PID:-}; do
    if [[ -n "$pid" ]]; then
      kill "$pid" 2>/dev/null || true
      wait "$pid" 2>/dev/null || true
    fi
  done
  if [[ "${COMPOSE_STARTED:-}" == 1 && "${KEEP_UP:-}" != 1 ]]; then
    # -v drops postgres and minio so a later offline run cannot see a
    # previous improvement batch and skip the improver.
    compose down -v --remove-orphans >/dev/null 2>&1 || true
  fi
  exit "$code"
}

if [[ "${BASH_SOURCE[0]}" != "$0" ]]; then
  return 0
fi

while [[ $# -gt 0 ]]; do
  case "$1" in
    --manual) MANUAL=1 ;;
    *) die "unknown argument $1" ;;
  esac
  shift
done

if [[ "$MANUAL" == 1 ]]; then
  KEEP_UP=1
fi

phase_start="$SECONDS"
if ! require_mode_secrets; then
  exit 1
fi
apply_live_inference_env
phase_report "prep" "$phase_start"

command -v docker >/dev/null || die "docker not found"
command -v go >/dev/null || die "go not found"
command -v curl >/dev/null || die "curl not found"
command -v python3 >/dev/null || die "python3 not found"
command -v openssl >/dev/null || die "openssl not found"
command -v git >/dev/null || die "git not found"
docker info >/dev/null 2>&1 || die "docker daemon is not reachable"
detect_platform

if ! pick_port M6_PORT_GUARD 8080 >/dev/null; then
  die "port 8080 is already in use"
fi
if ! pick_port M6_PORT_GUARD 9000 >/dev/null; then
  die "port 9000 is already in use"
fi
if ! pick_port M6_PORT_GUARD 9001 >/dev/null; then
  die "port 9001 is already in use"
fi

mkdir -p "$WORK"
wipe_dir "$EVIDENCE" || true
wipe_dir "$SIGNAL" || true
mkdir -p "$EVIDENCE" "$SIGNAL"
umask 077
FACTORY_API_KEY="$(random_hex)"
export FACTORY_API_KEY
M6_API_KEY="$FACTORY_API_KEY"
export M6_API_KEY
printf '%s\n' "$FACTORY_API_KEY" >"$WORK/api-key"
chmod 600 "$WORK/api-key"
FACTORY_WORKER_API_KEY="$(random_hex)"
export FACTORY_WORKER_API_KEY
S3_ACCESS_KEY="${S3_ACCESS_KEY:-factoryadmin}"
export S3_ACCESS_KEY
S3_SECRET_KEY="$(random_hex)"
export S3_SECRET_KEY
FACTORY_SECRET_M6_PLANTED="$(random_hex)"
export FACTORY_SECRET_M6_PLANTED
if [[ -z "${FACTORY_GITHUB_WEBHOOK_SECRET:-}" ]]; then
  FACTORY_GITHUB_WEBHOOK_SECRET="$(random_hex)"
  export FACTORY_GITHUB_WEBHOOK_SECRET
fi
if [[ "$MODE" == offline ]]; then
  FACTORY_INFERENCE_OPENAI_API_KEY="$(random_hex)"
  export FACTORY_INFERENCE_OPENAI_API_KEY
  FACTORY_GITHUB_APP_ID=17
  export FACTORY_GITHUB_APP_ID
  openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out "$WORK/app.pem" >/dev/null 2>&1
  chmod 644 "$WORK/app.pem"
  M6_APP_KEY_FILE="$WORK/app.pem"
  export M6_APP_KEY_FILE
fi

OS_NAME="$(docker info --format '{{.OperatingSystem}}')"
GW="$(bridge_gateway)"
TASK_HOST="$(host_for_tasks "$OS_NAME" "$GW" "$(uname -s)" "")"
export TASK_HOST
printf 'task host: %s\n' "$TASK_HOST"

if [[ "$MODE" != offline ]]; then
  if ! preflight_live; then
    die "live preflight failed"
  fi
fi

CONSOLE_PORT="$(pick_port M6_CONSOLE_PORT "${M6_CONSOLE_PORT:-}")"
export M6_CONSOLE_PORT="$CONSOLE_PORT"
GIT_PORT="$(pick_port M6_GIT_PORT "${M6_GIT_PORT:-}")"
export M6_GIT_PORT="$GIT_PORT"
FORGE_PORT="$(pick_port M6_FORGE_PORT "${M6_FORGE_PORT:-}")"
export M6_FORGE_PORT="$FORGE_PORT"
if [[ "$MODE" == offline ]]; then
  ROUTER_PORT="$(pick_port M6_ROUTER_PORT "${M6_ROUTER_PORT:-}")"
  export M6_ROUTER_PORT="$ROUTER_PORT"
fi
printf 'console %s git %s forge %s\n' "$CONSOLE_PORT" "$GIT_PORT" "$FORGE_PORT"

M6_FACTORYD_GIT_URL="git://host.docker.internal:${GIT_PORT}/m6/factory.git"
M6_TASK_GIT_URL="git://${TASK_HOST}:${GIT_PORT}/m6/factory.git"
M6_FACTORYD_GITHUB_API="http://host.docker.internal:${FORGE_PORT}"
M6_TASK_GITHUB_API="http://${TASK_HOST}:${FORGE_PORT}"
export M6_FACTORYD_GIT_URL M6_TASK_GIT_URL M6_FACTORYD_GITHUB_API M6_TASK_GITHUB_API
export FACTORY_EXTERNAL_URL="http://127.0.0.1:${CONSOLE_PORT}"
export FACTORY_TASK_SERVER_URL="http://${TASK_HOST}:${CONSOLE_PORT}"
export FACTORY_REVIEWERS_ADMINS="${FACTORY_REVIEWERS_ADMINS:-m6-reviewer}"
export FACTORY_SAMPLER_INTERVAL="${FACTORY_SAMPLER_INTERVAL:-2s}"
export FACTORY_IMPROVEMENT_INTERVAL="${FACTORY_IMPROVEMENT_INTERVAL:-2s}"
if [[ "$MODE" == offline ]]; then
  export FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL="http://${TASK_HOST}:${ROUTER_PORT}/v1"
fi
PLAYWRIGHT_HEADED_URL="http://127.0.0.1:${CONSOLE_PORT}"
export PLAYWRIGHT_HEADED_URL

trap cleanup EXIT INT TERM

prepare_definition
seed_git
start_git
start_forge
if [[ "$MODE" == offline ]]; then
  start_router
fi

build_images
stack_start="$SECONDS"
compose down -v --remove-orphans >/dev/null 2>&1 || true
COMPOSE_STARTED=1
if ! compose up -d --wait --remove-orphans; then
  phase_report "stack up" "$stack_start"
  die "compose up failed"
fi
if ! wait_http "http://127.0.0.1:${CONSOLE_PORT}/healthz"; then
  phase_report "stack up" "$stack_start"
  die "factoryd-m6 /healthz did not become ready"
fi
if ! wait_factory_name "http://127.0.0.1:${CONSOLE_PORT}/api/v1/factory" "m6"; then
  phase_report "stack up" "$stack_start"
  die "factory m6 did not appear"
fi
phase_report "stack up" "$stack_start"

if [[ "$MANUAL" == 1 ]]; then
  printf 'console: http://127.0.0.1:%s\n' "$CONSOLE_PORT"
  printf 'api key file: %s\n' "$WORK/api-key"
  trap - EXIT INT TERM
  exit 0
fi

watch_candidate &
WATCH_PID=$!

say "Playwright"
play_start="$SECONDS"
set +e
play_log="$EVIDENCE/playwright.log"
run_playwright 2>&1 | tee "$play_log"
play_code="${PIPESTATUS[0]}"
set -e
phase_report "Playwright" "$play_start"
touch "$SIGNAL/stop" || true
if [[ -n "${WATCH_PID:-}" ]]; then
  wait "$WATCH_PID" 2>/dev/null || true
  WATCH_PID=""
fi
save_logs
copy_shots
if [[ -f "$WORK/install-token" ]]; then
  INSTALL_TOKEN="$(tr -d '[:space:]' <"$WORK/install-token")"
  export INSTALL_TOKEN
fi
export EVIDENCE_EXTRA_SECRETS="FACTORY_SECRET_M6_PLANTED FACTORY_GITHUB_WEBHOOK_SECRET M6_TASK_GIT_URL M6_TASK_GITHUB_API INSTALL_TOKEN"
export EVIDENCE_HAR EVIDENCE_TRACE EVIDENCE_HOSTS
evidence_start="$SECONDS"
if ! grep -F "$PINNED_SHA" "$EVIDENCE/compose.log" >/dev/null 2>&1 &&
  ! grep -F "$PINNED_SHA" "$EVIDENCE/head-override.txt" >/dev/null 2>&1; then
  phase_report "evidence" "$evidence_start"
  die "pinned startingRepoRefs SHA is not in the factory logs or the task argv"
fi
if ! assert_evidence; then
  phase_report "evidence" "$evidence_start"
  die "evidence check"
fi
phase_report "evidence" "$evidence_start"
if [[ -f "$EVIDENCE/exit-trace.txt" ]]; then
  cat "$EVIDENCE/exit-trace.txt"
fi
if [[ "$play_code" -ne 0 ]]; then
  print_playwright_failures "$play_log"
  die "playwright exit ${play_code}"
fi
if [[ "${KEEP_UP:-}" == 1 ]]; then
  printf 'console: http://127.0.0.1:%s\n' "$CONSOLE_PORT"
  printf 'api key file: %s\n' "$WORK/api-key"
  trap - EXIT INT TERM
fi
mode_name="$(printf '%s' "$MODE" | tr '[:lower:]' '[:upper:]')"
printf 'M6 %s PASS\n' "$mode_name"
