#!/usr/bin/env bash
# Tests for spike/lib/cleanup.sh. No compose stack. Target under about 20s
# so the CI shell job can run this from spike/m4/run_test.sh.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
LIB="$ROOT/spike/lib/cleanup.sh"
PASS=0
FAIL=0

say() { printf '%s\n' "$*"; }

# launch_driver starts the driver in its own process group so an INT to
# that group is what a terminal Ctrl-C sends, and it does not hit this test.
launch_driver() {
  local monitor=0
  case $- in
    *m*) monitor=1 ;;
  esac
  set -m
  "$@" &
  LAUNCH_PID=$!
  if [[ "$monitor" -eq 0 ]]; then
    set +m
  fi
}

run_case() {
  local name="$1"
  shift
  local err
  err="$(mktemp)"
  if "$@" >"$err.out" 2>"$err"; then
    say "ok   ${name}"
    PASS=$((PASS + 1))
  else
    say "FAIL ${name}"
    cat "$err" >&2 || true
    FAIL=$((FAIL + 1))
  fi
  rm -f "$err" "$err.out"
}

# run_driver starts a small driver and returns its pid via the named file.
write_driver() {
  local path="$1" reg="$2" work="$3"
  cat >"$path" <<EOF
#!/usr/bin/env bash
set -euo pipefail
. $(printf '%q' "$LIB")
SPIKE_STOP_GRACE=1
SPIKE_REG=$(printf '%q' "$reg")
WORK=$(printf '%q' "$work")
spike_run_init t76
spike_install_trap
EOF
  cat >>"$path"
  chmod +x "$path"
}

case_term_ignoring() {
  local dir pid ec start
  dir="$(mktemp -d)"
  write_driver "$dir/driver.sh" "$dir/reg" "$dir/work" <<'EOF'
spike_spawn --class stack ignorer python3 -c '
import os, signal, time
signal.signal(signal.SIGTERM, signal.SIG_IGN)
if os.fork() == 0:
    os.execv("/bin/sleep", ["spike-marker-grand-TERM", "600"])
time.sleep(600)
'
echo READY
wait || true
EOF
  launch_driver "$dir/driver.sh" >"$dir/out" 2>"$dir/err"
  pid=$LAUNCH_PID
  start="$(date +%s)"
  for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
    if grep -q READY "$dir/out" 2>/dev/null; then
      break
    fi
    sleep 0.05
  done
  kill -TERM "$pid"
  set +e
  wait "$pid"
  ec=$?
  set -e
  local elapsed=$(($(date +%s) - start))
  if [[ "$ec" -ne 143 ]]; then
    echo "exit ${ec}, want 143" >&2
    return 1
  fi
  if [[ "$elapsed" -gt 4 ]]; then
    echo "took ${elapsed}s, want within grace+2" >&2
    return 1
  fi
  if pgrep -f '[s]pike-marker-grand-TERM' >/dev/null; then
    echo "grandchild survived" >&2
    return 1
  fi
  if pgrep -f 'signal.SIGTERM, signal.SIG_IG[N]' >/dev/null; then
    echo "child survived" >&2
    return 1
  fi
  if [[ -s "$dir/err" ]]; then
    echo "unexpected stderr" >&2
    cat "$dir/err" >&2
    return 1
  fi
  rm -rf "$dir"
}

