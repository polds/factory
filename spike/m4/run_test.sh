#!/usr/bin/env bash
# Unit tests for spike/m4/run.sh. No docker stack.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
SCRIPT="$HERE/run.sh"

pass=0
fail=0

run_case() {
  local name="$1" rc
  shift
  set +e
  "$@"
  rc=$?
  set -e
  if [[ "$rc" -eq 0 ]]; then
    echo "PASS ${name}"
    pass=$((pass + 1))
  else
    echo "FAIL ${name}"
    fail=$((fail + 1))
  fi
}

assert_eq() {
  local got="$1" want="$2"
  if [[ "$got" != "$want" ]]; then
    echo "got ${got}" >&2
    echo "want ${want}" >&2
    return 1
  fi
}

case_hourly_disabled() {
  local text=""
  text="$(cat "$ROOT/deploy/m4-factory/automations/hourly/automation.md")"
  [[ "$text" == *"enabled: false"* ]] || return 1
  [[ "$text" != *"enabled: true"* ]] || return 1
}

case_sign_github() {
  local tmp="" got=""
  tmp="$(mktemp)"
  printf 'hello\n' >"$tmp"
  got="$(sign_github "m4-test-secret" "$tmp")"
  rm -f "$tmp"
  assert_eq "$got" "sha256=a30c147c58e38aeb028e4bf5ddf55f6dc30efb2735f1f33b0f1f7d8b7a2c5c4c"
}

case_sign_slack() {
  local tmp="" got=""
  tmp="$(mktemp)"
  printf 'hello\n' >"$tmp"
  got="$(sign_slack "m4-test-secret" "1700000000" "$tmp")"
  rm -f "$tmp"
  assert_eq "$got" "v0=96f715c2e6d77b1074ad260d8cd3d1d7cd871ec6f70ce85a0a70f31ef0538971"
}

case_host_desktop() {
  assert_eq "$(host_for_tasks "Docker Desktop" "172.17.0.1")" "host.docker.internal"
}

case_host_linux() {
  assert_eq "$(host_for_tasks "Ubuntu 24.04.1 LTS" "172.17.0.1")" "172.17.0.1"
}

case_task_lines() {
  local got=""
  got="$(printf '%s\n' 'task_01M4Z4NA6FBSV8SZHVTA2P8NAT stage=building status=open title=Hello' 'noise' | filter_task_lines)"
  assert_eq "$got" "task_01M4Z4NA6FBSV8SZHVTA2P8NAT"
}

case_building_script() {
  local tmp="" first=""
  tmp="$(mktemp)"
  write_building_script "$tmp" "http://172.17.0.1:18080/_spike/commits"
  first="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))[0]["final"])' "$tmp")"
  rm -f "$tmp"
  assert_eq "$first" "intake: scope recorded"
}

case_live_key_copy() {
  local dir="" src="" dest="" src_mode="" dest_mode="" saved=""
  dir="$(mktemp -d)"
  src="$dir/source.pem"
  dest="$dir/live/github-app.pem"
  printf 'live-key-bytes\n' >"$src"
  chmod 600 "$src"
  stage_live_app_key "$src" "$dest" || return 1
  cmp -s "$src" "$dest" || return 1
  src_mode="$(file_mode "$src")"
  dest_mode="$(file_mode "$dest")"
  assert_eq "$src_mode" "600" || return 1
  assert_eq "$dest_mode" "644" || return 1
  assert_eq "$(live_app_key_source)" "$HERE/secrets/github-app.pem" || return 1
  saved="${FACTORY_GITHUB_APP_PRIVATE_KEY_FILE:-}"
  FACTORY_GITHUB_APP_PRIVATE_KEY_FILE="$src"
  assert_eq "$(live_app_key_source)" "$src" || return 1
  if [[ -n "$saved" ]]; then
    FACTORY_GITHUB_APP_PRIVATE_KEY_FILE="$saved"
  else
    unset FACTORY_GITHUB_APP_PRIVATE_KEY_FILE
  fi
  git -C "$ROOT" check-ignore -q -- spike/m4/secrets/github-app.pem || return 1
  git -C "$ROOT" check-ignore -q -- spike/m4/.work/live/github-app.pem || return 1
  rm -rf "$dir"
}

case_live_model_url() {
  local text=""
  [[ "$LIVE_MODEL_URL" == "http://host.docker.internal:8317/v1" ]] || return 1
  text="$(print_live_checklist)"
  [[ "$text" == *"Point the model server at ${LIVE_MODEL_URL}"* ]] || return 1
  [[ "$text" == *"spike/m4/secrets/github-app.pem"* ]] || return 1
  [[ "$text" == *"spike/m4/.work/live/github-app.pem"* ]] || return 1
  [[ "$text" != *"deploy/secrets/github-app.pem"* ]] || return 1
  [[ "$text" != *"host.docker.internal:8317."* ]] || return 1
}

case_no_gnu_flags() {
  if grep -n -E '(grep[[:space:]]+-P|sed[[:space:]]+-i|[[:space:]]mapfile[[:space:]]|^mapfile[[:space:]]|(^|[[:space:]])timeout[[:space:]])' \
    "$SCRIPT" "$HERE/run_test.sh"; then
    echo "gnu-only flag present" >&2
    return 1
  fi
}

source_run() {
  # shellcheck source=spike/m4/run.sh
  source "$SCRIPT"
}

source_run

run_case hourly_disabled case_hourly_disabled
run_case sign_github case_sign_github
run_case sign_slack case_sign_slack
run_case host_desktop case_host_desktop
run_case host_linux case_host_linux
run_case task_lines case_task_lines
run_case building_script case_building_script
run_case live_key_copy case_live_key_copy
run_case live_model_url case_live_model_url
run_case no_gnu_flags case_no_gnu_flags

echo
echo "${pass} passed, ${fail} failed"
if [[ "$fail" -ne 0 ]]; then
  exit 1
fi
