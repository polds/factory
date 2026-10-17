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
  grep -F 'wait for the container to stop' "$impl" >/dev/null
  if grep -F 'failed_by_agent' "$triage" >/dev/null; then
    echo "triage prompt owns the agent failure" >&2
    return 1
  fi
}

case_prepare_definition_locked_tree() {
  local tree=""
  tree="$(mktemp -d)"
  set +e
  (
    DEFS="$tree"
    MODE=offline
    mkdir -p "$DEFS/m5-factory"
    docker run --rm --user 0 -v "$DEFS:/defs" golang:1.26-alpine \
      sh -c 'mkdir -p /defs/m5-factory/kept && echo locked >/defs/m5-factory/kept/file && chown -R 65532:65532 /defs/m5-factory && chmod 700 /defs/m5-factory'
    if rm -rf "$DEFS/m5-factory" 2>/dev/null; then
      echo "host removed the mode-700 tree; fixture is not locked" >&2
      exit 1
    fi
    prepare_definition
    if [[ ! -f "$DEFS/m5-factory/factory.yaml" ]]; then
      echo "first prepare did not replace the locked tree" >&2
      exit 1
    fi
    docker run --rm --user 0 -v "$DEFS:/defs" golang:1.26-alpine \
      sh -c 'mkdir -p /defs/m5-factory/kept && echo locked >/defs/m5-factory/kept/file && chown -R 65532:65532 /defs/m5-factory && chmod 700 /defs/m5-factory'
    prepare_definition
    if [[ ! -f "$DEFS/m5-factory/factory.yaml" ]]; then
      echo "second prepare did not replace the locked tree" >&2
      exit 1
    fi
  )
  local code=$?
  set -e
  if ! rm -rf "$tree" 2>/dev/null; then
    docker run --rm --user 0 -v "$tree:/wipe" golang:1.26-alpine \
      sh -c 'find /wipe -mindepth 1 -depth -exec rm -rf {} +'
    rm -rf "$tree"
  fi
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
run_case prepare-locked-tree case_prepare_definition_locked_tree
run_case compose-config case_compose_config

if [[ "$fails" -ne 0 ]]; then
  printf '%s case(s) failed\n' "$fails" >&2
  exit 1
fi
printf 'spike/m5/run_test.sh PASS\n'
