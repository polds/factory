#!/usr/bin/env bash
# M2 laptop profile — factoryd, Postgres, MinIO, one upstream worker.
#
#   MODE=offline ./spike/m2/run.sh    # default; ends with "M2 OFFLINE PASS"
#   MODE=live    ./spike/m2/run.sh    # real model + GitHub; prints the PR URL
#
# Offline serves spike/m1/fixture on the Docker bridge gateway and points a
# scripted model at the same address. Task containers are on that bridge,
# not the compose network, so factoryd is reached at
# http://<gateway>:8080 (172.17.0.1 on a typical Linux engine).
#
# Requires: docker (running), go, git, curl, python3. Live also requires gh.
# Live reads deploy/.env when that file exists (KEY=VALUE text, not executed).
# A non-empty shell variable wins over the file. OPENAI_BASE_URL, M2_MODEL,
# GH_TOKEN, and optional OPENAI_API_KEY are the shell names.
# FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL, FACTORY_INFERENCE_OPENAI_API_KEY,
# and FACTORY_SECRET_GH_TOKEN are the same values. Nothing in this script
# prints those values.
set -euo pipefail
# shellcheck source=spike/lib/common.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/../lib/common.sh"
# shellcheck source=spike/lib/cleanup.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/../lib/cleanup.sh"

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
WORK="$HERE/.work"
COMPOSE_FILE="$ROOT/deploy/docker-compose.yml"
PROJECT="factory-m2"
NETWORK="${PROJECT}_default"

HARNESS_IMAGE="factory-harness:dev"
TASK_IMAGE="factory-m1-task:dev"
MC_IMAGE="bitnamilegacy/minio-client:2025.7.21-debian-12-r3@sha256:73bd39f7899a0cef12b8dd5df13aa93a3ed1aaa44236542442e9ac76819ac158"
MODE="${MODE:-offline}"

# Long enough that a leaked value cannot be a substring of ordinary log text.
OFFLINE_DUMMY_KEY="m2-offline-dummy-7f3c9a"
OFFLINE_GH="m2-offline-gh-7f3c9a2e"
PLANTED_DUMMY="m2-planted-dummy-7f3c9a"

MODEL_PID=""
GITD_PIDFILE=""
COMPOSE_STARTED=0
SECRETS=()
LEAK_ANNOUNCED=0

# shellcheck disable=SC2317,SC2329 # invoked by the EXIT/INT/TERM trap
cleanup() {
  local code=$?
  local why="${1:-EXIT}"
  spike_teardown_begin "$why" || return 0
  set +e
  if [[ -n "${GITD_PIDFILE}" && -f "${GITD_PIDFILE}" ]]; then
    kill "$(cat "${GITD_PIDFILE}")" 2>/dev/null || true
  fi
  if [[ "${COMPOSE_STARTED}" -eq 1 ]]; then
    save_compose_logs || true
    docker compose -p "$PROJECT" -f "$COMPOSE_FILE" down -v --timeout 20 >/dev/null 2>&1 ||
      docker compose -p "$PROJECT" -f "$COMPOSE_FILE" down -v --timeout 20 || true
    COMPOSE_STARTED=0
  fi
  if ! scan_logs_for_secrets; then
    announce_leak
    code=1
  fi
  spike_teardown_finish "$code"
}
say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }

die() {
  fail "$*"
}

# on_err is the ERR trap. A non-zero command under errexit must still say why.
on_err() {
  local code="$1" line="$2"
  trap - ERR
  fail "command failed at line ${line} (exit ${code})"
}

apply_live_env_aliases() {
  copy_if_empty OPENAI_BASE_URL FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL
  copy_if_empty OPENAI_API_KEY FACTORY_INFERENCE_OPENAI_API_KEY
  copy_if_empty GH_TOKEN FACTORY_SECRET_GH_TOKEN
}

# GNU date accepts %N. macOS date does not; python3 is already required.
unix_ns() {
  local ts=""
  if [[ -z "${M2_DATE_NS_MODE:-}" ]]; then
    ts="$(date +%s%N 2>/dev/null || true)"
    if [[ "$ts" =~ ^[0-9]{19}$ ]]; then
      M2_DATE_NS_MODE=gnu
    else
      M2_DATE_NS_MODE=python
    fi
  fi
  if [[ "$M2_DATE_NS_MODE" == gnu ]]; then
    date +%s%N
    return
  fi
  python3 -c 'import time; print(time.time_ns())'
}

compose() {
  docker compose -p "$PROJECT" -f "$COMPOSE_FILE" "$@"
}

