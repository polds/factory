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
  local dir mode child_stack transient_pid unrelated_pid d class pid
  dir="$(mktemp -d)"
  sleep 600 &
  unrelated_pid=$!
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
  "$dir/driver.sh" >"$dir/out" 2>"$dir/err" || true
  child_stack=""
  transient_pid=""
  for d in "$dir/reg"/children/*; do
    [[ -d "$d" ]] || continue
    class="$(tr -d '\n' <"$d/class")"
    pid="$(tr -d '\n' <"$d/pid")"
    if [[ "$class" == stack ]]; then
      child_stack="$pid"
    fi
    if [[ "$class" == transient ]]; then
      transient_pid="$pid"
    fi
  done
  if [[ -z "$child_stack" || -z "$transient_pid" ]]; then
    echo "registry missing stack or transient" >&2
    kill -KILL "$unrelated_pid" 2>/dev/null || true
    if [[ -n "$child_stack" ]]; then
      kill -KILL "$child_stack" 2>/dev/null || true
    fi
    if [[ -n "$transient_pid" ]]; then
      kill -KILL "$transient_pid" 2>/dev/null || true
    fi
    return 1
  fi
  if ! kill -0 "$child_stack" 2>/dev/null; then
    echo "stack child died under KEEP_UP" >&2
    kill -KILL "$unrelated_pid" 2>/dev/null || true
    return 1
  fi
  if kill -0 "$transient_pid" 2>/dev/null; then
    echo "transient ${transient_pid} still alive after driver exit" >&2
    kill -KILL "$child_stack" "$transient_pid" "$unrelated_pid" 2>/dev/null || true
    return 1
  fi
  if ! kill -0 "$unrelated_pid" 2>/dev/null; then
    echo "unrelated sleep was killed" >&2
    kill -KILL "$child_stack" 2>/dev/null || true
    return 1
  fi
  mode="$(python3 -c 'import os,sys; print(format(os.stat(sys.argv[1]).st_mode & 0o777, "03o"))' "$dir/stack.env")"
  if [[ "$mode" != 600 ]]; then
    echo "stack.env mode ${mode}, want 600" >&2
    kill -KILL "$child_stack" "$unrelated_pid" 2>/dev/null || true
    return 1
  fi
  if ! grep -F stacker "$dir/stack.env" >/dev/null; then
    echo "stack.env missing stack child" >&2
    kill -KILL "$child_stack" "$unrelated_pid" 2>/dev/null || true
    return 1
  fi
  if grep -F transient "$dir/stack.env" >/dev/null; then
    echo "stack.env recorded a transient child" >&2
    kill -KILL "$child_stack" "$unrelated_pid" 2>/dev/null || true
    return 1
  fi
  kill -KILL "$child_stack" "$unrelated_pid" 2>/dev/null || true
  rm -rf "$dir"
}

# A TERM that arrives after sleep is forked and before that pid is recorded
# used to exit the driver with an empty registry and leave the sleep running.
case_signal_before_child_record() {
  local dir ec pid
  dir="$(mktemp -d)"
  set +e
  bash -c '
    set -euo pipefail
    . "$1"
    SPIKE_REG="$2/reg"
    WORK="$2/work"
    spike_run_init t76
    spike_install_trap
    SPIKE_SPAWN_CRITICAL=1
    sleep 600 &
    pid=$!
    printf "%s\n" "$pid" > "$2/sleep.pid"
    spike_trap_cleanup TERM
    mkdir -p "$SPIKE_REG/children/0001"
    printf "%s\n" transient > "$SPIKE_REG/children/0001/class"
    printf "%s\n" fg > "$SPIKE_REG/children/0001/name"
    printf "%s\n" "$pid" > "$SPIKE_REG/children/0001/pid"
    pgid="$(ps -o pgid= -p "$pid" | tr -d "[:space:]" || true)"
    printf "%s\n" "${pgid:-$pid}" > "$SPIKE_REG/children/0001/pgid"
    printf "%s\n" "$(ps -o lstart= -p "$pid" || true)" > "$SPIKE_REG/children/0001/lstart"
    printf "%s\n" "$(ps -o args= -p "$pid" || true)" > "$SPIKE_REG/children/0001/args"
    printf "1\n" > "$SPIKE_REG/seq"
    SPIKE_SPAWN_CRITICAL=0
    why="${SPIKE_STOP_DEFERRED:-}"
    SPIKE_STOP_DEFERRED=
    if [[ -z "$why" ]]; then
      echo "signal was not deferred" >&2
      exit 1
    fi
    spike_trap_cleanup "$why"
    echo should-have-exited >&2
    exit 1
  ' _ "$LIB" "$dir" >"$dir/out" 2>"$dir/err"
  ec=$?
  set -e
  pid="$(cat "$dir/sleep.pid" 2>/dev/null || true)"
  if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
    echo "unrecorded sleep ${pid} survived the signal" >&2
    kill -KILL "$pid" 2>/dev/null || true
    rm -rf "$dir"
    return 1
  fi
  if [[ "$ec" -ne 143 ]]; then
    echo "exit ${ec}, want 143" >&2
    cat "$dir/err" >&2 || true
    rm -rf "$dir"
    return 1
  fi
  if grep -F should-have-exited "$dir/err" >/dev/null 2>&1; then
    echo "deferred cleanup did not exit" >&2
    rm -rf "$dir"
    return 1
  fi
  rm -rf "$dir"
}

# A bookkeeping error after fork must not leave the child running, and must
# restore monitor mode and the critical-section flag. kind=seq fails the seq
# write. kind=record fails a later registry write. kind=redirect fails the
# log open before fork. kind=seq-term is the seq failure with a deferred
# TERM already latched.
case_spawn_bookkeeping_failure() {
  local kind="$1" dir ec foreign=0 driver_pid=0 waited=0
  dir="$(mktemp -d)"
  sleep 600 &
  foreign=$!
  cat >"$dir/driver.sh" <<'EOF'
set -euo pipefail
. "$1"
kind="$2"
reg="$3/reg"
WORK="$3/work"
SPIKE_REG="$reg"
spike_run_init t76
spike_install_trap
SPIKE_BOOK_STATE="$3/state"
record_spawn_state() {
  if [[ -f "$SPIKE_BOOK_STATE" ]]; then
    return 0
  fi
  printf 'critical=%s dash=%s deferred=%s\n' "${SPIKE_SPAWN_CRITICAL-}" "$-" "${SPIKE_STOP_DEFERRED-}" >"$SPIKE_BOOK_STATE"
}
trap record_spawn_state ERR
if [[ "$kind" == record ]]; then
  printf '0\n' >"$reg/seq"
  mkdir -p "$reg/children/0001"
  mkdir "$reg/children/0001/pid"
elif [[ "$kind" == redirect ]]; then
  mkdir "$3/notfile"
else
  rm -f "$reg/seq"
  mkdir "$reg/seq"
fi
set +m
if [[ "$kind" == seq-term ]]; then
  SPIKE_STOP_DEFERRED=TERM
elif [[ "$kind" == seq-int ]]; then
  SPIKE_STOP_DEFERRED=INT
fi
if [[ "$kind" == subshell ]]; then
  (
    printf 'outer=%s\nbashpid=%s\n' "$$" "$BASHPID" >"$3/ids"
    spike_spawn --class transient broken sleep 600
  )
elif [[ "$kind" == nested ]]; then
  (
    (
      printf 'outer=%s\nbashpid=%s\n' "$$" "$BASHPID" >"$3/ids"
      spike_spawn --class transient broken sleep 600
    )
  )
elif [[ "$kind" == redirect ]]; then
  spike_spawn --class transient --out "$3/notfile" broken sleep 600
else
  spike_spawn --class transient broken sleep 600
fi
echo CONTINUED >"$3/continued"
EOF
  set +e
  bash "$dir/driver.sh" "$LIB" "$kind" "$dir" >"$dir/out" 2>"$dir/err" &
  driver_pid=$!
  while [[ "$waited" -lt 5 ]]; do
    if ! kill -0 "$driver_pid" 2>/dev/null; then
      break
    fi
    sleep 1
    waited=$((waited + 1))
  done
  if kill -0 "$driver_pid" 2>/dev/null; then
    kill -KILL "$driver_pid" 2>/dev/null || true
    wait "$driver_pid" 2>/dev/null || true
    ec=124
  else
    wait "$driver_pid"
    ec=$?
  fi
  set -e
  local orphans=""
  orphans="$(pgrep -f '[s]leep 600' || true)"
  if [[ -n "$orphans" ]]; then
    orphans="$(printf '%s\n' "$orphans" | grep -vx "$foreign" || true)"
  fi
  if [[ -n "$orphans" ]]; then
    echo "bookkeeping failure left sleep: ${orphans}" >&2
    # shellcheck disable=SC2086 # pids from pgrep, one per line
    kill -KILL $orphans 2>/dev/null || true
    kill -KILL "$foreign" 2>/dev/null || true
    rm -rf "$dir"
    return 1
  fi
  if ! kill -0 "$foreign" 2>/dev/null; then
    echo "reap killed foreign sleep ${foreign}" >&2
    rm -rf "$dir"
    return 1
  fi
  kill -KILL "$foreign" 2>/dev/null || true
  if [[ "$ec" -eq 0 || -f "$dir/continued" ]]; then
    echo "spawn failure was ignored, exit ${ec}" >&2
    rm -rf "$dir"
    return 1
  fi
  if [[ "$ec" -eq 124 ]]; then
    echo "spawn bookkeeping hung" >&2
    rm -rf "$dir"
    return 1
  fi
  if [[ "$kind" == seq-term || "$kind" == seq-int ]]; then
    local want=143
    if [[ "$kind" == seq-int ]]; then
      want=130
    fi
    if [[ "$ec" -ne "$want" ]]; then
      echo "deferred signal exit ${ec}, want ${want}" >&2
      cat "$dir/err" >&2 || true
      rm -rf "$dir"
      return 1
    fi
  elif [[ "$kind" == subshell || "$kind" == nested ]]; then
    local outer="" bashpid=""
    outer="$(sed -n 's/^outer=//p' "$dir/ids")"
    bashpid="$(sed -n 's/^bashpid=//p' "$dir/ids")"
    if [[ -z "$outer" || -z "$bashpid" || "$outer" == "$bashpid" ]]; then
      echo "spawn did not run in a subshell (outer=${outer} bashpid=${bashpid})" >&2
      rm -rf "$dir"
      return 1
    fi
  elif [[ ! -f "$dir/state" ]] || ! grep -q '^critical=0 dash=.*e' "$dir/state" || grep -q 'dash=[^ ]*m' "$dir/state" || grep -q 'deferred=.' "$dir/state"; then
    echo "monitor or critical section not restored (exit ${ec})" >&2
    cat "$dir/state" >&2 || true
    cat "$dir/err" >&2 || true
    rm -rf "$dir"
    return 1
  fi
  rm -rf "$dir"
}

# After go, an external command's recorded lstart and args match ps, and a
# normal stop kills that process. lstart is still one-second text.
case_gate_post_go_identity() {
  local dir pid rec_l rec_a live_l live_a
  dir="$(mktemp -d)"
  # shellcheck source=spike/lib/cleanup.sh
  . "$LIB"
  SPIKE_REG="$dir/reg"
  WORK="$dir/work"
  spike_run_init t76
  spike_spawn --class transient ident sleep 30
  pid="$SPIKE_LAST_PID"
  rec_l="$(spike_read_field "$SPIKE_LAST_DIR/lstart")"
  rec_a="$(spike_read_field "$SPIKE_LAST_DIR/args")"
  live_l="$(ps -o lstart= -p "$pid" 2>/dev/null || true)"
  live_a="$(ps -o args= -p "$pid" 2>/dev/null || true)"
  if [[ "$rec_l" != "$live_l" || "$rec_a" != "$live_a" || "$live_a" != "sleep 30" ]]; then
    echo "post-go identity rec=[$rec_l][$rec_a] live=[$live_l][$live_a]" >&2
    kill -KILL "$pid" 2>/dev/null || true
    rm -rf "$dir"
    return 1
  fi
  spike_stop_processes
  if kill -0 "$pid" 2>/dev/null; then
    echo "recorded sleep was not stopped" >&2
    kill -KILL "$pid" 2>/dev/null || true
    rm -rf "$dir"
    return 1
  fi
  rm -rf "$dir"
}

# A same-shell function runs only after go. Its process stays the shell.
case_gate_function() {
  local dir pid kids=""
  dir="$(mktemp -d)"
  # shellcheck source=spike/lib/cleanup.sh
  . "$LIB"
  # spike_spawn runs this function in a subshell of this shell.
  # shellcheck disable=SC2329
  gate_fn() {
    printf 'RAN\n' >"$1"
    sleep 30
  }
  SPIKE_REG="$dir/reg"
  WORK="$dir/work"
  spike_run_init t76
  spike_spawn --class transient fn gate_fn "$dir/ran"
  pid="$SPIKE_LAST_PID"
  sleep 0.2
  if [[ "$(cat "$dir/ran" 2>/dev/null || true)" != "RAN" ]]; then
    echo "function did not run" >&2
    kids="$(ps -o pid= --ppid "$pid" 2>/dev/null || true)"
    # shellcheck disable=SC2086
    kill -KILL $pid $kids 2>/dev/null || true
    rm -rf "$dir"
    return 1
  fi
  if [[ "$(spike_read_field "$SPIKE_LAST_DIR/args")" != "$(ps -o args= -p "$pid" 2>/dev/null || true)" ]]; then
    echo "function identity drifted" >&2
    kids="$(ps -o pid= --ppid "$pid" 2>/dev/null || true)"
    # shellcheck disable=SC2086
    kill -KILL $pid $kids 2>/dev/null || true
    rm -rf "$dir"
    return 1
  fi
  kids="$(ps -o pid= --ppid "$pid" 2>/dev/null || true)"
  # shellcheck disable=SC2086
  kill -KILL $pid $kids 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  rm -rf "$dir"
}

# The released command is not tied to the parent's exit.
case_gate_parent_exit() {
  local dir pid
  dir="$(mktemp -d)"
  bash -c '
    set -euo pipefail
    . "$1"
    SPIKE_REG="$2/reg"
    WORK="$2/work"
    spike_run_init t76
    spike_spawn --class stack hold sleep 30
    printf "%s\n" "$SPIKE_LAST_PID" >"$2/pid"
  ' _ "$LIB" "$dir"
  pid="$(cat "$dir/pid")"
  sleep 0.2
  if ! kill -0 "$pid" 2>/dev/null; then
    echo "released sleep died with the parent" >&2
    rm -rf "$dir"
    return 1
  fi
  if [[ "$(ps -o args= -p "$pid" 2>/dev/null || true)" != "sleep 30" ]]; then
    echo "surviving process is not sleep" >&2
    kill -KILL "$pid" 2>/dev/null || true
    rm -rf "$dir"
    return 1
  fi
  kill -KILL "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  rm -rf "$dir"
}

# EXIT while the waiter is blocked closes the write end. The user command
# does not start. spike_teardown_finish exits this driver.
case_gate_exit_before_go() {
  local dir
  dir="$(mktemp -d)"
  bash -c '
    set -euo pipefail
    . "$1"
    mark="$2/marker"
    SPIKE_REG="$2/reg"
    WORK="$2/work"
    spike_run_init t76
    spike_install_trap
    spike_gate_open
    (
      eval "exec ${SPIKE_GATE_W}>&-"
      if ! IFS= read -r _ <&"${SPIKE_GATE_R}"; then
        printf EOF >"$mark"
        exit 7
      fi
      printf RAN >"$mark"
      exec sleep 600
    ) &
    eval "exec ${SPIKE_GATE_R}>&-"
    SPIKE_GATE_R=
    spike_trap_cleanup EXIT
  ' _ "$LIB" "$dir" >"$dir/out" 2>"$dir/err" || true
  if [[ "$(cat "$dir/marker" 2>/dev/null || true)" != "EOF" ]]; then
    echo "EXIT before go marker=$(cat "$dir/marker" 2>/dev/null || echo missing)" >&2
    cat "$dir/err" >&2 || true
    rm -rf "$dir"
    return 1
  fi
  if pgrep -f '[s]leep 600' >/dev/null; then
    echo "EXIT before go started sleep" >&2
    # shellcheck disable=SC2046
    kill -KILL $(pgrep -f '[s]leep 600') 2>/dev/null || true
    rm -rf "$dir"
    return 1
  fi
  rm -rf "$dir"
}

# The go write must not kill the shell when the reader is already gone,
# and it must not leave the caller's PIPE trap changed.
case_gate_sigpipe() {
  local rc=0 before after
  # shellcheck source=spike/lib/cleanup.sh
  . "$LIB"
  before="$(trap -p PIPE || true)"
  spike_gate_open
  eval "exec ${SPIKE_GATE_R}>&-"
  SPIKE_GATE_R=
  # The pipe's creating process can still hold a read end for a moment.
  # A write then succeeds. Retry until that holder exits and the write fails.
  set +e
  for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
    spike_gate_send "$SPIKE_GATE_W"
    rc=$?
    if [[ "$rc" -ne 0 ]]; then
      break
    fi
  done
  set -e
  after="$(trap -p PIPE || true)"
  spike_gate_release
  if [[ "$rc" -eq 0 || "$rc" -eq 141 ]]; then
    echo "go write status ${rc}" >&2
    return 1
  fi
  if [[ "$before" != "$after" ]]; then
    echo "PIPE trap changed" >&2
    return 1
  fi
}

# A background command started from teardown must not keep the waiter up,
# because the write end is closed before that fork. A sibling started while
# the write end is still open would pin the waiter; production does not
# start one. This is the constraint Peter accepted, not a general sibling proof.
case_gate_exit_closes_first() {
  local dir extra=""
  dir="$(mktemp -d)"
  bash -c '
    set -euo pipefail
    . "$1"
    mark="$2"
    SPIKE_REG="$2/reg"
    WORK="$2/work"
    spike_run_init t76
    spike_install_trap
    spike_cleanup_pre() {
      sleep 30 &
      printf "%s\n" "$!" >"$mark/extra"
      if kill -0 "$!" 2>/dev/null; then
        printf alive >"$mark/extra_state"
      fi
    }
    spike_gate_open
    (
      eval "exec ${SPIKE_GATE_W}>&-"
      if ! IFS= read -r _ <&"${SPIKE_GATE_R}"; then
        printf EOF >"$mark/marker"
        exit 7
      fi
      printf RAN >"$mark/marker"
      exec sleep 600
    ) &
    eval "exec ${SPIKE_GATE_R}>&-"
    SPIKE_GATE_R=
    spike_trap_cleanup EXIT
  ' _ "$LIB" "$dir" >"$dir/out" 2>"$dir/err" || true
  extra="$(cat "$dir/extra" 2>/dev/null || true)"
  if [[ "$(cat "$dir/marker" 2>/dev/null || true)" != "EOF" ]]; then
    echo "waiter was not released before teardown" >&2
    [[ -n "$extra" ]] && kill -KILL "$extra" 2>/dev/null || true
    rm -rf "$dir"
    return 1
  fi
  if [[ "$(cat "$dir/extra_state" 2>/dev/null || true)" != "alive" ]]; then
    echo "teardown child was not alive after the write end closed" >&2
    [[ -n "$extra" ]] && kill -KILL "$extra" 2>/dev/null || true
    rm -rf "$dir"
    return 1
  fi
  if [[ -n "$extra" ]] && kill -0 "$extra" 2>/dev/null; then
    kill -KILL "$extra" 2>/dev/null || true
    wait "$extra" 2>/dev/null || true
  fi
  rm -rf "$dir"
}

# An empty ps identity must not start the user command and must not signal
# a foreign pid. The registry write also fails.
case_spawn_empty_identity() {
  local dir foreign=0 ec orphans=""
  dir="$(mktemp -d)"
  sleep 600 &
  foreign=$!
  mkdir -p "$dir/bin"
  cat >"$dir/bin/ps" <<'EOF'
#!/bin/bash
exit 0
EOF
  chmod +x "$dir/bin/ps"
  cat >"$dir/driver.sh" <<'EOF'
set -euo pipefail
. "$1"
export PATH="$2:${PATH}"
reg="$3/reg"
WORK="$3/work"
SPIKE_REG="$reg"
spike_run_init t76
spike_install_trap
rm -f "$reg/seq"
mkdir "$reg/seq"
spike_spawn --class transient broken sleep 600
echo CONTINUED >"$3/continued"
EOF
  set +e
  bash "$dir/driver.sh" "$LIB" "$dir/bin" "$dir" >"$dir/out" 2>"$dir/err"
  ec=$?
  set -e
  orphans="$(pgrep -f '[s]leep 600' || true)"
  if [[ -n "$orphans" ]]; then
    orphans="$(printf '%s\n' "$orphans" | grep -vx "$foreign" || true)"
  fi
  if [[ -n "$orphans" ]]; then
    echo "empty identity left sleep: ${orphans}" >&2
    # shellcheck disable=SC2086
    kill -KILL $orphans 2>/dev/null || true
    kill -KILL "$foreign" 2>/dev/null || true
    rm -rf "$dir"
    return 1
  fi
  if ! kill -0 "$foreign" 2>/dev/null; then
    echo "empty identity killed foreign sleep ${foreign}" >&2
    rm -rf "$dir"
    return 1
  fi
  kill -KILL "$foreign" 2>/dev/null || true
  if [[ "$ec" -eq 0 || -f "$dir/continued" ]]; then
    echo "empty identity spawn was ignored, exit ${ec}" >&2
    rm -rf "$dir"
    return 1
  fi
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

case_arm_signal_teardown() {
  local ec
  set +e
  bash -c '
    set -euo pipefail
    . "$1"
    spike_arm_signal_teardown INT
    if spike_arm_signal_teardown TERM; then
      echo "second arm succeeded" >&2
      exit 1
    fi
    spike_teardown_finish 0
  ' _ "$LIB" >/dev/null
  ec=$?
  set -e
  if [[ "$ec" -ne 130 ]]; then
    echo "exit ${ec}, want 130" >&2
    return 1
  fi
  set +e
  bash -c '
    set -euo pipefail
    . "$1"
    spike_arm_signal_teardown TERM
    spike_teardown_finish 0
  ' _ "$LIB" >/dev/null
  ec=$?
  set -e
  if [[ "$ec" -ne 143 ]]; then
    echo "exit ${ec}, want 143" >&2
    return 1
  fi
}

run_case arm-signal-teardown case_arm_signal_teardown
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
run_case signal-before-child-record case_signal_before_child_record
run_case spawn-seq-failure case_spawn_bookkeeping_failure seq
run_case spawn-record-failure case_spawn_bookkeeping_failure record
run_case spawn-redirect-failure case_spawn_bookkeeping_failure redirect
run_case spawn-seq-deferred-term case_spawn_bookkeeping_failure seq-term
run_case spawn-seq-subshell case_spawn_bookkeeping_failure subshell
run_case spawn-seq-nested case_spawn_bookkeeping_failure nested
run_case spawn-seq-deferred-int case_spawn_bookkeeping_failure seq-int
run_case spawn-empty-identity case_spawn_empty_identity
run_case gate-post-go-identity case_gate_post_go_identity
run_case gate-function case_gate_function
run_case gate-parent-exit case_gate_parent_exit
run_case gate-exit-before-go case_gate_exit_before_go
run_case gate-sigpipe case_gate_sigpipe
run_case gate-exit-closes-first case_gate_exit_closes_first
run_case playwright-argv case_playwright_argv

say "${PASS} passed, ${FAIL} failed"
if [[ "$FAIL" -ne 0 ]]; then
  exit 1
fi
