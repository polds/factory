#!/usr/bin/env bash
# T76b cases for spike/m6/run_test.sh. No live network and no compose stack.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
M6="$ROOT/spike/m6"
WORK_REAL="$M6/.work"
NAME="${1:-}"
shift || true

fail() {
  printf '%s\n' "$*" >&2
  exit 1
}

reap_pids() {
  local file="$1" pid=""
  [[ -f "$file" ]] || return 0
  while IFS= read -r pid; do
    [[ -n "$pid" ]] || continue
    kill -KILL "$pid" 2>/dev/null || true
  done <"$file"
}

install_record_stub() {
  local bin="$1" name="$2" log="$3"
  cat >"$bin/$name" <<EOF
#!/bin/bash
printf '%s\n' "\$*" >> $(printf '%q' "$log")
exit 99
EOF
  chmod +x "$bin/$name"
}

install_docker_stub() {
  local bin="$1"
  cat >"$bin/docker" <<'EOF'
#!/bin/bash
printf '%q\n' "$@" >> "${DOCKER_LOG:?}"
cmd="${1:-}"
if [[ "$cmd" == run ]]; then
  if [[ "$*" == *golang:1.26-alpine* ]]; then
    exit 0
  fi
  printf 'cid-pw\n' >"${DOCKER_CID:?}"
  if [[ "${DOCKER_RUN_SLEEP:-}" == 1 ]]; then
    python3 -c 'import os,signal,time
signal.signal(signal.SIGTERM, signal.SIG_IGN)
open(os.environ["DOCKER_PID"], "w").write(str(os.getpid()) + "\n")
time.sleep(600)'
    exit 0
  fi
  exit 0
fi
if [[ "$cmd" == ps ]]; then
  if [[ -s "${DOCKER_CID:-}" ]]; then
    cat "${DOCKER_CID}"
  fi
  exit 0
fi
if [[ "$cmd" == stop ]]; then
  if [[ -n "${M6_ORDER_LOG:-}" ]]; then
    printf 'docker-stop %s\n' "$*" >>"$M6_ORDER_LOG"
  fi
  printf 'STOP %s\n' "$*" >>"${DOCKER_LOG:?}"
  exit 0
fi
if [[ "$cmd" == rm ]]; then
  printf 'RM %s\n' "$*" >>"${DOCKER_LOG:?}"
  exit 0
fi
if [[ "$cmd" == compose ]]; then
  joined="$*"
  case "$joined" in
    *" build "* | *" up "*)
      printf 'unexpected compose %s\n' "$*" >>"${DOCKER_LOG:?}"
      exit 99
      ;;
  esac
  case "$joined" in
    *" logs "*)
      printf '%s\n' "c0ffeec0ffeec0ffeec0ffeec0ffeec0ffeec0ff"
      exit 0
      ;;
    *" ps "*)
      printf '%s\n' "factoryd-m6"
      exit 0
      ;;
  esac
  exit 0
fi
exit 0
EOF
  chmod +x "$bin/docker"
}

install_curl_stub() {
  local bin="$1"
  cat >"$bin/curl" <<'EOF'
#!/bin/bash
log="${CURL_LOG:?}"
max=5
conn=""
out=""
method="GET"
url=""
prev=""
for arg in "$@"; do
  case "$prev" in
    -m) max="$arg" ;;
    -o) out="$arg" ;;
    -X) method="$arg" ;;
    --url) url="$arg" ;;
    --connect-timeout) conn="$arg" ;;
  esac
  case "$arg" in
    http://* | https://*) url="$arg" ;;
  esac
  prev="$arg"
done
printf '%s m=%s conn=%s\n' "$method" "$max" "${conn:-}" >>"$log"
if [[ -n "${M6_CLEANUP_DEADLINE:-}" ]]; then
  printf 'deadline-set\n' >>"$log"
fi
sleep_for="${CURL_SLEEP:-0}"
if [[ -n "${CURL_HONOR_M:-}" ]]; then
  if python3 -c 'import sys; raise SystemExit(0 if float(sys.argv[1]) + 1e-9 < float(sys.argv[2]) else 1)' "$max" "$sleep_for"; then
    python3 -c 'import time,sys; time.sleep(float(sys.argv[1]))' "$max"
    exit 28
  fi
fi
if python3 -c 'import sys; raise SystemExit(0 if float(sys.argv[1]) > 0 else 1)' "$sleep_for"; then
  python3 -c 'import time,sys; time.sleep(float(sys.argv[1]))' "$sleep_for"
fi
number="1"
case "$url" in
  */pulls/*)
    number="$(printf '%s' "$url" | sed -n 's#.*/pulls/\([0-9][0-9]*\).*#\1#p')"
    number="${number:-1}"
    ;;
esac
body='{}'
case "$method" in
  GET)
    body="$(printf '%s' "{\"number\":${number},\"state\":\"open\",\"html_url\":\"https://github.com/acme/widgets/pull/${number}\",\"head\":{\"ref\":\"improve/ibt_new\",\"label\":\"acme:improve/ibt_new\"},\"base\":{\"ref\":\"m6-run/local-s1/main\"},\"requested_reviewers\":[],\"labels\":[]}")"
    ;;
  PATCH)
    body='{"state":"closed"}'
    ;;
esac
if [[ -n "$out" ]]; then
  printf '%s' "$body" >"$out"
else
  printf '%s' "$body"
fi
printf '200'
EOF
  chmod +x "$bin/curl"
}

install_git_stub() {
  local bin="$1"
  cat >"$bin/git" <<'EOF'
#!/bin/bash
printf '%s\n' "$*" >>"${GIT_LOG:?}"
joined="$*"
case "$joined" in
  *" push "*)
    if [[ -n "${GIT_CHECK_PR:-}" ]]; then
      python3 -c 'import json,os,sys
doc=json.load(open(os.environ["LEDGER"], encoding="utf-8"))
prs=[e for e in doc["entries"] if e.get("kind")=="pr"]
if not prs or any(e.get("phase")!="cleaned" for e in prs):
    sys.stderr.write("pr not cleaned before ref delete\n")
    raise SystemExit(3)
