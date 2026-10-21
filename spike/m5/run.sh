#!/usr/bin/env bash
# M5 day of factory.
#
#   MODE=offline ./spike/m5/run.sh
#   MODE=live ./spike/m5/run.sh
#   MODE=live-ci ./spike/m5/run.sh
#   KEEP_UP=1 ./spike/m5/run.sh
#   ./spike/m5/run.sh --manual
#   ./spike/m5/run.sh specs
#   ./spike/m5/run.sh down
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
# M1_OPENAI_EXTRA_HEADERS is optional. live-ci copies it to
# FACTORY_INFERENCE_OPENAI_COMPATIBLE_EXTRA_HEADERS. live reads that
# variable from the environment. A missing required variable prints
# "missing <NAME>" and exits before any container starts. The model id
# must be listed by GET $base/models. That probe sends
# Authorization: Bearer when the API key is non-empty, then any extra
# headers. An extra Authorization line replaces the default. Values
# are not printed.
#
# Playwright receives M5_REVIEWING_MODEL. Offline sets it to
# m5-reviewing. Live and live-ci set it to M5_LIVE_MODEL_REVIEWING
# when that is set, otherwise M5_LIVE_MODEL. The Done card asserts
# "reviewer · " plus that id.
#
# Live and live-ci keep going after a report-only assertion fails, so
# one run lists every independent mismatch. Navigation, the next click
# target, and work-item state transitions stay hard expects. Offline
# keeps hard expects for every check. A failed run still exits
# non-zero, prints the Playwright failure list, and prints M5 FAIL.
#
# Preflight, outside live-ci and GitHub Actions, fetches origin when
# this directory is a git checkout. When HEAD is behind origin/main it
# prints a warning with the commit count and git pull. The warning
# does not fail the run. A fetch that cannot reach origin is skipped.
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
# Offline copies deploy/m5-factory and sets the implementer
# idleTimeoutMinutes to 1. The file in deploy stays at 5, which live
# and live-ci keep. Review still starts only after that idle.
#
# Each phase prints one line, "m5 phase <name> <seconds>s", for image
# build, stack up, Playwright, and evidence. The line has no env values.
#
# Each run generates a fresh FACTORY_API_KEY and ignores deploy/.env for
# that one variable. The driver signs deliveries with the run's webhook
# secret. Evidence strips the API key. Planted secrets must be absent.
#
# spike/m5/run_test.sh sources this file. Executing it runs the stack.

set -euo pipefail
# shellcheck source=spike/lib/common.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/../lib/common.sh"
# shellcheck source=spike/lib/driver.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/../lib/driver.sh"
# shellcheck source=spike/lib/cleanup.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/../lib/cleanup.sh"

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
WATCH_PID=""

die() {
  echo "$*" >&2
  exit 1
}

say() {
  printf '\n== %s\n' "$*"
}

# phase_report prints the seconds since start, which is a bash SECONDS
# value taken at the say marker for that phase.

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

# host_catalog_probe_url rewrites host.docker.internal to 127.0.0.1.
# Scheme, port, and path stay. Every other host is unchanged.

# apply_live_inference_env copies the live-ci secrets onto the factory
# inference variables. M1_OPENAI_EXTRA_HEADERS is optional. Unset leaves
# the extra-headers variable unset. MODE=live keeps the caller's values.

# write_catalog_headers writes the curl -H lines for the catalog probe.
# A non-empty API key becomes Authorization: Bearer. Extra headers then
# replace a default of the same name, case-insensitively. The file is
# the only copy of the values. Errors name the variable and the line
# number and do not include the line text.

# catalog_probe GETs $base/models. Header values are passed to curl as
# -H arguments and are not written to stdout or stderr.

