#!/usr/bin/env bash
# M5 day of factory.
#
#   MODE=offline ./spike/m5/run.sh
#   MODE=live ./spike/m5/run.sh
#   MODE=live-ci ./spike/m5/run.sh
#   KEEP_UP=1 ./spike/m5/run.sh
#   ./spike/m5/run.sh --manual
#   HEADED=1 ./spike/m5/run.sh
#
# MODE defaults to offline. offline, live, and live-ci run the same
# Playwright spec. offline uses spike/m5/router.py in front of
# spike/m1/model-stub. The question stub's first step is ask_input and
# its next step is the final reply. That process stays up across the
# parked run and the resumed execution.
#
# The offline router port is M5_ROUTER_PORT. When that is unset, the
# driver picks a port that binds on both 0.0.0.0 and 127.0.0.1. After
# the router is up, GET /v1/models on 127.0.0.1 and from a container
# via host.docker.internal must list the m5-* models. Anything else
# stops with "router port <p> is answered by another process".
# Live and live-ci do not start this router.
#
# live reads FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL and M5_LIVE_MODEL.
# live-ci reads M1_OPENAI_BASE_URL, M1_OPENAI_API_KEY, and M5_LIVE_MODEL.
# A missing variable prints "missing <NAME>" and exits before any
# container starts. The model id must be listed by GET $base/models.
#
# --manual leaves the stack up and prints the console URL plus the live
# checklist. HEADED=1 runs Playwright on the host and requires Node.
# The default browser is the pinned Playwright image on the compose
# network. Task containers reach published ports through the bridge
# gateway on Linux and host.docker.internal on OrbStack or Docker
# Desktop. M5_TASK_HOST overrides that. A host-side catalog probe of
# host.docker.internal dials 127.0.0.1 with the same scheme, port, and
# path.
#
# Each run generates a fresh FACTORY_API_KEY and ignores deploy/.env for
# that one variable. The driver signs deliveries with the run's webhook
# secret. Evidence strips the API key. Planted secrets must be absent.
#
# spike/m5/run_test.sh sources this file. Executing it runs the stack.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="$ROOT/spike/m5/.work"
SIGNAL="$WORK/signal"
EVIDENCE="$WORK/evidence"
DEFS="$WORK/defs"
PLAYWRIGHT_IMAGE="${PLAYWRIGHT_IMAGE:-mcr.microsoft.com/playwright:v1.58.2-noble@sha256:6446946a1d9fd62d9ae501312a2d76a43ee688542b21622056a372959b65d63d}"
ROUTER_PORT=""
MODE="${MODE:-offline}"
MANUAL=0
COMPOSE_STARTED=0
ROUTER_PID=""
WATCH_PID=""
STUB_PIDS=""

die() {
  echo "$*" >&2
  exit 1
}

say() {
  printf '\n== %s\n' "$*"
}

accept_mode() {
  case "${1:-}" in
    offline | live | live-ci) return 0 ;;
    *) return 1 ;;
  esac
}

# load_env_file reads KEY=VALUE lines. It does not expand or execute values.
# Empty values are ignored. A variable that is already non-empty is kept.
load_env_file() {
  local file="$1" line="" n=0 key="" val=""
  local -a keys=() vals=()
  [[ -f "$file" ]] || return 1
  while IFS= read -r line || [[ -n "$line" ]]; do
    n=$((n + 1))
    line="${line%$'\r'}"
    line="${line#"${line%%[![:space:]]*}"}"
    [[ -z "$line" || "$line" == \#* ]] && continue
    if [[ "$line" == export\ * ]]; then
      line="${line#export }"
      line="${line#"${line%%[![:space:]]*}"}"
    fi
    if [[ ! "$line" =~ ^([A-Za-z_][A-Za-z0-9_]*)=(.*)$ ]]; then
      echo "deploy/.env:${n}: invalid line" >&2
      return 1
    fi
    key="${BASH_REMATCH[1]}"
    val="${BASH_REMATCH[2]}"
    if [[ ${#val} -ge 2 && "$val" == \"*\" && "$val" == *\" ]]; then
      val="${val:1:${#val}-2}"
    elif [[ ${#val} -ge 2 && "$val" == \'*\' && "$val" == *\' ]]; then
      val="${val:1:${#val}-2}"
    fi
    if [[ -z "$val" || -n "${!key:-}" ]]; then
      continue
    fi
    keys+=("$key")
    vals+=("$val")
  done <"$file"
  local i
  for i in "${!keys[@]}"; do
    printf -v "${keys[$i]}" '%s' "${vals[$i]}"
    export "${keys[$i]}"
  done
}

require_mode_secrets() {
  case "${MODE:-}" in
    offline) return 0 ;;
    live)
      if [[ -z "${FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL:-}" ]]; then
        echo "missing FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL" >&2
        return 1
      fi
      if [[ -z "${M5_LIVE_MODEL:-}" ]]; then
        echo "missing M5_LIVE_MODEL" >&2
        return 1
      fi
      ;;
    live-ci)
      if [[ -z "${M1_OPENAI_BASE_URL:-}" ]]; then
        echo "missing M1_OPENAI_BASE_URL" >&2
        return 1
      fi
      if [[ -z "${M1_OPENAI_API_KEY:-}" ]]; then
        echo "missing M1_OPENAI_API_KEY" >&2
        return 1
      fi
      if [[ -z "${M5_LIVE_MODEL:-}" ]]; then
        echo "missing M5_LIVE_MODEL" >&2
        return 1
      fi
      ;;
    *)
      echo "MODE must be offline, live, or live-ci" >&2
      return 1
      ;;
  esac
}

require_headed_node() {
  if [[ "${HEADED:-}" != 1 ]]; then
    return 0
  fi
  if ! command -v node >/dev/null 2>&1; then
    echo "missing Node" >&2
    return 1
  fi
  local ver=""
  ver="$(node --version 2>/dev/null || true)"
  if [[ ! "$ver" =~ ^v[0-9]+ ]]; then
    echo "missing Node" >&2
    return 1
  fi
}

normalize_docker_platform() {
  case "$1" in
    linux/aarch64 | linux/arm64) printf '%s\n' linux/arm64 ;;
    linux/x86_64 | linux/amd64) printf '%s\n' linux/amd64 ;;
    *) return 1 ;;
  esac
}

goarch_for_platform() {
  case "$1" in
    linux/arm64) printf '%s\n' arm64 ;;
    linux/amd64) printf '%s\n' amd64 ;;
    *) return 1 ;;
  esac
}

