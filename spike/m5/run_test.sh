#!/usr/bin/env bash
# Shell checks for spike/m5/run.sh. No network and no running stack.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=spike/m5/run.sh
source "$ROOT/spike/m5/run.sh"

fails=0

run_case() {
  local name="$1"
  shift
  local err="$ROOT/spike/m5/.work/case-${name}.err"
  mkdir -p "$ROOT/spike/m5/.work"
  if "$@" >"$ROOT/spike/m5/.work/case-${name}.out" 2>"$err"; then
    printf 'ok   %s\n' "$name"
  else
    printf 'FAIL %s\n' "$name"
    cat "$err" >&2 || true
    fails=$((fails + 1))
  fi
}

expect_fail() {
  local name="$1" needle="$2"
  shift 2
  local err="$ROOT/spike/m5/.work/case-${name}.err"
  mkdir -p "$ROOT/spike/m5/.work"
  if "$@" >"$ROOT/spike/m5/.work/case-${name}.out" 2>"$err"; then
    printf 'FAIL %s (expected failure)\n' "$name"
    fails=$((fails + 1))
    return 0
  fi
  if ! grep -F "$needle" "$err" >/dev/null; then
    printf 'FAIL %s (stderr missing %s)\n' "$name" "$needle"
    cat "$err" >&2 || true
    fails=$((fails + 1))
    return 0
  fi
  printf 'ok   %s\n' "$name"
}

case_missing_live_url() {
  (
    unset FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL M5_LIVE_MODEL
    MODE=live
    require_mode_secrets
  )
}

case_missing_live_model() {
  (
    FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL="http://127.0.0.1:9/v1"
    unset M5_LIVE_MODEL
    MODE=live
    require_mode_secrets
  )
}

case_missing_live_ci_url() {
  (
    unset M1_OPENAI_BASE_URL M1_OPENAI_API_KEY M5_LIVE_MODEL
    MODE=live-ci
    require_mode_secrets
  )
}

case_missing_live_ci_key() {
  (
    M1_OPENAI_BASE_URL="http://127.0.0.1:9/v1"
    unset M1_OPENAI_API_KEY M5_LIVE_MODEL
    MODE=live-ci
    require_mode_secrets
  )
}

case_missing_live_ci_model() {
  (
    M1_OPENAI_BASE_URL="http://127.0.0.1:9/v1"
    M1_OPENAI_API_KEY="present"
    unset M5_LIVE_MODEL
    MODE=live-ci
    require_mode_secrets
  )
}

case_catalog_rejects() {
  local dir
  dir="$(mktemp -d)"
  printf '%s\n' '{"data":[{"id":"listed-model"}]}' >"$dir/catalog.json"
  reject_unlisted_model "$dir/catalog.json" "missing-model"
}

case_catalog_accepts() {
  local dir
  dir="$(mktemp -d)"
  printf '%s\n' '{"data":[{"id":"listed-model"}]}' >"$dir/catalog.json"
  reject_unlisted_model "$dir/catalog.json" "listed-model"
}

case_rewrite_copy_only() {
  local dir src
  dir="$(mktemp -d)"
  src="$ROOT/deploy/m5-factory"
  cp -R "$src" "$dir/copy"
  local before
  before="$(mktemp)"
  (cd "$src" && find . -type f -print | sort | while IFS= read -r path; do
    printf '%s ' "$path"
    cksum <"$src/$path"
  done) >"$before"
  rewrite_live_models "$dir/copy" "live-triage" "live-planning" "live-building" "live-reviewing"
  local after
  after="$(mktemp)"
  (cd "$src" && find . -type f -print | sort | while IFS= read -r path; do
    printf '%s ' "$path"
    cksum <"$src/$path"
  done) >"$after"
  cmp -s "$before" "$after"
  grep -F "live-building" "$dir/copy/agents/implementer/agent.md" >/dev/null
  if grep -R -F "live-building" "$src" >/dev/null; then
    echo "rewrite touched deploy/m5-factory" >&2
    return 1
  fi
}

case_env_file() {
  local file
  file="$(mktemp)"
  printf '%s\n' 'M5_LIVE_MODEL=from-file' 'M5_TASK_HOST=from-file' >"$file"
  (
    export M5_TASK_HOST="from-shell"
    unset M5_LIVE_MODEL
    load_env_file "$file"
    [[ "$M5_LIVE_MODEL" == "from-file" ]]
    [[ "$M5_TASK_HOST" == "from-shell" ]]
  )
}

case_headed_without_node() {
  local dir
  dir="$(mktemp -d)"
  printf '%s\n' '#!/bin/sh' 'exit 1' >"$dir/node"
  chmod 755 "$dir/node"
  (
    PATH="${dir}:/usr/bin:/bin"
    hash -r
    HEADED=1
    require_headed_node
  )
}

case_question_script() {
  local dir
  dir="$(mktemp -d)"
  write_offline_scripts "$dir"
  python3 - "$dir/question.json" <<'PY'
import json, sys
steps = json.load(open(sys.argv[1], encoding="utf-8"))
if steps[0].get("tool") != "ask_input":
    raise SystemExit("first step is not ask_input")
if steps[0].get("args", {}).get("text") != "Which staging env?":
    raise SystemExit("ask_input text mismatch")
if "final" not in steps[1]:
    raise SystemExit("second step is not final")
PY
}

case_probe_rewrites_desktop_host() {
  local got
  got="$(host_catalog_probe_url "http://host.docker.internal:8317/v1" "")"
  [[ "$got" == "http://127.0.0.1:8317/v1" ]]
}

case_task_host_linux() {
  local got
  got="$(M5_TASK_HOST='' host_for_tasks "Docker Engine" "172.17.0.1" "Linux" "")"
  [[ "$got" == "172.17.0.1" ]]
}

case_task_host_orbstack() {
  local got
  got="$(M5_TASK_HOST='' host_for_tasks "OrbStack" "192.168.215.1" "Darwin" "")"
  [[ "$got" == "host.docker.internal" ]]
}

