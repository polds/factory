#!/usr/bin/env bash
# M6 exit loop.
#
#   MODE=offline ./spike/m6/run.sh
#   MODE=live ./spike/m6/run.sh
#   MODE=live-ci ./spike/m6/run.sh
#   KEEP_UP=1 ./spike/m6/run.sh
#   ./spike/m6/run.sh --manual
#   ./spike/m6/run.sh specs
#   ./spike/m6/run.sh down
#
# offline plays the agent, the judge, and the improver. live and live-ci
# use the same spec with structural assertions. A missing live variable
# prints "missing <NAME>" and exits before any container starts.
#
# Live and live-ci give M6_DEFINITION_GIT_URL to factoryd and to the
# task secret DEFINITION_GIT_URL, and M6_GITHUB_API_BASE to
# FACTORY_GITHUB_API_BASE and the task secret GITHUB_API_BASE. The
# same value is used for both processes and is not rewritten. Offline
# ignores those two variables and uses the seeded git daemon and the
# local forge. The seeded repo is not the live definition.
#
# Live and live-ci also preflight the App, push a run-scoped base, watch
# this run's improvement pull request, and delete only that run's remote
# resources. Offline skips that layer. ./spike/m6/run.sh --cleanup-remote
# repeats the remote cleanup from the saved ledger.
#
# The offline improver pushes the branch and does not open a pull
# request, so factoryd's create is the 201 that carries improve.PRBody.
# M6_IMPROVER_OPENS_PR=1 is the live-only branch: the improver opens
# the request first and factoryd's create is 422, then a lookup.

set -euo pipefail
# shellcheck source=spike/lib/common.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/../lib/common.sh"
# shellcheck source=spike/lib/driver.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/../lib/driver.sh"
# shellcheck source=spike/lib/cleanup.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/../lib/cleanup.sh"

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

# m6_on_err names a set -e exit. The ERR trap also runs when errexit is
# off; those regions (Playwright, specs) already record the status and
# call die(). A subshell exits without a line so the shell that armed
# the trap reports the step once. die() is unchanged: exit does not
# raise ERR.
# shellcheck disable=SC2317,SC2329 # invoked by the ERR trap
m6_on_err() {
  local status="${1:-1}"
  if [[ $- != *e* ]]; then
    return 0
  fi
  if [[ -z "${m6_main_bashpid:-}" || "$BASHPID" -ne "$m6_main_bashpid" ]]; then
    exit "$status"
  fi
  trap - ERR
  printf 'M6 FAIL — %s: exit %s\n' "${m6_step:-run}" "$status" >&2
  exit "$status"
}

m6_arm_err() {
  m6_main_bashpid=$BASHPID
  m6_step="${m6_step:-startup}"
  set -E
  trap 'm6_on_err $?' ERR
}

# m6_publish_stdout replaces dest only after the command has finished
# writing. The temp file sits in the same directory, so the rename is
# atomic. A non-zero status leaves the previous body in place.
m6_publish_stdout() {
  local dest="$1"
  shift
  local tmp="${dest}.tmp" status=0
  mkdir -p "$(dirname "$dest")"
  if "$@" >"$tmp"; then
    mv -f "$tmp" "$dest"
    return 0
  else
    # $? after fi is 0. The failing command's status is only visible here.
    status=$?
  fi
  rm -f "$tmp"
  return "$status"
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
      require_var M6_DEFINITION_GIT_URL || return 1
      require_var M6_GITHUB_API_BASE || return 1
      require_var FACTORY_GITHUB_APP_ID || return 1
      require_key_file || return 1
      ;;
    live-ci)
      require_var M1_OPENAI_BASE_URL || return 1
      require_var M1_OPENAI_API_KEY || return 1
      require_var M6_LIVE_MODEL || return 1
      require_var M6_DEFINITION_GIT_URL || return 1
      require_var M6_GITHUB_API_BASE || return 1
      require_var FACTORY_GITHUB_APP_ID || return 1
      require_key_file || return 1
      ;;
    *)
      echo "MODE must be offline, live, or live-ci" >&2
      return 1
      ;;
  esac
}

# assign_app_key is the host path compose mounts into factoryd.
# Offline uses the generated .work/app.pem. Live and live-ci use the
# file require_key_file already checked.
assign_app_key() {
  case "${MODE:-}" in
    offline)
      M6_APP_KEY_FILE="$WORK/app.pem"
      ;;
    live | live-ci)
      M6_APP_KEY_FILE="$FACTORY_GITHUB_APP_PRIVATE_KEY_FILE"
      ;;
    *)
      echo "MODE must be offline, live, or live-ci" >&2
      return 1
      ;;
  esac
  export M6_APP_KEY_FILE
}