uname_for_platform() {
  case "$1" in
    linux/arm64) printf '%s\n' aarch64 ;;
    linux/amd64) printf '%s\n' x86_64 ;;
    *) return 1 ;;
  esac
}

detect_platform() {
  local arch=""
  arch="$(docker info --format '{{.Architecture}}')"
  case "$arch" in
    aarch64 | arm64) OCI_PLATFORM="linux/arm64" ;;
    x86_64 | amd64) OCI_PLATFORM="linux/amd64" ;;
    *)
      echo "unsupported docker architecture ${arch}" >&2
      return 1
      ;;
  esac
  GOARCH_TARGET="$(goarch_for_platform "$OCI_PLATFORM")"
  export OCI_PLATFORM GOARCH_TARGET
}

bridge_gateway() {
  local gw=""
  gw="$(docker network inspect bridge --format '{{(index .IPAM.Config 0).Gateway}}')"
  if [[ ! "$gw" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "could not determine an IPv4 gateway for the docker bridge" >&2
    return 1
  fi
  printf '%s\n' "$gw"
}

# host_for_tasks is the host a bridge container uses for published ports.
# Args: docker operating system, gateway, kernel, server host.
host_for_tasks() {
  local os="$1" gw="$2" kernel="${3:-}" server_host="${4:-}"
  if [[ -z "$kernel" ]]; then
    kernel="$(uname -s)"
  fi
  if [[ -n "${M5_TASK_HOST:-}" ]]; then
    printf '%s\n' "$M5_TASK_HOST"
    return 0
  fi
  if [[ -n "$server_host" ]]; then
    printf '%s\n' "$server_host"
    return 0
  fi
  case "$os" in
    *Docker\ Desktop* | *OrbStack*)
      printf '%s\n' "host.docker.internal"
      return 0
      ;;
  esac
  if [[ "$kernel" == "Darwin" ]]; then
    printf '%s\n' "host.docker.internal"
    return 0
  fi
  printf '%s\n' "$gw"
}

url_hostname() {
  python3 - "$1" <<'PY'
import sys
from urllib.parse import urlparse
print(urlparse(sys.argv[1]).hostname or "")
PY
}

# host_catalog_probe_url rewrites host.docker.internal to 127.0.0.1.
# Scheme, port, and path stay. Every other host is unchanged.
host_catalog_probe_url() {
  local raw="$1" task_host="${2:-}"
  python3 - "$raw" "$task_host" <<'PY'
import sys
from urllib.parse import urlparse, urlunparse
raw, task_host = sys.argv[1], sys.argv[2]
parts = urlparse(raw)
host = parts.hostname or ""
if host != "host.docker.internal" and not (task_host and host == task_host):
    print(raw)
    raise SystemExit(0)
netloc = "127.0.0.1"
if parts.port is not None:
    netloc = "127.0.0.1:%d" % parts.port
print(urlunparse((parts.scheme, netloc, parts.path, parts.params, parts.query, "")))
PY
}

catalog_contains_id() {
  local file="$1" id="$2"
  python3 - "$file" "$id" <<'PY'
import json, sys
path, want = sys.argv[1], sys.argv[2]
with open(path, encoding="utf-8") as fh:
    data = json.load(fh)
ids = []
if isinstance(data, dict):
    for row in data.get("data") or []:
        if isinstance(row, dict) and row.get("id"):
            ids.append(row["id"])
raise SystemExit(0 if want in ids else 1)
PY
}

reject_unlisted_model() {
  local file="$1" id="$2"
  if catalog_contains_id "$file" "$id"; then
    return 0
  fi
  echo "model id ${id} is not in the catalog" >&2
  return 1
}

# rewrite_live_models edits a copy. deploy/m5-factory is not a target.
rewrite_live_models() {
  local dir="$1"
  local triage="${2:-}" planning="${3:-}" building="${4:-}" reviewing="${5:-}"
  python3 - "$dir" "$triage" "$planning" "$building" "$reviewing" <<'PY'
import os, sys
root, triage, planning, building, reviewing = sys.argv[1:]
repls = {
    "m5-triage": triage,
    "m5-planning": planning,
    "m5-building": building,
    "m5-reviewing": reviewing,
}
for dirpath, _, files in os.walk(root):
    for name in files:
        path = os.path.join(dirpath, name)
        with open(path, encoding="utf-8") as fh:
            text = fh.read()
        orig = text
        for old, new in repls.items():
            if new:
                text = text.replace(old, new)
        if text != orig:
            with open(path, "w", encoding="utf-8") as fh:
                fh.write(text)
PY
}

write_offline_scripts() {
  local dir="$1"
  mkdir -p "$dir"
  cat >"$dir/question.json" <<'EOF'
[
  {"tool":"ask_input","args":{"text":"Which staging env?"}},
  {"final":"staging noted"}
]
EOF
  cat >"$dir/agent.json" <<'EOF'
[
  {"final":"{\"status\":\"failed_by_agent\",\"summary\":\"m5 agent failure\"}"}
]
EOF
  printf '%s\n' '[{"final":"triage summary"}]' >"$dir/triage.json"
  printf '%s\n' '[{"final":"planning summary"}]' >"$dir/planning.json"
  printf '%s\n' '[{"final":"building summary"}]' >"$dir/building.json"
  printf '%s\n' '[{"final":"reviewing: no findings"}]' >"$dir/reviewing.json"
}

random_hex() {
  openssl rand -hex 24
}

compose() {
  docker compose -p factory-m5 \
    -f "$ROOT/deploy/docker-compose.yml" \
    -f "$ROOT/spike/m5/compose.yml" \
    --profile m5 \
    "$@"
}

prepare_definition() {
  # factoryd (uid 65532) replaces this tree with mode 700. A later run
  # cannot rm it as the host user. wipe_dir falls back to a root container
  # and only for a resolved directory strictly inside spike/m5/.work.
  wipe_dir "$DEFS"
  mkdir -p "$DEFS"
  cp -R "$ROOT/deploy/m5-factory" "$DEFS/m5-factory"
  if [[ "$MODE" != offline ]]; then
    local triage="${M5_LIVE_MODEL_TRIAGE:-$M5_LIVE_MODEL}"
    local planning="${M5_LIVE_MODEL_PLANNING:-$M5_LIVE_MODEL}"
    local building="${M5_LIVE_MODEL_BUILDING:-$M5_LIVE_MODEL}"
    local reviewing="${M5_LIVE_MODEL_REVIEWING:-$M5_LIVE_MODEL}"
    rewrite_live_models "$DEFS/m5-factory" "$triage" "$planning" "$building" "$reviewing"
  fi
  chmod -R a+rwX "$DEFS"
  export M5_DEFS_HOST="$DEFS"
  export FACTORY_DEFINITION_HOST_PATH="$DEFS"
  export FACTORY_DEFINITION_MOUNT=/definitions
}

prepare_git_repo() {
  local bare="$WORK/git/factory.git" wt="$WORK/git-wt"
  rm -rf "$WORK/git" "$wt"
  mkdir -p "$WORK/git" "$wt"
  git init --bare -b main "$bare"
  git -C "$wt" init -b main
  cp -R "$ROOT/spike/m5/git-factory/." "$wt/"
  git -C "$wt" add .
  git -C "$wt" -c user.email=factory@example.com -c user.name=factory commit -m "m5 git factory"
  git -C "$wt" -c protocol.file.allow=always remote add origin "$bare"
  git -C "$wt" -c protocol.file.allow=always push origin HEAD:main
  chmod -R a+rX "$WORK/git"
  export M5_GIT_HOST="$WORK/git"
}

wait_http() {
  local url="$1" i
  for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30; do
    if curl -fsS --connect-timeout 2 -m 5 "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  return 1
}

# wait_factory_name polls the public factory list until name is present.
# The response body is not printed.
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

# router_py runs one command in spike/m5/router.py.
# The port number is the only stdout. Refusals stay on stderr.
router_py() {
  python3 - "$ROOT/spike/m5/router.py" "$@" <<'PY'
import importlib.util
import sys

path = sys.argv[1]
spec = importlib.util.spec_from_file_location("m5router", path)
if spec is None or spec.loader is None:
    raise SystemExit("router import failed")
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)
cmd = sys.argv[2]
args = sys.argv[3:]
if cmd == "resolve":
    configured = args[0] if args else ""
    print(mod.resolve_port(configured))
elif cmd == "ensure":
    mod.ensure_port_available(int(args[0]))
elif cmd == "identity":
    raise SystemExit(0 if mod.file_is_router(args[0]) else 1)
else:
    raise SystemExit("unknown router command")
PY
}