# scan_files fails when any non-empty secret is present. It prints nothing.
scan_files() {
  local secret files=()
  while [[ $# -gt 0 ]]; do
    if [[ -f "$1" ]]; then
      files+=("$1")
    fi
    shift
  done
  if [[ ${#files[@]} -eq 0 ]]; then
    return 0
  fi
  for secret in "${SECRETS[@]+"${SECRETS[@]}"}"; do
    if [[ -z "$secret" ]]; then
      continue
    fi
    if grep -F -q -e "$secret" "${files[@]}"; then
      return 1
    fi
  done
  return 0
}

scan_logs_for_secrets() {
  scan_files \
    "$WORK/compose.log" \
    "$WORK/watch-run.ts" \
    "$WORK/watch-cancel.ts" \
    "$WORK/factoryctl-run.err" \
    "$WORK/factoryctl-cancel.err" \
    "$WORK/image-exec.err" \
    "$WORK/model-stub.log" \
    "$WORK/git-daemon.log" \
    "$WORK/evidence/events.ndjson" \
    "$WORK/evidence/cancel-events.ndjson"
}

discard_captured_logs() {
  rm -rf "$WORK/evidence"
  rm -f \
    "$WORK/compose.log" \
    "$WORK/watch-run.ts" \
    "$WORK/watch-cancel.ts" \
    "$WORK/factoryctl-run.err" \
    "$WORK/factoryctl-cancel.err" \
    "$WORK/image-exec.err"
}

announce_leak() {
  discard_captured_logs
  if [[ "$LEAK_ANNOUNCED" -eq 0 ]]; then
    echo "M2 FAIL — a secret value appeared in the logs, watch output, or archived events; output withheld"
    LEAK_ANNOUNCED=1
  fi
}

# prove_scanner plants a dummy and requires the scanner to reject it.
prove_scanner() {
  local proof="$WORK/planted-secret.txt"
  local saved=()
  mkdir -p "$WORK"
  printf 'planted %s\n' "$PLANTED_DUMMY" >"$proof"
  saved=("${SECRETS[@]+"${SECRETS[@]}"}")
  SECRETS=("$PLANTED_DUMMY")
  local rejected=0
  if ! scan_files "$proof"; then
    rejected=1
  fi
  SECRETS=("${saved[@]+"${saved[@]}"}")
  rm -f "$proof"
  if [[ -e "$proof" ]]; then
    die "planted dummy file was not removed"
  fi
  if [[ "$rejected" -ne 1 ]]; then
    die "secret scanner did not fail on a planted value"
  fi
  echo "secret scanner rejects a planted dummy"
}

dump_failure() {
  # A failing tail must not skip the M2 FAIL line under errexit.
  set +e
  echo
  echo "── compose log (tail) ────────────────────────────────────────"
  if [[ -f "$WORK/compose.log" ]]; then
    tail -n 120 "$WORK/compose.log"
  else
    echo "(no compose log)"
  fi
  echo "── factoryctl run (tail) ─────────────────────────────────────"
  if [[ -f "$WORK/watch-run.ts" ]]; then
    tail -n 40 "$WORK/watch-run.ts"
  else
    echo "(no watch transcript)"
  fi
  echo "── factoryctl run stderr ─────────────────────────────────────"
  if [[ -f "$WORK/factoryctl-run.err" ]]; then
    tail -n 80 "$WORK/factoryctl-run.err"
  else
    echo "(no factoryctl-run.err)"
  fi
  if [[ -f "$WORK/factoryctl-cancel.err" ]]; then
    echo "── factoryctl cancel stderr ──────────────────────────────────"
    tail -n 40 "$WORK/factoryctl-cancel.err"
  fi
  if [[ -f "$WORK/image-exec.err" ]]; then
    echo "── image exec (tail) ─────────────────────────────────────────"
    tail -n 40 "$WORK/image-exec.err"
  fi
  if [[ -f "$WORK/model-stub.log" ]]; then
    echo "── model stub (tail) ─────────────────────────────────────────"
    tail -n 20 "$WORK/model-stub.log"
  fi
  if [[ -f "$WORK/git-daemon.log" ]]; then
    echo "── git daemon (tail) ─────────────────────────────────────────"
    tail -n 20 "$WORK/git-daemon.log"
  fi
  return 0
}

fail() {
  local reason="$*"
  trap - ERR
  set +e
  save_compose_logs
  if ! scan_logs_for_secrets; then
    announce_leak
    exit 1
  fi
  dump_failure
  echo
  echo "M2 FAIL — ${reason}"
  exit 1
}

# host_name_resolves is true when this machine can resolve name. Docker Desktop
# installs host.docker.internal on the host. A Linux engine does not.
host_name_resolves() {
  python3 - "$1" <<'PY'
import socket
import sys
try:
    socket.getaddrinfo(sys.argv[1], None)
except socket.gaierror:
    sys.exit(1)
PY
}

# loopback_task_host is where a task container reaches a service bound to the
# host's loopback. Docker Desktop provides host.docker.internal. A Linux
# engine uses the bridge gateway. resolved is yes or no.
loopback_task_host() {
  local os="$1" resolved="$2" gateway="$3"
  case "$os" in
    *Docker\ Desktop*)
      printf '%s\n' host.docker.internal
      return 0
      ;;
    *)
      ;;
  esac
  if [[ "$resolved" == yes ]]; then
    printf '%s\n' host.docker.internal
    return 0
  fi
  printf '%s\n' "$gateway"
}

# loopback_reachable is false when the chosen host is host.docker.internal
# and a container cannot resolve it. Docker Desktop resolves that name
# natively. There is no extra_hosts fallback. resolves is yes or no.
loopback_reachable() {
  local chosen="$1" resolves="$2"
  if [[ "$chosen" == host.docker.internal && "$resolves" != yes ]]; then
    return 1
  fi
}

# container_resolves is true when a container can resolve name. nslookup
# ignores /etc/hosts; Docker Desktop answers the name from its resolver.
container_resolves() {
  local name="$1"
  # shellcheck disable=SC2016 # single quotes are the container script
  spike_docker run --rm --platform "$OCI_PLATFORM" busybox:1.36 \
    sh -c 'grep -w -- "$1" /etc/hosts >/dev/null 2>&1 || nslookup "$1" >/dev/null 2>&1' \
    sh "$name"
}

# task_base_url rewrites a loopback host to replacement. Other hosts are
# unchanged. replacement is host.docker.internal or the bridge gateway.
# The value is a URL, not a secret.
task_base_url() {
  local raw="$1" replacement="$2"
  RAW="$raw" HOST="$replacement" python3 - <<'PY'
import os
from urllib.parse import urlparse, urlunparse
raw = os.environ["RAW"]
replacement = os.environ["HOST"]
parts = urlparse(raw)
host = parts.hostname or ""
if host in {"127.0.0.1", "localhost", "::1"}:
    port = f":{parts.port}" if parts.port else ""
    user = parts.username or ""
    if parts.password:
        user = f"{user}:{parts.password}"
    if user:
        user = user + "@"
    parts = parts._replace(netloc=user + replacement + port)
print(urlunparse(parts))
PY
}

# reset_stale_host_logs drops model-stub and git-daemon output from a previous
# run. Live mode does not start those processes, and a failure dump would
# otherwise print the old offline log.
reset_stale_host_logs() {
  rm -f "$WORK/model-stub.log" "$WORK/git-daemon.log"
}

# apply_offline_secrets overwrites live inference and GitHub values before any
# offline child is forked. The previous values stay on the redact list.
apply_offline_secrets() {
  local gw="$1"
  local live_gh="${FACTORY_SECRET_GH_TOKEN:-}"
  local live_key="${FACTORY_INFERENCE_OPENAI_API_KEY:-}"
  local live_url="${FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL:-}"
  local live_shell_gh="${GH_TOKEN:-}"
  local live_shell_key="${OPENAI_API_KEY:-}"
  local stub_url="http://${gw}:9090"
  unset GH_TOKEN OPENAI_API_KEY OPENAI_BASE_URL
  export FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL="$stub_url"
  export FACTORY_INFERENCE_OPENAI_API_KEY="$OFFLINE_DUMMY_KEY"
  export FACTORY_SECRET_GH_TOKEN="$OFFLINE_GH"
  redact_if_live "$live_gh" "$OFFLINE_GH"
  redact_if_live "$live_key" "$OFFLINE_DUMMY_KEY"
  redact_if_live "$live_url" "$stub_url"
  redact_if_live "$live_shell_gh" "$OFFLINE_GH"
  redact_if_live "$live_shell_key" "$OFFLINE_DUMMY_KEY"
  SECRETS+=("$OFFLINE_DUMMY_KEY" "$OFFLINE_GH")
}

# m2_fifo_reader prefixes each factoryctl line with a timestamp.
m2_fifo_reader() {
  local fifo="$1" out="$2" line
  while IFS= read -r line || [[ -n "$line" ]]; do
    printf '%s\t%s\n' "$(unix_ns)" "$line"
  done <"$fifo" >"$out"
}

# timestamped_run writes "<unix-ns>\t<line>" as factoryctl prints each line.
# The caller must be in a context where a non-zero return is not fatal
# (run_capture, or set +e). Re-enabling errexit before the return aborts
# the script, and fail never prints. The command runs under spike_fg so a
# trapped signal is handled while factoryctl is still in the foreground.
timestamped_run() {
  local out="$1"
  shift
  local fifo="$WORK/watch.fifo" reader="" code=0 wait_code=0
  rm -f "$fifo"
  mkfifo "$fifo"
  : >"$out"
  spike_spawn --class transient fifo-reader m2_fifo_reader "$fifo" "$out"
  reader="$SPIKE_LAST_PID"
  set +e
  spike_fg "$@" >"$fifo"
  code=$?
  wait "$reader" || wait_code=$?
  rm -f "$fifo"
  if [[ "$code" -eq 0 && "$wait_code" -ne 0 ]]; then
    code=$wait_code
  fi
  return "$code"
}

assert_streamed_before_terminal() {
  local file="$1"
  local first="" term="" ts line
  while IFS=$'\t' read -r ts line || [[ -n "${ts:-}" ]]; do
    case "$line" in
      message.delta\ * | tool.call\ * | tool.result\ * | usage\ *)
        if [[ -z "$first" ]]; then
          first="$ts"
        fi
        ;;
      terminal\ *)
        term="$ts"
        ;;
      *)
        ;;
    esac
  done <"$file"
  if [[ -z "$first" || -z "$term" ]]; then
    echo "missing transcript or terminal line in $file" >&2
    return 1
  fi
  if [[ "$first" -ge "$term" ]]; then
    echo "transcript timestamp $first is not before terminal $term" >&2
    return 1
  fi
  local gap=$((term - first))
  if [[ "$gap" -lt 1000000000 ]]; then
    echo "transcript and terminal are ${gap}ns apart; not a live stream" >&2
    return 1
  fi
  echo "transcript streamed ${gap}ns before terminal"
}

