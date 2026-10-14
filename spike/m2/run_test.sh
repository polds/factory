#!/usr/bin/env bash
# Unit tests for spike/m2/run.sh. No docker stack.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
SCRIPT="$HERE/run.sh"

pass=0
fail=0

run_case() {
  local name="$1" rc
  shift
  # Not an if/&& context: a test subshell must still honor set -e.
  # The runner itself stays alive when a test returns non-zero.
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
  local got="$1" want="$2" label="$3"
  if [[ "$got" != "$want" ]]; then
    echo "${label}: got $(printf '%q' "$got") want $(printf '%q' "$want")" >&2
    return 1
  fi
}

source_run() {
  # shellcheck source=spike/m2/run.sh
  source "$SCRIPT"
}

test_platform_map() {
  local got
  got="$(
    source_run
    normalize_docker_platform linux/aarch64
  )"
  assert_eq "$got" linux/arm64 "aarch64" || return 1
  got="$(
    source_run
    normalize_docker_platform linux/arm64
  )"
  assert_eq "$got" linux/arm64 "arm64" || return 1
  got="$(
    source_run
    normalize_docker_platform linux/x86_64
  )"
  assert_eq "$got" linux/amd64 "x86_64" || return 1
  got="$(
    source_run
    normalize_docker_platform linux/amd64
  )"
  assert_eq "$got" linux/amd64 "amd64" || return 1
  if normalize_docker_platform darwin/arm64 >/dev/null 2>&1; then
    echo "darwin/arm64 was accepted" >&2
    return 1
  fi
  got="$(
    source_run
    goarch_for_platform linux/arm64
  )"
  assert_eq "$got" arm64 "goarch arm64" || return 1
  got="$(
    source_run
    goarch_for_platform linux/amd64
  )"
  assert_eq "$got" amd64 "goarch amd64" || return 1
  got="$(
    source_run
    uname_for_platform linux/arm64
  )"
  assert_eq "$got" aarch64 "uname arm" || return 1
  got="$(
    source_run
    uname_for_platform linux/amd64
  )"
  assert_eq "$got" x86_64 "uname amd" || return 1
}

test_env_file_aliases_and_precedence() {
  local dir file
  dir="$(mktemp -d)"
  file="$dir/env"
  cat >"$file" <<'EOF'
# comment
export FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL=http://from-file.example/v1
FACTORY_INFERENCE_OPENAI_API_KEY="file-key"
FACTORY_SECRET_GH_TOKEN='file-token'
M2_MODEL=file-model
OPENAI_BASE_URL=
GH_TOKEN=
EOF
  # shell wins, empty assignments do not clobber, aliases fill the shell names
  local out
  out="$(
    source_run
    unset M2_MODEL OPENAI_API_KEY GH_TOKEN FACTORY_SECRET_GH_TOKEN \
      FACTORY_INFERENCE_OPENAI_API_KEY FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL
    OPENAI_BASE_URL="http://from-shell.example/v1"
    export OPENAI_BASE_URL
    load_env_file "$file"
    apply_live_env_aliases
    if [[ "$OPENAI_BASE_URL" != "http://from-shell.example/v1" ]]; then
      echo "shell base url lost" >&2
      exit 1
    fi
    if [[ "$OPENAI_API_KEY" != "file-key" ]]; then
      echo "api key alias missing" >&2
      exit 1
    fi
    if [[ "$GH_TOKEN" != "file-token" ]]; then
      echo "gh token alias missing" >&2
      exit 1
    fi
    if [[ "$M2_MODEL" != "file-model" ]]; then
      echo "model missing" >&2
      exit 1
    fi
    echo ok
  )"
  assert_eq "$out" ok "env aliases" || return 1
}

test_env_file_does_not_execute_or_echo() {
  local dir file err
  dir="$(mktemp -d)"
  file="$dir/env"
  # The value is a literal command substitution. It must not be expanded.
  # shellcheck disable=SC2016
  printf '%s\n' 'OPENAI_API_KEY=$(touch '"$dir"'/pwned)' >"$file"
  (
    source_run
    unset OPENAI_API_KEY
    load_env_file "$file"
    # shellcheck disable=SC2016
    [[ "$OPENAI_API_KEY" == '$(touch '"$dir"'/pwned)' ]]
  ) || return 1
  if [[ -e "$dir/pwned" ]]; then
    echo "env file executed a command" >&2
    return 1
  fi
  printf '%s\n' 'not a valid line m2-test-secret-7f3c9a2e' >"$file"
  err="$(
    source_run
    load_env_file "$file" 2>&1 || true
  )"
  if [[ "$err" != *"invalid line"* ]]; then
    echo "invalid line was not rejected" >&2
    return 1
  fi
  if printf '%s\n' "$err" | grep -F -q -e "m2-test-secret-7f3c9a2e"; then
    echo "invalid line echoed its contents" >&2
    return 1
  fi
}