resolve_router_port() {
  local chosen=""
  if ! chosen="$(router_py resolve "${M5_ROUTER_PORT-}")"; then
    return 1
  fi
  if [[ ! "$chosen" =~ ^[1-9][0-9]*$ ]]; then
    echo "M5_ROUTER_PORT must be a TCP port" >&2
    return 1
  fi
  ROUTER_PORT="$chosen"
}

# container_needs_host_gateway is true when the engine does not already
# publish host.docker.internal. OrbStack and Docker Desktop do, and that
# name is the one that forwards to the host loopback. Replacing it with
# host-gateway would hide a process bound only to 127.0.0.1.
container_needs_host_gateway() {
  local os="$1" kernel="$2"
  case "$os" in
    *OrbStack* | *Docker\ Desktop*)
      return 1
      ;;
  esac
  if [[ "$kernel" == "Darwin" ]]; then
    return 1
  fi
  return 0
}

fetch_container_router_models() {
  local port="$1" dest="$2"
  local url="http://host.docker.internal:${port}/v1/models"
  if container_needs_host_gateway "${OS_NAME:-}" "$(uname -s)"; then
    docker run --rm --network bridge \
      --add-host=host.docker.internal:host-gateway \
      busybox:1.36 \
      wget -q -T 5 -O - "$url" >"$dest"
    return
  fi
  docker run --rm --network bridge \
    busybox:1.36 \
    wget -q -T 5 -O - "$url" >"$dest"
}