case_int_ignored() {
  local dir pid ec
  dir="$(mktemp -d)"
  write_driver "$dir/driver.sh" "$dir/reg" "$dir/work" <<'EOF'
spike_spawn --class stack ignorer python3 -c '
import os, signal, time
signal.signal(signal.SIGINT, signal.SIG_IGN)
if os.fork() == 0:
    os.execv("/bin/sleep", ["spike-marker-grand-INT", "600"])
time.sleep(600)
'
echo READY
wait || true
EOF
  launch_driver "$dir/driver.sh" >"$dir/out" 2>"$dir/err"
  pid=$LAUNCH_PID
  local pgid
  for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
    if grep -q READY "$dir/out" 2>/dev/null; then
      break
    fi
    sleep 0.05
  done
  pgid="$(ps -o pgid= -p "$pid" | tr -d '[:space:]')"
  kill -INT -- -"$pgid"
  set +e
  wait "$pid"
  ec=$?
  set -e
  if [[ "$ec" -ne 130 ]]; then
    echo "exit ${ec}, want 130" >&2
    return 1
  fi
  if pgrep -f '[s]pike-marker-grand-INT' >/dev/null; then
    echo "SIGINT-ignored grandchild survived" >&2
    return 1
  fi
  rm -rf "$dir"
}

case_term_during_fg() {
  local dir pid ec start end
  dir="$(mktemp -d)"
  write_driver "$dir/driver.sh" "$dir/reg" "$dir/work" <<'EOF'
echo READY
spike_fg sleep 600
EOF
  launch_driver "$dir/driver.sh" >"$dir/out" 2>"$dir/err"
  pid=$LAUNCH_PID
  for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
    if grep -q READY "$dir/out" 2>/dev/null; then
      break
    fi
    sleep 0.05
  done
  start="$(date +%s.%N)"
  kill -TERM "$pid"
  set +e
  wait "$pid"
  ec=$?
  set -e
  end="$(date +%s.%N)"
  python3 -c 'import sys; elapsed=float(sys.argv[2])-float(sys.argv[1]); print("%.3f"%elapsed); raise SystemExit(0 if elapsed < 1.5 else 1)' "$start" "$end"
  if [[ "$ec" -ne 143 ]]; then
    echo "exit ${ec}, want 143" >&2
    return 1
  fi
  rm -rf "$dir"
}

case_exit_status() {
  local want="$1" dir pid ec
  dir="$(mktemp -d)"
  write_driver "$dir/driver.sh" "$dir/reg" "$dir/work" <<EOF
spike_spawn --class stack sleeper sleep 600
exit ${want}
EOF
  launch_driver "$dir/driver.sh" >"$dir/out" 2>"$dir/err"
  pid=$LAUNCH_PID
  set +e
  wait "$pid"
  ec=$?
  set -e
  if [[ "$ec" -ne "$want" ]]; then
    echo "exit ${ec}, want ${want}" >&2
    return 1
  fi
  # The driver registry records the pid. It must be dead.
  local child
  child="$(cat "$dir/reg"/children/*/pid 2>/dev/null || true)"
  if [[ -n "$child" ]] && kill -0 "$child" 2>/dev/null; then
    echo "child ${child} survived exit ${want}" >&2
    kill -KILL "$child" 2>/dev/null || true
    return 1
  fi
  rm -rf "$dir"
}

case_exit_zero() { case_exit_status 0; }
case_exit_one() { case_exit_status 1; }

case_idempotent() {
  local dir pid ec
  dir="$(mktemp -d)"
  : >"$dir/hook.count"
  write_driver "$dir/driver.sh" "$dir/reg" "$dir/work" <<EOF
spike_cleanup_pre() { echo x >> $(printf '%q' "$dir/hook.count"); }
spike_spawn --class transient short sleep 30
echo READY
wait || true
EOF
  launch_driver "$dir/driver.sh" >"$dir/out" 2>"$dir/err"
  pid=$LAUNCH_PID
  for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
    if grep -q READY "$dir/out" 2>/dev/null; then
      break
    fi
    sleep 0.05
  done
  kill -TERM "$pid" || true
  kill -TERM "$pid" || true
  set +e
  wait "$pid"
  ec=$?
  set -e
  if [[ "$ec" -ne 143 ]]; then
    echo "exit ${ec}, want 143" >&2
    return 1
  fi
  local n
  n="$(wc -l <"$dir/hook.count" | tr -d '[:space:]')"
  if [[ "$n" -ne 1 ]]; then
    echo "hook ran ${n} times, want 1" >&2
    return 1
  fi
  if [[ -s "$dir/err" ]]; then
    echo "unexpected stderr" >&2
    cat "$dir/err" >&2
    return 1
  fi
  rm -rf "$dir"
}