case_task_host_override() {
  local got
  got="$(M5_TASK_HOST=tasks.example host_for_tasks "Docker Engine" "172.17.0.1" "Linux" "")"
  [[ "$got" == "tasks.example" ]]
}

case_no_gnu_flags() {
  # The pattern lives in this file, so the scan covers the driver only.
  if grep -n -E 'sed -i|date -d|readlink -f|grep -P' "$ROOT/spike/m5/run.sh"; then
    echo "GNU-only flag" >&2
    return 1
  fi
}

case_build_flags() {
  grep -F 'GOOS=linux' "$ROOT/spike/m5/run.sh" >/dev/null
  grep -F 'GOARCH=' "$ROOT/spike/m5/run.sh" >/dev/null
  grep -F -- '--platform' "$ROOT/spike/m5/run.sh" >/dev/null
}

case_spec_static() {
  local spec="$ROOT/spike/m5/e2e/day.spec.ts" token
  for token in \
    'curl' 'factoryctl' 'page.request' 'request.' 'APIRequestContext' \
    'fetch(' 'page.route' 'context.route'; do
    if grep -n -F "$token" "$spec"; then
      echo "spec contains ${token}" >&2
      return 1
    fi
  done
}

case_failure_routes_on_building() {
  python3 - "$ROOT/spike/m5/router.py" <<'PY'
import importlib.util
import sys

path = sys.argv[1]
spec = importlib.util.spec_from_file_location("m5router", path)
if spec is None or spec.loader is None:
    raise SystemExit("router import failed")
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)
route = mod.Router._route

def check(model, text, want):
    got = route(None, model, text)
    if got != want:
        raise SystemExit(f"{model} {text!r} -> {got}, want {want}")

check("m5-triage", "title m5-agent-failure", "triage")
check("m5-planning", "title m5-agent-failure", "planning")
check("m5-building", "m5-agent-failure the agent should report failure", "agent")
check("m5-triage", "m5-infra-kill wait for the container to stop", "triage")
check("m5-building", "m5-infra-kill wait for the container to stop", "hold-infra")
check("m5-reviewing", "m5-infra-kill", "reviewing")
check("m5-building", "m5-day without follow", "hold-day")
check("m5-building", "m5-day and m5-follow-up", "building")
check("m5-triage", "m5-question which staging", "question")
PY
}

case_spec_drives_step8() {
  local spec="$ROOT/spike/m5/e2e/day.spec.ts"
  local impl="$ROOT/deploy/m5-factory/agents/implementer/agent.md"
  local triage="$ROOT/deploy/m5-factory/agents/triage/agent.md"
  grep -F 'm5-infra-kill wait for the container to stop' "$spec" >/dev/null
  grep -F 'm5-agent-failure the agent should report failure' "$spec" >/dev/null
  grep -F 'attempt <= 2' "$spec" >/dev/null
  grep -F 'step 8: infrastructure kill did not land after two attempts' "$spec" >/dev/null
  grep -F 'step 8: agent failure was not agent / agent_reported after two attempts' "$spec" >/dev/null
  grep -F 'approveOnly(page, title)' "$spec" >/dev/null
  grep -F 'failed_by_agent' "$impl" >/dev/null
  grep -F 'idleTimeoutMinutes: 5' "$impl" >/dev/null
  grep -F 'timeout: 8 * 60 * 1000' "$spec" >/dev/null
  grep -F 'wait for the container to stop' "$impl" >/dev/null
  if grep -F 'failed_by_agent' "$triage" >/dev/null; then
    echo "triage prompt owns the agent failure" >&2
    return 1
  fi
}

lock_mode700() {
  local parent="$1"
  docker run --rm --user 0 -v "$parent:/defs" golang:1.26-alpine \
    sh -c 'mkdir -p /defs/m5-factory/kept && echo locked >/defs/m5-factory/kept/file && chown -R 65532:65532 /defs/m5-factory && chmod 700 /defs/m5-factory'
}

case_wipe_refuses_outside() {
  local outside="" err=""
  outside="$(mktemp -d)"
  err="$(mktemp)"
  lock_mode700 "$outside"
  if rm -rf "$outside/m5-factory" 2>/dev/null; then
    echo "host removed the outside fixture; it is not locked" >&2
    rm -rf "$outside"
    return 1
  fi
  if wipe_dir "$outside/m5-factory" 2>"$err"; then
    echo "wipe_dir accepted a path outside .work" >&2
    return 1
  fi
  if ! grep -F "wipe_dir: refusing" "$err" >/dev/null; then
    echo "outside refusal missing" >&2
    cat "$err" >&2
    return 1
  fi
  if [[ ! -d "$outside/m5-factory" ]]; then
    echo "outside locked directory was removed" >&2
    return 1
  fi
  if ! docker run --rm --user 0 -v "$outside:/probe:ro" golang:1.26-alpine \
    sh -c 'test -f /probe/m5-factory/kept/file'; then
    echo "outside locked file was removed" >&2
    return 1
  fi
  # The fixture sits outside .work so this case can prove wipe_dir left it.
  # Removing it is the test's own cleanup.
  docker run --rm --user 0 -v "$outside:/probe" golang:1.26-alpine \
    sh -c 'rm -rf /probe/m5-factory'
  rm -rf "$outside"
  rm -f "$err"
}

case_wipe_refuses_symlink() {
  local outside="" link="$WORK/wipe-escape" err=""
  outside="$(mktemp -d)"
  err="$(mktemp)"
  echo keep >"$outside/keep"
  rm -f "$link"
  ln -s "$outside" "$link"
  if wipe_dir "$link" 2>"$err"; then
    echo "wipe_dir followed a symlink out of .work" >&2
    rm -f "$link"
    rm -rf "$outside"
    return 1
  fi
  if ! grep -F "wipe_dir: refusing" "$err" >/dev/null; then
    echo "symlink refusal missing" >&2
    cat "$err" >&2
    rm -f "$link"
    rm -rf "$outside"
    return 1
  fi
  if [[ ! -f "$outside/keep" || ! -L "$link" ]]; then
    echo "symlink or its target was removed" >&2
    rm -f "$link"
    rm -rf "$outside"
    return 1
  fi
  rm -f "$link" "$err"
  rm -rf "$outside"
}