host_router_identity_ok() {
  local port="$1" body
  body="$(mktemp)"
  if ! curl -fsS --connect-timeout 2 -m 5 -o "$body" \
    "http://127.0.0.1:${port}/v1/models"; then
    rm -f "$body"
    return 1
  fi
  if ! router_py identity "$body"; then
    rm -f "$body"
    return 1
  fi
  rm -f "$body"
}

verify_router_identity() {
  local port="$1" body
  if ! host_router_identity_ok "$port"; then
    echo "router port ${port} is answered by another process" >&2
    return 1
  fi
  body="$(mktemp)"
  if ! fetch_container_router_models "$port" "$body"; then
    echo "router port ${port} is answered by another process" >&2
    rm -f "$body"
    return 1
  fi
  if ! router_py identity "$body"; then
    echo "router port ${port} is answered by another process" >&2
    rm -f "$body"
    return 1
  fi
  rm -f "$body"
}

start_stub() {
  local name="$1" port="$2" script="$3"
  "$WORK/model-stub" -addr "127.0.0.1:${port}" -script "$script" \
    >"$WORK/stub-${name}.log" 2>&1 &
  STUB_PIDS="${STUB_PIDS} $!"
  if ! wait_http "http://127.0.0.1:${port}/healthz"; then
    echo "model-stub ${name} did not become reachable" >&2
    return 1
  fi
}

start_offline_models() {
  say "Build model-stub and the router"
  mkdir -p "$WORK"
  (cd "$ROOT" && CGO_ENABLED=0 GOOS="$(uname -s | tr '[:upper:]' '[:lower:]')" GOARCH="$(go env GOARCH)" \
    go build -o "$WORK/model-stub" ./spike/m1/model-stub)
  write_offline_scripts "$WORK/scripts"
  start_stub question 9091 "$WORK/scripts/question.json"
  start_stub agent 9092 "$WORK/scripts/agent.json"
  start_stub triage 9093 "$WORK/scripts/triage.json"
  start_stub planning 9094 "$WORK/scripts/planning.json"
  start_stub building 9095 "$WORK/scripts/building.json"
  start_stub reviewing 9096 "$WORK/scripts/reviewing.json"
  if ! router_py ensure "$ROUTER_PORT"; then
    return 1
  fi
  M5_STUB_QUESTION=http://127.0.0.1:9091 \
    M5_STUB_AGENT=http://127.0.0.1:9092 \
    M5_STUB_TRIAGE=http://127.0.0.1:9093 \
    M5_STUB_PLANNING=http://127.0.0.1:9094 \
    M5_STUB_BUILDING=http://127.0.0.1:9095 \
    M5_STUB_REVIEWING=http://127.0.0.1:9096 \
    python3 "$ROOT/spike/m5/router.py" --addr "0.0.0.0:${ROUTER_PORT}" \
    >"$WORK/router.log" 2>&1 &
  ROUTER_PID=$!
  if ! wait_http "http://127.0.0.1:${ROUTER_PORT}/healthz"; then
    echo "model router did not become reachable" >&2
    return 1
  fi
  if ! verify_router_identity "$ROUTER_PORT"; then
    return 1
  fi
}