run_status() {
  local id="$1" body
  body="$(curl -fsS -H "Authorization: Bearer ${FACTORY_API_KEY}" \
    "http://127.0.0.1:8080/api/v1/agent/runs/${id}")"
  printf '%s\n' "$body" | jq -r .status
}

wait_status() {
  local id="$1" want="$2" tries="$3" st
  local _
  for _ in $(seq 1 "$tries"); do
    st="$(run_status "$id" || true)"
    if [[ "$st" == "$want" ]]; then
      printf '%s\n' "$st"
      return 0
    fi
    sleep 1
  done
  printf '%s\n' "${st:-}"
  return 1
}

# mc_sh runs bash -s inside the pinned mc image on the compose network.
# The script is stdin. Secrets arrive only as container env.
mc_sh() {
  spike_docker run --rm -i --network "$NETWORK" \
    -e MINIO_ROOT_USER="$S3_ACCESS_KEY" \
    -e MINIO_ROOT_PASSWORD="$S3_SECRET_KEY" \
    -e RUN_ID="${RUN_ID:-}" \
    -e KEY="${KEY:-}" \
    --entrypoint /bin/bash \
    "$MC_IMAGE" -s
}

archive_object() {
  local run_id="$1" dest="$2"
  local found=""
  local _
  for _ in $(seq 1 30); do
    found="$(
      RUN_ID="$run_id" mc_sh <<'EOS'
set -euo pipefail
mkdir -p /tmp/mc
mc --config-dir /tmp/mc alias set local http://minio:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null
mc --config-dir /tmp/mc find "local/factory/runs/${RUN_ID}" --name events.ndjson
EOS
    )"
    if printf '%s\n' "$found" | grep -F 'events.ndjson' >/dev/null; then
      break
    fi
    sleep 1
  done
  local key="" line
  while IFS= read -r line; do
    case "$line" in
      *events.ndjson*)
        key="${line//$'\r'/}"
        break
        ;;
      *)
        ;;
    esac
  done <<<"$found"
  if [[ -z "$key" ]]; then
    return 1
  fi
  echo "mc stat $key"
  RUN_ID="$run_id" KEY="$key" mc_sh <<'EOS' || return 1
