#!/usr/bin/env bash
# Unit tests for spike/m4/run.sh. No docker stack.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
SCRIPT="$HERE/run.sh"

# shellcheck source=spike/m4/run.sh
source "$SCRIPT"

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

case_no_gnu_flags() {
  if grep -n -E '(grep[[:space:]]+-P|sed[[:space:]]+-i|[[:space:]]mapfile[[:space:]]|^mapfile[[:space:]]|(^|[[:space:]])timeout[[:space:]])' \
    "$SCRIPT" "$HERE/run_test.sh"; then
    echo "gnu-only flag present" >&2
    return 1
  fi
}

run_case hourly_disabled case_hourly_disabled
run_case sign_github case_sign_github
run_case sign_slack case_sign_slack
run_case host_desktop case_host_desktop
run_case host_linux case_host_linux
run_case no_gnu_flags case_no_gnu_flags

echo
echo "${pass} passed, ${fail} failed"
if [[ "$fail" -ne 0 ]]; then
  exit 1
fi