build_images() {
  say "Build images for ${OCI_PLATFORM}"
  docker build --platform "$OCI_PLATFORM" -t factoryd:dev \
    -f "$ROOT/deploy/factoryd/Dockerfile" "$ROOT"
  docker build --platform "$OCI_PLATFORM" -t factory-upstream-worker:dev \
    "$ROOT/deploy/worker"
  (cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH_TARGET" \
    go build -o harness/generic-openai/factory-harness ./cmd/factory-harness)
  docker build --platform "$OCI_PLATFORM" -t factory-harness:dev \
    "$ROOT/harness/generic-openai"
  docker build --platform "$OCI_PLATFORM" -t factory-m1-task:dev \
    "$ROOT/spike/m1/task-image"
  docker build --platform "$OCI_PLATFORM" -t factoryd-git:dev \
    -f "$ROOT/spike/m5/factoryd-git.Dockerfile" "$ROOT/spike/m5"
}

driver_log() {
  mkdir -p "$EVIDENCE"
  printf '%s\n' "$*" >>"$EVIDENCE/driver.log"
}

ack_file() {
  local name="$1" body="$2"
  printf '%s\n' "$body" >"$SIGNAL/${name}.ack"
  rm -f "$SIGNAL/${name}"
}

handle_kill() {
  local line="" run_id="" cid="" killed=0
  line="$(tr -d '\r' <"$SIGNAL/kill")"
  run_id="${line#kill }"
  run_id="${run_id#"${run_id%%[![:space:]]*}"}"
  if [[ -z "$run_id" || "$run_id" == "$line" ]]; then
    ack_file kill "error bad kill signal"
    return 0
  fi
  while IFS= read -r cid; do
    [[ -z "$cid" ]] && continue
    docker kill "$cid" >/dev/null
    killed=$((killed + 1))
    driver_log "kill ${run_id} ${cid}"
  done <<<"$(docker ps -q --filter "label=oz-task-id=${run_id}")"
  if [[ "$killed" -eq 0 ]]; then
    ack_file kill "none"
    return 0
  fi
  ack_file kill "killed ${killed}"
}

handle_idle() {
  local run_id=""
  run_id="$(tr -d '[:space:]' <"$SIGNAL/idle")"
  if docker ps -q --filter "label=oz-task-id=${run_id}" | grep -q .; then
    driver_log "idle ${run_id} running"
    ack_file idle "running"
  else
    driver_log "idle ${run_id} gone"
    ack_file idle "gone"
  fi
}

write_github_body() {
  local kind="$1" dest="$2"
  python3 - "$kind" "$dest" <<'PY'
import json, sys
kind, dest = sys.argv[1], sys.argv[2]
repo = {
    "id": 50,
    "name": "m5",
    "full_name": "polds/m5",
    "owner": {"login": "polds", "id": 7, "type": "Organization"},
}
sender = {"login": "m5-human", "id": 1001, "type": "User"}
if kind == "issue":
    body = {
        "action": "opened",
        "issue": {
            "id": 4200,
            "number": 42,
            "title": "m5-day",
            "body": "Day of factory. Finish with a short summary and no tool calls.",
            "html_url": "https://github.com/polds/m5/issues/42",
            "user": sender,
            "labels": [],
            "created_at": "2026-10-17T00:00:00Z",
            "updated_at": "2026-10-17T00:00:00Z",
        },
        "repository": repo,
        "sender": sender,
    }
elif kind == "pr":
    body = {
        "action": "opened",
        "number": 42,
        "pull_request": {
            "id": 4201,
            "number": 42,
            "title": "m5-day",
            "body": "Day of factory.",
            "html_url": "https://github.com/polds/m5/pull/7",
            "merged": False,
            "updated_at": "2026-10-17T00:10:00Z",
            "labels": [],
            "user": sender,
            "head": {"ref": "m5-day", "sha": "abc123"},
            "base": {"ref": "main", "sha": "def456"},
        },
        "repository": repo,
        "sender": sender,
    }
else:
    raise SystemExit("unknown github body")
with open(dest, "w", encoding="utf-8") as fh:
    json.dump(body, fh)
PY
}

post_signed() {
  local url="$1" dest="$2"
  shift 2
  local sig="" code=""
  sig="$(openssl dgst -sha256 -hmac "$FACTORY_SECRET_M5_WEBHOOK" "$dest" | awk '{print $NF}')"
  code="$(curl --connect-timeout 2 -m 5 -sS -o "$WORK/deliver-body.txt" -w '%{http_code}' \
    -X POST "$url" \
    -H "Content-Type: application/json" \
    -H "X-Hub-Signature-256: sha256=${sig}" \
    "$@" \
    --data-binary @"$dest" || true)"
  printf '%s' "$code"
}

handle_deliver() {
  local op="" uid="" match="" code="" dest="$WORK/deliver.json" id=""
  op="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("op",""))' "$SIGNAL/deliver")"
  case "$op" in
    issue)
      id="m5-issue-${RANDOM}"
      write_github_body issue "$dest"
      code="$(post_signed "http://127.0.0.1:8080/api/v1/ingress/github" "$dest" \
        -H "X-GitHub-Event: issues" -H "X-GitHub-Delivery: ${id}")"
      ;;
    pr)
      id="m5-pr-${RANDOM}"
      write_github_body pr "$dest"
      code="$(post_signed "http://127.0.0.1:8080/api/v1/ingress/github" "$dest" \
        -H "X-GitHub-Event: pull_request" -H "X-GitHub-Delivery: ${id}")"
      ;;
    webhook)
      uid="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("uid",""))' "$SIGNAL/deliver")"
      match="$(python3 -c 'import json,sys; print("true" if json.load(open(sys.argv[1])).get("match") else "false")' "$SIGNAL/deliver")"
      if [[ ! "$uid" =~ ^whk_[0-9A-Z]+$ ]]; then
        ack_file deliver "error bad webhook uid"
        return 0
      fi
      id="m5-wh-${RANDOM}"
      if [[ "$match" == true ]]; then
        printf '%s\n' '{"action":"opened","source":"m5"}' >"$dest"
      else
        printf '%s\n' '{"action":"closed","source":"m5"}' >"$dest"
      fi
      code="$(post_signed "http://127.0.0.1:8080/api/v1/webhooks/${uid}" "$dest" \
        -H "X-Request-Id: ${id}")"
      ;;
    *)
      ack_file deliver "error unknown op"
      return 0
      ;;
  esac
  driver_log "deliver ${op} ${id} ${code}"
  case "$code" in
    2*) ack_file deliver "status ${code}" ;;
    *) ack_file deliver "error status ${code}" ;;
  esac
}