set -euo pipefail
mkdir -p /tmp/mc
mc --config-dir /tmp/mc alias set local http://minio:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null
mc --config-dir /tmp/mc stat "$KEY"
EOS
  mkdir -p "$(dirname "$dest")"
  RUN_ID="$run_id" KEY="$key" mc_sh <<'EOS' >"$dest" || return 1
set -euo pipefail
mkdir -p /tmp/mc
mc --config-dir /tmp/mc alias set local http://minio:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null
mc --config-dir /tmp/mc cat "$KEY"
EOS
  [[ -s "$dest" ]]
}

patch_model() {
  local dest="$1" model="$2"
  rm -rf "$dest"
  mkdir -p "$dest"
  cp -a "$ROOT/deploy/m2-factory/." "$dest/"
  DEF="$dest" NEW_MODEL="$model" python3 - <<'PY'
import os
from pathlib import Path
root = Path(os.environ["DEF"])
old = "m2-offline-stub"
new = os.environ["NEW_MODEL"]
for path in root.rglob("*"):
    if path.is_file():
        text = path.read_text()
        if old in text:
            path.write_text(text.replace(old, new))
PY
  chmod -R a+rX "$dest"
}

# Sourced by spike/m2/run_test.sh. Executing the file runs the laptop stack.
if [[ "${BASH_SOURCE[0]}" != "$0" ]]; then
  return 0
fi

trap 'cleanup EXIT' EXIT
trap 'cleanup INT' INT
trap 'cleanup TERM' TERM
trap 'on_err $? $LINENO' ERR

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
command -v python3 >/dev/null || die "python3 not found"
command -v jq >/dev/null || die "jq not found"
docker info >/dev/null 2>&1 || die "docker daemon is not reachable"