test_env_crlf() {
  local dir file
  dir="$(mktemp -d)"
  file="$dir/env"
  printf 'M2_MODEL=crlf-model\r\n' >"$file"
  (
    source_run
    unset M2_MODEL
    load_env_file "$file"
    [[ "$M2_MODEL" == "crlf-model" ]]
  ) || return 1
}

test_url_log_host_strips_userinfo() {
  local host
  host="$(
    source_run
    url_log_host 'http://user:m2-test-secret-7f3c9a2e@example.test:8443/v1'
  )"
  assert_eq "$host" "example.test:8443" "log host" || return 1
  if printf '%s\n' "$host" | grep -F -q -e "m2-test-secret-7f3c9a2e"; then
    echo "url log included a secret" >&2
    return 1
  fi
}

test_unix_ns_without_gnu_date() {
  local dir ts
  dir="$(mktemp -d)"
  cat >"$dir/date" <<'EOF'
#!/bin/sh
if [ "$1" = "+%s%N" ]; then
  echo "date: illegal time format" >&2
  exit 1
fi
echo "unexpected date args: $*" >&2
exit 1
EOF
  chmod +x "$dir/date"
  ts="$(
    source_run
    unset M2_DATE_NS_MODE
    PATH="$dir:$PATH"
    unix_ns
  )"
  if [[ ! "$ts" =~ ^[0-9]{19}$ ]]; then
    echo "fallback timestamp is not 19 digits" >&2
    return 1
  fi
}

test_watch_nonzero_prints_reason_and_err() {
  local work out rc
  work="$(mktemp -d)"
  cat >"$work/fake" <<'EOF'
#!/bin/sh
echo run_test123
echo 'terminal status=failed summary=container exited with non-zero status: 126' >&2
exit 1
EOF
  chmod +x "$work/fake"
  set +e
  out="$(
    set -euo pipefail
    source_run
    WORK="$work"
    COMPOSE_STARTED=0
    SECRETS=()
    LEAK_ANNOUNCED=0
    mkdir -p "$WORK"
    trap 'on_err $? $LINENO' ERR
    RUN_CODE=0
    run_capture RUN_CODE timestamped_run "$WORK/watch-run.ts" \
      "$work/fake" run --prompt p --factory m2 --agent implementer --title t --watch \
      2>"$WORK/factoryctl-run.err"
    if [[ "$RUN_CODE" -ne 0 ]]; then
      fail "factoryctl run --watch exited ${RUN_CODE}"
    fi
    echo UNREACHED
  )"
  rc=$?
  if [[ "$rc" -ne 1 ]]; then
    echo "watch failure exit ${rc}, want 1" >&2
    return 1
  fi
  if [[ "$out" == *UNREACHED* ]]; then
    echo "watch failure continued after fail" >&2
    return 1
  fi
  if [[ "$out" != *"M2 FAIL — factoryctl run --watch exited 1"* ]]; then
    echo "missing M2 FAIL reason" >&2
    printf '%s\n' "$out" >&2
    return 1
  fi
  if [[ "$out" != *"container exited with non-zero status: 126"* ]]; then
    echo "stderr tail missing from failure output" >&2
    return 1
  fi
}

test_secret_in_err_is_withheld() {
  local work out secret
  work="$(mktemp -d)"
  secret="m2-test-secret-7f3c9a2e"
  set +e
  out="$(
    set -euo pipefail
    source_run
    WORK="$work"
    COMPOSE_STARTED=0
    SECRETS=("$secret")
    LEAK_ANNOUNCED=0
    mkdir -p "$WORK"
    printf 'leak %s\n' "$secret" >"$WORK/factoryctl-run.err"
    fail "factoryctl run --watch exited 1"
  )"
  if [[ "$out" != *"M2 FAIL — a secret value appeared"* ]]; then
    echo "secret failure did not announce M2 FAIL" >&2
    return 1
  fi
  if printf '%s\n' "$out" | grep -F -q -e "$secret"; then
    echo "secret appeared in failure output" >&2
    return 1
  fi
  if [[ -f "$work/factoryctl-run.err" ]]; then
    echo "err file was kept after a leak" >&2
    return 1
  fi
}