watch_signals() {
  while [[ ! -f "$SIGNAL/done" ]]; do
    if [[ -f "$SIGNAL/kill" && ! -f "$SIGNAL/kill.ack" ]]; then
      handle_kill || true
    fi
    if [[ -f "$SIGNAL/deliver" && ! -f "$SIGNAL/deliver.ack" ]]; then
      handle_deliver || true
    fi
    if [[ -f "$SIGNAL/idle" && ! -f "$SIGNAL/idle.ack" ]]; then
      handle_idle || true
    fi
    sleep 0.2
  done
}

# resolve_wipe_path prints the physical path of one argument.
# It refuses an empty path, /, spike/m5/.work itself, and anything that
# realpath follows outside that directory, including a symlink.
resolve_wipe_path() {
  python3 - "$WORK" "${1-}" <<'PY'
import os, sys

work = os.path.realpath(sys.argv[1])
raw = sys.argv[2]
if raw == "":
    sys.stderr.write("wipe_dir: empty path\n")
    raise SystemExit(1)
if work in ("", "/"):
    sys.stderr.write("wipe_dir: refusing work root %s\n" % work)
    raise SystemExit(1)
resolved = os.path.realpath(raw)
try:
    inside = os.path.commonpath([work, resolved]) == work
except ValueError:
    inside = False
if resolved in ("", "/") or resolved == work or not inside:
    sys.stderr.write("wipe_dir: refusing %s\n" % resolved)
    raise SystemExit(1)
print(resolved)
PY
}

wipe_dir() {
  local path="${1-}" resolved=""
  if ! resolved="$(resolve_wipe_path "$path")"; then
    return 1
  fi
  if [[ ! -e "$resolved" ]]; then
    return 0
  fi
  if rm -rf "$resolved" 2>/dev/null; then
    return 0
  fi
  # Bind the resolved directory itself. A parent mount would let the
  # root find walk sideways out of the tree we agreed to delete.
  docker run --rm --user 0 -v "$resolved:/wipe" golang:1.26-alpine \
    sh -c 'find /wipe -mindepth 1 -depth -exec rm -rf {} +'
}

copy_e2e() {
  wipe_dir "$WORK/e2e"
  wipe_dir "$SIGNAL"
  wipe_dir "$EVIDENCE"
  mkdir -p "$WORK/e2e" "$EVIDENCE" "$SIGNAL"
  chmod 777 "$EVIDENCE" "$SIGNAL"
  cp "$ROOT/spike/m5/e2e/package.json" \
    "$ROOT/spike/m5/e2e/package-lock.json" \
    "$ROOT/spike/m5/e2e/playwright.config.ts" \
    "$ROOT/spike/m5/e2e/day.spec.ts" \
    "$WORK/e2e/"
}

run_playwright() {
  copy_e2e
  : >"$EVIDENCE/driver.log"
  watch_signals &
  WATCH_PID=$!
  if [[ "${HEADED:-}" == 1 ]]; then
    (cd "$WORK/e2e" && npm ci &&
      M5_API_KEY="$FACTORY_API_KEY" \
        M5_SIGNAL="$SIGNAL" \
        M5_EVIDENCE="$EVIDENCE" \
        FACTORY_URL="http://127.0.0.1:8080" \
        FACTORY_GIT_URL="http://127.0.0.1:8081" \
        npx playwright test --reporter=list)
    return $?
  fi
  docker run --rm \
    --network factory-m5_default \
    -v "$WORK/e2e:/work" \
    -v "$EVIDENCE:/evidence" \
    -v "$SIGNAL:/signal" \
    -e "M5_API_KEY=${FACTORY_API_KEY}" \
    -e M5_SIGNAL=/signal \
    -e M5_EVIDENCE=/evidence \
    -e FACTORY_URL=http://factoryd:8080 \
    -e FACTORY_GIT_URL=http://factoryd-git:8080 \
    -w /work \
    "$PLAYWRIGHT_IMAGE" \
    bash -lc 'npm ci && npx playwright test --reporter=list'
}

save_logs() {
  mkdir -p "$EVIDENCE"
  compose logs --no-color >"$EVIDENCE/compose.log" 2>&1 || true
  if [[ -f "$WORK/router.log" ]]; then
    cp "$WORK/router.log" "$EVIDENCE/router.log"
  fi
}