case_wipe_refuses_empty() {
  local err=""
  err="$(mktemp)"
  if wipe_dir "" 2>"$err"; then
    echo "wipe_dir accepted an empty path" >&2
    rm -f "$err"
    return 1
  fi
  if ! grep -F "wipe_dir: empty path" "$err" >/dev/null; then
    echo "empty refusal missing" >&2
    cat "$err" >&2
    rm -f "$err"
    return 1
  fi
  if wipe_dir 2>"$err"; then
    echo "wipe_dir accepted a missing argument" >&2
    rm -f "$err"
    return 1
  fi
  grep -F "wipe_dir: empty path" "$err" >/dev/null
  rm -f "$err"
}

case_wipe_refuses_work_and_root() {
  local canary="$WORK/wipe-root-canary" err=""
  err="$(mktemp)"
  echo keep >"$canary"
  if wipe_dir "$WORK" 2>"$err"; then
    echo "wipe_dir accepted .work" >&2
    rm -f "$canary" "$err"
    return 1
  fi
  if ! grep -F "wipe_dir: refusing" "$err" >/dev/null; then
    echo ".work refusal missing" >&2
    cat "$err" >&2
    rm -f "$canary" "$err"
    return 1
  fi
  if [[ ! -f "$canary" ]]; then
    echo ".work canary was removed" >&2
    return 1
  fi
  rm -f "$canary"
  if wipe_dir / 2>"$err"; then
    echo "wipe_dir accepted /" >&2
    rm -f "$err"
    return 1
  fi
  if ! grep -F "wipe_dir: refusing" "$err" >/dev/null; then
    echo "/ refusal missing" >&2
    cat "$err" >&2
    rm -f "$err"
    return 1
  fi
  [[ -f "$ROOT/spike/m5/run.sh" ]]
  rm -f "$err"
}

case_wipe_locked_inside_work() {
  local tree="$WORK/wipe-locked"
  mkdir -p "$tree"
  lock_mode700 "$tree"
  if rm -rf "$tree/m5-factory" 2>/dev/null; then
    echo "host removed the mode-700 tree; fixture is not locked" >&2
    return 1
  fi
  wipe_dir "$tree"
  if [[ -e "$tree/m5-factory" || -e "$tree/m5-factory/kept/file" ]]; then
    echo "locked tree inside .work survived wipe_dir" >&2
    return 1
  fi
  rm -rf "$tree"
}

case_keep_up_skips_defs_wipe() {
  local defs="$WORK/keep-up-defs"
  mkdir -p "$defs" "$WORK/keep-up-evidence" "$WORK/keep-up-signal"
  echo keep >"$defs/marker"
  touch "$WORK/keep-up-evidence/compose.log"
  (
    KEEP_UP=1
    DEFS="$defs"
    EVIDENCE="$WORK/keep-up-evidence"
    SIGNAL="$WORK/keep-up-signal"
    COMPOSE_STARTED=0
    WATCH_PID=""
    ROUTER_PID=""
    STUB_PIDS=""
    cleanup
  )
  if [[ ! -f "$defs/marker" ]]; then
    echo "KEEP_UP=1 wiped the definition tree" >&2
    return 1
  fi
  rm -rf "$defs" "$WORK/keep-up-evidence" "$WORK/keep-up-signal"
}

case_prepare_definition_locked_tree() {
  local tree="$WORK/prepare-locked" code=0
  mkdir -p "$tree"
  set +e
  (
    DEFS="$tree"
    MODE=offline
    lock_mode700 "$DEFS"
    if rm -rf "$DEFS/m5-factory" 2>/dev/null; then
      echo "host removed the mode-700 tree; fixture is not locked" >&2
      exit 1
    fi
    prepare_definition
    if [[ ! -f "$DEFS/m5-factory/factory.yaml" ]]; then
      echo "first prepare did not replace the locked tree" >&2
      exit 1
    fi
    lock_mode700 "$DEFS"
    prepare_definition
    if [[ ! -f "$DEFS/m5-factory/factory.yaml" ]]; then
      echo "second prepare did not replace the locked tree" >&2
      exit 1
    fi
  )
  code=$?
  set -e
  wipe_dir "$tree" || true
  rm -rf "$tree"
  return "$code"
}

case_compose_config() {
  local out defs gitdir
  defs="$(mktemp -d)"
  gitdir="$(mktemp -d)"
  out="$ROOT/spike/m5/.work/compose-config.yml"
  mkdir -p "$ROOT/spike/m5/.work" "$defs" "$gitdir"
  M5_DEFS_HOST="$defs" \
    M5_GIT_HOST="$gitdir" \
    FACTORY_DEFINITION_HOST_PATH="$defs" \
    FACTORY_DEFINITION_MOUNT=/definitions \
    docker compose -p factory-m5 \
    -f "$ROOT/deploy/docker-compose.yml" \
    -f "$ROOT/spike/m5/compose.yml" \
    --profile m5 config >"$out"
  python3 - "$out" "$defs" <<'PY'
import sys
text = open(sys.argv[1], encoding="utf-8").read()
defs = sys.argv[2]
if "--idle-on-complete=0s" in text:
    raise SystemExit("worker command still passes --idle-on-complete=0s")
if 'published: "8081"' not in text or "target: 8080" not in text:
    raise SystemExit("factoryd-git is not published on 8081")
if defs not in text or "/definitions" not in text:
    raise SystemExit("definition parent mount is missing")
# The parent mount is read-write. A :ro short form or read_only: true
# on that target fails the check.
section = text.split("factoryd:", 1)[1].split("\n  factoryd-git:", 1)[0]
if "FACTORY_INFERENCE_OPENAI_COMPATIBLE_EXTRA_HEADERS" not in section:
    raise SystemExit("factoryd is missing extra header passthrough")
if "read_only: true" in section and "/definitions" in section:
    # read_only true is allowed on other mounts in the same service.
    # The /definitions bind must not be read-only.
    blocks = section.split("- type:")
    for block in blocks:
        if "/definitions" in block and "read_only: true" in block and "bind" in block:
            raise SystemExit("definition mount is read-only")
print("compose config ok")
PY
}