# apply_definition_targets sets the URLs factoryd clones and the URLs
# task containers clone. Offline uses the seeded daemon and the local
# forge. Live and live-ci use M6_DEFINITION_GIT_URL and
# M6_GITHUB_API_BASE unchanged for both sides.
apply_definition_targets() {
  if [[ "${MODE:-}" == offline ]]; then
    M6_FACTORYD_GIT_URL="git://host.docker.internal:${GIT_PORT}/m6/factory.git"
    M6_TASK_GIT_URL="git://${TASK_HOST}:${GIT_PORT}/m6/factory.git"
    M6_FACTORYD_GITHUB_API="http://host.docker.internal:${FORGE_PORT}"
    M6_TASK_GITHUB_API="http://${TASK_HOST}:${FORGE_PORT}"
  else
    M6_FACTORYD_GIT_URL="$M6_DEFINITION_GIT_URL"
    M6_TASK_GIT_URL="$M6_DEFINITION_GIT_URL"
    M6_FACTORYD_GITHUB_API="${M6_GITHUB_API_BASE%/}"
    M6_TASK_GITHUB_API="${M6_GITHUB_API_BASE%/}"
  fi
  export M6_FACTORYD_GIT_URL M6_TASK_GIT_URL M6_FACTORYD_GITHUB_API M6_TASK_GITHUB_API
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
  local base="$1" model="$2" dest="" hdr="" line="" name="" code="" status=0
  local -a names=()
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
      name="${line%%:*}"
      name="${name#"${name%%[![:space:]]*}"}"
      names+=("$name")
    fi
  done <"$hdr"
  rm -f "$hdr"
  args+=(-H "Content-Type: application/json")
  names+=("Content-Type")
  args+=(-d "{\"model\":\"${model}\",\"max_tokens\":1,\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}")
  args+=("${base%/}/chat/completions")
  code="$("${TOKEN_CURL:-curl}" "${args[@]}")" || status=$?
  code="${code//$'\n'/}"
  code="${code//$'\r'/}"
  if [[ -z "$code" ]]; then
    code="curl-${status}"
  fi
  probe_report "token probe" "$code" "$dest" "$model" "${names[@]}"
  rm -f "$dest"
  [[ "$status" -eq 0 && "$code" == 2* ]]
}

preflight_live() {
  local probe="" catalog="" id=""
  reject_loopback "$FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL"
  probe="$(host_catalog_probe_url "$FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL" "${TASK_HOST:-}")"
  catalog="$WORK/catalog.json"
  mkdir -p "$WORK"
  if ! catalog_probe "$probe" "$catalog"; then
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
  spike_spawn --class stack git --out "$WORK/git-daemon.log" --err "$WORK/git-daemon.log" \
    git daemon --reuseaddr --export-all --enable=receive-pack \
    --listen=0.0.0.0 --port="$GIT_PORT" \
    --base-path="$WORK/git" "$WORK/git"
  spike_fg wait_port "$GIT_PORT"
}

start_forge() {
  FAKE_PORT="$(pick_port M6_FAKE_PORT "")"
  export FAKE_PORT
  spike_fg_dir "$ROOT" env \
    CGO_ENABLED=0 \
    GOOS="$(uname -s | tr '[:upper:]' '[:lower:]')" \
    GOARCH="$(go env GOARCH)" \
    go build -o "$WORK/fake-github" ./spike/m4/fakes/github
  spike_spawn --class stack fake --out "$WORK/fake-github.log" --err "$WORK/fake-github.log" \
    "$WORK/fake-github" -addr "127.0.0.1:${FAKE_PORT}"
  if ! spike_fg wait_http "http://127.0.0.1:${FAKE_PORT}/healthz"; then
    echo "fake-github did not become reachable" >&2
    return 1
  fi
  spike_spawn --class stack forge --out "$WORK/forge.log" --err "$WORK/forge.log" \
    env \
    M6_UPSTREAM_GITHUB="http://127.0.0.1:${FAKE_PORT}" \
    M6_TOKEN_FILE="$WORK/install-token" \
    python3 "$ROOT/spike/m6/forge.py" --addr "0.0.0.0:${FORGE_PORT}"
  if ! spike_fg wait_http "http://127.0.0.1:${FORGE_PORT}/_spike/m6"; then
    echo "forge did not become reachable" >&2
    return 1
  fi
}

start_router() {
  spike_spawn --class stack router --out "$WORK/router.log" --err "$WORK/router.log" \
    env \
    M6_DEFINITION_GIT_URL="$M6_TASK_GIT_URL" \
    M6_GITHUB_API_BASE="$M6_TASK_GITHUB_API" \
    python3 "$ROOT/spike/m6/router.py" --addr "0.0.0.0:${ROUTER_PORT}"
  if ! spike_fg wait_http "http://127.0.0.1:${ROUTER_PORT}/healthz"; then
    echo "model router did not become reachable" >&2
    return 1
  fi
}