assert_evidence() {
  python3 - "$EVIDENCE" <<'PY'
import json, os, sys, zipfile
from pathlib import Path
from urllib.parse import urlparse

root = Path(sys.argv[1])
planted = []
for name in (
    "FACTORY_SECRET_M5_WEBHOOK",
    "FACTORY_WORKER_API_KEY",
    "S3_SECRET_KEY",
    "FACTORY_SECRET_M5_PLANTED",
    "FACTORY_INFERENCE_OPENAI_API_KEY",
):
    value = os.environ.get(name, "")
    if len(value) >= 8:
        planted.append(value.encode())
api_key = os.environ.get("FACTORY_API_KEY", "").encode()

def walk_files():
    for path in root.rglob("*"):
        if path.is_file():
            yield path

for path in walk_files():
    data = path.read_bytes()
    for secret in planted:
        if secret in data:
            sys.stderr.write("planted secret in %s\n" % path.name)
            raise SystemExit(1)

for path in walk_files():
    if path.suffix not in {".html", ".log", ".txt"}:
        continue
    if api_key and api_key in path.read_bytes():
        sys.stderr.write("api key in %s\n" % path.name)
        raise SystemExit(1)

har_path = root / "day.har"
if har_path.is_file():
    har = json.loads(har_path.read_text(encoding="utf-8"))
    allowed_hosts = {"factoryd", "factoryd-git", "127.0.0.1", "localhost"}
    for entry in har.get("log", {}).get("entries", []):
        req = entry.get("request") or {}
        url = req.get("url") or ""
        host = urlparse(url).hostname or ""
        if host not in allowed_hosts:
            sys.stderr.write("off-origin %s\n" % url)
            raise SystemExit(1)
        rtype = entry.get("_resourceType") or ""
        path = urlparse(url).path or ""
        if rtype in {"xhr", "fetch", "eventsource", "websocket"} and not path.startswith("/api/v1/"):
            sys.stderr.write("non-api %s %s\n" % (rtype, path))
            raise SystemExit(1)
        content = ((entry.get("response") or {}).get("content") or {}).get("text") or ""
        if api_key and api_key.decode() in content:
            sys.stderr.write("api key in a response body\n")
            raise SystemExit(1)
        for header in req.get("headers") or []:
            if str(header.get("name", "")).lower() == "authorization":
                header["value"] = "REDACTED"
        raw = json.dumps(entry)
        if api_key and api_key.decode() in raw:
            pass
    text = json.dumps(har)
    if api_key:
        text = text.replace(api_key.decode(), "REDACTED")
    rewritten = har_path.with_name(har_path.name + ".next")
    rewritten.write_text(text, encoding="utf-8")
    os.replace(rewritten, har_path)

trace = root / "day-trace.zip"
if trace.is_file() and api_key:
    blob = trace.read_bytes()
    for secret in planted:
        if secret in blob:
            sys.stderr.write("planted secret in day-trace.zip\n")
            raise SystemExit(1)
    out = trace.with_suffix(".zip.next")
    with zipfile.ZipFile(trace) as zin, zipfile.ZipFile(out, "w") as zout:
        for info in zin.infolist():
            data = zin.read(info.filename)
            for secret in planted:
                if secret in data:
                    sys.stderr.write("planted secret in trace entry\n")
                    raise SystemExit(1)
            data = data.replace(api_key, b"REDACTED")
            zout.writestr(info, data)
    out.replace(trace)
PY
}

print_checklist() {
  cat <<'EOF'
Live run checklist
1. Open the console and sign in with the API key for this run.
2. Create a work item from the board.
3. Approve the spec gate from the inbox.
4. Watch the transcript, reload, and confirm the rows match.
5. Interrupt the building turn and send a follow-up.
6. Confirm the handoff stage and the pull request link.
7. Answer the inbox question "Which staging env?".
8. Confirm the agent failure and the infrastructure failure on /runs.
9. Pause and resume an automation.
10. Replay the unmatched delivery.
11. Validate and apply a definition edit, then open the git factory.
12. Check the worker heartbeat and the secret names.
EOF
}

stop_background() {
  if [[ -n "$WATCH_PID" ]]; then
    kill "$WATCH_PID" 2>/dev/null || true
    wait "$WATCH_PID" 2>/dev/null || true
    WATCH_PID=""
  fi
  if [[ "${KEEP_UP:-}" == 1 ]]; then
    return 0
  fi
  if [[ -n "$ROUTER_PID" ]]; then
    kill "$ROUTER_PID" 2>/dev/null || true
    wait "$ROUTER_PID" 2>/dev/null || true
    ROUTER_PID=""
  fi
  local pid
  for pid in $STUB_PIDS; do
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  done
  STUB_PIDS=""
  if [[ "$COMPOSE_STARTED" == 1 ]]; then
    compose down -v --remove-orphans >/dev/null 2>&1 || true
    COMPOSE_STARTED=0
  fi
}