platform="$(docker info --format '{{.OSType}}/{{.Architecture}}' 2>/dev/null || true)"
case "$platform" in
  linux/aarch64 | linux/arm64 | linux/x86_64 | linux/amd64) ;;
  *)
    die "unsupported docker platform: $platform"
    ;;
esac
OCI_PLATFORM="$(normalize_docker_platform "$platform")" || die "unsupported docker platform: ${platform}"
GOARCH_TARGET="$(goarch_for_platform "$OCI_PLATFORM")" || die "unsupported docker platform: ${platform}"
# Builds and runs follow the daemon, not a leftover DOCKER_DEFAULT_PLATFORM.
export DOCKER_DEFAULT_PLATFORM="$OCI_PLATFORM"
echo "docker platform: $platform"
echo "image platform: $OCI_PLATFORM"
echo "go: $(go version)"
echo "mode: $MODE"

if [[ -f "$ROOT/deploy/.env" ]]; then
  load_env_file "$ROOT/deploy/.env" || die "deploy/.env is invalid"
fi
apply_live_env_aliases

if [[ "$MODE" == live ]]; then
  command -v gh >/dev/null || die "gh not found (required for MODE=live)"
  [[ -n "${OPENAI_BASE_URL:-}" ]] || die "OPENAI_BASE_URL or FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL is required for MODE=live"
  [[ -n "${M2_MODEL:-}" ]] || die "M2_MODEL is required for MODE=live"
  [[ -n "${GH_TOKEN:-}" ]] || die "GH_TOKEN or FACTORY_SECRET_GH_TOKEN is required for MODE=live"
  M2_REPO="${M2_REPO:-polds/factory}"
  echo "live repo: ${M2_REPO}"
fi

mkdir -p "$WORK" "$WORK/evidence"
spike_run_init m2
reset_stale_host_logs
prove_scanner

GW="$(bridge_gateway)"
echo "docker bridge gateway: $GW"
echo "task containers reach factoryd at http://${GW}:8080"
DOCKER_OS="$(docker info --format '{{.OperatingSystem}}' 2>/dev/null || true)"
name_resolves=no
if host_name_resolves host.docker.internal; then
  name_resolves=yes
fi
LOOPBACK_HOST="$(loopback_task_host "$DOCKER_OS" "$name_resolves" "$GW")"
echo "loopback from a task container: ${LOOPBACK_HOST}"

export POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-factory}"
export S3_ACCESS_KEY="${S3_ACCESS_KEY:-factoryadmin}"
export S3_SECRET_KEY="${S3_SECRET_KEY:-m2s3-secret-7f3c9a2e1b4d}"
export FACTORY_API_KEY="${FACTORY_API_KEY:-m2-api-7f3c9a2e1b4d}"
export FACTORY_WORKER_API_KEY="${FACTORY_WORKER_API_KEY:-m2-worker-7f3c9a2e1b4d}"
export FACTORY_EXTERNAL_URL="${FACTORY_EXTERNAL_URL:-http://127.0.0.1:8080}"
export FACTORY_TASK_SERVER_URL="http://${GW}:8080"
export FACTORY_LOG_LEVEL="${FACTORY_LOG_LEVEL:-info}"
SECRETS+=("$S3_SECRET_KEY" "$FACTORY_API_KEY" "$FACTORY_WORKER_API_KEY")

DEF="$ROOT/deploy/m2-factory"
if [[ "$MODE" == offline ]]; then
  # Replace live tokens before git daemon and the model stub are forked.
  apply_offline_secrets "$GW"
  say "Serve the fixture and the model stub on ${GW}"
  rm -rf "$WORK/git"
  mkdir -p "$WORK/git"
  seed="$(mktemp -d)"
  git init -b main "$seed" >/dev/null
  cp -a "$ROOT/spike/m1/fixture/." "$seed/"
  git -C "$seed" -c user.email=fixture@example.com -c user.name=fixture add -A
  git -C "$seed" -c user.email=fixture@example.com -c user.name=fixture commit -m "fixture baseline" >/dev/null
  git init --bare -b main "$WORK/git/remote.git" >/dev/null
  git -C "$seed" remote add origin "$WORK/git/remote.git"
  git -C "$seed" push -q origin main
  git --git-dir="$WORK/git/remote.git" symbolic-ref HEAD refs/heads/main
  rm -rf "$seed"
  git --git-dir="$WORK/git/remote.git" show main:greet/greet.go | grep -F '"Hello, " + name' >/dev/null

  sed "s#__M1_GIT_HOST__#${GW}#g" "$ROOT/spike/m1/offline-script.json" >"$WORK/offline-script.json"
  if grep -q '__M1_GIT_HOST__' "$WORK/offline-script.json"; then
    die "git host was not substituted into the offline script"
  fi

  GITD_PIDFILE="$WORK/git-daemon.pid"
  rm -f "$GITD_PIDFILE"
  spike_spawn --class stack git-daemon --out "$WORK/git-daemon.log" --err "$WORK/git-daemon.log" \
    git daemon --reuseaddr --export-all --enable=receive-pack \
    --listen="$GW" --port=9418 --pid-file="$GITD_PIDFILE" \
    --base-path="$WORK/git" "$WORK/git"
  spike_fg_dir "$ROOT" env CGO_ENABLED=0 go build -o "$WORK/model-stub" ./spike/m1/model-stub
  spike_spawn --class stack model-stub --out "$WORK/model-stub.log" --err "$WORK/model-stub.log" \
    "$WORK/model-stub" -addr "${GW}:9090" -script "$WORK/offline-script.json"
  MODEL_PID=$SPIKE_LAST_PID