case_prebound_loopback_detected() {
  local dir="$ROOT/spike/m5/.work/prebound-$$" port="" pid=""
  mkdir -p "$dir"
  python3 - "$dir/port" <<'PY' &
import socket
import sys
import time

path = sys.argv[1]
sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
sock.bind(("127.0.0.1", 0))
sock.listen(1)
with open(path, "w", encoding="utf-8") as fh:
    fh.write(str(sock.getsockname()[1]))
while True:
    time.sleep(0.2)
PY
  pid=$!
  local i
  for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
    if [[ -s "$dir/port" ]]; then
      break
    fi
    sleep 0.1
  done
  port="$(tr -d '[:space:]' <"$dir/port")"
  if [[ -z "$port" ]]; then
    echo "prebound listener did not report a port" >&2
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    rm -rf "$dir"
    return 1
  fi
  local code=0 err
  err="$(mktemp)"
  set +e
  (
    M5_ROUTER_PORT="$port"
    resolve_router_port
  ) 2>"$err"
  code=$?
  set -e
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  rm -rf "$dir"
  if [[ "$code" -eq 0 ]]; then
    echo "prebound listener was accepted" >&2
    rm -f "$err"
    return 1
  fi
  grep -F "router port ${port} is answered by another process" "$err"
  rm -f "$err"
}

case_router_port_picked() {
  (
    unset M5_ROUTER_PORT
    resolve_router_port
    [[ "$ROUTER_PORT" =~ ^[1-9][0-9]*$ ]]
    router_py ensure "$ROUTER_PORT"
  )
}

case_router_port_configured() {
  local port
  port="$(
    python3 - <<'PY'
import socket

sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
sock.bind(("127.0.0.1", 0))
print(sock.getsockname()[1])
sock.close()
PY
  )"
  (
    M5_ROUTER_PORT="$port"
    resolve_router_port
    [[ "$ROUTER_PORT" == "$port" ]]
  )
}

case_router_port_invalid() {
  (
    M5_ROUTER_PORT="nope"
    resolve_router_port
  )
}

case_router_identity_matches() {
  local dir="$ROOT/spike/m5/.work/router-id-$$" port="" pid="" body="" hdr=""
  mkdir -p "$dir"
  port="$(
    unset M5_ROUTER_PORT
    resolve_router_port
    printf '%s\n' "$ROUTER_PORT"
  )"
  M5_STUB_QUESTION=http://127.0.0.1:9 \
    M5_STUB_AGENT=http://127.0.0.1:9 \
    M5_STUB_TRIAGE=http://127.0.0.1:9 \
    M5_STUB_PLANNING=http://127.0.0.1:9 \
    M5_STUB_BUILDING=http://127.0.0.1:9 \
    M5_STUB_REVIEWING=http://127.0.0.1:9 \
    python3 "$ROOT/spike/m5/router.py" --addr "0.0.0.0:${port}" \
    >"$dir/router.log" 2>&1 &
  pid=$!
  body="$(mktemp)"
  hdr="$(mktemp)"
  if ! wait_http "http://127.0.0.1:${port}/healthz"; then
    echo "router did not become reachable" >&2
    cat "$dir/router.log" >&2 || true
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    rm -rf "$dir" "$body" "$hdr"
    return 1
  fi
  if ! curl -fsS --connect-timeout 2 -m 5 -D "$hdr" -o "$body" \
    "http://127.0.0.1:${port}/v1/models"; then
    echo "models request failed" >&2
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    rm -rf "$dir" "$body" "$hdr"
    return 1
  fi
  if ! grep -i '^X-M5-Router: m5-offline-router' "$hdr" >/dev/null; then
    echo "identity header missing" >&2
    cat "$hdr" >&2
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    rm -rf "$dir" "$body" "$hdr"
    return 1
  fi
  if ! host_router_identity_ok "$port"; then
    echo "host identity check rejected our router" >&2
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    rm -rf "$dir" "$body" "$hdr"
    return 1
  fi
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  rm -rf "$dir" "$body" "$hdr"
}

case_foreign_models_rejected() {
  local dir="$ROOT/spike/m5/.work/foreign-$$" port="" pid="" code=0
  mkdir -p "$dir"
  python3 - "$dir/port" <<'PY' &
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

path = sys.argv[1]

class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        body = b'{"object":"list","data":[{"id":"other"}]}'
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        return

server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
with open(path, "w", encoding="utf-8") as fh:
    fh.write(str(server.server_address[1]))
server.serve_forever()
PY
  pid=$!
  local i
  for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
    if [[ -s "$dir/port" ]]; then
      break
    fi
    sleep 0.1
  done
  port="$(tr -d '[:space:]' <"$dir/port")"
  if [[ -z "$port" ]] || ! wait_http "http://127.0.0.1:${port}/"; then
    echo "foreign listener did not answer" >&2
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    rm -rf "$dir"
    return 1
  fi
  set +e
  host_router_identity_ok "$port"
  code=$?
  set -e
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  rm -rf "$dir"
  if [[ "$code" -eq 0 ]]; then
    echo "foreign model list was accepted" >&2
    return 1
  fi
}

case_router_port_static() {
  grep -F 'M5_ROUTER_PORT' "$ROOT/spike/m5/run.sh" >/dev/null
  grep -F 'host.docker.internal' "$ROOT/spike/m5/run.sh" >/dev/null
  grep -F 'router port ' "$ROOT/spike/m5/run.sh" >/dev/null
  grep -F 'M5_ROUTER_PORT' "$ROOT/spike/m5/README.md" >/dev/null
  grep -F 'm5-offline-router' "$ROOT/spike/m5/router.py" >/dev/null
  if grep -F 'ROUTER_PORT=8318' "$ROOT/spike/m5/run.sh"; then
    echo "router port is still hardcoded" >&2
    return 1
  fi
  if grep -F '0.0.0.0:8318' "$ROOT/spike/m5/router.py"; then
    echo "router still defaults to 8318" >&2
    return 1
  fi
}