# rewrite_offline_idle edits a copy. deploy/m5-factory stays at 5 minutes
# so live and live-ci keep the longer idle. Offline review starts after
# one minute instead of five.
rewrite_offline_idle() {
  local dir="$1"
  python3 - "$dir" <<'PY'
import os, sys

path = os.path.join(sys.argv[1], "agents", "implementer", "agent.md")
with open(path, encoding="utf-8") as fh:
    text = fh.read()
old = "idleTimeoutMinutes: 5\n"
new = "idleTimeoutMinutes: 1\n"
if text.count(old) != 1:
    raise SystemExit("offline idle rewrite: implementer idleTimeoutMinutes: 5 not found")
with open(path, "w", encoding="utf-8") as fh:
    fh.write(text.replace(old, new, 1))
PY
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

# reviewing_model_id matches the model prepare_definition leaves on the
# reviewer agent. Offline keeps the definition id. Live and live-ci use
# the reviewing override, then M5_LIVE_MODEL.
reviewing_model_id() {
  if [[ "$MODE" == offline ]]; then
    printf '%s\n' "m5-reviewing"
    return 0
  fi
  printf '%s\n' "${M5_LIVE_MODEL_REVIEWING:-$M5_LIVE_MODEL}"
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

prepare_definition() {
  # factoryd (uid 65532) replaces this tree with mode 700. A later run
  # cannot rm it as the host user. wipe_dir falls back to a root container
  # and only for a resolved directory strictly inside spike/m5/.work.
  wipe_dir "$DEFS"
  mkdir -p "$DEFS"
  cp -R "$ROOT/deploy/m5-factory" "$DEFS/m5-factory"
  if [[ "$MODE" == offline ]]; then
    rewrite_offline_idle "$DEFS/m5-factory"
  else
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
    spike_docker run --rm --network bridge \
      --add-host=host.docker.internal:host-gateway \
      busybox:1.36 \
      wget -q -T 5 -O - "$url" >"$dest"
    return
  fi
  spike_docker run --rm --network bridge \
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
  spike_spawn --class stack "stub-${name}" --out "$WORK/stub-${name}.log" --err "$WORK/stub-${name}.log" \
    "$WORK/model-stub" -addr "127.0.0.1:${port}" -script "$script"
  if ! wait_http "http://127.0.0.1:${port}/healthz"; then
    echo "model-stub ${name} did not become reachable" >&2
    return 1
  fi
}

start_offline_models() {
  say "Build model-stub and the router"
  mkdir -p "$WORK"
  spike_fg_dir "$ROOT" env CGO_ENABLED=0 GOOS="$(uname -s | tr '[:upper:]' '[:lower:]')" GOARCH="$(go env GOARCH)" \
    go build -o "$WORK/model-stub" ./spike/m1/model-stub
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
  spike_spawn --class stack router --out "$WORK/router.log" --err "$WORK/router.log" \
    env \
    M5_STUB_QUESTION=http://127.0.0.1:9091 \
    M5_STUB_AGENT=http://127.0.0.1:9092 \
    M5_STUB_TRIAGE=http://127.0.0.1:9093 \
    M5_STUB_PLANNING=http://127.0.0.1:9094 \
    M5_STUB_BUILDING=http://127.0.0.1:9095 \
    M5_STUB_REVIEWING=http://127.0.0.1:9096 \
    python3 "$ROOT/spike/m5/router.py" --addr "0.0.0.0:${ROUTER_PORT}"
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
  local phase_start="$SECONDS"
  spike_fg docker build --platform "$OCI_PLATFORM" -t factoryd:dev \
    -f "$ROOT/deploy/factoryd/Dockerfile" "$ROOT"
  spike_fg docker build --platform "$OCI_PLATFORM" -t factory-upstream-worker:dev \
    "$ROOT/deploy/worker"
  spike_fg_dir "$ROOT" env CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH_TARGET" \
    go build -o harness/generic-openai/factory-harness ./cmd/factory-harness
  spike_fg docker build --platform "$OCI_PLATFORM" -t factory-harness:dev \
    "$ROOT/harness/generic-openai"
  spike_fg docker build --platform "$OCI_PLATFORM" -t factory-m1-task:dev \
    "$ROOT/spike/m1/task-image"
  spike_fg docker build --platform "$OCI_PLATFORM" -t factoryd-git:dev \
    -f "$ROOT/spike/m5/factoryd-git.Dockerfile" "$ROOT/spike/m5"
  phase_report "image build" "$phase_start"
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
import json, os, sys
kind, dest = sys.argv[1], sys.argv[2]
nonce = os.environ.get("M5_RUN_NONCE", "").strip()
if nonce and any(c not in "0123456789abcdef" for c in nonce):
    raise SystemExit("M5_RUN_NONCE must be hex")
day_title = "m5-day" if nonce == "" else f"m5-day {nonce}"
# A specs rerun must not reuse issue 42, or intake attaches the event
# to the work item the previous run already finished.
issue_number = 42 if nonce == "" else 100000 + int(nonce[:4], 16) % 50000
issue_id = 4200 if nonce == "" else 500000 + int(nonce[:4], 16)
# The pull request shares the issue's owner/repo#N thread. A second
# specs run needs its own number or the link lands on the previous item.
# The default URL stays pull/7, which is what the spec asserts.
pr_number = 42 if nonce == "" else issue_number
pr_id = 4201 if nonce == "" else issue_id + 1
pr_url = (
    "https://github.com/polds/m5/pull/7"
    if nonce == ""
    else f"https://github.com/polds/m5/pull/{pr_number}"
)
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
            "id": issue_id,
            "number": issue_number,
            "title": day_title,
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
        "number": pr_number,
        "pull_request": {
            "id": pr_id,
            "number": pr_number,
            "title": day_title,
            "body": "Day of factory.",
            "html_url": pr_url,
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
      if [[ -n "${M5_RUN_NONCE:-}" ]]; then
        id="m5-issue-${M5_RUN_NONCE}"
      else
        id="m5-issue-${RANDOM}"
      fi
      write_github_body issue "$dest"
      code="$(post_signed "http://127.0.0.1:8080/api/v1/ingress/github" "$dest" \
        -H "X-GitHub-Event: issues" -H "X-GitHub-Delivery: ${id}")"
      ;;
    pr)
      if [[ -n "${M5_RUN_NONCE:-}" ]]; then
        id="m5-pr-${M5_RUN_NONCE}"
      else
        id="m5-pr-${RANDOM}"
      fi
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
      if [[ -n "${M5_RUN_NONCE:-}" ]]; then
        id="m5-wh-${M5_RUN_NONCE}-${RANDOM}"
      else
        id="m5-wh-${RANDOM}"
      fi
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

# fetch_origin_quiet updates origin and gives up after 15 seconds.
# The status is 0 only when fetch itself succeeds. python3 is already
# required, and its timeout is not the GNU timeout command.

# warn_stale_checkout prints a warning when HEAD is behind origin/main.
# It does not fail the run. live-ci and GitHub Actions skip the fetch.
# root defaults to this repo. Tests pass a temporary checkout.

# print_playwright_failures repeats the list reporter's failure block.
# That block is where soft expects show up together. A passing log
# prints nothing.

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

# m5_write_stack_env records the stack under $WORK. It is not evidence.
# Live inference credentials are not written. Offline records the dummy key.
m5_write_stack_env() {
  local tmp=""
  if [[ -z "${SPIKE_RUN_LABEL:-}" ]]; then
    return 0
  fi
  umask 077
  tmp="$(mktemp "${WORK}/stack.env.XXXXXX")"
  {
    printf 'SPIKE_RUN_ID=%q\n' "${SPIKE_RUN_ID:-}"
    printf 'SPIKE_RUN_LABEL=%q\n' "${SPIKE_RUN_LABEL:-}"
    printf 'STACK_MODE=%q\n' "${MODE:-}"
    printf 'STACK_ROUTER_PORT=%q\n' "${ROUTER_PORT:-}"
    printf 'COMPOSE_PROJECT=%q\n' "${COMPOSE_PROJECT:-factory-m5}"
    printf 'FACTORY_API_KEY=%q\n' "${FACTORY_API_KEY:-}"
    printf 'FACTORY_SECRET_M5_WEBHOOK=%q\n' "${FACTORY_SECRET_M5_WEBHOOK:-}"
    printf 'FACTORY_GITHUB_WEBHOOK_SECRET=%q\n' "${FACTORY_GITHUB_WEBHOOK_SECRET:-}"
    printf 'FACTORY_WORKER_API_KEY=%q\n' "${FACTORY_WORKER_API_KEY:-}"
    printf 'S3_SECRET_KEY=%q\n' "${S3_SECRET_KEY:-}"
    printf 'FACTORY_SECRET_M5_PLANTED=%q\n' "${FACTORY_SECRET_M5_PLANTED:-}"
    if [[ "${MODE:-}" == offline ]]; then
      printf 'FACTORY_INFERENCE_OPENAI_API_KEY=%q\n' "${FACTORY_INFERENCE_OPENAI_API_KEY:-}"
    fi
    spike_emit_children
  } >"$tmp"
  chmod 600 "$tmp"
  mv -f "$tmp" "$WORK/stack.env"
}

# shellcheck disable=SC2317,SC2329 # invoked by the EXIT/INT/TERM trap
cleanup() {
  local code=$?
  local why="${1:-EXIT}"
  mkdir -p "$SIGNAL" 2>/dev/null || true
  touch "$SIGNAL/done" 2>/dev/null || true
  spike_teardown_begin "$why" || return 0
  set +e
  # KEEP_UP leaves factoryd running, so the definition bind is still live.
  if [[ "${KEEP_UP:-}" == 1 ]]; then
    if [[ -n "${SPIKE_RUN_LABEL:-}" ]]; then
      m5_write_stack_env || true
      printf 'specs: ./spike/m5/run.sh specs\n'
      printf 'down: ./spike/m5/run.sh down\n'
    fi
  else
    if [[ "${COMPOSE_STARTED:-0}" == 1 ]]; then
      compose down -v --remove-orphans >/dev/null 2>&1 || true
      COMPOSE_STARTED=0
    fi
    wipe_dir "$DEFS" || true
    rm -f "$WORK/stack.env"
  fi
  if [[ -d "$EVIDENCE" && ! -f "$EVIDENCE/compose.log" ]]; then
    save_logs || true
  fi
  spike_teardown_finish "$code"
}

# A live stack.env means some recorded stack pid is still alive.
# A stale file (every pid dead) is removed so the default path can run.
m5_refuse_live_stack() {
  if [[ ! -f "$WORK/stack.env" ]]; then
    return 0
  fi
  if (
    set +u
    # shellcheck disable=SC1091
    . "$WORK/stack.env"
    i=1
    n="${SPIKE_CHILD_COUNT:-0}"
    while [[ "$i" -le "$n" ]]; do
      eval "pid=\${SPIKE_CHILD_${i}_PID}"
      eval "class=\${SPIKE_CHILD_${i}_CLASS}"
      if [[ "$class" == stack && -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
        exit 0
      fi
      i=$((i + 1))
    done
    exit 1
  ); then
    echo "M5 FAIL — KEEP_UP stack is still up (./spike/m5/run.sh down or ./spike/m5/run.sh specs)" >&2
    exit 1
  fi
  rm -f "$WORK/stack.env"
}

m5_load_stack_env() {
  # shellcheck disable=SC1091
  . "$WORK/stack.env"
  export SPIKE_RUN_ID SPIKE_RUN_LABEL
  SPIKE_REG="${WORK}/spike-run"
  export SPIKE_REG
  spike_load_children_from_env
}

m5_pids_match() {
  local dir="" name="" pid=""
  for dir in "$SPIKE_REG"/children/*; do
    [[ -d "$dir" ]] || continue
    name="$(cat "$dir/name")"
    pid="$(cat "$dir/pid")"
    if ! spike_pid_identity_ok "$dir"; then
      echo "M5 FAIL — stale stack: ${name} pid ${pid} is gone" >&2
      return 1
    fi
  done
}

m5_compose_ready() {
  local services=""
  services="$(compose ps --status running --services 2>/dev/null || true)"
  if ! printf '%s\n' "$services" | grep -qx 'factoryd'; then
    echo "M5 FAIL — compose services factoryd and factoryd-git are not running" >&2
    return 1
  fi
  if ! printf '%s\n' "$services" | grep -qx 'factoryd-git'; then
    echo "M5 FAIL — compose services factoryd and factoryd-git are not running" >&2
    return 1
  fi
}

m5_health_once() {
  if ! curl -fsS --connect-timeout 2 -m 5 "http://127.0.0.1:8080/healthz" >/dev/null; then
    echo "M5 FAIL — factoryd /healthz did not answer" >&2
    return 1
  fi
  if ! curl -fsS --connect-timeout 2 -m 5 "http://127.0.0.1:8081/healthz" >/dev/null; then
    echo "M5 FAIL — factoryd-git /healthz did not answer" >&2
    return 1
  fi
}

# Route A: keep compose and the database. Restart offline stubs and the
# router from the recorded argv so their scripts start fresh.
m5_restart_stack_processes() {
  local i=1 n=0 class="" name="" argv_q="" out="" err="" append=""
  local spawned_stub=0 spawned_router=0 router_pid=""
  local -a argv=()
  n="${SPIKE_CHILD_COUNT:-0}"
  if [[ "$n" -eq 0 ]]; then
    return 0
  fi
  KEEP_UP=0
  spike_stop_processes || true
  KEEP_UP=1
  rm -rf "$SPIKE_REG/children"
  mkdir -p "$SPIKE_REG/children"
  printf '%s\n' 0 >"$SPIKE_REG/seq"
  while [[ "$i" -le "$n" ]]; do
    eval "class=\${SPIKE_CHILD_${i}_CLASS}"
    if [[ "$class" != stack ]]; then
      i=$((i + 1))
      continue
    fi
    eval "name=\${SPIKE_CHILD_${i}_NAME}"
    eval "argv_q=\${SPIKE_CHILD_${i}_ARGV}"
    eval "out=\${SPIKE_CHILD_${i}_OUT}"
    eval "err=\${SPIKE_CHILD_${i}_ERR}"
    eval "append=\${SPIKE_CHILD_${i}_APPEND}"
    argv=()
    # argv_q is a list of %q words produced by spike_spawn.
    # shellcheck disable=SC2294
    eval "argv=( ${argv_q} )"
    if [[ -n "$out" && -n "$err" ]]; then
      if [[ "$append" == 1 ]]; then
        spike_spawn --class stack --append --out "$out" --err "$err" "$name" "${argv[@]}"
      else
        spike_spawn --class stack --out "$out" --err "$err" "$name" "${argv[@]}"
      fi
    else
      spike_spawn --class stack "$name" "${argv[@]}"
    fi
    case "$name" in
      stub-*) spawned_stub=1 ;;
      router)
        spawned_router=1
        router_pid="$SPIKE_LAST_PID"
        ;;
      *) ;;
    esac
    i=$((i + 1))
  done
  local port=""
  if [[ "$spawned_stub" -eq 1 ]]; then
    for port in 9091 9092 9093 9094 9095 9096; do
      if ! wait_http "http://127.0.0.1:${port}/healthz"; then
        echo "M5 FAIL — model-stub on ${port} did not become reachable" >&2
        return 1
      fi
    done
  fi
  if [[ "$spawned_router" -eq 1 ]]; then
    ROUTER_PORT="${STACK_ROUTER_PORT:-}"
    if [[ -n "$ROUTER_PORT" ]]; then
      # The previous listener can still own the port for a moment after
      # SIGKILL. Respawn only after that process has exited.
      local try=0
      while [[ "$try" -lt 20 ]]; do
        if curl -fsS --connect-timeout 1 -m 2 "http://127.0.0.1:${ROUTER_PORT}/healthz" >/dev/null 2>&1; then
          return 0
        fi
        if [[ -n "$router_pid" ]] && kill -0 "$router_pid" 2>/dev/null; then
          sleep 0.2
          try=$((try + 1))
          continue
        fi
        i=1
        while [[ "$i" -le "$n" ]]; do
          eval "name=\${SPIKE_CHILD_${i}_NAME}"
          if [[ "$name" == router ]]; then
            eval "argv_q=\${SPIKE_CHILD_${i}_ARGV}"
            eval "out=\${SPIKE_CHILD_${i}_OUT}"
            eval "err=\${SPIKE_CHILD_${i}_ERR}"
            eval "append=\${SPIKE_CHILD_${i}_APPEND}"
            argv=()
            # shellcheck disable=SC2294
            eval "argv=( ${argv_q} )"
            if [[ -n "$out" && -n "$err" ]]; then
              spike_spawn --class stack --out "$out" --err "$err" router "${argv[@]}"
            else
              spike_spawn --class stack router "${argv[@]}"
            fi
            router_pid="$SPIKE_LAST_PID"
            break
          fi
          i=$((i + 1))
        done
        sleep 0.3
        try=$((try + 1))
      done
      echo "M5 FAIL — model router did not become reachable" >&2
      return 1
    fi
  fi
}

m5_prepare_playwright_watch() {
  export PLAYWRIGHT_WATCH_IN_PARENT=1
  PLAYWRIGHT_DOCKER_ARGS="--init --label spike-run=${SPIKE_RUN_LABEL} --name spike-pw-${SPIKE_RUN_LABEL}"
  export PLAYWRIGHT_DOCKER_ARGS
  # cleanup touches signal/done so the previous watcher exits. A specs
  # rerun has to remove that file before the new watcher starts, or the
  # loop sees done and never handles kill/deliver/idle.
  mkdir -p "$SIGNAL"
  rm -f "$SIGNAL/done" "$SIGNAL/kill" "$SIGNAL/kill.ack" \
    "$SIGNAL/deliver" "$SIGNAL/deliver.ack" "$SIGNAL/idle" "$SIGNAL/idle.ack"
  if declare -F watch_signals >/dev/null; then
    spike_spawn --class transient watch watch_signals
    WATCH_PID=$SPIKE_LAST_PID
    export WATCH_PID
  fi
}

m5_playwright_pipeline() {
  local code=0
  set +e
  run_playwright 2>&1 | tee "$PLAY_LOG"
  code="${PIPESTATUS[0]}"
  return "$code"
}

m5_playwright_and_evidence() {
  local play_start=0 evidence_start=0 play_code=0 mode_name=""
  say "Playwright"
  play_start="$SECONDS"
  set +e
  mkdir -p "$EVIDENCE"
  PLAY_LOG="$EVIDENCE/playwright.log"
  m5_prepare_playwright_watch
  spike_fg m5_playwright_pipeline
  play_code=$?
  set -e
  phase_report "Playwright" "$play_start"
  if [[ ! -f "$SIGNAL/done" ]]; then
    touch "$SIGNAL/done"
  fi
  if [[ -n "$WATCH_PID" ]]; then
    wait "$WATCH_PID" 2>/dev/null || true
    WATCH_PID=""
  fi
  save_logs
  if [[ "$play_code" -ne 0 ]]; then
    print_playwright_failures "$PLAY_LOG"
  fi
  evidence_start="$SECONDS"
  if ! assert_evidence; then
    phase_report "evidence" "$evidence_start"
    die "M5 FAIL — evidence check"
  fi
  phase_report "evidence" "$evidence_start"
  if [[ "$play_code" -ne 0 ]]; then
    die "M5 FAIL — playwright exit ${play_code}"
  fi
  mode_name="$(printf '%s' "$MODE" | tr '[:lower:]' '[:upper:]')"
  printf 'M5 %s PASS\n' "$mode_name"
}

m5_down() {
  if [[ ! -f "$WORK/stack.env" ]]; then
    printf 'nothing to stop\n'
    exit 0
  fi
  m5_load_stack_env
  KEEP_UP=0
  export KEEP_UP
  COMPOSE_STARTED=1
  trap 'cleanup EXIT' EXIT
  trap 'cleanup INT' INT
  trap 'cleanup TERM' TERM
  exit 0
}

m5_specs() {
  if [[ ! -f "$WORK/stack.env" ]]; then
    echo "M5 FAIL — no KEEP_UP stack (run KEEP_UP=1 ./spike/m5/run.sh first)" >&2
    exit 1
  fi
  m5_load_stack_env
  if [[ "$MODE" != "${STACK_MODE:-}" ]]; then
    echo "M5 FAIL — MODE is ${MODE}, recorded stack is ${STACK_MODE:-unset}" >&2
    exit 1
  fi
  if ! m5_pids_match; then
    exit 1
  fi
  if ! m5_compose_ready; then
    exit 1
  fi
  if ! m5_health_once; then
    exit 1
  fi
  if [[ "$MODE" != offline ]]; then
    if [[ -f "$ROOT/deploy/.env" ]]; then
      load_env_file "$ROOT/deploy/.env"
    fi
    apply_live_inference_env
  fi
  KEEP_UP=1
  export KEEP_UP
  COMPOSE_STARTED=1
  mkdir -p "$WORK"
  trap 'cleanup EXIT' EXIT
  trap 'cleanup INT' INT
  trap 'cleanup TERM' TERM
  if [[ "$MODE" == offline ]]; then
    ROUTER_PORT="${STACK_ROUTER_PORT:-}"
    export ROUTER_PORT
    if ! m5_restart_stack_processes; then
      exit 1
    fi
  fi
  # A new nonce per specs invocation. Unset on the default path, so that
  # run's titles stay exactly as they are today.
  M5_RUN_NONCE="$(openssl rand -hex 4)"
  if [[ ! "$M5_RUN_NONCE" =~ ^[0-9a-f]{8}$ ]]; then
    echo "M5 FAIL — could not mint a spec nonce" >&2
    exit 1
  fi
  export M5_RUN_NONCE
  case " ${PLAYWRIGHT_EXTRA_ENV:-} " in
    *" M5_RUN_NONCE "*) ;;
    *) PLAYWRIGHT_EXTRA_ENV="${PLAYWRIGHT_EXTRA_ENV:+$PLAYWRIGHT_EXTRA_ENV }M5_RUN_NONCE" ;;
  esac
  export PLAYWRIGHT_EXTRA_ENV
  m5_playwright_and_evidence
  exit 0
}

if [[ "${BASH_SOURCE[0]}" != "$0" ]]; then
  return 0
fi

SUB=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --manual)
      if [[ -n "$SUB" ]]; then
        die "unknown argument $1"
      fi
      MANUAL=1
      ;;
    specs | down)
      if [[ "$MANUAL" -eq 1 || -n "$SUB" ]]; then
        die "unknown argument $1"
      fi
      SUB="$1"
      ;;
    *) die "unknown argument $1" ;;
  esac
  shift
done

if [[ -n "$SUB" && "${KEEP_UP:-}" == 1 ]]; then
  die "unknown argument ${SUB}"
fi

case "$SUB" in
  down) m5_down ;;
  specs) m5_specs ;;
  "") ;;
  *) die "unknown argument ${SUB}" ;;
esac

# File, MODE, and pid checks happen before any docker or go.
m5_refuse_live_stack

if [[ "$MANUAL" == 1 ]]; then
  KEEP_UP=1
  export KEEP_UP
fi

trap 'cleanup EXIT' EXIT
trap 'cleanup INT' INT
trap 'cleanup TERM' TERM

say "Preflight"
if ! accept_mode "$MODE"; then
  die "MODE must be offline, live, or live-ci (got ${MODE})"
fi
warn_stale_checkout
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
apply_live_inference_env

mkdir -p "$WORK"
spike_run_init m5
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
  if ! catalog_probe "$probe" "$catalog"; then
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
stack_start="$SECONDS"
compose down -v --remove-orphans >/dev/null 2>&1 || true
COMPOSE_STARTED=1
if ! spike_fg compose up -d --wait; then
  mkdir -p "$EVIDENCE"
  save_logs || true
  phase_report "stack up" "$stack_start"
  die "compose up failed"
fi

if ! wait_http "http://127.0.0.1:8080/healthz"; then
  phase_report "stack up" "$stack_start"
  die "factoryd /healthz did not become ready"
fi
if ! wait_http "http://127.0.0.1:8081/healthz"; then
  phase_report "stack up" "$stack_start"
  die "factoryd-git /healthz did not become ready"
fi
if ! wait_factory_name "http://127.0.0.1:8080/api/v1/factory" "m5"; then
  phase_report "stack up" "$stack_start"
  die "factory m5 did not appear"
fi
if ! wait_factory_name "http://127.0.0.1:8081/api/v1/factory" "m5-git"; then
  phase_report "stack up" "$stack_start"
  die "factory m5-git did not appear"
fi
phase_report "stack up" "$stack_start"

if [[ "$MANUAL" == 1 ]]; then
  printf 'console: http://127.0.0.1:8080\n'
  printf 'git console: http://127.0.0.1:8081\n'
  printf 'api key file: %s\n' "$WORK/api-key"
  print_checklist
  exit 0
fi

m5_playwright_and_evidence