case_reused_pid() {
  local dir pid ec child
  dir="$(mktemp -d)"
  write_driver "$dir/driver.sh" "$dir/reg" "$dir/work" <<'EOF'
spike_spawn --class stack victim sleep 600
printf '%s\n' 'not-the-real-start' >"$SPIKE_LAST_DIR/lstart"
echo READY
wait || true
EOF
  launch_driver "$dir/driver.sh" >"$dir/out" 2>"$dir/err"
  pid=$LAUNCH_PID
  for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
    if grep -q READY "$dir/out" 2>/dev/null; then
      break
    fi
    sleep 0.05
  done
  child="$(cat "$dir/reg"/children/*/pid)"
  kill -TERM "$pid"
  set +e
  wait "$pid"
  ec=$?
  set -e
  if [[ "$ec" -ne 143 ]]; then
    echo "exit ${ec}, want 143" >&2
    return 1
  fi
  if ! kill -0 "$child" 2>/dev/null; then
    echo "reused-identity pid ${child} was signalled" >&2
    return 1
  fi
  kill -KILL "$child" 2>/dev/null || true
  wait "$child" 2>/dev/null || true
  rm -rf "$dir"
}

case_container_label() {
  local dir
  dir="$(mktemp -d)"
  cat >"$dir/docker" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "${DOCKER_LOG}"
if [[ "$1" == ps ]]; then
  joined="$*"
  if [[ "$joined" == *"label=spike-run=m5-testrun"* ]]; then
    printf '%s\n' cid-labelled
  fi
  exit 0
fi
exit 0
EOF
  chmod +x "$dir/docker"
  DOCKER_LOG="$dir/docker.log" PATH="$dir:$PATH" SPIKE_RUN_LABEL=m5-testrun \
    SPIKE_STOP_GRACE=1 bash -c '
      . "$1"
      spike_stop_containers
    ' _ "$LIB"
  if ! grep -F 'stop -t 1 cid-labelled' "$dir/docker.log" >/dev/null; then
    echo "labelled container was not stopped" >&2
    cat "$dir/docker.log" >&2
    return 1
  fi
  if ! grep -F 'rm -f cid-labelled' "$dir/docker.log" >/dev/null; then
    echo "labelled container was not removed" >&2
    return 1
  fi
  if grep -F cid-other "$dir/docker.log" >/dev/null; then
    echo "unlabelled container was touched" >&2
    return 1
  fi
  rm -rf "$dir"
}