case_container_gateway_linux() {
  container_needs_host_gateway "Docker Engine" "Linux"
}

case_container_gateway_orbstack() {
  if container_needs_host_gateway "OrbStack" "Darwin"; then
    echo "OrbStack must keep its own host.docker.internal" >&2
    return 1
  fi
}

case_container_gateway_desktop() {
  if container_needs_host_gateway "Docker Desktop" "Linux"; then
    echo "Docker Desktop must keep its own host.docker.internal" >&2
    return 1
  fi
}

git_quiet() {
  git -c user.email=factory@example.com -c user.name=factory "$@"
}

# make_behind_checkout prints a clone that is extra commits behind origin/main.
# extra 0 is level with origin. Git chatter stays off stdout.
make_behind_checkout() {
  local parent="$1" extra="$2"
  local origin="$parent/origin.git" seed="$parent/seed" checkout="$parent/checkout" i=0
  git init --bare -b main "$origin" >/dev/null
  git init -b main "$seed" >/dev/null
  git_quiet -C "$seed" commit --allow-empty -m base >/dev/null
  git -C "$seed" remote add origin "$origin"
  git -C "$seed" push origin HEAD:main >/dev/null 2>&1
  git clone -q "$origin" "$checkout"
  while [[ "$i" -lt "$extra" ]]; do
    i=$((i + 1))
    git_quiet -C "$seed" commit --allow-empty -m "extra ${i}" >/dev/null
    git -C "$seed" push origin HEAD:main >/dev/null 2>&1
  done
  printf '%s\n' "$checkout"
}

assert_no_warning() {
  local err="$1"
  if grep -F "WARNING:" "$err" >/dev/null; then
    echo "unexpected stale-checkout warning" >&2
    cat "$err" >&2
    return 1
  fi
}

case_stale_offline_behind() {
  local parent checkout err before after seed_head
  parent="$(mktemp -d)"
  checkout="$(make_behind_checkout "$parent" 2)"
  err="$(mktemp)"
  before="$(git -C "$checkout" rev-parse origin/main)"
  seed_head="$(git -C "$parent/seed" rev-parse HEAD)"
  [[ "$before" != "$seed_head" ]]
  (
    MODE=offline
    unset GITHUB_ACTIONS
    warn_stale_checkout "$checkout"
  ) 2>"$err"
  after="$(git -C "$checkout" rev-parse origin/main)"
  [[ "$after" == "$seed_head" ]]
  grep -F "WARNING: HEAD is 2 commits behind origin/main." "$err" >/dev/null
  grep -F "Run git pull to update this checkout." "$err" >/dev/null
  rm -rf "$parent"
  rm -f "$err"
}

case_stale_live_behind() {
  local parent checkout err
  parent="$(mktemp -d)"
  checkout="$(make_behind_checkout "$parent" 1)"
  err="$(mktemp)"
  (
    MODE=live
    unset GITHUB_ACTIONS
    warn_stale_checkout "$checkout"
  ) 2>"$err"
  grep -F "WARNING: HEAD is 1 commit behind origin/main." "$err" >/dev/null
  grep -F "Run git pull to update this checkout." "$err" >/dev/null
  rm -rf "$parent"
  rm -f "$err"
}

case_stale_live_ci_skips() {
  local parent checkout err before after
  parent="$(mktemp -d)"
  checkout="$(make_behind_checkout "$parent" 2)"
  err="$(mktemp)"
  before="$(git -C "$checkout" rev-parse origin/main)"
  (
    MODE=live-ci
    unset GITHUB_ACTIONS
    warn_stale_checkout "$checkout"
  ) 2>"$err"
  after="$(git -C "$checkout" rev-parse origin/main)"
  [[ "$before" == "$after" ]]
  assert_no_warning "$err"
  rm -rf "$parent"
  rm -f "$err"
}

case_stale_actions_offline_skips() {
  local parent checkout err before after
  parent="$(mktemp -d)"
  checkout="$(make_behind_checkout "$parent" 2)"
  err="$(mktemp)"
  before="$(git -C "$checkout" rev-parse origin/main)"
  (
    MODE=offline
    GITHUB_ACTIONS=true
    warn_stale_checkout "$checkout"
  ) 2>"$err"
  after="$(git -C "$checkout" rev-parse origin/main)"
  [[ "$before" == "$after" ]]
  assert_no_warning "$err"
  rm -rf "$parent"
  rm -f "$err"
}

case_stale_actions_live_skips() {
  local parent checkout err before after
  parent="$(mktemp -d)"
  checkout="$(make_behind_checkout "$parent" 2)"
  err="$(mktemp)"
  before="$(git -C "$checkout" rev-parse origin/main)"
  (
    MODE=live
    GITHUB_ACTIONS=true
    warn_stale_checkout "$checkout"
  ) 2>"$err"
  after="$(git -C "$checkout" rev-parse origin/main)"
  [[ "$before" == "$after" ]]
  assert_no_warning "$err"
  rm -rf "$parent"
  rm -f "$err"
}

case_stale_fetch_failure() {
  local parent repo err
  parent="$(mktemp -d)"
  repo="$parent/repo"
  git init -b main "$repo" >/dev/null
  git_quiet -C "$repo" commit --allow-empty -m base >/dev/null
  git -C "$repo" remote add origin "$parent/missing.git"
  err="$(mktemp)"
  (
    MODE=offline
    unset GITHUB_ACTIONS
    warn_stale_checkout "$repo"
  ) 2>"$err"
  assert_no_warning "$err"
  rm -rf "$parent"
  rm -f "$err"
}