'
    fi
    if python3 -c 'import sys; raise SystemExit(0 if float(sys.argv[1]) > 0 else 1)' "${GIT_SLEEP:-0}"; then
      python3 -c 'import time,sys; time.sleep(float(sys.argv[1]))' "${GIT_SLEEP:-0}"
    fi
    for arg in "$@"; do
      case "$arg" in
        :refs/heads/*)
          printf -- '-\t%s\t[deleted]\n' "$arg"
          ;;
      esac
    done
    exit 0
    ;;
esac
exec /usr/bin/git "$@"
EOF
  chmod +x "$bin/git"
}

write_ledger() {
  local dest="$1" prs="$2" phase="${3:-observed}"
  python3 - "$dest" "$prs" "$phase" <<'PY'
import json, sys
dest, prs, phase = sys.argv[1], sys.argv[2].split(), sys.argv[3]
entries = []
for number in prs:
    if not number:
        continue
    entries.append({
        "kind": "pr",
        "number": int(number),
        "head": "improve/ibt_new",
        "base": "m6-run/local-s1/main",
        "phase": phase,
    })
entries.append({"kind": "ref", "name": "m6-run/local-s1/main", "phase": phase if phase == "cleaned" else "created"})
doc = {
    "version": 1,
    "run_id": "local-s1",
    "owner": "acme",
    "repo": "widgets",
    "git_url": "https://example.invalid/acme/widgets.git",
    "api_base": "http://stub.invalid",
    "base_ref": "m6-run/local-s1/main",
    "candidate_ref": "m6-run/local-s1/candidate",
    "batch_id": "ibt_new",
    "entries": entries,
}
open(dest, "w", encoding="utf-8").write(json.dumps(doc))
PY
  chmod 600 "$dest"
}

write_token() {
  local dest="$1"
  printf '%s\n' "ghs_planted_m6_token_value" >"$dest"
  chmod 600 "$dest"
}

launch_sigdef() {
  local monitor=0
  case $- in
    *m*) monitor=1 ;;
  esac
  set -m
  python3 -c 'import os, signal, sys
signal.signal(signal.SIGINT, signal.SIG_DFL)
signal.signal(signal.SIGTERM, signal.SIG_DFL)
os.execv("/bin/bash", ["bash", *sys.argv[1:]])' "$@" &
  LAUNCH_PID=$!
  if [[ "$monitor" -eq 0 ]]; then
    set +m
  fi
}

wait_ready() {
  local file="$1" i=0
  while [[ "$i" -lt 80 ]]; do
    if [[ -f "$file" ]]; then
      return 0
    fi
    sleep 0.05
    i=$((i + 1))
  done
  fail "driver did not become ready"
}

assert_elapsed_max() {
  local elapsed="$1" cap="$2"
  python3 -c 'import sys; raise SystemExit(0 if float(sys.argv[1]) <= float(sys.argv[2]) else 1)' "$elapsed" "$cap" ||
    fail "elapsed ${elapsed}s exceeds ${cap}s"
}

order_before() {
  local file="$1" first="$2" second="$3"
  python3 - "$file" "$first" "$second" <<'PY'
import sys
lines = open(sys.argv[1], encoding="utf-8").read().splitlines()
def at(needle):
    for i, line in enumerate(lines):
        if line.startswith(needle):
            return i
    raise SystemExit("missing " + needle)
if at(sys.argv[2]) >= at(sys.argv[3]):
    raise SystemExit(sys.argv[2] + " is not before " + sys.argv[3])
PY
}

# driver_playwright is executed under a reset SIGINT.
write_playwright_driver() {
  local dest="$1"
  cat >"$dest" <<'EOF'
#!/bin/bash
set -euo pipefail
ROOT="$1"
MODE="$2"
CASE="$3"
# shellcheck source=spike/m6/run.sh
. "$ROOT/spike/m6/run.sh"
WORK="$CASE"
SIGNAL="$WORK/signal"
EVIDENCE="$WORK/evidence"
export MODE WORK SIGNAL EVIDENCE
SPIKE_STOP_GRACE=1
export SPIKE_STOP_GRACE
M6_CLEANUP_PRE_COUNT="$WORK/pre.count"
M6_ORDER_LOG="$WORK/order.log"
export M6_CLEANUP_PRE_COUNT M6_ORDER_LOG
export PATH="$WORK/bin:${PATH}"
export DOCKER_LOG="$WORK/docker.log"
export DOCKER_CID="$WORK/cid"
export DOCKER_PID="$WORK/docker.pid"
export DOCKER_RUN_SLEEP=1
mkdir -p "$WORK" "$SIGNAL" "$EVIDENCE"
FACTORY_API_KEY=k
M6_API_KEY=k
export FACTORY_API_KEY M6_API_KEY
PLAY_LOG="$WORK/playwright.log"
export PLAY_LOG
COMPOSE_STARTED=0
spike_run_init m6
trap 'cleanup EXIT' EXIT
trap 'cleanup INT' INT
trap 'cleanup TERM' TERM
spike_spawn --class stack forge sleep 600
printf '%s\n' "$SPIKE_LAST_PID" >>"$WORK/pids"
spike_spawn --class stack router sleep 600
printf '%s\n' "$SPIKE_LAST_PID" >>"$WORK/pids"
spike_spawn --class stack git sleep 600
printf '%s\n' "$SPIKE_LAST_PID" >>"$WORK/pids"
printf 'ready\n' >"$WORK/ready"
PLAYWRIGHT_DOCKER_ARGS="--init --label spike-run=${SPIKE_RUN_LABEL} --name spike-pw-${SPIKE_RUN_LABEL}"
export PLAYWRIGHT_DOCKER_ARGS
spike_fg spike_playwright_pipeline
EOF
  chmod +x "$dest"
}

case_term_or_int_playwright() {
  local mode="$1" sig="$2" how="${3:-pid}"
  local dir="$WORK_REAL/t76b-pw-${mode}-${sig}-${how}"
  rm -rf "$dir"
  mkdir -p "$dir/bin"
  install_docker_stub "$dir/bin"
  write_playwright_driver "$dir/driver.sh"
  # shellcheck disable=SC2064
  trap "reap_pids '$dir/pids'; reap_pids '$dir/docker.pid'; rm -rf '$dir'" EXIT
  launch_sigdef "$dir/driver.sh" "$ROOT" "$mode" "$dir"
  wait_ready "$dir/ready"
  sleep 0.3
  local start="" pgid="" ec=0
  start="$(python3 -c 'import time; print(time.time())')"
  pgid="$(ps -o pgid= -p "$LAUNCH_PID" | tr -d '[:space:]')"
  if [[ "$sig" == INT && "$how" == group ]]; then
    kill -INT -- -"$pgid"
  elif [[ "$sig" == INT ]]; then
    kill -INT "$LAUNCH_PID"
  else
    kill -TERM "$LAUNCH_PID"
  fi
  set +e
  wait "$LAUNCH_PID"
  ec=$?
  set -e
  local elapsed=""
  elapsed="$(python3 -c 'import time,sys; print("%.3f" % (time.time()-float(sys.argv[1])))' "$start")"
  printf 'playwright %s %s %s elapsed %ss rc=%s\n' "$mode" "$sig" "$how" "$elapsed" "$ec"
  assert_elapsed_max "$elapsed" 8
  if [[ "$sig" == INT && "$ec" -ne 130 ]]; then
    fail "exit ${ec}, want 130"
  fi
  if [[ "$sig" == TERM && "$ec" -ne 143 ]]; then
    fail "exit ${ec}, want 143"
  fi
  [[ "$(tr -d '[:space:]' <"$dir/pre.count")" == 1 ]] || fail "cleanup pre count"
  grep -F -- '--init' "$dir/docker.log" >/dev/null || fail "docker run missed --init"
  grep -F -- '--label' "$dir/docker.log" >/dev/null || fail "docker run missed --label"
  grep -F 'spike-run=m6-' "$dir/docker.log" >/dev/null || fail "docker run missed the run label"
  grep -F 'spike-pw-m6-' "$dir/docker.log" >/dev/null || fail "docker run missed the container name"
  grep -F 'STOP stop -t ' "$dir/docker.log" >/dev/null || fail "docker stop -t was not recorded"
  grep -F 'RM rm -f ' "$dir/docker.log" >/dev/null || fail "docker rm -f was not recorded"
  order_before "$dir/order.log" "cleanup-pre" "docker-stop"
  if [[ "$mode" != offline ]]; then
    order_before "$dir/order.log" "remote-begin" "docker-stop"
  fi
  local pid=""
  while IFS= read -r pid; do
    if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
      fail "stack pid ${pid} survived"
    fi
  done <"$dir/pids"
  if [[ -s "$dir/docker.pid" ]]; then
    pid="$(tr -d '[:space:]' <"$dir/docker.pid")"
    if kill -0 "$pid" 2>/dev/null; then
      fail "playwright marker ${pid} survived"
    fi
  fi
  if [[ -s "$dir/cid" ]] && grep -q cid-pw "$dir/cid"; then
    :
  fi
  trap - EXIT
  reap_pids "$dir/pids"
  reap_pids "$dir/docker.pid"
  rm -rf "$dir"
}

write_remote_driver() {
  local dest="$1" foreground="$2"
  cat >"$dest" <<EOF
#!/bin/bash
set -euo pipefail
ROOT="\$1"
MODE="\$2"
CASE="\$3"
VARIANT="\$4"
# shellcheck source=spike/m6/run.sh
. "\$ROOT/spike/m6/run.sh"
WORK="\$CASE"
SIGNAL="\$WORK/signal"
EVIDENCE="\$WORK/evidence"
export MODE WORK SIGNAL EVIDENCE
export PATH="\$WORK/bin:\${PATH}"
export CURL_LOG="\$WORK/curl.log"
export GIT_LOG="\$WORK/git.log"
export LEDGER="\$WORK/remote-ledger.json"
export DOCKER_LOG="\$WORK/docker.log"
export DOCKER_CID="\$WORK/cid"
export DOCKER_PID="\$WORK/docker.pid"
export M6_CLEANUP_PRE_COUNT="\$WORK/pre.count"
export M6_ORDER_LOG="\$WORK/order.log"
export M6_TRACE_CANCEL=1
export M6_REUSE_TOKEN=1
SPIKE_STOP_GRACE=5
export SPIKE_STOP_GRACE
mkdir -p "\$WORK" "\$SIGNAL" "\$EVIDENCE"
printf 'cid-pw\n' >"\$WORK/cid"
COMPOSE_STARTED=0
write_token() { :; }
# token and ledger are written by the parent into CASE before launch
# when VARIANT is watch, a watch child is started below.
spike_run_init m6
trap 'cleanup EXIT' EXIT
trap 'cleanup INT' INT
trap 'cleanup TERM' TERM
if [[ "\$VARIANT" == watch ]]; then
  spike_spawn --class transient watch bash "\$WORK/watch.sh"
  printf '%s\n' "\$SPIKE_LAST_PID" >>"\$WORK/pids"
fi
printf 'ready\n' >"\$WORK/ready"
${foreground}
EOF
  chmod +x "$dest"
}

case_cancel_window() {
  local mode="$1" variant="$2"
  local dir="$WORK_REAL/t76b-cancel-${mode}-${variant}"
  rm -rf "$dir"
  mkdir -p "$dir/bin" "$dir/evidence"
  install_docker_stub "$dir/bin"
  install_curl_stub "$dir/bin"
  install_git_stub "$dir/bin"
  write_token "$dir/github-app-token"
  if [[ "$variant" == fast ]]; then
    write_ledger "$dir/remote-ledger.json" "1" observed
    export CURL_SLEEP=0
    unset CURL_HONOR_M || true
  elif [[ "$variant" == slow ]]; then
    write_ledger "$dir/remote-ledger.json" "1 3" observed
    printf '1.9\n' >"$dir/sleep"
  elif [[ "$variant" == watch ]]; then
    write_ledger "$dir/remote-ledger.json" "1" cleaned
    cat >"$dir/watch.sh" <<'EOF'
#!/bin/bash
sleep 30
python3 -c 'import json,os
p=os.environ["LEDGER"]
doc=json.load(open(p, encoding="utf-8"))
for entry in doc["entries"]:
    entry["phase"] = "observed"
open(p, "w", encoding="utf-8").write(json.dumps(doc))
open(os.environ["REVERTED"], "w", encoding="utf-8").write("yes\n")
'
EOF
    chmod +x "$dir/watch.sh"
  else
    fail "unknown variant ${variant}"
  fi
  # The driver reads CURL_* from its own file so the parent export is not required.
  cat >"$dir/env.sh" <<EOF
export CURL_SLEEP="${CURL_SLEEP:-0}"
export CURL_HONOR_M="${CURL_HONOR_M:-}"
export GIT_SLEEP=0
export LEDGER="$dir/remote-ledger.json"
export REVERTED="$dir/reverted"
EOF
  if [[ "$variant" == slow ]]; then
    cat >"$dir/env.sh" <<EOF
export CURL_SLEEP=1.9
export CURL_HONOR_M=1
export GIT_SLEEP=1.9
export LEDGER="$dir/remote-ledger.json"
export REVERTED="$dir/reverted"
EOF
  fi
  write_remote_driver "$dir/driver.sh" 'spike_fg sleep 600'
  # Prepend the env file source.
  python3 - "$dir/driver.sh" "$dir/env.sh" <<'PY'
import pathlib, sys
driver, env = sys.argv[1:]
text = pathlib.Path(driver).read_text(encoding="utf-8")
needle = "set -euo pipefail\n"
insert = needle + ". " + env + "\n"
if needle not in text:
    raise SystemExit("driver preamble missing")
pathlib.Path(driver).write_text(text.replace(needle, insert, 1), encoding="utf-8")
PY
  cat >"$dir/harness.py" <<'PY'
import os, signal, subprocess, sys, time
driver, work = sys.argv[1], sys.argv[2]
signal.signal(signal.SIGINT, signal.SIG_DFL)
signal.signal(signal.SIGTERM, signal.SIG_DFL)
out = open(os.path.join(work, "driver.out"), "w", encoding="utf-8")
err = open(os.path.join(work, "driver.err"), "w", encoding="utf-8")
proc = subprocess.Popen(
    ["/bin/bash", driver, *sys.argv[3:]],
    stdout=out,
    stderr=err,
    start_new_session=True,
)
ready = os.path.join(work, "ready")
deadline = time.time() + 20
while not os.path.exists(ready):
    if proc.poll() is not None or time.time() > deadline:
        sys.stderr.write("driver exited before ready\n")
        sys.exit(1)
    time.sleep(0.05)
time.sleep(0.3)
t0 = time.time()
os.kill(proc.pid, signal.SIGINT)
term_sent = False
killed = False
while proc.poll() is None:
    elapsed = time.time() - t0
    if elapsed >= 10.0:
        os.killpg(proc.pid, signal.SIGKILL)
        killed = True
        break
    if elapsed >= 7.5 and not term_sent:
        os.kill(proc.pid, signal.SIGTERM)
        term_sent = True
    time.sleep(0.05)
proc.wait(timeout=5)
total = time.time() - t0
print("cancel-window total %.3fs rc=%s killed=%s" % (total, proc.returncode, int(killed)))
if killed or total >= 10.0:
    sys.exit(1)
PY
  # shellcheck disable=SC2064
  trap "reap_pids '$dir/pids'; rm -rf '$dir'" EXIT
  local line=""
  line="$(python3 "$dir/harness.py" "$dir/driver.sh" "$dir" "$ROOT" "$mode" "$dir" "$variant")"
  printf '%s %s %s\n' "$mode" "$variant" "$line"
  mkdir -p "$WORK_REAL"
  printf '%s %s %s\n' "$mode" "$variant" "$line" >>"$WORK_REAL/cancel-window-times.txt"
  python3 -c 'import sys; raise SystemExit(0 if "killed=0" in sys.argv[1] else 1)' "$line" ||
    fail "harness killed the driver: ${line}"
  python3 -c 'import sys; raise SystemExit(0 if "rc=-2" in sys.argv[1] or "rc=130" in sys.argv[1] else 1)' "$line" ||
    fail "want exit 130, got ${line}"
  [[ "$(tr -d '[:space:]' <"$dir/pre.count")" == 1 ]] || fail "cleanup pre count"
  grep -F 'cancel-window e=' "$dir/driver.out" >/dev/null || fail "missing cancel-window trace"
  local e="" g=""
  e="$(sed -n 's/.*cancel-window e=\([^ ]*\).*/\1/p' "$dir/driver.out" | head -n 1)"
  g="$(sed -n 's/.*cancel-window e=[^ ]* G=\([^ ]*\).*/\1/p' "$dir/driver.out" | head -n 1)"
  local expect=""
  expect="$(python3 -c 'import math,sys; e=float(sys.argv[1]); cap=5; g=math.floor((6.2-e)/2); print(max(0, min(g, cap)))' "$e")"
  [[ "$g" == "$expect" ]] || fail "G=${g} want ${expect} for e=${e}"
  grep -F "STOP stop -t ${g} " "$dir/docker.log" >/dev/null || fail "docker stop -t ${g} missing"
  if [[ -f "$dir/evidence/remote-ledger.json" ]]; then
    :
  else
    fail "ledger was not copied to evidence"
  fi
  if [[ "$variant" == fast ]]; then
    python3 -c 'import json,sys