else
  TS="$(date -u +%Y%m%dT%H%M%SZ 2>/dev/null || true)"
  if [[ ! "$TS" =~ ^[0-9]{8}T[0-9]{6}Z$ ]]; then
    TS="$(python3 -c 'from datetime import datetime, timezone; print(datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ"))')"
  fi
  BRANCH="m2/run-${TS}"
  say "Point the definition at ${M2_MODEL}"
  patch_model "$WORK/definition" "$M2_MODEL"
  DEF="$WORK/definition"
  export FACTORY_DEFINITION_HOST_PATH="$DEF"
  live_base="$(task_base_url "$OPENAI_BASE_URL" "$LOOPBACK_HOST")"
  echo "task OPENAI_BASE_URL host: $(url_log_host "$live_base")"
  export FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL="$live_base"
  export FACTORY_INFERENCE_OPENAI_API_KEY="${OPENAI_API_KEY:-}"
  export FACTORY_SECRET_GH_TOKEN="$GH_TOKEN"
  SECRETS+=("$GH_TOKEN")
  if [[ -n "${OPENAI_API_KEY:-}" ]]; then
    SECRETS+=("$OPENAI_API_KEY")
  fi
fi

# ---------------------------------------------------------------------------
say "Build factoryctl, the harness image, and the task image"
spike_fg_dir "$ROOT" env CGO_ENABLED=0 go build -o "$WORK/factoryctl" ./cmd/factoryctl
# The harness is copied into a Linux image and exec'd in the task container.
# A host binary (darwin/arm64 on a Mac) is not a Linux ELF; bash exits 126.
spike_fg_dir "$ROOT" env CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH_TARGET" \
  go build -o harness/generic-openai/factory-harness ./cmd/factory-harness
assert_linux_elf "$ROOT/harness/generic-openai/factory-harness" "$GOARCH_TARGET" ||
  fail "harness binary is not a linux/${GOARCH_TARGET} ELF"
spike_fg docker build --platform "$OCI_PLATFORM" -q -t "$HARNESS_IMAGE" "$ROOT/harness/generic-openai" >/dev/null
assert_image_platform "$HARNESS_IMAGE" "$OCI_PLATFORM" ||
  fail "harness image platform does not match ${OCI_PLATFORM}"
spike_fg docker build --platform "$OCI_PLATFORM" -q -t "$TASK_IMAGE" "$ROOT/spike/m1/task-image" >/dev/null
assert_image_platform "$TASK_IMAGE" "$OCI_PLATFORM" ||
  fail "task image platform does not match ${OCI_PLATFORM}"
HARNESS_EXEC_CODE=0
run_capture HARNESS_EXEC_CODE spike_docker run --rm --platform "$OCI_PLATFORM" \
  --entrypoint /factory-harness "$HARNESS_IMAGE" \
  >"$WORK/image-exec.out" 2>"$WORK/image-exec.err"
if ! harness_contract_exit "$HARNESS_EXEC_CODE"; then
  fail "harness image cannot execute on ${OCI_PLATFORM} (exit ${HARNESS_EXEC_CODE})"
fi
rm -f "$WORK/image-exec.out" "$WORK/image-exec.err"
TASK_UNAME_CODE=0
run_capture TASK_UNAME_CODE spike_docker run --rm --platform "$OCI_PLATFORM" \
  --entrypoint /bin/sh "$TASK_IMAGE" -c 'uname -m && git --version >/dev/null && go version >/dev/null && command -v gh >/dev/null' \
  >"$WORK/task-uname.txt" 2>"$WORK/image-exec.err"
TASK_UNAME="$(tr -d '[:space:]' <"$WORK/task-uname.txt" 2>/dev/null || true)"
want_uname="$(uname_for_platform "$OCI_PLATFORM")"
if [[ "$TASK_UNAME_CODE" -ne 0 || "$TASK_UNAME" != "$want_uname" ]]; then
  fail "task image failed on ${OCI_PLATFORM} (uname ${TASK_UNAME:-empty}, exit ${TASK_UNAME_CODE}, want ${want_uname})"