test_die_prints_m2_fail_without_logs() {
  local out
  set +e
  out="$(
    set -euo pipefail
    source_run
    WORK="$(mktemp -d)"
    COMPOSE_STARTED=0
    SECRETS=()
    LEAK_ANNOUNCED=0
    die "MODE must be offline or live"
  )"
  if [[ "$out" != *"M2 FAIL — MODE must be offline or live"* ]]; then
    echo "die did not print M2 FAIL" >&2
    printf '%s\n' "$out" >&2
    return 1
  fi
  if [[ "$out" != *"(no factoryctl-run.err)"* ]]; then
    echo "die did not mention the missing err file" >&2
    return 1
  fi
}

test_run_capture_keeps_nonzero() {
  local out rc
  set +e
  out="$(
    set -euo pipefail
    source_run
    WORK="$(mktemp -d)"
    COMPOSE_STARTED=0
    SECRETS=()
    LEAK_ANNOUNCED=0
    trap 'on_err $? $LINENO' ERR
    code=0
    run_capture code false
    printf 'code=%s\n' "$code"
    (exit 2) &
    pid=$!
    run_capture code wait "$pid"
    printf 'wait=%s\n' "$code"
  )"
  rc=$?
  if [[ "$rc" -ne 0 ]]; then
    echo "run_capture aborted the shell" >&2
    printf '%s\n' "$out" >&2
    return 1
  fi
  if [[ "$out" == *"M2 FAIL"* ]]; then
    echo "run_capture treated a captured status as a failure" >&2
    return 1
  fi
  if [[ "$out" != *"code=1"* || "$out" != *"wait=2"* ]]; then
    echo "run_capture lost the status" >&2
    printf '%s\n' "$out" >&2
    return 1
  fi
}

test_elf_arch() {
  if [[ "${M2_REQUIRE_GO:-}" != 1 ]] && ! command -v go >/dev/null 2>&1; then
    echo "skip elf (go not on PATH)"
    return 0
  fi
  command -v go >/dev/null 2>&1 || {
    echo "go is required" >&2
    return 1
  }
  local dir
  dir="$(mktemp -d)"
  (
    cd "$ROOT"
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o "$dir/linux-arm64" ./cmd/factory-harness
    CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o "$dir/darwin-arm64" ./cmd/factory-harness
  )
  (
    source_run
    assert_linux_elf "$dir/linux-arm64" arm64
  ) || return 1
  if (
    source_run
    assert_linux_elf "$dir/darwin-arm64" arm64
  ) >/dev/null 2>&1; then
    echo "darwin binary was accepted as linux/arm64" >&2
    return 1
  fi
  if (
    source_run
    assert_linux_elf "$dir/linux-arm64" amd64
  ) >/dev/null 2>&1; then
    echo "arm64 ELF was accepted as amd64" >&2
    return 1
  fi
}

test_loopback_task_host() {
  source_run
  local got
  got="$(loopback_task_host "Docker Desktop" no 172.17.0.1)"
  assert_eq "$got" host.docker.internal "desktop os" || return 1
  got="$(loopback_task_host "Docker Desktop 4.41.2" no 192.168.215.1)"
  assert_eq "$got" host.docker.internal "desktop version" || return 1
  got="$(loopback_task_host "Ubuntu 24.04.3 LTS" no 172.17.0.1)"
  assert_eq "$got" 172.17.0.1 "linux gateway" || return 1
  got="$(loopback_task_host "Ubuntu 24.04.3 LTS" yes 172.17.0.1)"
  assert_eq "$got" host.docker.internal "resolved name" || return 1
  got="$(loopback_task_host "linux" no 192.168.215.1)"
  assert_eq "$got" 192.168.215.1 "desktop-looking gateway stays an address" || return 1
}

test_task_base_url_rewrites_loopback() {
  source_run
  local got
  got="$(task_base_url 'http://127.0.0.1:8317/chat/completions' host.docker.internal)"
  assert_eq "$got" "http://host.docker.internal:8317/chat/completions" "ipv4 loopback" || return 1
  got="$(task_base_url 'http://localhost:8317/v1' 172.17.0.1)"
  assert_eq "$got" "http://172.17.0.1:8317/v1" "localhost gateway" || return 1
  got="$(task_base_url 'http://[::1]:8317/v1' host.docker.internal)"
  assert_eq "$got" "http://host.docker.internal:8317/v1" "ipv6 loopback" || return 1
  got="$(task_base_url 'https://api.example.test/v1' host.docker.internal)"
  assert_eq "$got" "https://api.example.test/v1" "other host" || return 1
  got="$(task_base_url 'http://user:local-only@127.0.0.1:8317/v1' host.docker.internal)"
  assert_eq "$got" "http://user:local-only@host.docker.internal:8317/v1" "userinfo" || return 1
}

test_loopback_reachable() {
  source_run
  loopback_reachable host.docker.internal yes || return 1
  loopback_reachable 172.17.0.1 no || return 1
  if loopback_reachable host.docker.internal no; then
    echo "unresolved host.docker.internal was accepted" >&2
    return 1
  fi
}