case_keep_up() {
  local dir mode child_stack
  dir="$(mktemp -d)"
  write_driver "$dir/driver.sh" "$dir/reg" "$dir/work" <<EOF
KEEP_UP=1
spike_spawn --class stack stacker sleep 600
spike_spawn --class transient transient sleep 600
echo READY
umask 077
{
  printf 'STACK_MODE=%q\n' offline
  spike_emit_children
} > $(printf '%q' "$dir/stack.env")
chmod 600 $(printf '%q' "$dir/stack.env")
exit 0
EOF
  "$dir/driver.sh" >"$dir/out" 2>"$dir/err"
  child_stack="$(awk -F= '/^SPIKE_CHILD_1_PID=/{print $2}' "$dir/stack.env" | tr -d "'")"
  # %q may quote the pid as 123 or $'123'. Accept a plain integer.
  child_stack="$(python3 -c 'import ast,sys; print(ast.literal_eval(sys.argv[1]))' "$child_stack")"
  if ! kill -0 "$child_stack" 2>/dev/null; then
    echo "stack child died under KEEP_UP" >&2
    return 1
  fi
  if pgrep -fc '[s]leep 600' >/dev/null; then
    local n
    n="$(pgrep -fc '[s]leep 600' || true)"
    if [[ "$n" -ne 1 ]]; then
      echo "expected 1 surviving sleep, saw ${n}" >&2
      kill -KILL "$child_stack" 2>/dev/null || true
      return 1
    fi
  else
    echo "stack sleep is not visible to pgrep" >&2
    kill -KILL "$child_stack" 2>/dev/null || true
    return 1
  fi
  mode="$(python3 -c 'import os,sys; print(format(os.stat(sys.argv[1]).st_mode & 0o777, "03o"))' "$dir/stack.env")"
  if [[ "$mode" != 600 ]]; then
    echo "stack.env mode ${mode}, want 600" >&2
    kill -KILL "$child_stack" 2>/dev/null || true
    return 1
  fi
  if ! grep -F stacker "$dir/stack.env" >/dev/null; then
    echo "stack.env missing stack child" >&2
    kill -KILL "$child_stack" 2>/dev/null || true
    return 1
  fi
  if grep -F transient "$dir/stack.env" >/dev/null; then
    echo "stack.env recorded a transient child" >&2
    kill -KILL "$child_stack" 2>/dev/null || true
    return 1
  fi
  kill -KILL "$child_stack" 2>/dev/null || true
  rm -rf "$dir"
}

case_playwright_argv() {
  local dir
  dir="$(mktemp -d)"
  mkdir -p "$dir/bin" "$dir/src" "$dir/work"
  printf '{}\n' >"$dir/src/package.json"
  printf '{}\n' >"$dir/src/package-lock.json"
  printf '{}\n' >"$dir/src/playwright.config.ts"
  printf '{}\n' >"$dir/src/day.spec.ts"
  cat >"$dir/bin/docker" <<'EOF'
#!/usr/bin/env bash
if [[ "${1:-}" == run ]]; then
  printf '%s\n' "$@" >"${DOCKER_LOG}"
  exit 0
fi
exit 0
EOF
  chmod +x "$dir/bin/docker"
  DOCKER_LOG="$dir/argv.txt" PATH="$dir/bin:$PATH" bash -c '
    set -euo pipefail
    unset PLAYWRIGHT_DOCKER_ARGS PLAYWRIGHT_WATCH_IN_PARENT PLAYWRIGHT_EXTRA_ENV SPIKE_RUN_LABEL
    unset M5_REVIEWING_MODEL M6_API_KEY M6_MODE
    ROOT="$1"
    WORK="$2/work"
    EVIDENCE="$WORK/evidence"
    SIGNAL="$WORK/signal"
    E2E_SRC="$2/src"
    PLAYWRIGHT_IMAGE="mcr.microsoft.com/playwright:test"
    FACTORY_API_KEY="k"
    MODE="offline"
    mkdir -p "$WORK" "$EVIDENCE" "$SIGNAL"
    # shellcheck source=spike/lib/common.sh
    . "$ROOT/spike/lib/common.sh"
    # shellcheck source=spike/lib/driver.sh
    . "$ROOT/spike/lib/driver.sh"
    run_playwright
  ' _ "$ROOT" "$dir"
  cat >"$dir/want.txt" <<EOF
run
--rm
--network
factory-m5_default
-v
${dir}/work/e2e:/work
-v
${dir}/work/evidence:/evidence
-v
${dir}/work/signal:/signal
-e
M5_API_KEY=k
-e
M5_MODE=offline
-e
M5_REVIEWING_MODEL=
-e
M5_SIGNAL=/signal
-e
M5_EVIDENCE=/evidence
-e
M6_API_KEY=
-e
M6_MODE=offline
-e
M6_SIGNAL=/signal
-e
M6_EVIDENCE=/evidence
-e
FACTORY_URL=http://factoryd:8080
-e
FACTORY_GIT_URL=http://factoryd-git:8080
-w
/work
mcr.microsoft.com/playwright:test
bash
-lc
npm ci && npx playwright test --reporter=list
EOF
  if ! cmp -s "$dir/want.txt" "$dir/argv.txt"; then
    echo "run_playwright argv drifted from 317157c" >&2
    diff -u "$dir/want.txt" "$dir/argv.txt" >&2 || true
    return 1
  fi
  rm -rf "$dir"
}