case_stale_current() {
  local parent checkout err
  parent="$(mktemp -d)"
  checkout="$(make_behind_checkout "$parent" 0)"
  err="$(mktemp)"
  (
    MODE=offline
    unset GITHUB_ACTIONS
    warn_stale_checkout "$checkout"
  ) 2>"$err"
  assert_no_warning "$err"
  rm -rf "$parent"
  rm -f "$err"
}

case_stale_ahead() {
  local parent checkout err
  parent="$(mktemp -d)"
  checkout="$(make_behind_checkout "$parent" 0)"
  git_quiet -C "$checkout" commit --allow-empty -m local >/dev/null
  err="$(mktemp)"
  (
    MODE=live
    unset GITHUB_ACTIONS
    warn_stale_checkout "$checkout"
  ) 2>"$err"
  assert_no_warning "$err"
  rm -rf "$parent"
  rm -f "$err"
}

case_stale_not_checkout() {
  local parent err
  parent="$(mktemp -d)"
  err="$(mktemp)"
  (
    MODE=live
    unset GITHUB_ACTIONS
    warn_stale_checkout "$parent"
  ) 2>"$err"
  assert_no_warning "$err"
  rm -rf "$parent"
  rm -f "$err"
}

case_stale_no_origin() {
  local parent repo err
  parent="$(mktemp -d)"
  repo="$parent/repo"
  git init -b main "$repo" >/dev/null
  git_quiet -C "$repo" commit --allow-empty -m base >/dev/null
  err="$(mktemp)"
  (
    MODE=offline
    unset GITHUB_ACTIONS
    warn_stale_checkout "$repo"
  ) 2>"$err"
  assert_no_warning "$err"
  rm -rf "$parent"
  rm -f "$err"
}

case_playwright_failure_summary() {
  local log out
  log="$(mktemp)"
  out="$(mktemp)"
  cat >"$log" <<'EOF'
Running 2 tests using 1 worker

  ✘  1 soft.spec.ts:9:5 › two soft failures and continue (8ms)

  1) soft.spec.ts:9:5 › two soft failures and continue ─────────────────────────────────────────────

    Error: card text

    Expected: 2
    Received: 1

    Error: model id

    Expected: other-model
    Received: m5-reviewing

  2 failed
    soft.spec.ts:9:5 › two soft failures and continue ──────────────────────────────────────────────
EOF
  print_playwright_failures "$log" 2>"$out"
  grep -F "== Playwright failures" "$out" >/dev/null
  grep -F "Error: card text" "$out" >/dev/null
  grep -F "Error: model id" "$out" >/dev/null
  if grep -F "Running 2 tests" "$out" >/dev/null; then
    echo "summary included the progress log" >&2
    rm -f "$log" "$out"
    return 1
  fi
  printf '%s\n' "  1 passed" >"$log"
  if print_playwright_failures "$log" 2>&1 | grep -F "Playwright"; then
    echo "summary printed for a passing log" >&2
    rm -f "$log" "$out"
    return 1
  fi
  rm -f "$log" "$out"
}

case_spec_outcome_modes() {
  local spec="$ROOT/spike/m5/e2e/day.spec.ts"
  grep -F 'function outcome<T>' "$spec" >/dev/null
  grep -F 'const check = liveMode(mode) ? expect.soft : expect;' "$spec" >/dev/null
  grep -F 'await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();' "$spec" >/dev/null
  grep -F 'await expect(building.first()).toBeVisible({ timeout: 180_000 });' "$spec" >/dev/null
  grep -F 'await expect(followUp).toBeEnabled({ timeout: 60_000 });' "$spec" >/dev/null
  grep -F 'await expect(dayCard).toBeVisible({ timeout: 8 * 60 * 1000 });' "$spec" >/dev/null
  grep -F 'await expect(agentListRow).toBeVisible({ timeout: 60_000 });' "$spec" >/dev/null
  grep -F 'await expect(infraListRow).toBeVisible();' "$spec" >/dev/null
  grep -F 'await expect(infraPanel).toBeVisible();' "$spec" >/dev/null
  grep -F 'await outcome(infraPanel).toHaveAttribute("data-failure-class", "infrastructure");' "$spec" >/dev/null
  grep -F 'await outcome(page.getByText("All factories")).toBeVisible();' "$spec" >/dev/null
  grep -F 'await expect(page.getByRole("button", { name: "Close", exact: true })).toBeVisible();' "$spec" >/dev/null
  grep -F 'outcome(dayCard).toContainText(' "$spec" >/dev/null
  grep -F 'outcome(replayPosts, "step 10: replay posted once")' "$spec" >/dev/null
  grep -F 'outcome(axeFindings.join("\n"), "axe")' "$spec" >/dev/null
  if grep -F 'expect(dayCard).toContainText(' "$spec" >/dev/null; then
    echo "reviewer model id is still a hard expect" >&2
    return 1
  fi
  if grep -F 'await expect(page.getByText("All factories")).toBeVisible();' "$spec" >/dev/null; then
    echo "dashboard subtitle is still a hard expect" >&2
    return 1
  fi
}

install_curl_stub() {
  local dir="$1"
  cat >"$dir/curl" <<'EOF'
#!/bin/bash
set -euo pipefail
out="${CURL_ARGS_FILE:?}"
: >"$out"
chmod 600 "$out"
prev=""
dest=""
for arg in "$@"; do
  printf '%s\0' "$arg" >>"$out"
  if [[ "$prev" == "-o" ]]; then
    dest="$arg"
  fi
  prev="$arg"
done
if [[ -n "$dest" ]]; then
  printf '%s\n' '{"data":[{"id":"listed-model"}]}' >"$dest"
fi
EOF
  chmod 755 "$dir/curl"
}