test_host_name_resolves() {
  source_run
  if ! host_name_resolves localhost; then
    echo "localhost did not resolve" >&2
    return 1
  fi
  if host_name_resolves no-such-host.invalid; then
    echo "missing name resolved" >&2
    return 1
  fi
}

test_offline_secrets_replaced_before_children() {
  local out
  out="$(
    source_run
    SECRETS=()
    FACTORY_SECRET_GH_TOKEN='live-gh-token-7f3c9a'
    FACTORY_INFERENCE_OPENAI_API_KEY='live-api-key-7f3c9a'
    FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL='http://127.0.0.1:8317/v1'
    GH_TOKEN='live-gh-token-7f3c9a'
    OPENAI_API_KEY='live-api-key-7f3c9a'
    export FACTORY_SECRET_GH_TOKEN FACTORY_INFERENCE_OPENAI_API_KEY \
      FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL GH_TOKEN OPENAI_API_KEY
    apply_offline_secrets 172.17.0.1
    [[ "$FACTORY_SECRET_GH_TOKEN" == "$OFFLINE_GH" ]] || {
      echo "github token was not replaced" >&2
      exit 1
    }
    [[ "$FACTORY_INFERENCE_OPENAI_API_KEY" == "$OFFLINE_DUMMY_KEY" ]] || {
      echo "api key was not replaced" >&2
      exit 1
    }
    [[ "$FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL" == "http://172.17.0.1:9090" ]] || {
      echo "base url was not replaced" >&2
      exit 1
    }
    [[ -z "${GH_TOKEN:-}" && -z "${OPENAI_API_KEY:-}" ]] || {
      echo "shell aliases were not cleared" >&2
      exit 1
    }
    child="$(bash -c 'printf %s "${FACTORY_SECRET_GH_TOKEN:-}"')"
    [[ "$child" == "$OFFLINE_GH" ]] || {
      echo "child did not see the dummy token" >&2
      exit 1
    }
    dir="$(mktemp -d)"
    printf '%s\n' 'live-gh-token-7f3c9a' >"$dir/proof"
    if scan_files "$dir/proof"; then
      echo "replaced token was not redacted" >&2
      exit 1
    fi
    printf '%s\n' 'live-api-key-7f3c9a' >"$dir/proof"
    if scan_files "$dir/proof"; then
      echo "replaced api key was not redacted" >&2
      exit 1
    fi
  )"
  if printf '%s\n' "$out" | grep -F -q -e 'live-gh-token-7f3c9a' -e 'live-api-key-7f3c9a'; then
    echo "offline secret replacement printed a live value" >&2
    return 1
  fi
}

test_reset_stale_host_logs() {
  local work
  work="$(mktemp -d)"
  (
    source_run
    WORK="$work"
    printf 'old offline model\n' >"$WORK/model-stub.log"
    printf 'old offline git\n' >"$WORK/git-daemon.log"
    reset_stale_host_logs
    if [[ -e "$WORK/model-stub.log" || -e "$WORK/git-daemon.log" ]]; then
      echo "stale host logs were kept" >&2
      exit 1
    fi
  ) || return 1
}

test_harness_contract_exit() {
  source_run
  harness_contract_exit 0 || return 1
  harness_contract_exit 20 || return 1
  harness_contract_exit 30 || return 1
  if harness_contract_exit 126; then
    echo "exit 126 was treated as a harness contract code" >&2
    return 1
  fi
  if harness_contract_exit 2; then
    echo "exit 2 was treated as a harness contract code" >&2
    return 1
  fi
}

run_case platform_map test_platform_map
run_case loopback_task_host test_loopback_task_host
run_case task_base_url test_task_base_url_rewrites_loopback
run_case loopback_reachable test_loopback_reachable
run_case host_name_resolves test_host_name_resolves
run_case offline_secrets test_offline_secrets_replaced_before_children
run_case reset_stale_logs test_reset_stale_host_logs
run_case harness_contract_exit test_harness_contract_exit
run_case env_aliases test_env_file_aliases_and_precedence
run_case env_no_exec test_env_file_does_not_execute_or_echo
run_case env_crlf test_env_crlf
run_case url_log_host test_url_log_host_strips_userinfo
run_case unix_ns test_unix_ns_without_gnu_date
run_case watch_fail test_watch_nonzero_prints_reason_and_err
run_case secret_withheld test_secret_in_err_is_withheld
run_case die_message test_die_prints_m2_fail_without_logs
run_case run_capture test_run_capture_keeps_nonzero
run_case elf_arch test_elf_arch

echo "${pass} passed, ${fail} failed"
[[ "$fail" -eq 0 ]]