doc=json.load(open(sys.argv[1], encoding="utf-8"))
if any(e.get("phase")!="cleaned" for e in doc["entries"]):
    raise SystemExit("fast cleanup left a pending entry")
' "$dir/remote-ledger.json" || fail "fast ledger not cleaned"
  elif [[ "$variant" == slow ]]; then
    python3 -c 'import sys; raise SystemExit(0 if float(sys.argv[1])<=5.3 and float(sys.argv[1])>=4.2 else 1)' "$e" ||
      fail "slow e=${e} outside 4.2..5.3"
    [[ "$g" == 0 ]] || fail "slow G=${g}, want 0"
    grep -F "remote cleanup incomplete" "$dir/driver.out" >/dev/null || fail "missing incomplete line"
    python3 -c 'import json,sys
doc=json.load(open(sys.argv[1], encoding="utf-8"))
if all(e.get("phase")=="cleaned" for e in doc["entries"]):
    raise SystemExit("slow cleanup cleaned every entry")
' "$dir/remote-ledger.json" || fail "slow ledger has no pending entry"
  elif [[ "$variant" == watch ]]; then
    [[ ! -f "$dir/reverted" ]] || fail "watch rewrote the ledger"
    python3 -c 'import json,sys
doc=json.load(open(sys.argv[1], encoding="utf-8"))
if any(e.get("phase")!="cleaned" for e in doc["entries"]):
    raise SystemExit("watch path regressed a cleaned entry")