watch_candidate_offline() {
  local bare="$WORK/git/m6/factory.git" ref="" sha="" id="" cmd=""
  while [[ ! -f "$SIGNAL/stop" ]]; do
    # improve/<batch id> is refs/heads/improve/<id>, not refs/heads/improve.
    ref="$(git --git-dir="$bare" for-each-ref --format='%(refname)' 'refs/heads/improve/*' 2>/dev/null | head -n 1 || true)"
    if [[ -n "$ref" ]]; then
      sha="$(git --git-dir="$bare" rev-parse "$ref")"
      git --git-dir="$bare" update-ref refs/heads/candidate "$sha"
      mkdir -p "$EVIDENCE" "$SIGNAL"
      # skill.diff is rewritten on later polls, after candidate-ready.
      # Publish the finished diff so a reader never sees a torn file.
      m6_publish_stdout "$EVIDENCE/skill.diff" \
        git --git-dir="$bare" diff main "$ref" || true
      printf '%s\n' "$sha" >"$SIGNAL/candidate-ready"
    fi
    if [[ -n "${FORGE_PORT:-}" ]]; then
      m6_publish_stdout "$EVIDENCE/forge.json" \
        curl -fsS --connect-timeout 2 -m 5 "http://127.0.0.1:${FORGE_PORT}/_spike/m6" || true
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

watch_candidate_live() {
  local id="" cmd=""
  while [[ ! -f "$SIGNAL/stop" ]]; do
    if ! remote_cmd watch-once; then
      printf 'watch\n' >>"$WORK/watch.err" || true
    fi
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

watch_candidate() {
  if [[ "${MODE:-}" == offline ]]; then
    watch_candidate_offline
    return
  fi
  watch_candidate_live
}

remote_cmd() {
  MODE="$MODE" \
    M6_WORK="$WORK" \
    M6_EVIDENCE="${EVIDENCE:-}" \
    M6_SIGNAL="${SIGNAL:-}" \
    M6_DEFINITION="$WORK/definition" \
    python3 "$ROOT/spike/m6/remote.py" "$@"
}

preflight_remote() {
  if [[ "${MODE:-}" == offline ]]; then
    return 0
  fi
  remote_cmd preflight
}

seed_remote() {
  if [[ "${MODE:-}" == offline ]]; then
    unset M6_DEFINITION_REF
    unset M6_RUN_BASE
    return 0
  fi
  if ! spike_fg remote_cmd seed; then
    local reason="remote seed failed"
    if [[ -f "$WORK/remote-fail" ]]; then
      IFS= read -r reason <"$WORK/remote-fail" || true
    fi
    die "$reason"
  fi
  M6_DEFINITION_REF="$(tr -d '[:space:]' <"$WORK/m6-definition-ref")"
  M6_RUN_BASE="$M6_DEFINITION_REF"
  export M6_DEFINITION_REF M6_RUN_BASE
}

keep_remote_state() {
  [[ "${KEEP_UP:-}" == 1 || "${M6_KEEP_REMOTE:-}" == 1 || "${MANUAL:-}" == 1 ]]
}

note_remote_kept() {
  if [[ "${MODE:-}" == offline || ! -f "$WORK/remote-ledger.json" ]]; then
    return 0
  fi
  printf 'remote ledger: %s\n' "$WORK/remote-ledger.json"
  printf 'cleanup: ./spike/m6/run.sh --cleanup-remote\n'
}

copy_remote_ledger() {
  if [[ -f "$WORK/remote-ledger.json" ]]; then
    mkdir -p "$EVIDENCE"
    cp "$WORK/remote-ledger.json" "$EVIDENCE/remote-ledger.json"
  fi
}

# cleanup reads reviewer logins. A later down or --cleanup-remote is a
# new process, so apply the same default the main path uses.
m6_arm_reviewers() {
  export FACTORY_REVIEWERS_ADMINS="${FACTORY_REVIEWERS_ADMINS:-m6-reviewer}"
}

cleanup_remote() {
  if [[ "${MODE:-}" == offline ]]; then
    return 0
  fi
  m6_arm_reviewers
  if keep_remote_state; then
    note_remote_kept
    copy_remote_ledger
    return 0
  fi
  remote_cmd cleanup || true
  copy_remote_ledger
}

copy_shots() {
  local name="" dest="/opt/cursor/artifacts"
  # GitHub-hosted runners cannot create /opt/cursor (root-owned /opt).
  # Skip the copy instead of failing the run after Playwright passed.
  if ! mkdir -p "$dest"; then
    printf 'copy_shots: cannot create %s; skipping\n' "$dest"
    return 0
  fi
  for name in scorers inbox improvement benchmarks; do
    if [[ -f "$EVIDENCE/${name}.png" ]]; then
      cp "$EVIDENCE/${name}.png" "$dest/${name}.png"
    fi
  done
}

m6_note() {
  if [[ -n "${M6_ORDER_LOG:-}" ]]; then
    printf '%s\n' "$1" >>"$M6_ORDER_LOG"
  fi
}

m6_count_pre() {
  local n=0
  if [[ -z "${M6_CLEANUP_PRE_COUNT:-}" ]]; then
    return 0
  fi
  if [[ -f "$M6_CLEANUP_PRE_COUNT" ]]; then
    n="$(tr -d '[:space:]' <"$M6_CLEANUP_PRE_COUNT" || true)"
  fi
  n=$((${n:-0} + 1))
  printf '%s\n' "$n" >"$M6_CLEANUP_PRE_COUNT"
}

# m6_stop_writers TERM-then-KILLs the watch group and an in-flight seed
# before cleanup_remote reads the ledger. The cap is about half a second.
m6_stop_writers() {
  local dir="" name="" child_argv="" pid="" pgid="" i=0 alive=0
  local -a pids=() pgids=()
  [[ -n "${SPIKE_REG:-}" && -d "${SPIKE_REG}/children" ]] || return 0
  for dir in "$SPIKE_REG"/children/*; do
    [[ -d "$dir" ]] || continue
    name="$(spike_read_field "$dir/name")"
    child_argv="$(spike_read_field "$dir/argv")"
    if [[ "$name" != watch && ("$child_argv" != *remote_cmd* || "$child_argv" != *seed*) ]]; then
      continue
    fi
    if ! spike_pid_identity_ok "$dir"; then
      continue
    fi
    pid="$(spike_read_field "$dir/pid")"
    pgid="$(spike_read_field "$dir/pgid")"
    if [[ -n "$pid" && "$pid" != "$$" ]]; then
      kill -TERM "$pid" 2>/dev/null || true
      pids+=("$pid")
    fi
    pgids+=("$pgid")
    spike_signal_group TERM "$pgid"
  done
  while [[ "$i" -lt 5 ]]; do
    alive=0
    if [[ "${#pids[@]}" -gt 0 ]]; then
      for pid in "${pids[@]}"; do
        if kill -0 "$pid" 2>/dev/null; then
          alive=1
        fi
      done
    fi
    if [[ "$alive" -eq 0 ]]; then
      break
    fi
    sleep 0.1
    i=$((i + 1))
  done
  if [[ "${#pgids[@]}" -gt 0 ]]; then
    for pgid in "${pgids[@]}"; do
      spike_signal_group KILL "$pgid"
    done
  fi
  if [[ "${#pids[@]}" -gt 0 ]]; then
    for pid in "${pids[@]}"; do
      kill -KILL "$pid" 2>/dev/null || true
      disown "$pid" 2>/dev/null || true
      wait "$pid" 2>/dev/null || true
    done
  fi
}

m6_budgeted_remote_cleanup() {
  local start="" end="" elapsed="" cap="" g=""
  if [[ "${MODE:-}" == offline ]]; then
    return 0
  fi
  if keep_remote_state; then
    note_remote_kept
    copy_remote_ledger
    return 0
  fi
  m6_arm_reviewers
  start="$(python3 -c 'import time; print("%.3f" % time.time())')"
  M6_CLEANUP_DEADLINE="$(python3 -c 'import time; print("%.3f" % (time.time() + 5))')"
  export M6_CLEANUP_DEADLINE
  m6_note "remote-begin"
  if ! remote_cmd cleanup; then
    printf 'remote cleanup incomplete; ledger %s; run ./spike/m6/run.sh --cleanup-remote\n' "$WORK/remote-ledger.json"
  fi
  unset M6_CLEANUP_DEADLINE
  end="$(python3 -c 'import time; print("%.3f" % time.time())')"
  elapsed="$(python3 -c 'import sys; print("%.3f" % (float(sys.argv[1]) - float(sys.argv[2])))' "$end" "$start")"
  cap="${SPIKE_STOP_GRACE:-5}"
  g="$(python3 -c 'import math,sys; e=float(sys.argv[1]); cap=int(float(sys.argv[2])); g=math.floor((6.2-e)/2); print(max(0, min(g, cap)))' "$elapsed" "$cap")"
  SPIKE_STOP_GRACE="$g"
  export SPIKE_STOP_GRACE
  if [[ -n "${M6_TRACE_CANCEL:-}" ]]; then
    printf 'cancel-window e=%s G=%s\n' "$elapsed" "$g"
  fi
  copy_remote_ledger
}

# shellcheck disable=SC2317,SC2329 # invoked by the EXIT/INT/TERM trap
spike_cleanup_pre() {
  m6_count_pre
  m6_note "cleanup-pre"
  mkdir -p "$SIGNAL" 2>/dev/null || true
  touch "$SIGNAL/stop" 2>/dev/null || true
  m6_stop_writers
  m6_note "writers-stopped"
  if [[ "${SPIKE_STOP_WHY:-EXIT}" == INT || "${SPIKE_STOP_WHY:-EXIT}" == TERM ]] && [[ "${MODE:-}" != offline ]]; then
    m6_budgeted_remote_cleanup
  else
    cleanup_remote
  fi
}

# Names only. stack_env_write is the one writer. Live inference values
# and token contents are not in this list.
m6_save_stack() {
  local STACK_MODE="${MODE:-}"
  local COMPOSE_PROJECT="${COMPOSE_PROJECT:-factory-m6}"
  local -a names=()
  names=(
    SPIKE_RUN_ID SPIKE_RUN_LABEL STACK_MODE COMPOSE_PROJECT
    CONSOLE_PORT GIT_PORT FORGE_PORT FAKE_PORT
    TASK_HOST M6_TASK_GIT_URL M6_TASK_GITHUB_API
    FACTORY_API_KEY FACTORY_WORKER_API_KEY S3_SECRET_KEY
    FACTORY_SECRET_M6_PLANTED FACTORY_GITHUB_WEBHOOK_SECRET
    M6_APP_KEY_FILE
  )
  if [[ "${MODE:-}" == offline ]]; then
    names+=(ROUTER_PORT FACTORY_INFERENCE_OPENAI_API_KEY FACTORY_GITHUB_APP_ID)
  else
    names+=(M6_DEFINITION_REF M6_RUN_BASE)
  fi
  stack_env_write "${names[@]}"
}

m6_remote_pending() {
  local rc=0
  [[ "${MODE:-}" == offline ]] && return 1
  [[ -f "$WORK/remote-ledger.json" ]] || return 1
  python3 - "$WORK/remote-ledger.json" <<'PY' || rc=$?
import json, sys
doc = json.load(open(sys.argv[1], encoding="utf-8"))
entries = doc.get("entries") or []
pending = any(isinstance(row, dict) and row.get("phase") != "cleaned" for row in entries)
raise SystemExit(0 if pending else 1)
PY
  if [[ "$rc" -eq 0 ]]; then
    return 0
  fi
  if [[ "$rc" -eq 1 ]]; then
    return 1
  fi
  return 0
}

# shellcheck disable=SC2317,SC2329 # invoked by the EXIT/INT/TERM trap
cleanup() {
  local code=$?
  local why="${1:-EXIT}"
  spike_teardown_begin "$why" || return 0
  if [[ "${KEEP_UP:-}" == 1 ]]; then
    if [[ -n "${SPIKE_RUN_LABEL:-}" ]]; then
      m6_save_stack || true
      printf 'specs: ./spike/m6/run.sh specs\n'
      printf 'down: ./spike/m6/run.sh down\n'
    fi
  else
    if [[ "${COMPOSE_STARTED:-0}" == 1 ]]; then
      # -v drops postgres and minio so a later offline run cannot see a
      # previous improvement batch and skip the improver.
      compose down -v --remove-orphans >/dev/null 2>&1 || true
      COMPOSE_STARTED=0
    fi
    rm -f "$WORK/stack.env"
  fi
  spike_teardown_finish "$code"
}

m6_stop_named() {
  local want="$1" dir=""
  [[ -n "${SPIKE_REG:-}" && -d "${SPIKE_REG}/children" ]] || return 0
  for dir in "$SPIKE_REG"/children/*; do
    [[ -d "$dir" ]] || continue
    if [[ "$(spike_read_field "$dir/name")" != "$want" ]]; then
      continue
    fi
    m6_stop_one "$dir"
    rm -rf "$dir"
  done
}

m6_stop_one() {
  local dir="$1" pid="" pgid="" i=0
  pid="$(spike_read_field "$dir/pid")"
  pgid="$(spike_read_field "$dir/pgid")"
  if [[ -n "$pid" && "$pid" != "$$" ]]; then
    kill -TERM "$pid" 2>/dev/null || true
  fi
  spike_signal_group TERM "$pgid"
  while [[ "$i" -lt 5 ]]; do
    if [[ -z "$pid" ]] || ! kill -0 "$pid" 2>/dev/null; then
      break
    fi
    sleep 0.1
    i=$((i + 1))
  done
  spike_signal_group KILL "$pgid"
  if [[ -n "$pid" && "$pid" != "$$" ]]; then
    kill -KILL "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  fi
}

m6_respawn_named() {
  local want="$1" i=1 n=0 name=""
  m6_stop_named "$want"
  n="${SPIKE_CHILD_COUNT:-0}"
  while [[ "$i" -le "$n" ]]; do
    eval "name=\${SPIKE_CHILD_${i}_NAME-}"
    if [[ "$name" == "$want" ]]; then
      spike_respawn_child "$i"
      printf '%s\n' "$SPIKE_LAST_PID"
      return 0
    fi
    i=$((i + 1))
  done
  return 1
}

m6_wait_listener() {
  local url="$1" pid="$2" index="$3" try=0
  while [[ "$try" -lt 20 ]]; do
    if curl -fsS --connect-timeout 1 -m 2 "$url" >/dev/null 2>&1; then
      return 0
    fi
    if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
      sleep 0.2
      try=$((try + 1))
      continue
    fi
    pid="$(m6_respawn_named "$index")"
    sleep 0.3
    try=$((try + 1))
  done
  return 1
}

m6_retire_open_batches() {
  local base="" factories="" uid="" batches="" started="" now="" id="" code="" rc=0
  base="http://127.0.0.1:${CONSOLE_PORT}"
  factories="$(curl -fsS --connect-timeout 2 -m 5 \
    -H "Authorization: Bearer ${FACTORY_API_KEY}" \
    "${base}/api/v1/factory" || true)"
  uid="$(printf '%s' "$factories" | python3 -c 'import json,sys
try:
    rows=json.load(sys.stdin)
except Exception:
    rows=[]
if isinstance(rows, list):
    for row in rows:
        if isinstance(row, dict) and row.get("name")=="m6":
            print(row.get("id") or "")
            break
')"
  if [[ -z "$uid" ]]; then
    return 1
  fi
  POST_SIGNED_SECRET="${FACTORY_GITHUB_WEBHOOK_SECRET:-}"
  export POST_SIGNED_SECRET
  started="$(date +%s)"
  while true; do
    batches="$(curl -fsS --connect-timeout 2 -m 5 \
      -H "Authorization: Bearer ${FACTORY_API_KEY}" \
      "${base}/api/v1/factory/${uid}/improvement-batches" || true)"
    set +e
    printf '%s' "$batches" | python3 -c 'import json,sys
try:
    rows=json.load(sys.stdin)
except Exception:
    raise SystemExit(2)
if not isinstance(rows, list):
    raise SystemExit(2)
raise SystemExit(0 if any(isinstance(r, dict) and r.get("status")=="pr_open" for r in rows) else 1)'
    rc=$?
    set -e
    if [[ "$rc" -eq 1 ]]; then
      return 0
    fi
    if [[ "$rc" -ne 0 ]]; then
      return 1
    fi
    python3 - "$batches" "$WORK" <<'PY'
import json, sys
from pathlib import Path
rows = json.loads(sys.argv[1] or "[]")
work = Path(sys.argv[2])
for row in rows:
    if not isinstance(row, dict) or row.get("status") != "pr_open":
        continue
    batch = str(row.get("id") or "")
    pr_url = str(row.get("pr_url") or "")
    parts = [part for part in pr_url.split("/") if part]
    number = 0
    owner, repo = "m6", "factory"
    if "pull" in parts:
        i = parts.index("pull")
        if i >= 2:
            owner, repo = parts[i - 2], parts[i - 1]
        if i + 1 < len(parts) and parts[i + 1].isdigit():
            number = int(parts[i + 1])
    body = {
        "action": "closed",
        "number": number,
        "pull_request": {
            "id": number or 1,
            "number": number,
            "title": "improvement",
            "body": "closed without merge",
            "html_url": pr_url or "https://github.com/m6/factory/pull/0",
            "merged": False,
            "updated_at": "2026-01-02T05:00:00Z",
            "labels": [],
            "head": {"ref": "improve/" + batch, "sha": "abc123"},
            "base": {"ref": "main", "sha": "def456"},
        },
        "repository": {"name": repo, "owner": {"login": owner}},
        "sender": {"login": "m6-reviewer", "id": 1, "type": "User"},
    }
    (work / ("retire-%s.json" % batch)).write_text(json.dumps(body), encoding="utf-8")
PY
    while IFS= read -r id; do
      [[ -n "$id" ]] || continue
      code="$(post_signed "${base}/api/v1/ingress/github" "$WORK/retire-${id}.json" \
        -H "X-GitHub-Event: pull_request" \
        -H "X-GitHub-Delivery: m6-close-${id}")"
      case "$code" in
        2*) ;;
        *) return 1 ;;
      esac
    done < <(python3 -c 'import json,sys
rows=json.loads(sys.argv[1] or "[]")
for row in rows:
    if isinstance(row, dict) and row.get("status")=="pr_open":
        print(row.get("id") or "")
' "$batches")
    now="$(date +%s)"
    if [[ $((now - started)) -gt 20 ]]; then
      return 1
    fi
    sleep 0.4
  done
}

m6_reset_offline_refs() {
  local bare="$WORK/git/m6/factory.git" ref=""
  [[ -d "$bare" ]] || return 0
  while IFS= read -r ref; do
    [[ -n "$ref" ]] || continue
    git --git-dir="$bare" update-ref -d "$ref" || true
  done < <(git --git-dir="$bare" for-each-ref --format='%(refname)' 'refs/heads/improve/*' || true)
  if git --git-dir="$bare" rev-parse --verify --quiet refs/heads/main >/dev/null 2>&1; then
    git --git-dir="$bare" update-ref refs/heads/candidate refs/heads/main
  fi
}

m6_restart_offline_children() {
  local forge_pid="" router_pid="" forge_index="" router_index="" i=1 n=0 name=""
  n="${SPIKE_CHILD_COUNT:-0}"
  while [[ "$i" -le "$n" ]]; do
    eval "name=\${SPIKE_CHILD_${i}_NAME-}"
    case "$name" in
      forge) forge_index="$i" ;;
      router) router_index="$i" ;;
      *) ;;
    esac
    i=$((i + 1))
  done
  if [[ -n "$forge_index" ]]; then
    forge_pid="$(m6_respawn_named forge)"
    if ! m6_wait_listener "http://127.0.0.1:${FORGE_PORT}/_spike/m6" "$forge_pid" forge; then
      echo "forge did not become reachable" >&2
      return 1
    fi
  fi
  if [[ -n "$router_index" ]]; then
    router_pid="$(m6_respawn_named router)"
    if ! m6_wait_listener "http://127.0.0.1:${ROUTER_PORT}/healthz" "$router_pid" router; then
      echo "model router did not become reachable" >&2
      return 1
    fi
  fi
}

# m6_redact_evidence scrubs the evidence directory in place. A failed scan
# removes the directory and returns non-zero. A missing directory is success.
m6_redact_evidence() {
  if [[ ! -d "$EVIDENCE" ]]; then
    return 0
  fi
  python3 "$ROOT/spike/m6/redact_evidence.py" --key-file "$WORK/api-key" "$EVIDENCE"
}

m6_playwright_and_evidence() {
  local play_start=0 evidence_start=0 play_code=0 play_log=""
  say "Playwright"
  play_start="$SECONDS"
  mkdir -p "$EVIDENCE" "$SIGNAL"
  rm -f "$SIGNAL/stop" "$SIGNAL/candidate-ready"
  PLAY_LOG="$EVIDENCE/playwright.log"
  play_log="$PLAY_LOG"
  export PLAY_LOG
  PLAYWRIGHT_DOCKER_ARGS="--init --label spike-run=${SPIKE_RUN_LABEL} --name spike-pw-${SPIKE_RUN_LABEL}"
  export PLAYWRIGHT_DOCKER_ARGS
  spike_spawn --class transient watch watch_candidate
  WATCH_PID=$SPIKE_LAST_PID
  export WATCH_PID
  set +e
  spike_fg spike_playwright_pipeline
  play_code=$?
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
  if [[ -f "$WORK/github-app-token" ]]; then
    M6_APP_TOKEN="$(tr -d '[:space:]' <"$WORK/github-app-token")"
    export M6_APP_TOKEN
  fi
  copy_remote_ledger
  # M6_TASK_GIT_URL and M6_TASK_GITHUB_API are on the extra-secret list so
  # offline git:// and http:// values, and any URL with userinfo or a query,
  # still fail the evidence check. redact_evidence.public_https_url is the
  # shared rule: a public definition URL, its /pull/<digits> link, and a
  # bare https origin are not secrets. assert_evidence skips those values.
  # A query token, an extra path segment, a percent-encoded token, and a
  # credentialed URL are still scrubbed and still planted.
  export EVIDENCE_EXTRA_SECRETS="FACTORY_SECRET_M6_PLANTED FACTORY_GITHUB_WEBHOOK_SECRET M6_TASK_GIT_URL M6_TASK_GITHUB_API INSTALL_TOKEN M6_APP_TOKEN"
  export EVIDENCE_HAR EVIDENCE_TRACE EVIDENCE_HOSTS
  evidence_start="$SECONDS"
  # Playwright can fail before assert_evidence. Scrub the HAR, trace, and
  # logs first so a later upload cannot publish the console key or a token.
  if ! m6_redact_evidence; then
    phase_report "evidence" "$evidence_start"
    die "evidence redaction"
  fi
  # A Playwright failure is the run's error. Report it before the pinned
  # SHA check, which otherwise masks the excerpt with a different die().
  if [[ "$play_code" -ne 0 ]]; then
    print_playwright_failures "$play_log"
    phase_report "evidence" "$evidence_start"
    if [[ -f "$EVIDENCE/exit-trace.txt" ]]; then
      cat "$EVIDENCE/exit-trace.txt"
    fi
    die "playwright exit ${play_code}"
  fi
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
}

m6_arm_playwright_env() {
  # specs is a new process. The key lives in stack.env as FACTORY_API_KEY.
  M6_API_KEY="${M6_API_KEY:-${FACTORY_API_KEY:-}}"
  export M6_API_KEY
  export M6_FACTORY_URL="http://127.0.0.1:${CONSOLE_PORT}"
  if [[ -z "${PLAYWRIGHT_EXTRA_ENV:-}" ]]; then
    PLAYWRIGHT_EXTRA_ENV="M6_IMPROVER_OPENS_PR M6_RUN_BASE"
  else
    case " ${PLAYWRIGHT_EXTRA_ENV} " in
      *" M6_IMPROVER_OPENS_PR "*) ;;
      *) PLAYWRIGHT_EXTRA_ENV="${PLAYWRIGHT_EXTRA_ENV} M6_IMPROVER_OPENS_PR" ;;
    esac
    case " ${PLAYWRIGHT_EXTRA_ENV} " in
      *" M6_RUN_BASE "*) ;;
      *) PLAYWRIGHT_EXTRA_ENV="${PLAYWRIGHT_EXTRA_ENV} M6_RUN_BASE" ;;
    esac
  fi
  for name in FACTORY_REVIEWERS_ADMINS M6_BENCHMARK_REPETITIONS; do
    case " ${PLAYWRIGHT_EXTRA_ENV} " in
      *" ${name} "*) ;;
      *) PLAYWRIGHT_EXTRA_ENV="${PLAYWRIGHT_EXTRA_ENV} ${name}" ;;
    esac
  done
  if [[ "${M6_STUB_QUESTION:-}" == "1" ]]; then
    case " ${PLAYWRIGHT_EXTRA_ENV} " in
      *" M6_STUB_QUESTION "*) ;;
      *) PLAYWRIGHT_EXTRA_ENV="${PLAYWRIGHT_EXTRA_ENV} M6_STUB_QUESTION" ;;
    esac
  fi
  export PLAYWRIGHT_EXTRA_ENV
}

m6_down() {
  local pending=0
  if m6_remote_pending; then
    pending=1
  fi
  if [[ ! -f "$WORK/stack.env" && "$pending" -eq 0 ]]; then
    printf 'nothing to stop\n'
    exit 0
  fi
  if [[ -f "$WORK/stack.env" ]]; then
    stack_env_load
    COMPOSE_STARTED=1
  fi
  KEEP_UP=0
  export KEEP_UP
  trap 'cleanup EXIT' EXIT
  trap 'cleanup INT' INT
  trap 'cleanup TERM' TERM
  exit 0
}

m6_specs() {
  if [[ ! -f "$WORK/stack.env" ]]; then
    echo "M6 FAIL — no KEEP_UP stack (run KEEP_UP=1 ./spike/m6/run.sh first)" >&2
    exit 1
  fi
  stack_env_load
  if [[ "$MODE" != "${STACK_MODE:-}" ]]; then
    echo "M6 FAIL — MODE is ${MODE}, recorded stack is ${STACK_MODE:-unset}" >&2
    exit 1
  fi
  if ! stack_pids_match "M6"; then
    exit 1
  fi
  if ! stack_compose_ready "M6" factoryd-m6; then
    exit 1
  fi
  if ! stack_health_once "M6" "factoryd-m6=http://127.0.0.1:${CONSOLE_PORT}/healthz"; then
    exit 1
  fi
  if [[ "$MODE" != offline ]]; then
    if [[ -f "$ROOT/deploy/.env" ]]; then
      load_env_file "$ROOT/deploy/.env"
    fi
    apply_live_inference_env
  fi
  if ! m6_retire_open_batches; then
    die "could not retire the previous improvement batch"
  fi
  if [[ "$MODE" == offline ]]; then
    m6_reset_offline_refs
    if ! m6_restart_offline_children; then
      exit 1
    fi
  fi
  KEEP_UP=1
  export KEEP_UP
  COMPOSE_STARTED=1
  mkdir -p "$WORK"
  trap 'cleanup EXIT' EXIT
  trap 'cleanup INT' INT
  trap 'cleanup TERM' TERM
  M6_RUN_NONCE="$(openssl rand -hex 4)"
  if [[ ! "$M6_RUN_NONCE" =~ ^[0-9a-f]{8}$ ]]; then
    die "could not mint a spec nonce"
  fi
  export M6_RUN_NONCE
  case " ${PLAYWRIGHT_EXTRA_ENV:-} " in
    *" M6_RUN_NONCE "*) ;;
    *) PLAYWRIGHT_EXTRA_ENV="${PLAYWRIGHT_EXTRA_ENV:+$PLAYWRIGHT_EXTRA_ENV }M6_RUN_NONCE" ;;
  esac
  export PLAYWRIGHT_EXTRA_ENV
  m6_arm_playwright_env
  m6_step=playwright
  m6_playwright_and_evidence
  mode_name="$(printf '%s' "$MODE" | tr '[:lower:]' '[:upper:]')"
  printf 'M6 %s PASS\n' "$mode_name"
  exit 0
}

if [[ "${BASH_SOURCE[0]}" != "$0" ]]; then
  return 0
fi

m6_arm_err

CLEANUP_ONLY=0
SUB=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --manual)
      if [[ -n "$SUB" || "$CLEANUP_ONLY" -eq 1 ]]; then
        die "unknown argument $1"
      fi
      MANUAL=1
      ;;
    --cleanup-remote)
      if [[ "$MANUAL" -eq 1 || -n "$SUB" ]]; then
        die "unknown argument $1"
      fi
      CLEANUP_ONLY=1
      ;;
    specs | down)
      if [[ "$MANUAL" -eq 1 || "$CLEANUP_ONLY" -eq 1 || -n "$SUB" ]]; then
        die "unknown argument $1"
      fi
      SUB="$1"
      ;;
    *) die "unknown argument $1" ;;
  esac
  shift
done

if [[ "$MANUAL" == 1 ]]; then
  KEEP_UP=1
fi

if [[ -n "$SUB" && "${KEEP_UP:-}" == 1 ]]; then
  die "unknown argument ${SUB}"
fi

case "$SUB" in
  down) m6_down ;;
  specs) m6_specs ;;
  "") ;;
esac

if [[ "$CLEANUP_ONLY" == 1 ]]; then
  if [[ "$MODE" == offline ]]; then
    die "--cleanup-remote is for live and live-ci"
  fi
  command -v python3 >/dev/null || die "python3 not found"
  command -v go >/dev/null || die "go not found"
  command -v git >/dev/null || die "git not found"
  command -v curl >/dev/null || die "curl not found"
  m6_arm_reviewers
  mkdir -p "$WORK"
  if ! remote_cmd cleanup; then
    reason="remote cleanup failed"
    if [[ -f "$WORK/remote-fail" ]]; then
      IFS= read -r reason <"$WORK/remote-fail" || true
    fi
    die "$reason"
  fi
  exit 0
fi

phase_start="$SECONDS"
if ! require_mode_secrets; then
  exit 1
fi
stack_refuse_live "M6" "./spike/m6/run.sh"
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
fi
assign_app_key

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

export FACTORY_REVIEWERS_ADMINS="${FACTORY_REVIEWERS_ADMINS:-m6-reviewer}"
if ! preflight_remote; then
  reason="remote preflight failed"
  if [[ -f "$WORK/remote-fail" ]]; then
    IFS= read -r reason <"$WORK/remote-fail" || true
  fi
  die "$reason"
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

apply_definition_targets
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

spike_run_init m6
trap 'cleanup EXIT' EXIT
trap 'cleanup INT' INT
trap 'cleanup TERM' TERM

m6_step=prepare_definition
prepare_definition
m6_step=seed_git
seed_git
m6_step=seed_remote
seed_remote
m6_step=start_git
start_git
m6_step=start_forge
start_forge
if [[ "$MODE" == offline ]]; then
  m6_step=start_router
  start_router
fi

m6_step=build_images
spike_fg build_images
m6_step=compose
stack_start="$SECONDS"
spike_fg compose down -v --remove-orphans >/dev/null 2>&1 || true
COMPOSE_STARTED=1
if ! spike_fg compose up -d --wait --remove-orphans; then
  phase_report "stack up" "$stack_start"
  die "compose up failed"
fi
if ! spike_fg wait_http "http://127.0.0.1:${CONSOLE_PORT}/healthz"; then
  phase_report "stack up" "$stack_start"
  die "factoryd-m6 /healthz did not become ready"
fi
if ! spike_fg wait_factory_name "http://127.0.0.1:${CONSOLE_PORT}/api/v1/factory" "m6"; then
  phase_report "stack up" "$stack_start"
  die "factory m6 did not appear"
fi
phase_report "stack up" "$stack_start"

m6_arm_playwright_env

if [[ "$MANUAL" == 1 ]]; then
  printf 'console: http://127.0.0.1:%s\n' "$CONSOLE_PORT"
  printf 'api key file: %s\n' "$WORK/api-key"
  exit 0
fi

m6_step=playwright
m6_playwright_and_evidence
if [[ "${KEEP_UP:-}" == 1 ]]; then
  printf 'console: http://127.0.0.1:%s\n' "$CONSOLE_PORT"
  printf 'api key file: %s\n' "$WORK/api-key"
fi
mode_name="$(printf '%s' "$MODE" | tr '[:lower:]' '[:upper:]')"
printf 'M6 %s PASS\n' "$mode_name"