fi
rm -f "$WORK/task-uname.txt" "$WORK/image-exec.err"
FACTORYCTL="$WORK/factoryctl"
"$FACTORYCTL" validate "$DEF"

if [[ "$MODE" == offline ]]; then
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
    fail "git daemon or model stub did not become reachable on ${GW}"
  fi
  spike_docker run --rm --network bridge --entrypoint git "$TASK_IMAGE" \
    ls-remote "git://${GW}/remote.git" >/dev/null
  spike_docker run --rm --network bridge --entrypoint curl "$TASK_IMAGE" \
    -fsS "http://${GW}:9090/healthz" >/dev/null
  echo "fixture and model stub reachable from a bridge container"
fi

if [[ "$LOOPBACK_HOST" == host.docker.internal ]]; then
  container_ok=no
  if container_resolves host.docker.internal >/dev/null 2>&1; then
    container_ok=yes
  fi
  if ! loopback_reachable "$LOOPBACK_HOST" "$container_ok"; then
    fail "host.docker.internal does not resolve in a task container"
  fi
fi

# ---------------------------------------------------------------------------
say "Start the laptop stack"
COMPOSE_STARTED=1
spike_fg compose build
assert_image_platform factoryd:dev "$OCI_PLATFORM" ||
  fail "factoryd image platform does not match ${OCI_PLATFORM}"
assert_image_platform factory-upstream-worker:dev "$OCI_PLATFORM" ||
  fail "worker image platform does not match ${OCI_PLATFORM}"
spike_fg compose up -d --wait postgres
spike_fg compose --profile migrate run --rm migrate
# Exact laptop command, from the directory that holds the compose file.
# shellcheck disable=SC2016 # single quotes are the child script
spike_fg bash -c 'cd "$1" && docker compose -p "$2" up -d --build --wait' spike-fg-dir "$ROOT/deploy" "$PROJECT"
COMPOSE_STARTED=1

for _ in $(seq 1 30); do
  if curl -fsS "http://127.0.0.1:8080/healthz" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
if ! curl -fsS "http://127.0.0.1:8080/healthz" >/dev/null 2>&1; then
  fail "factoryd /healthz did not become ready"
fi
echo "factoryd healthy on :8080"

export FACTORY_URL="http://127.0.0.1:8080"

# ---------------------------------------------------------------------------
if [[ "$MODE" == offline ]]; then
  PROMPT='M1 offline: clone the fixture, make Hello add an exclamation mark, test, and push branch m1-run.'
  TITLE="M2 offline agent run"
else
  PROMPT="Clone https://github.com/${M2_REPO} into repo. Change spike/m1/fixture/greet/greet.go so Hello returns the concatenation of \"Hello, \", name, and \"!\". Update spike/m1/fixture/greet/greet_test.go so the expected greeting for M1 is Hello, M1 with an exclamation mark. Run go test ./... in spike/m1/fixture. Create and push branch ${BRANCH}. GH_TOKEN is already exported; run gh auth setup-git before git push, and do not print GH_TOKEN or OPENAI_API_KEY. Open a draft pull request with gh pr create --draft --title 'M2: emphasize Hello' --body 'Update the fixture greeting.' Reply with the pull request URL."
  TITLE="M2 live agent run"
fi

say "factoryctl run --watch"
RUN_CODE=0
run_capture RUN_CODE timestamped_run "$WORK/watch-run.ts" \
  "$FACTORYCTL" run --prompt "$PROMPT" --factory m2 --agent implementer --title "$TITLE" --watch \
  2>"$WORK/factoryctl-run.err"
if [[ "$RUN_CODE" -ne 0 ]]; then
  fail "factoryctl run --watch exited ${RUN_CODE}"
fi
RUN_ID="$(awk -F '\t' 'NR==1 {print $2}' "$WORK/watch-run.ts")"
if [[ ! "$RUN_ID" =~ ^run_ ]]; then
  fail "first watch line was not a run id"
fi
echo "run id: $RUN_ID"
assert_streamed_before_terminal "$WORK/watch-run.ts" || fail "transcript was not streamed before terminal"
# Watch returns on the harness terminal event. factoryd marks the run
# succeeded when the worker's task_completed arrives, which is after the
# container exits.
STATUS="$(wait_status "$RUN_ID" succeeded 60 || true)"
if [[ "$STATUS" != "succeeded" ]]; then
  fail "run status is ${STATUS:-unknown}, want succeeded"
fi
echo "run ${RUN_ID} succeeded"

say "Archive in MinIO"
if ! archive_object "$RUN_ID" "$WORK/evidence/events.ndjson"; then
  fail "events object missing in MinIO for ${RUN_ID}"
fi
echo "archived events: spike/m2/.work/evidence/events.ndjson"