# probe_with_stub runs catalog_probe through a fake curl. The fake curl
# records arguments and does not print them. mode selects the assertion.
probe_with_stub() {
  local mode="$1" dir="" code=0
  dir="$(mktemp -d)"
  chmod 700 "$dir"
  install_curl_stub "$dir"
  # Assignments in the caller are not exported. Export here so the probe
  # and the checker see them, without an export inside each case subshell.
  export FACTORY_INFERENCE_OPENAI_API_KEY
  export FACTORY_INFERENCE_OPENAI_COMPATIBLE_EXTRA_HEADERS
  CURL_ARGS_FILE="$dir/args" CATALOG_CURL="$dir/curl" \
    catalog_probe "http://127.0.0.1:9/v1" "$dir/catalog.json" >"$dir/out" 2>"$dir/err" || code=$?
  python3 - "$mode" "$dir" "$code" <<'PY'
import os, sys
mode, root, code = sys.argv[1], sys.argv[2], int(sys.argv[3])
out = open(os.path.join(root, "out"), encoding="utf-8").read()
err = open(os.path.join(root, "err"), encoding="utf-8").read()
args_path = os.path.join(root, "args")
args = []
if os.path.exists(args_path):
    raw = open(args_path, "rb").read().split(b"\0")
    args = [item.decode() for item in raw if item]
headers = []
prev = ""
for arg in args:
    if prev == "-H":
        headers.append(arg)
    prev = arg
key = os.environ.get("FACTORY_INFERENCE_OPENAI_API_KEY", "")
extra = os.environ.get("FACTORY_INFERENCE_OPENAI_COMPATIBLE_EXTRA_HEADERS", "")
secrets = [key]
for line in extra.split("\n"):
    if ":" in line:
        secrets.append(line.split(":", 1)[1].strip())
        value = secrets[-1]
        if value.lower().startswith("bearer "):
            secrets.append(value[7:].strip())
secrets = [item for item in secrets if len(item) >= 8]

def leaked():
    blob = out + "\n" + err
    return any(item in blob for item in secrets)

if leaked():
    sys.stderr.write("probe output contains a secret\n")
    raise SystemExit(1)

if mode == "reject":
    if code == 0:
        sys.stderr.write("malformed headers were accepted\n")
        raise SystemExit(1)
    if os.path.exists(args_path):
        sys.stderr.write("curl ran for malformed headers\n")
        raise SystemExit(1)
    if "CR or LF inside a header value" not in err and "is malformed" not in err:
        sys.stderr.write("missing header parse error\n")
        raise SystemExit(1)
    raise SystemExit(0)

if code != 0:
    sys.stderr.write("catalog probe failed\n")
    raise SystemExit(1)
auth = [item for item in headers if item.lower().startswith("authorization:")]
if mode == "auth":
    want = "Authorization: Bearer " + key
    if auth != [want]:
        sys.stderr.write("authorization header was not sent\n")
        raise SystemExit(1)
    if len(headers) != 1:
        sys.stderr.write("unexpected extra header\n")
        raise SystemExit(1)
elif mode == "override":
    replacement = ""
    for line in extra.split("\n"):
        if ":" not in line:
            continue
        name, value = line.split(":", 1)
        if name.strip().lower() == "authorization":
            replacement = value.strip()
    sent = auth[0].split(":", 1)[1].strip() if len(auth) == 1 else ""
    if len(auth) != 1 or replacement == "" or sent != replacement or (key and key in sent):
        sys.stderr.write("extra authorization did not replace the api key\n")
        raise SystemExit(1)
    if key and any(key in item for item in headers):
        sys.stderr.write("api key still present after override\n")
        raise SystemExit(1)
elif mode == "auth-and-extra":
    want = "Authorization: Bearer " + key
    if auth != [want]:
        sys.stderr.write("authorization header was not sent\n")
        raise SystemExit(1)
    if not any(item.lower().startswith("x-litellm-api-key:") for item in headers):
        sys.stderr.write("extra header was not sent\n")
        raise SystemExit(1)
elif mode == "no-auth":
    if auth or headers:
        sys.stderr.write("empty api key sent a header\n")
        raise SystemExit(1)
else:
    sys.stderr.write("unknown probe mode\n")
    raise SystemExit(1)
PY
  local py=$?
  rm -rf "$dir"
  return "$py"
}

case_catalog_auth_live() {
  (
    MODE=live
    FACTORY_INFERENCE_OPENAI_API_KEY='sk-planted-api-key-value'
    unset FACTORY_INFERENCE_OPENAI_COMPATIBLE_EXTRA_HEADERS
    unset M1_OPENAI_EXTRA_HEADERS
    apply_live_inference_env
    probe_with_stub auth
  )
}

case_catalog_auth_live_ci() {
  (
    MODE=live-ci
    M1_OPENAI_BASE_URL='http://127.0.0.1:9/v1'
    M1_OPENAI_API_KEY='sk-planted-api-key-value'
    unset M1_OPENAI_EXTRA_HEADERS
    unset FACTORY_INFERENCE_OPENAI_API_KEY
    unset FACTORY_INFERENCE_OPENAI_COMPATIBLE_EXTRA_HEADERS
    apply_live_inference_env
    probe_with_stub auth
  )
}

case_catalog_auth_override() {
  (
    FACTORY_INFERENCE_OPENAI_API_KEY='sk-planted-api-key-value'
    FACTORY_INFERENCE_OPENAI_COMPATIBLE_EXTRA_HEADERS='authorization: Bearer oauth-planted-token-value'
    probe_with_stub override
  )
}

case_catalog_extra_and_auth() {
  (
    FACTORY_INFERENCE_OPENAI_API_KEY='sk-planted-api-key-value'
    FACTORY_INFERENCE_OPENAI_COMPATIBLE_EXTRA_HEADERS='x-litellm-api-key: Bearer sk-planted-extra-header-value'
    probe_with_stub auth-and-extra
  )
}

case_catalog_omits_empty_auth() {
  (
    FACTORY_INFERENCE_OPENAI_API_KEY=''
    unset FACTORY_INFERENCE_OPENAI_COMPATIBLE_EXTRA_HEADERS
    probe_with_stub no-auth
  )
}