' "$dir/remote-ledger.json" || fail "watch ledger regressed"
    order_before "$dir/order.log" "writers-stopped" "remote-begin"
    if [[ -s "$dir/pids" ]]; then
      local pid=""
      pid="$(head -n 1 "$dir/pids")"
      if kill -0 "$pid" 2>/dev/null; then
        fail "watch pid survived"
      fi
    fi
  fi
  trap - EXIT
  reap_pids "$dir/pids"
  rm -rf "$dir"
}

case_term_during_remote() {
  local mode="$1"
  local dir="$WORK_REAL"
  local scratch="$WORK_REAL/t76b-remote-${mode}"
  rm -rf "$scratch"
  mkdir -p "$scratch/bin" "$dir"
  install_curl_stub "$scratch/bin"
  install_git_stub "$scratch/bin"
  install_docker_stub "$scratch/bin"
  # Remote cleanup reads the script's WORK, which is spike/m6/.work.
  write_token "$dir/github-app-token"
  write_ledger "$dir/remote-ledger.json" "1" observed
  cat >"$scratch/driver.sh" <<EOF
#!/bin/bash
set -euo pipefail
exec 2>>$(printf '%q' "$scratch/driver.err")
ROOT="\$1"
MODE="\$2"
# shellcheck source=spike/m6/run.sh
. "\$ROOT/spike/m6/run.sh"
export MODE
export PATH="$(printf '%q' "$scratch/bin"):\${PATH}"
export CURL_LOG="$(printf '%q' "$scratch/curl.log")"
export GIT_LOG="$(printf '%q' "$scratch/git.log")"
export LEDGER="$(printf '%q' "$dir/remote-ledger.json")"
export CURL_SLEEP=0.6
export GIT_SLEEP=0.6
export GIT_CHECK_PR=1
export M6_REUSE_TOKEN=1
export M6_CLEANUP_PRE_COUNT="$(printf '%q' "$scratch/pre.count")"
export M6_ORDER_LOG="$(printf '%q' "$scratch/order.log")"
export DOCKER_LOG="$(printf '%q' "$scratch/docker.log")"
export DOCKER_CID="$(printf '%q' "$scratch/cid")"
export DOCKER_PID="$(printf '%q' "$scratch/docker.pid")"
SPIKE_STOP_GRACE=1
export SPIKE_STOP_GRACE
COMPOSE_STARTED=0
mkdir -p "\$SIGNAL" "\$EVIDENCE"
spike_run_init m6
trap 'cleanup EXIT' EXIT
trap 'cleanup INT' INT
trap 'cleanup TERM' TERM
printf 'ready\n' >"$(printf '%q' "$scratch/ready")"
spike_fg sleep 600
EOF
  chmod +x "$scratch/driver.sh"
  # shellcheck disable=SC2064
  trap "reap_pids '$scratch/pids'; rm -f '$dir/remote-ledger.json' '$dir/github-app-token' '$dir/remote-fail'; rm -rf '$scratch' '$dir/spike-run'" EXIT
  launch_sigdef "$scratch/driver.sh" "$ROOT" "$mode"
  wait_ready "$scratch/ready"
  sleep 0.2
  kill -INT "$LAUNCH_PID"
  sleep 0.3
  kill -INT "$LAUNCH_PID" 2>/dev/null || true
  kill -TERM "$LAUNCH_PID" 2>/dev/null || true
  local ec=0
  set +e
  wait "$LAUNCH_PID"
  ec=$?
  set -e
  [[ "$ec" -eq 130 ]] || fail "remote cleanup signal exit ${ec}, want 130"
  [[ "$(tr -d '[:space:]' <"$scratch/pre.count")" == 1 ]] || fail "hook ran more than once"
  if grep -F 'KeyboardInterrupt' "$scratch/driver.err" >/dev/null 2>&1; then
    fail "KeyboardInterrupt during remote cleanup"
  fi
  # driver stderr is the driver's, which we did not capture. The shell
  # mixed it into this script's stderr. Search the git log instead.
  local curls=""
  curls="$(grep -c ' m=' "$scratch/curl.log" || true)"
  [[ "$curls" -eq 2 ]] || fail "curl calls ${curls}, want 2"
  grep -F 'push' "$scratch/git.log" >/dev/null || fail "git push did not run"
  python3 -c 'import json,sys
doc=json.load(open(sys.argv[1], encoding="utf-8"))
if any(e.get("phase")!="cleaned" for e in doc["entries"]):
    raise SystemExit("ledger not cleaned")
' "$dir/remote-ledger.json" || fail "ledger not fully cleaned"
  local before=""
  before="$(wc -c <"$scratch/curl.log" | tr -d '[:space:]')"
  PATH="$scratch/bin:$PATH" M6_REUSE_TOKEN=1 MODE="$mode" CURL_LOG="$scratch/curl.log" GIT_LOG="$scratch/git.log" \
    "$M6/run.sh" --cleanup-remote
  local after=""
  after="$(wc -c <"$scratch/curl.log" | tr -d '[:space:]')"
  [[ "$before" == "$after" ]] || fail "second --cleanup-remote called curl"
  trap - EXIT
  rm -f "$dir/remote-ledger.json" "$dir/github-app-token" "$dir/remote-fail"
  rm -rf "$scratch" "$dir/spike-run"
}