if [[ "$MODE" == offline ]]; then
  greet="$(git --git-dir="$WORK/git/remote.git" show m1-run:greet/greet.go 2>/dev/null || true)"
  testf="$(git --git-dir="$WORK/git/remote.git" show m1-run:greet/greet_test.go 2>/dev/null || true)"
  if ! printf '%s\n' "$greet" | grep -F 'name + "!"' >/dev/null; then
    fail "branch m1-run is missing the updated Hello line"
  fi
  if ! printf '%s\n' "$testf" | grep -F 'Hello, M1!' >/dev/null; then
    fail "branch m1-run is missing the updated test"
  fi
  echo "branch m1-run pushed"

  say "Cancel a second long run"
  if [[ -n "${MODEL_PID}" ]]; then
    kill "${MODEL_PID}" 2>/dev/null || true
    wait "${MODEL_PID}" 2>/dev/null || true
    MODEL_PID=""
  fi
  cat >"$WORK/hang-script.json" <<'JSON'
[
  {
    "tool": "bash",
    "args": {
      "command": "sleep 180"
    }
  }
]
JSON
  spike_spawn --class stack model-stub --out "$WORK/model-stub.log" --err "$WORK/model-stub.log" --append \
    "$WORK/model-stub" -addr "${GW}:9090" -script "$WORK/hang-script.json"
  MODEL_PID=$SPIKE_LAST_PID
  for _ in $(seq 1 50); do
    if curl -fsS "http://${GW}:9090/healthz" >/dev/null 2>&1; then
      break
    fi
    sleep 0.2
  done
  if ! curl -fsS "http://${GW}:9090/healthz" >/dev/null 2>&1; then
    fail "model stub did not restart for the cancel run"
  fi

  CANCEL_ID="$("$FACTORYCTL" run --prompt "M2 offline: sleep so this run can be cancelled." \
    --factory m2 --agent implementer --title "M2 cancel" 2>"$WORK/factoryctl-cancel.err")"
  if [[ ! "$CANCEL_ID" =~ ^run_ ]]; then
    fail "cancel run did not return a run id"
  fi
  echo "cancel run id: $CANCEL_ID"

  # The background shell must not treat watch's exit 2 as a script failure.
  trap - ERR
  set +e
  spike_spawn --class transient factoryctl-watch --err "$WORK/factoryctl-cancel.err" --append \
    timestamped_run "$WORK/watch-cancel.ts" \
    "$FACTORYCTL" watch "$CANCEL_ID"
  WATCH_PID=$SPIKE_LAST_PID
  set -e
  trap 'on_err $? $LINENO' ERR
  saw_tool=0
  for _ in $(seq 1 90); do
    if [[ -f "$WORK/watch-cancel.ts" ]] && grep -F -q $'tool.call' "$WORK/watch-cancel.ts"; then
      saw_tool=1
      break
    fi
    if ! kill -0 "$WATCH_PID" 2>/dev/null; then
      break
    fi
    sleep 1
  done
  if [[ "$saw_tool" -ne 1 ]]; then
    kill "$WATCH_PID" 2>/dev/null || true
    wait "$WATCH_PID" 2>/dev/null || true
    fail "cancel run did not stream a tool.call before cancellation"
  fi
  "$FACTORYCTL" cancel "$CANCEL_ID" >>"$WORK/factoryctl-cancel.err"
  WATCH_CODE=0
  run_capture WATCH_CODE wait "$WATCH_PID"
  if [[ "$WATCH_CODE" -ne 2 ]]; then
    fail "factoryctl watch on the cancelled run exited ${WATCH_CODE}, want 2"
  fi
  CANCEL_STATUS="$(wait_status "$CANCEL_ID" cancelled 90 || true)"
  if [[ "$CANCEL_STATUS" != "cancelled" ]]; then
    fail "cancel run status is ${CANCEL_STATUS:-unknown}, want cancelled"
  fi
  echo "run ${CANCEL_ID} cancelled"
  archive_object "$CANCEL_ID" "$WORK/evidence/cancel-events.ndjson" ||
    echo "cancel run archive not required"

  save_compose_logs || true
  if ! scan_logs_for_secrets; then
    announce_leak
    exit 1
  fi
  echo
  echo "M2 OFFLINE PASS — factoryctl run --watch streamed the transcript, pushed m1-run, archived events, and cancelled a second run."
  exit 0
fi

PR_URL="$(gh pr list -R "$M2_REPO" --head "$BRANCH" --json url -q '.[0].url')"
if [[ -z "$PR_URL" || "$PR_URL" == "null" ]]; then
  fail "no pull request for ${BRANCH} on ${M2_REPO}"
fi
save_compose_logs || true
if ! scan_logs_for_secrets; then
  announce_leak
  exit 1
fi
echo
echo "$PR_URL"
echo "M2 LIVE PASS — factoryd opened ${PR_URL}."
exit 0