case_spawn_redirects() {
  local dir
  dir="$(mktemp -d)"
  bash -c '
    set -euo pipefail
    # shellcheck source=spike/lib/cleanup.sh
    . "$1"
    SPIKE_STOP_GRACE=1
    SPIKE_REG="$2/reg"
    WORK="$2/work"
    spike_run_init t76
    spike_install_trap
    spike_spawn --class transient --out "$2/before.out" --err "$2/before.err" before \
      bash -c "echo OUT; echo ERR >&2"
    wait "$SPIKE_LAST_PID"
    spike_spawn --class transient after-name --out "$2/after.out" --err "$2/after.err" \
      bash -c "echo OUT; echo ERR >&2"
    wait "$SPIKE_LAST_PID"
    spike_spawn --class transient joined --out "$2/both.log" --err "$2/both.log" \
      bash -c "echo OUT; echo ERR >&2"
    wait "$SPIKE_LAST_PID"
    spike_spawn --class transient flags --out "$2/flags.out" --err "$2/flags.err" \
      bash -c "printf \"%s\n\" \"\$@\"" _ --reuseaddr --port=9418
    wait "$SPIKE_LAST_PID"
  ' _ "$LIB" "$dir"
  grep -qx OUT "$dir/before.out"
  grep -qx ERR "$dir/before.err"
  grep -qx OUT "$dir/after.out"
  grep -qx ERR "$dir/after.err"
  grep -qx OUT "$dir/both.log"
  grep -qx ERR "$dir/both.log"
  grep -qx -- --reuseaddr "$dir/flags.out"
  grep -qx -- --port=9418 "$dir/flags.out"
  if [[ -s "$dir/flags.err" ]]; then
    echo "command flags were not left on the command" >&2
    return 1
  fi
  rm -rf "$dir"
}

case_spawn_flags_one_loop() {
  local dir
  dir="$(mktemp -d)"
  if ! bash -c '
    set -euo pipefail
    # shellcheck source=spike/lib/cleanup.sh
    . "$1"
    SPIKE_STOP_GRACE=1
    SPIKE_REG="$2/reg"
    WORK="$2/work"
    mkdir -p "$WORK"
    spike_run_init t76
    spike_install_trap
    printf "OLD\n" >"$2/both.out"
    spike_spawn --out "$2/both.out" --class transient both --err "$2/both.err" --append \
      bash -c "echo OUT; echo ERR >&2; printf \"%s\n\" \"\$@\"" _ --reuseaddr
    wait "$SPIKE_LAST_PID"
    class="$(cat "$SPIKE_LAST_DIR/class")"
    [[ "$class" == transient ]]
    spike_spawn -- dash-name --out "$2/dash.out" --err "$2/dash.err" \
      bash -c "echo DASH"
    wait "$SPIKE_LAST_PID"
    [[ "$(cat "$SPIKE_LAST_DIR/name")" == dash-name ]]
    set +e
    spike_spawn --out >"$2/miss-out.err" 2>&1
    out_ec=$?
    spike_spawn --class transient --err >"$2/miss-err.err" 2>&1
    err_ec=$?
    set -e
    [[ "$out_ec" -ne 0 && "$err_ec" -ne 0 ]]
  ' _ "$LIB" "$dir"; then
    echo "spawn flag loop failed" >&2
    return 1
  fi
  if ! grep -qx OLD "$dir/both.out"; then
    echo "append before the command did not keep the old line" >&2
    return 1
  fi
  if ! grep -qx OUT "$dir/both.out" || ! grep -qx ERR "$dir/both.err"; then
    echo "flags on both sides did not redirect" >&2
    return 1
  fi
  if ! grep -qx -- --reuseaddr "$dir/both.out"; then
    echo "command flag was scanned" >&2
    return 1
  fi
  if ! grep -qx DASH "$dir/dash.out"; then
    echo "-- before the name did not leave the command" >&2
    return 1
  fi
  if ! grep -qx "spike_spawn: --out needs a path" "$dir/miss-out.err"; then
    echo "missing --out path line" >&2
    cat "$dir/miss-out.err" >&2
    return 1
  fi
  if ! grep -qx "spike_spawn: --err needs a path" "$dir/miss-err.err"; then
    echo "missing --err path line" >&2
    cat "$dir/miss-err.err" >&2
    return 1
  fi
  rm -rf "$dir"
}