case_exit_unbudgeted() {
  local mode="$1"
  local dir="$WORK_REAL/t76b-exit-${mode}"
  rm -rf "$dir"
  mkdir -p "$dir/bin" "$dir/evidence"
  install_curl_stub "$dir/bin"
  install_git_stub "$dir/bin"
  install_docker_stub "$dir/bin"
  write_token "$dir/github-app-token"
  write_ledger "$dir/remote-ledger.json" "1" observed
  printf 'cid-pw\n' >"$dir/cid"
  cat >"$dir/driver.sh" <<EOF
#!/bin/bash
set -euo pipefail
ROOT="\$1"
MODE="\$2"
CASE="\$3"
# shellcheck source=spike/m6/run.sh
. "\$ROOT/spike/m6/run.sh"
WORK="\$CASE"
SIGNAL="\$WORK/signal"
EVIDENCE="\$WORK/evidence"
export MODE WORK SIGNAL EVIDENCE
export PATH="\$WORK/bin:\${PATH}"
export CURL_LOG="\$WORK/curl.log"
export GIT_LOG="\$WORK/git.log"
export DOCKER_LOG="\$WORK/docker.log"
export DOCKER_CID="\$WORK/cid"
export DOCKER_PID="\$WORK/docker.pid"
export M6_REUSE_TOKEN=1
SPIKE_STOP_GRACE=5
export SPIKE_STOP_GRACE
mkdir -p "\$SIGNAL" "\$EVIDENCE"
COMPOSE_STARTED=0
spike_run_init m6
trap 'cleanup EXIT' EXIT
trap 'cleanup INT' INT
trap 'cleanup TERM' TERM
exit 0
EOF
  chmod +x "$dir/driver.sh"
  bash "$dir/driver.sh" "$ROOT" "$mode" "$dir"
  if grep -F 'deadline-set' "$dir/curl.log" >/dev/null; then
    fail "EXIT path set M6_CLEANUP_DEADLINE"
  fi
  grep -F 'm=5 conn=2' "$dir/curl.log" >/dev/null || fail "unbudgeted curl limits drifted"
  grep -F 'STOP stop -t 5 ' "$dir/docker.log" >/dev/null || fail "EXIT grace was not 5"
  rm -rf "$dir"
}

prepare_stubs_fail() {
  local bin="$1" log="$2"
  mkdir -p "$bin"
  : >"$log"
  install_record_stub "$bin" docker "$log"
  install_record_stub "$bin" go "$log"
  install_record_stub "$bin" git "$log"
  install_record_stub "$bin" curl "$log"
  install_record_stub "$bin" python3 "$log"
}

unset_live_vars() {
  env \
    -u FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL \
    -u M6_LIVE_MODEL \
    -u M6_DEFINITION_GIT_URL \
    -u M6_GITHUB_API_BASE \
    -u FACTORY_GITHUB_APP_ID \
    -u FACTORY_GITHUB_APP_PRIVATE_KEY_FILE \
    -u M1_OPENAI_BASE_URL \
    -u M1_OPENAI_API_KEY \
    -u M1_OPENAI_EXTRA_HEADERS \
    "$@"
}

case_preflight_starts_nothing() {
  local mode="$1" want="$2"
  local dir="$WORK_REAL/t76b-preflight-${mode}"
  rm -rf "$dir" "$WORK_REAL/spike-run" "$WORK_REAL/stack.env"
  mkdir -p "$dir/bin" "$WORK_REAL"
  prepare_stubs_fail "$dir/bin" "$dir/calls"
  local err="$dir/err" out="$dir/out" ec=0
  set +e
  unset_live_vars MODE="$mode" PATH="$dir/bin:$PATH" "$M6/run.sh" >"$out" 2>"$err"
  ec=$?
  set -e
  [[ "$ec" -eq 1 ]] || fail "preflight exit ${ec}"
  [[ "$(head -n 1 "$err")" == "$want" ]] || fail "first line [$(head -n 1 "$err")] want [${want}]"
  [[ ! -s "$dir/calls" ]] || fail "preflight invoked a tool"
  [[ ! -d "$WORK_REAL/spike-run" ]] || fail "preflight created spike-run"
  [[ ! -f "$WORK_REAL/stack.env" ]] || fail "preflight created stack.env"
  rm -rf "$dir"
}

case_specs_negative() {
  local kind="$1" mode="$2"
  local dir="$WORK_REAL/t76b-specneg-${kind}-${mode}"
  rm -rf "$dir" "$WORK_REAL/spike-run"
  mkdir -p "$dir/bin" "$WORK_REAL"
  prepare_stubs_fail "$dir/bin" "$dir/calls"
  local want="" pid=""
  case "$kind" in
    none)
      rm -f "$WORK_REAL/stack.env"
      want="M6 FAIL — no KEEP_UP stack (run KEEP_UP=1 ./spike/m6/run.sh first)"
      ;;
    stale)
      pid=1234
      if kill -0 1234 2>/dev/null; then
        pid=999999
      fi
      cat >"$WORK_REAL/stack.env" <<EOF
SPIKE_RUN_ID=abcd1234
SPIKE_RUN_LABEL=m6-abcd1234
STACK_MODE=${mode}
SPIKE_CHILD_1_CLASS=stack
SPIKE_CHILD_1_NAME=forge
SPIKE_CHILD_1_PID=${pid}
SPIKE_CHILD_1_PGID=${pid}
SPIKE_CHILD_1_LSTART=gone
SPIKE_CHILD_1_ARGS=gone
SPIKE_CHILD_1_ARGV=
SPIKE_CHILD_1_OUT=
SPIKE_CHILD_1_ERR=
SPIKE_CHILD_1_APPEND=0
SPIKE_CHILD_COUNT=1
EOF
      chmod 600 "$WORK_REAL/stack.env"
      want="M6 FAIL — stale stack: forge pid ${pid} is gone"
      ;;
    mode)
      cat >"$WORK_REAL/stack.env" <<EOF
SPIKE_RUN_ID=abcd1234
SPIKE_RUN_LABEL=m6-abcd1234
STACK_MODE=offline
SPIKE_CHILD_COUNT=0
EOF
      chmod 600 "$WORK_REAL/stack.env"
      want="M6 FAIL — MODE is ${mode}, recorded stack is offline"
      ;;
    *) fail "unknown specs negative ${kind}" ;;
  esac
  local start="" err="$dir/err" ec=0 elapsed=""
  start="$(date +%s)"
  set +e
  MODE="$mode" PATH="$dir/bin:$PATH" "$M6/run.sh" specs >"$dir/out" 2>"$err"
  ec=$?
  set -e
  elapsed="$(($(date +%s) - start))"
  [[ "$ec" -eq 1 ]] || fail "specs ${kind} exit ${ec}"
  [[ "$elapsed" -le 5 ]] || fail "specs ${kind} took ${elapsed}s"
  grep -F "$want" "$err" >/dev/null || fail "missing [${want}]"
  [[ ! -s "$dir/calls" ]] || fail "specs ${kind} started a tool"
  rm -f "$WORK_REAL/stack.env"
  rm -rf "$dir" "$WORK_REAL/spike-run"
}