cleanup() {
  local code=$?
  mkdir -p "$SIGNAL"
  touch "$SIGNAL/done" 2>/dev/null || true
  stop_background
  # Drop the definition tree after the mount is gone so the next prep
  # is not the first thing that has to delete a mode-700 directory.
  # KEEP_UP leaves factoryd running, so the bind is still live.
  # --manual sets KEEP_UP and also clears this trap.
  if [[ "${KEEP_UP:-}" != 1 ]]; then
    wipe_dir "$DEFS" || true
  fi
  if [[ -d "$EVIDENCE" && ! -f "$EVIDENCE/compose.log" ]]; then
    save_logs || true
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
  export KEEP_UP
fi

trap cleanup EXIT INT TERM

say "Preflight"
if ! accept_mode "$MODE"; then
  die "MODE must be offline, live, or live-ci (got ${MODE})"
fi
if [[ -f "$ROOT/deploy/.env" ]]; then
  load_env_file "$ROOT/deploy/.env"
fi
if ! require_mode_secrets; then
  exit 1
fi
if ! require_headed_node; then
  exit 1
fi

command -v docker >/dev/null || die "docker not found"
command -v go >/dev/null || die "go not found"
command -v curl >/dev/null || die "curl not found"
command -v python3 >/dev/null || die "python3 not found"
command -v openssl >/dev/null || die "openssl not found"
command -v git >/dev/null || die "git not found"
docker info >/dev/null 2>&1 || die "docker daemon is not reachable"
detect_platform

if docker ps --format '{{.Ports}}' | grep -F '0.0.0.0:8080->' >/dev/null; then
  die "port 8080 is already published"
fi

FACTORY_API_KEY="$(random_hex)"
export FACTORY_API_KEY
FACTORY_SECRET_M5_WEBHOOK="$(random_hex)"
export FACTORY_SECRET_M5_WEBHOOK
FACTORY_GITHUB_WEBHOOK_SECRET="$FACTORY_SECRET_M5_WEBHOOK"
export FACTORY_GITHUB_WEBHOOK_SECRET
FACTORY_WORKER_API_KEY="$(random_hex)"
export FACTORY_WORKER_API_KEY
S3_SECRET_KEY="$(random_hex)"
export S3_SECRET_KEY
FACTORY_SECRET_M5_PLANTED="$(random_hex)"
export FACTORY_SECRET_M5_PLANTED
if [[ "$MODE" == offline ]]; then
  FACTORY_INFERENCE_OPENAI_API_KEY="$(random_hex)"
  export FACTORY_INFERENCE_OPENAI_API_KEY
fi
if [[ "$MODE" == live-ci ]]; then
  export FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL="$M1_OPENAI_BASE_URL"
  export FACTORY_INFERENCE_OPENAI_API_KEY="$M1_OPENAI_API_KEY"
fi

mkdir -p "$WORK"
umask 077
printf '%s\n' "$FACTORY_API_KEY" >"$WORK/api-key"
chmod 600 "$WORK/api-key"

OS_NAME="$(docker info --format '{{.OperatingSystem}}')"
GW="$(bridge_gateway)"
TASK_HOST="$(host_for_tasks "$OS_NAME" "$GW" "$(uname -s)" "")"
export TASK_HOST
if [[ -z "${FACTORY_TASK_SERVER_URL:-}" ]]; then
  if [[ "$TASK_HOST" == "host.docker.internal" ]]; then
    die "Docker Desktop, OrbStack, or Darwin: set FACTORY_TASK_SERVER_URL=http://host.docker.internal:8080"
  fi
  export FACTORY_TASK_SERVER_URL="http://${TASK_HOST}:8080"
fi
printf 'task host: %s\n' "$TASK_HOST"

if [[ "$MODE" == offline ]]; then
  if ! resolve_router_port; then
    exit 1
  fi
  printf 'router port: %s\n' "$ROUTER_PORT"
  export FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL="http://${TASK_HOST}:${ROUTER_PORT}/v1"
else
  live_model="${M5_LIVE_MODEL}"
  probe="$(host_catalog_probe_url "$FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL" "$TASK_HOST")"
  catalog="$WORK/catalog.json"
  if ! curl -fsS --connect-timeout 2 -m 5 -o "$catalog" "${probe%/}/models"; then
    die "catalog probe failed"
  fi
  for id in \
    "${M5_LIVE_MODEL_TRIAGE:-$live_model}" \
    "${M5_LIVE_MODEL_PLANNING:-$live_model}" \
    "${M5_LIVE_MODEL_BUILDING:-$live_model}" \
    "${M5_LIVE_MODEL_REVIEWING:-$live_model}"; do
    if ! reject_unlisted_model "$catalog" "$id"; then
      exit 1
    fi
  done
fi

say "Prepare the definition and the git factory"
prepare_definition
prepare_git_repo
if [[ "$MODE" == offline ]]; then
  start_offline_models
fi

say "Build and start the stack"
build_images
compose down -v --remove-orphans >/dev/null 2>&1 || true
COMPOSE_STARTED=1
if ! compose up -d --wait; then
  mkdir -p "$EVIDENCE"
  save_logs || true
  die "compose up failed"
fi

if ! wait_http "http://127.0.0.1:8080/healthz"; then
  die "factoryd /healthz did not become ready"
fi
if ! wait_http "http://127.0.0.1:8081/healthz"; then
  die "factoryd-git /healthz did not become ready"
fi
if ! wait_factory_name "http://127.0.0.1:8080/api/v1/factory" "m5"; then
  die "factory m5 did not appear"
fi
if ! wait_factory_name "http://127.0.0.1:8081/api/v1/factory" "m5-git"; then
  die "factory m5-git did not appear"
fi

if [[ "$MANUAL" == 1 ]]; then
  printf 'console: http://127.0.0.1:8080\n'
  printf 'git console: http://127.0.0.1:8081\n'
  printf 'api key file: %s\n' "$WORK/api-key"
  print_checklist
  if [[ -n "$ROUTER_PID" ]]; then
    disown "$ROUTER_PID" 2>/dev/null || true
  fi
  stub_pid=""
  for stub_pid in $STUB_PIDS; do
    disown "$stub_pid" 2>/dev/null || true
  done
  trap - EXIT INT TERM
  exit 0
fi

say "Playwright"
set +e
run_playwright
play_code=$?
set -e
if [[ ! -f "$SIGNAL/done" ]]; then
  touch "$SIGNAL/done"
fi
if [[ -n "$WATCH_PID" ]]; then
  wait "$WATCH_PID" 2>/dev/null || true
  WATCH_PID=""
fi
save_logs
if ! assert_evidence; then
  die "M5 FAIL — evidence check"
fi
if [[ "$play_code" -ne 0 ]]; then
  die "M5 FAIL — playwright exit ${play_code}"
fi
mode_name="$(printf '%s' "$MODE" | tr '[:lower:]' '[:upper:]')"
printf 'M5 %s PASS\n' "$mode_name"