case_respawn_child() {
  local dir
  dir="$(mktemp -d)"
  cat >"$dir/first.sh" <<'EOF'
set -euo pipefail
# shellcheck source=spike/lib/cleanup.sh
. "$1"
SPIKE_STOP_GRACE=1
SPIKE_REG="$2/reg"
WORK="$2/work"
mkdir -p "$WORK"
spike_run_init t76
spike_install_trap
printf "OLD\n" >"$2/out"
printf "OLDERR\n" >"$2/err"
spike_spawn --class stack --append --out "$2/out" --err "$2/err" child \
  bash -c "echo NEW; echo NEWERR >&2"
wait "$SPIKE_LAST_PID"
spike_emit_children >"$2/emit"
EOF
  cat >"$dir/second.sh" <<'EOF'
set -euo pipefail
# shellcheck source=spike/lib/cleanup.sh
. "$1"
SPIKE_STOP_GRACE=1
SPIKE_REG="$2/reg2"
WORK="$2/work"
# shellcheck disable=SC1091
. "$2/emit"
spike_respawn_child 1
wait "$SPIKE_LAST_PID"
[[ "$(cat "$SPIKE_LAST_DIR/append")" == 1 ]]
[[ "$(cat "$SPIKE_LAST_DIR/out")" == "$2/out" ]]
[[ "$(cat "$SPIKE_LAST_DIR/err")" == "$2/err" ]]
[[ "$(cat "$SPIKE_LAST_DIR/argv")" == "$(cat "$2/reg/children/0001/argv")" ]]
EOF
  if ! bash "$dir/first.sh" "$LIB" "$dir"; then
    echo "respawn first spawn failed" >&2
    return 1
  fi
  if ! env -u SPIKE_RUN_LABEL bash "$dir/second.sh" "$LIB" "$dir"; then
    echo "respawn round trip failed" >&2
    return 1
  fi
  if ! grep -qx OLD "$dir/out" || ! grep -qx OLDERR "$dir/err"; then
    echo "respawn truncated the log" >&2
    return 1
  fi
  if [[ "$(grep -cx NEW "$dir/out")" -lt 2 || "$(grep -cx NEWERR "$dir/err")" -lt 2 ]]; then
    echo "respawn did not append" >&2
    return 1
  fi
  rm -rf "$dir"
}