case_default_refuses() {
  local mode="$1"
  local dir="$WORK_REAL/t76b-refuse-${mode}"
  rm -rf "$dir"
  mkdir -p "$dir/bin" "$WORK_REAL"
  prepare_stubs_fail "$dir/bin" "$dir/calls"
  sleep 60 &
  local pid=$!
  # shellcheck disable=SC2064
  trap "kill -KILL $pid 2>/dev/null || true; rm -f '$WORK_REAL/stack.env'; rm -rf '$dir'" EXIT
  cat >"$WORK_REAL/stack.env" <<EOF
SPIKE_CHILD_1_CLASS=stack
SPIKE_CHILD_1_PID=${pid}
SPIKE_CHILD_COUNT=1
SPIKE_RUN_LABEL=m6-refuse
EOF
  chmod 600 "$WORK_REAL/stack.env"
  printf 'key\n' >"$dir/key.pem"
  local -a extra=()
  if [[ "$mode" == live ]]; then
    extra=(
      FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL=https://example.invalid/v1
      M6_LIVE_MODEL=m
      M6_DEFINITION_GIT_URL=https://example.invalid/acme/widgets.git
      M6_GITHUB_API_BASE=https://example.invalid
      FACTORY_GITHUB_APP_ID=17
      FACTORY_GITHUB_APP_PRIVATE_KEY_FILE="$dir/key.pem"
    )
  elif [[ "$mode" == live-ci ]]; then
    extra=(
      M1_OPENAI_BASE_URL=https://example.invalid/v1
      M1_OPENAI_API_KEY=dummy
      M6_LIVE_MODEL=m
      M6_DEFINITION_GIT_URL=https://example.invalid/acme/widgets.git
      M6_GITHUB_API_BASE=https://example.invalid
      FACTORY_GITHUB_APP_ID=17
      FACTORY_GITHUB_APP_PRIVATE_KEY_FILE="$dir/key.pem"
    )
  fi
  local ec=0
  set +e
  env "${extra[@]}" MODE="$mode" PATH="$dir/bin:$PATH" "$M6/run.sh" >"$dir/out" 2>"$dir/err"
  ec=$?
  set -e
  [[ "$ec" -eq 1 ]] || fail "default refuse exit ${ec}"
  grep -F "M6 FAIL — KEEP_UP stack is still up (./spike/m6/run.sh down or ./spike/m6/run.sh specs)" "$dir/err" >/dev/null ||
    fail "refuse line missing"
  [[ ! -s "$dir/calls" ]] || fail "default path started a tool"
  kill -KILL "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  rm -f "$WORK_REAL/stack.env"
  trap - EXIT
  rm -rf "$dir"
}

case_down_twice() {
  local dir="$WORK_REAL/t76b-down"
  rm -rf "$dir" "$WORK_REAL/spike-run"
  mkdir -p "$dir/bin" "$WORK_REAL"
  install_docker_stub "$dir/bin"
  install_curl_stub "$dir/bin"
  install_git_stub "$dir/bin"
  cat >"$WORK_REAL/stack.env" <<'EOF'
SPIKE_RUN_ID=abcd1234
SPIKE_RUN_LABEL=m6-abcd1234
STACK_MODE=offline
COMPOSE_PROJECT=factory-m6
SPIKE_CHILD_COUNT=0
EOF
  chmod 600 "$WORK_REAL/stack.env"
  export DOCKER_LOG="$dir/docker.log" DOCKER_CID="$dir/cid" DOCKER_PID="$dir/docker.pid"
  MODE=offline PATH="$dir/bin:$PATH" "$M6/run.sh" down >"$dir/down1.out"
  grep -F 'down' "$dir/docker.log" >/dev/null || fail "first down did not compose down"
  [[ ! -f "$WORK_REAL/stack.env" ]] || fail "stack.env survived down"
  MODE=offline PATH="$dir/bin:$PATH" "$M6/run.sh" down >"$dir/down2.out"
  grep -qx 'nothing to stop' "$dir/down2.out" || fail "second offline down: $(cat "$dir/down2.out")"
  write_token "$WORK_REAL/github-app-token"
  write_ledger "$WORK_REAL/remote-ledger.json" "1" observed
  export CURL_LOG="$dir/curl.log" GIT_LOG="$dir/git.log" CURL_SLEEP=0 GIT_SLEEP=0
  unset CURL_HONOR_M || true
  : >"$dir/curl.log"
  : >"$dir/git.log"
  M6_REUSE_TOKEN=1 MODE=live PATH="$dir/bin:$PATH" "$M6/run.sh" down >"$dir/live1.out"
  local n=""
  n="$(grep -c ' m=' "$dir/curl.log" || true)"
  [[ "$n" -ge 1 ]] || fail "live down did not call curl"
  M6_REUSE_TOKEN=1 MODE=live PATH="$dir/bin:$PATH" "$M6/run.sh" down >"$dir/live2.out"
  grep -qx 'nothing to stop' "$dir/live2.out" || fail "second live down: $(cat "$dir/live2.out")"
  local n2=""
  n2="$(grep -c ' m=' "$dir/curl.log" || true)"
  [[ "$n" == "$n2" ]] || fail "second live down called curl again (${n} -> ${n2})"
  write_ledger "$WORK_REAL/remote-ledger.json" "1" observed
  local before=""
  before="$(wc -c <"$dir/curl.log" | tr -d '[:space:]')"
  M6_KEEP_REMOTE=1 M6_REUSE_TOKEN=1 MODE=live PATH="$dir/bin:$PATH" "$M6/run.sh" down >"$dir/keep.out"
  local after=""
  after="$(wc -c <"$dir/curl.log" | tr -d '[:space:]')"
  [[ "$before" == "$after" ]] || fail "M6_KEEP_REMOTE down called curl"
  grep -F 'cleanup: ./spike/m6/run.sh --cleanup-remote' "$dir/keep.out" >/dev/null ||
    fail "keep-remote did not print the cleanup command"
  rm -f "$WORK_REAL/remote-ledger.json" "$WORK_REAL/github-app-token" "$WORK_REAL/remote-fail" "$WORK_REAL/stack.env"
  rm -rf "$dir" "$WORK_REAL/spike-run"
}