case_catalog_rejects_header_cr() {
  (
    FACTORY_INFERENCE_OPENAI_API_KEY='sk-planted-api-key-value'
    FACTORY_INFERENCE_OPENAI_COMPATIBLE_EXTRA_HEADERS=$'x-litellm-api-key: Bearer sk-planted-extra-header-value\r'
    probe_with_stub reject
  )
}

case_compose_extra_headers_passthrough() {
  local out defs gitdir err code=0
  defs="$(mktemp -d)"
  gitdir="$(mktemp -d)"
  out="$ROOT/spike/m5/.work/compose-headers.yml"
  err="$ROOT/spike/m5/.work/compose-headers.err"
  mkdir -p "$ROOT/spike/m5/.work" "$defs" "$gitdir"
  M5_DEFS_HOST="$defs" \
    M5_GIT_HOST="$gitdir" \
    FACTORY_DEFINITION_HOST_PATH="$defs" \
    FACTORY_DEFINITION_MOUNT=/definitions \
    FACTORY_INFERENCE_OPENAI_COMPATIBLE_EXTRA_HEADERS=$'x-litellm-api-key: Bearer sk-planted-extra-header-value\nX-Request-Tag: alpha' \
    docker compose -p factory-m5 \
    -f "$ROOT/deploy/docker-compose.yml" \
    -f "$ROOT/spike/m5/compose.yml" \
    --profile m5 config >"$out" 2>"$err" || code=$?
  if [[ "$code" != 0 ]]; then
    if grep -F 'sk-planted-extra-header-value' "$err" >/dev/null 2>&1; then
      printf 'compose stderr contains a header value\n' >&2
    else
      printf 'compose config failed\n' >&2
    fi
    rm -f "$out" "$err"
    return 1
  fi
  python3 - "$out" "$err" <<'PY' || code=$?
import sys
text = open(sys.argv[1], encoding="utf-8").read()
err = open(sys.argv[2], encoding="utf-8").read()
planted = "sk-planted-extra-header-value"
if planted in err:
    sys.stderr.write("compose stderr contains a header value\n")
    raise SystemExit(1)
section = text.split("factoryd:", 1)[1].split("\n  factoryd-git:", 1)[0]
if "x-litellm-api-key:" not in section or "X-Request-Tag:" not in section or planted not in section:
    sys.stderr.write("extra headers were not passed to factoryd\n")
    raise SystemExit(1)
PY
  rm -f "$out" "$err"
  return "$code"
}

expect_fail missing-live-url "missing FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL" case_missing_live_url
expect_fail missing-live-model "missing M5_LIVE_MODEL" case_missing_live_model
expect_fail missing-live-ci-url "missing M1_OPENAI_BASE_URL" case_missing_live_ci_url
expect_fail missing-live-ci-key "missing M1_OPENAI_API_KEY" case_missing_live_ci_key
expect_fail missing-live-ci-model "missing M5_LIVE_MODEL" case_missing_live_ci_model
expect_fail catalog-rejects "model id missing-model is not in the catalog" case_catalog_rejects
run_case catalog-accepts case_catalog_accepts
run_case rewrite-copy-only case_rewrite_copy_only
run_case env-file case_env_file
expect_fail headed-without-node "missing Node" case_headed_without_node
run_case question-script case_question_script
run_case probe-desktop-host case_probe_rewrites_desktop_host
run_case task-host-linux case_task_host_linux
run_case task-host-orbstack case_task_host_orbstack
run_case task-host-override case_task_host_override
run_case no-gnu-flags case_no_gnu_flags
run_case build-flags case_build_flags
run_case spec-static case_spec_static
run_case failure-routes-on-building case_failure_routes_on_building
run_case spec-drives-step8 case_spec_drives_step8
run_case wipe-refuses-outside case_wipe_refuses_outside
run_case wipe-refuses-symlink case_wipe_refuses_symlink
run_case wipe-refuses-empty case_wipe_refuses_empty
run_case wipe-refuses-work-and-root case_wipe_refuses_work_and_root
run_case wipe-locked-inside case_wipe_locked_inside_work
run_case keep-up-skips-defs case_keep_up_skips_defs_wipe
run_case prepare-locked-tree case_prepare_definition_locked_tree
run_case compose-config case_compose_config
run_case compose-extra-headers case_compose_extra_headers_passthrough
run_case catalog-auth-live case_catalog_auth_live
run_case catalog-auth-live-ci case_catalog_auth_live_ci
run_case catalog-auth-override case_catalog_auth_override
run_case catalog-extra-and-auth case_catalog_extra_and_auth
run_case catalog-omits-empty-auth case_catalog_omits_empty_auth
run_case catalog-rejects-header-cr case_catalog_rejects_header_cr
run_case prebound-loopback case_prebound_loopback_detected
expect_fail router-port-invalid "M5_ROUTER_PORT must be a TCP port" case_router_port_invalid
run_case router-port-picked case_router_port_picked
run_case router-port-configured case_router_port_configured
run_case router-identity case_router_identity_matches
run_case foreign-models case_foreign_models_rejected
run_case router-port-static case_router_port_static
run_case container-gateway-linux case_container_gateway_linux
run_case container-gateway-orbstack case_container_gateway_orbstack
run_case container-gateway-desktop case_container_gateway_desktop
run_case stale-offline-behind case_stale_offline_behind
run_case stale-live-behind case_stale_live_behind
run_case stale-live-ci-skips case_stale_live_ci_skips
run_case stale-actions-offline-skips case_stale_actions_offline_skips
run_case stale-actions-live-skips case_stale_actions_live_skips
run_case stale-fetch-failure case_stale_fetch_failure
run_case stale-current case_stale_current
run_case stale-ahead case_stale_ahead
run_case stale-not-checkout case_stale_not_checkout
run_case stale-no-origin case_stale_no_origin
run_case playwright-failure-summary case_playwright_failure_summary
run_case spec-outcome-modes case_spec_outcome_modes

if [[ "$fails" -ne 0 ]]; then
  printf '%s case(s) failed\n' "$fails" >&2
  exit 1
fi
printf 'spike/m5/run_test.sh PASS\n'