case_stack_env_roundtrip() {
  local dir pid ec
  dir="$(mktemp -d)"
  bash -c '
    set -euo pipefail
    # shellcheck source=spike/lib/cleanup.sh
    . "$1"
    # shellcheck source=spike/lib/driver.sh
    . "$2"
    SPIKE_STOP_GRACE=1
    WORK="$3/work"
    SPIKE_REG="$3/reg"
    mkdir -p "$WORK"
    spike_run_init t76
    spike_spawn --class stack held sleep 60
    printf "%s\n" "$SPIKE_LAST_PID" >"$3/pid"
    DEMO_KEY=demo-value-not-a-secret
    stack_env_write DEMO_KEY
  ' _ "$LIB" "$ROOT/spike/lib/driver.sh" "$dir"
  pid="$(cat "$dir/pid")"
  mode="$(python3 -c 'import os,sys; print(oct(os.stat(sys.argv[1]).st_mode & 0o777))' "$dir/work/stack.env")"
  if [[ "$mode" != 0o600 ]]; then
    echo "stack.env mode ${mode}, want 0o600" >&2
    kill -KILL "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    return 1
  fi
  if ! grep -q '^DEMO_KEY=demo-value-not-a-secret$' "$dir/work/stack.env"; then
    echo "caller variable was not recorded" >&2
    kill -KILL "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    return 1
  fi
  kill -KILL "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  set +e
  bash -c '
    set -euo pipefail
    . "$1"
    . "$2"
    WORK="$3/work"
    stack_env_load
    [[ "$DEMO_KEY" == demo-value-not-a-secret ]]
    stack_pids_match ZZ
  ' _ "$LIB" "$ROOT/spike/lib/driver.sh" "$dir" >"$dir/out" 2>"$dir/err"
  ec=$?
  set -e
  if [[ "$ec" -eq 0 ]]; then
    echo "stale pid was accepted" >&2
    return 1
  fi
  if ! grep -F "ZZ FAIL — stale stack: held pid ${pid} is gone" "$dir/err" >/dev/null; then
    echo "stale line mismatch" >&2
    cat "$dir/err" >&2
    return 1
  fi
  rm -rf "$dir"
}

case_stack_refuse_live() {
  local dir pid ec
  dir="$(mktemp -d)"
  mkdir -p "$dir/work"
  sleep 60 &
  pid=$!
  cat >"$dir/work/stack.env" <<EOF
SPIKE_CHILD_1_CLASS=stack
SPIKE_CHILD_1_PID=${pid}
SPIKE_CHILD_COUNT=1
SPIKE_RUN_LABEL=t76-refuse
EOF
  set +e
  bash -c '
    set -euo pipefail
    . "$1"
    . "$2"
    WORK="$3"
    stack_refuse_live PRE ./cmd
  ' _ "$LIB" "$ROOT/spike/lib/driver.sh" "$dir/work" >"$dir/out" 2>"$dir/err"
  ec=$?
  set -e
  if [[ "$ec" -eq 0 ]]; then
    echo "live stack was not refused" >&2
    kill -KILL "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    return 1
  fi
  if ! grep -F "PRE FAIL — KEEP_UP stack is still up (./cmd down or ./cmd specs)" "$dir/err" >/dev/null; then
    echo "refuse line mismatch" >&2
    cat "$dir/err" >&2
    kill -KILL "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    return 1
  fi
  kill -KILL "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  if ! bash -c '
    set -euo pipefail
    . "$1"
    . "$2"
    WORK="$3"
    stack_refuse_live PRE ./cmd
    [[ ! -f "$WORK/stack.env" ]]
  ' _ "$LIB" "$ROOT/spike/lib/driver.sh" "$dir/work"; then
    echo "stale stack.env was kept" >&2
    return 1
  fi
  rm -rf "$dir"
}

run_case spawn-flags-one-loop case_spawn_flags_one_loop
run_case respawn-child case_respawn_child
run_case stack-env-roundtrip case_stack_env_roundtrip
run_case stack-refuse-live case_stack_refuse_live
run_case spawn-redirects case_spawn_redirects
run_case term-ignoring case_term_ignoring
run_case int-ignored case_int_ignored
run_case term-during-fg case_term_during_fg
run_case exit-zero case_exit_zero
run_case exit-one case_exit_one
run_case idempotent case_idempotent
run_case reused-pid case_reused_pid
run_case container-label case_container_label
run_case keep-up case_keep_up
run_case playwright-argv case_playwright_argv

say "${PASS} passed, ${FAIL} failed"
if [[ "$FAIL" -ne 0 ]]; then
  exit 1
fi