case_keep_up_records() {
  local mode="$1"
  local dir="$WORK_REAL/t76b-keep-${mode}"
  rm -rf "$dir"
  mkdir -p "$dir"
  write_ledger "$dir/remote-ledger.json" "1" observed
  cat >"$dir/driver.sh" <<'EOF'
#!/bin/bash
set -euo pipefail
ROOT="$1"
MODE="$2"
CASE="$3"
# shellcheck source=spike/m6/run.sh
. "$ROOT/spike/m6/run.sh"
WORK="$CASE"
SIGNAL="$WORK/signal"
EVIDENCE="$WORK/evidence"
export MODE WORK SIGNAL EVIDENCE
mkdir -p "$SIGNAL" "$EVIDENCE"
KEEP_UP=1
export KEEP_UP
COMPOSE_STARTED=0
FACTORY_INFERENCE_OPENAI_API_KEY=sk-live-should-not-appear
M6_APP_TOKEN=ghs_planted_m6_token_value
export FACTORY_INFERENCE_OPENAI_API_KEY M6_APP_TOKEN
CONSOLE_PORT=1
GIT_PORT=2
FORGE_PORT=3
FAKE_PORT=4
TASK_HOST=h
M6_TASK_GIT_URL=g
M6_TASK_GITHUB_API=a
FACTORY_API_KEY=k
FACTORY_WORKER_API_KEY=w
S3_SECRET_KEY=s
FACTORY_SECRET_M6_PLANTED=p
FACTORY_GITHUB_WEBHOOK_SECRET=wh
M6_APP_KEY_FILE="$CASE/app.pem"
M6_DEFINITION_REF=ref-value
M6_RUN_BASE=ref-value
export CONSOLE_PORT GIT_PORT FORGE_PORT FAKE_PORT TASK_HOST
export M6_TASK_GIT_URL M6_TASK_GITHUB_API FACTORY_API_KEY FACTORY_WORKER_API_KEY
export S3_SECRET_KEY FACTORY_SECRET_M6_PLANTED FACTORY_GITHUB_WEBHOOK_SECRET
export M6_APP_KEY_FILE M6_DEFINITION_REF M6_RUN_BASE
spike_run_init m6
trap 'cleanup EXIT' EXIT
trap 'cleanup INT' INT
trap 'cleanup TERM' TERM
spike_spawn --class stack held sleep 60
printf '%s\n' "$SPIKE_LAST_PID" >"$WORK/stack.pid"
spike_spawn --class transient gone sleep 60
printf '%s\n' "$SPIKE_LAST_PID" >"$WORK/gone.pid"
exit 0
EOF
  chmod +x "$dir/driver.sh"
  bash "$dir/driver.sh" "$ROOT" "$mode" "$dir" >"$dir/out"
  local modebits="" stack_pid="" gone_pid=""
  stack_pid="$(cat "$dir/stack.pid")"
  gone_pid="$(cat "$dir/gone.pid")"
  # shellcheck disable=SC2064
  trap "kill -KILL $stack_pid $gone_pid 2>/dev/null || true" EXIT
  modebits="$(python3 -c 'import os,sys; print(oct(os.stat(sys.argv[1]).st_mode & 0o777))' "$dir/stack.env")"
  [[ "$modebits" == 0o600 ]] || fail "stack.env mode ${modebits}"
  local name=""
  for name in SPIKE_RUN_ID SPIKE_RUN_LABEL STACK_MODE COMPOSE_PROJECT CONSOLE_PORT GIT_PORT FORGE_PORT FAKE_PORT TASK_HOST M6_TASK_GIT_URL M6_TASK_GITHUB_API FACTORY_API_KEY FACTORY_WORKER_API_KEY S3_SECRET_KEY FACTORY_SECRET_M6_PLANTED FACTORY_GITHUB_WEBHOOK_SECRET M6_APP_KEY_FILE M6_DEFINITION_REF M6_RUN_BASE; do
    grep -q "^${name}=" "$dir/stack.env" || fail "missing ${name}"
  done
  if grep -F 'sk-live-should-not-appear' "$dir/stack.env" >/dev/null; then
    fail "live inference value was written"
  fi
  if grep -F 'ghs_planted_m6_token_value' "$dir/stack.env" >/dev/null; then
    fail "token contents were written"
  fi
  grep -F "M6_APP_KEY_FILE=$(printf '%q' "$dir/app.pem")" "$dir/stack.env" >/dev/null ||
    grep -F "M6_APP_KEY_FILE=$dir/app.pem" "$dir/stack.env" >/dev/null ||
    fail "app key path missing"
  kill -0 "$stack_pid" 2>/dev/null || fail "stack child died"
  if kill -0 "$gone_pid" 2>/dev/null; then
    kill -KILL "$gone_pid" 2>/dev/null || true
    fail "transient child survived"
  fi
  grep -F "remote ledger: ${dir}/remote-ledger.json" "$dir/out" >/dev/null || fail "ledger path not printed"
  grep -F 'cleanup: ./spike/m6/run.sh --cleanup-remote' "$dir/out" >/dev/null || fail "cleanup command not printed"
  grep -F 'specs: ./spike/m6/run.sh specs' "$dir/out" >/dev/null || fail "specs line missing"
  grep -F 'down: ./spike/m6/run.sh down' "$dir/out" >/dev/null || fail "down line missing"
  kill -KILL "$stack_pid" 2>/dev/null || true
  wait "$stack_pid" 2>/dev/null || true
  trap - EXIT
  rm -rf "$dir"
}

case_stack_env_not_evidence() {
  # shellcheck source=spike/lib/driver.sh
  . "$ROOT/spike/lib/driver.sh"
  local dir="$WORK_REAL/t76b-evidence"
  rm -rf "$dir"
  mkdir -p "$dir/ev"
  local old_evidence="${EVIDENCE:-}" old_extra="${EVIDENCE_EXTRA_SECRETS:-}"
  EVIDENCE="$dir/ev"
  FACTORY_SECRET_M6_PLANTED=planted-m6-value-xxxx
  export FACTORY_SECRET_M6_PLANTED
  EVIDENCE_EXTRA_SECRETS=FACTORY_SECRET_M6_PLANTED
  export EVIDENCE_EXTRA_SECRETS
  printf 'hello\n' >"$EVIDENCE/compose.log"
  assert_evidence
  printf 'planted-m6-value-xxxx\n' >"$EVIDENCE/compose.log"
  if assert_evidence >"$dir/out" 2>"$dir/err"; then
    fail "contaminated evidence passed"
  fi
  grep -F 'planted secret' "$dir/err" >/dev/null || fail "evidence did not report the planted value"
  EVIDENCE="$old_evidence"
  EVIDENCE_EXTRA_SECRETS="$old_extra"
  export EVIDENCE EVIDENCE_EXTRA_SECRETS
  unset FACTORY_SECRET_M6_PLANTED
  rm -rf "$dir"
}

case_specs_fake_stack() {
  local dir="$WORK_REAL/t76b-fake"
  rm -rf "$dir" "$WORK_REAL/spike-run" "$WORK_REAL/stack.env" "$WORK_REAL/git"
  mkdir -p "$dir/bin" "$WORK_REAL"
  install_docker_stub "$dir/bin"
  install_record_stub "$dir/bin" go "$dir/go.log"
  cat >"$dir/bin/curl" <<'EOF'
#!/bin/bash
url=""
out=""
prev=""
for arg in "$@"; do
  case "$prev" in
    -o) out="$arg" ;;
  esac
  case "$arg" in
    http://* | https://*) url="$arg" ;;
  esac
  prev="$arg"
done
body="ok"
case "$url" in
  */improvement-batches) body="[]" ;;
  */api/v1/factory) body='[{"id":"fac_1","name":"m6"}]' ;;
esac
if [[ -n "$out" ]]; then
  printf '%s' "$body" >"$out"
else
  printf '%s' "$body"
fi
exit 0
EOF
  chmod +x "$dir/bin/curl"
  export DOCKER_LOG="$dir/docker.log" DOCKER_CID="$dir/cid" DOCKER_PID="$dir/docker.pid"
  unset DOCKER_RUN_SLEEP || true
  cat >"$dir/setup.sh" <<EOF
#!/bin/bash
set -euo pipefail
# shellcheck source=spike/m6/run.sh
. "$M6/run.sh"
MODE=offline
export MODE
CONSOLE_PORT=9
GIT_PORT=9
FORGE_PORT=9
FAKE_PORT=9
ROUTER_PORT=9
TASK_HOST=h
M6_TASK_GIT_URL=g
M6_TASK_GITHUB_API=a
FACTORY_API_KEY=k
FACTORY_WORKER_API_KEY=w
S3_SECRET_KEY=s
FACTORY_SECRET_M6_PLANTED=p
FACTORY_GITHUB_WEBHOOK_SECRET=wh
M6_APP_KEY_FILE="$dir/app.pem"
FACTORY_INFERENCE_OPENAI_API_KEY=k
FACTORY_GITHUB_APP_ID=17
export CONSOLE_PORT GIT_PORT FORGE_PORT FAKE_PORT ROUTER_PORT TASK_HOST
export M6_TASK_GIT_URL M6_TASK_GITHUB_API FACTORY_API_KEY FACTORY_WORKER_API_KEY
export S3_SECRET_KEY FACTORY_SECRET_M6_PLANTED FACTORY_GITHUB_WEBHOOK_SECRET
export M6_APP_KEY_FILE FACTORY_INFERENCE_OPENAI_API_KEY FACTORY_GITHUB_APP_ID
mkdir -p "\$SIGNAL" "\$EVIDENCE" "$dir"
bare="\$WORK/git/m6/factory.git"
mkdir -p "\$(dirname "\$bare")"
git init -q --bare "\$bare"
tmp="\$(mktemp -d)"
git init -q -b main "\$tmp"
git -C "\$tmp" config user.email t@example.com
git -C "\$tmp" config user.name t
printf 'x\n' >"\$tmp/f"
git -C "\$tmp" add f
git -C "\$tmp" commit -m init >/dev/null
git -C "\$tmp" remote add origin "\$bare"
git -C "\$tmp" push origin HEAD:refs/heads/main >/dev/null
git -C "\$tmp" push origin HEAD:refs/heads/candidate >/dev/null
git -C "\$tmp" push origin HEAD:refs/heads/improve/old >/dev/null
rm -rf "\$tmp"
spike_run_init m6
spike_spawn --class stack git sleep 600
printf '%s\n' "\$SPIKE_LAST_PID" >"$dir/git.pid"
spike_spawn --class stack fake sleep 600
printf '%s\n' "\$SPIKE_LAST_PID" >"$dir/fake.pid"
printf 'OLD\n' >"$dir/forge.out"
printf 'OLDERR\n' >"$dir/forge.err"
spike_spawn --class stack --append --out "$dir/forge.out" --err "$dir/forge.err" forge \
  bash -c 'echo NEW; echo NEWERR >&2; sleep 600'
printf '%s\n' "\$SPIKE_LAST_PID" >"$dir/forge.pid"
printf 'OLD\n' >"$dir/router.out"
printf 'OLDERR\n' >"$dir/router.err"
spike_spawn --class stack --append --out "$dir/router.out" --err "$dir/router.err" router \
  bash -c 'echo ROUTER; echo ROUTERERR >&2; sleep 600'
printf '%s\n' "\$SPIKE_LAST_PID" >"$dir/router.pid"
m6_save_stack
EOF
  chmod +x "$dir/setup.sh"
  bash "$dir/setup.sh"
  local git_pid="" fake_pid="" forge_pid="" router_pid=""
  git_pid="$(cat "$dir/git.pid")"
  fake_pid="$(cat "$dir/fake.pid")"
  forge_pid="$(cat "$dir/forge.pid")"
  router_pid="$(cat "$dir/router.pid")"
  # shellcheck disable=SC2064
  trap "kill -KILL $git_pid $fake_pid $forge_pid $router_pid 2>/dev/null || true; rm -f '$WORK_REAL/stack.env'; rm -rf '$WORK_REAL/spike-run' '$WORK_REAL/git' '$dir'" EXIT
  MODE=offline PATH="$dir/bin:$PATH" DOCKER_LOG="$dir/docker.log" DOCKER_CID="$dir/cid" DOCKER_PID="$dir/docker.pid" \
    "$M6/run.sh" specs >"$dir/out" 2>"$dir/err"
  grep -F 'M6 OFFLINE PASS' "$dir/out" >/dev/null || fail "specs did not pass: $(cat "$dir/err")"
  local runs=""
  runs="$(grep -c 'mcr.microsoft.com/playwright' "$dir/docker.log" || true)"
  [[ "$runs" -eq 1 ]] || fail "playwright docker runs ${runs}"
  if grep -E '(^| )build( |$)' "$dir/docker.log" >/dev/null; then
    fail "build ran"
  fi
  if grep -E '(^| )up( |$)' "$dir/docker.log" >/dev/null; then
    fail "compose up ran"
  fi
  [[ ! -s "$dir/go.log" ]] || fail "go ran during specs"
  # shellcheck disable=SC1091
  (
    set +u
    # shellcheck disable=SC1090
    . "$WORK_REAL/stack.env"
    [[ "$SPIKE_CHILD_COUNT" -ge 1 ]]
  )
  python3 - "$WORK_REAL/stack.env" "$git_pid" "$fake_pid" "$forge_pid" "$router_pid" <<'PY'
import shlex, sys
text = open(sys.argv[1], encoding="utf-8").read()
vals = {}
for line in text.splitlines():
    if "=" not in line:
        continue
    key, raw = line.split("=", 1)
    vals[key] = shlex.split(raw)[0] if raw.startswith("'") or raw.startswith('"') else raw
count = int(vals["SPIKE_CHILD_COUNT"])
by = {}
for i in range(1, count + 1):
    by[vals[f"SPIKE_CHILD_{i}_NAME"]] = vals[f"SPIKE_CHILD_{i}_PID"]
git_pid, fake_pid, forge_pid, router_pid = sys.argv[2:]
if by.get("git") != git_pid or by.get("fake") != fake_pid:
    raise SystemExit("git or fake was restarted")
if by.get("forge") == forge_pid or by.get("router") == router_pid:
    raise SystemExit("forge or router was not respawned")
PY
  grep -qx OLD "$dir/forge.out" || fail "forge log lost OLD"
  [[ "$(grep -cx NEW "$dir/forge.out")" -ge 2 ]] || fail "forge append missing"
  grep -qx OLDERR "$dir/forge.err" || fail "forge err lost OLDERR"
  [[ "$(grep -cx NEWERR "$dir/forge.err")" -ge 2 ]] || fail "forge err append missing"
  grep -qx OLD "$dir/router.out" || fail "router log lost OLD"
  [[ "$(grep -cx ROUTER "$dir/router.out")" -ge 2 ]] || fail "router append missing"
  local bare="$WORK_REAL/git/m6/factory.git"
  if git --git-dir="$bare" show-ref --verify --quiet refs/heads/improve/old; then
    fail "improve/old survived"
  fi
  local main_sha="" cand_sha=""
  main_sha="$(git --git-dir="$bare" rev-parse refs/heads/main)"
  cand_sha="$(git --git-dir="$bare" rev-parse refs/heads/candidate)"
  [[ "$main_sha" == "$cand_sha" ]] || fail "candidate is not main"
  # Reap every recorded pid, including the respawned ones.
  python3 - "$WORK_REAL/stack.env" <<'PY'
import os, shlex, signal, sys
text = open(sys.argv[1], encoding="utf-8").read()
vals = {}
for line in text.splitlines():
    if "=" not in line:
        continue
    key, raw = line.split("=", 1)
    vals[key] = shlex.split(raw)[0] if raw[:1] in {"'", '"'} else raw
count = int(vals.get("SPIKE_CHILD_COUNT") or 0)
for i in range(1, count + 1):
    pid = vals.get(f"SPIKE_CHILD_{i}_PID") or ""
    pgid = vals.get(f"SPIKE_CHILD_{i}_PGID") or ""
    if pid.isdigit():
        try:
            os.kill(int(pid), signal.SIGKILL)
        except OSError:
            pass
    if pgid.isdigit():
        try:
            os.killpg(int(pgid), signal.SIGKILL)
        except OSError:
            pass
PY
  trap - EXIT
  rm -f "$WORK_REAL/stack.env"
  rm -rf "$WORK_REAL/spike-run" "$WORK_REAL/git" "$dir"
}

dispatch() {
  case "$NAME" in
    term-during-playwright) case_term_or_int_playwright "$1" TERM pid ;;
    int-during-playwright) case_term_or_int_playwright "$1" INT "$2" ;;
    term-during-remote-cleanup) case_term_during_remote "$1" ;;
    cancel-window-budget) case_cancel_window "$1" "$2" ;;
    exit-path-unbudgeted) case_exit_unbudgeted "$1" ;;
    specs-no-stack) case_specs_negative none "$1" ;;
    specs-stale-pid) case_specs_negative stale "$1" ;;
    specs-mode-mismatch) case_specs_negative mode "$1" ;;
    specs-fake-stack) case_specs_fake_stack ;;
    down-twice) case_down_twice ;;
    default-refuses-live-stack) case_default_refuses "$1" ;;
    keep-up-records) case_keep_up_records "$1" ;;
    stack-env-not-evidence) case_stack_env_not_evidence ;;
    preflight-starts-nothing) case_preflight_starts_nothing "$1" "$2" ;;
    *) fail "unknown t76b case ${NAME}" ;;
  esac
}

dispatch "$@"
