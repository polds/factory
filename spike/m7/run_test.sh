#!/usr/bin/env bash
# No-cluster checks for the M7 driver, the lifted remote module, and the
# evidence scanner. Does not start kind and does not call a live model.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=run.sh
source "$ROOT/spike/m7/run.sh"

fail() {
  printf 'm7 test fail: %s\n' "$*" >&2
  exit 1
}

case_secrets() {
  local mode text
  for mode in live live-ci; do
    text="$(
      MODE="$mode" \
        M1_OPENAI_BASE_URL="" \
        M1_OPENAI_API_KEY="" \
        M6_GITHUB_APP_ID="" \
        M6_GITHUB_APP_PRIVATE_KEY="" \
        FACTORY_REVIEWERS_ADMINS="" \
        bash -c 'source "$1"; require_mode_secrets' _ "$ROOT/spike/m7/run.sh" 2>&1 || true
    )"
    for name in M1_OPENAI_BASE_URL M1_OPENAI_API_KEY M6_GITHUB_APP_ID M6_GITHUB_APP_PRIVATE_KEY reviewer; do
      if [[ "$text" != *"missing ${name}"* ]]; then
        fail "${mode} did not name ${name}"
      fi
    done
    if [[ "$text" == *sk-* || "$text" == *ghp_* || "$text" == *BEGIN* ]]; then
      fail "${mode} printed a secret value"
    fi
  done
  MODE=offline require_mode_secrets
}

case_cleanup_refused() {
  local text
  text="$(MODE=offline "$ROOT/spike/m7/run.sh" --cleanup-remote 2>&1 || true)"
  if [[ "$text" != *"cleanup-remote refused in offline"* ]]; then
    fail "offline cleanup-remote was accepted"
  fi
}

case_cancel_window() {
  local dir stub out
  dir="$(mktemp -d "$ROOT/spike/m7/.work/cancel.XXXXXX")"
  stub="$dir/kind"
  cat >"$stub" <<'EOF'
#!/bin/bash
printf '%s\n' "$*" >> "$M7_KIND_LOG"
exit 0
EOF
  chmod 755 "$stub"
  : >"$dir/kind.log"
  : >"$dir/remote.log"
  cat >"$dir/remote" <<'EOF'
#!/bin/bash
printf '%s\n' "$*" >> "$M7_REMOTE_LOG"
exit 0
EOF
  chmod 755 "$dir/remote"
  # cleanup INT exits 130. This bash exits on that status inside a
  # substitution when set -e is on, so the capture is outside set -e.
  set +e
  out="$(
    MODE=live-ci \
      M7_TRACE_CANCEL=1 \
      M7_WORK="$dir" \
      M7_LEDGER="$dir/missing-ledger.json" \
      M7_REMOTE_STUB="$dir/remote" \
      M7_KIND_LOG="$dir/kind.log" \
      M7_REMOTE_LOG="$dir/remote.log" \
      KIND="$stub" \
      CLUSTER_NAME=factory-m7-test \
      bash -c 'source "$1"; cluster_up=1; cleanup INT' _ "$ROOT/spike/m7/run.sh" 2>&1
  )"
  set -e
  if [[ "$out" != *"cancel-window e="* || "$out" != *" G="* ]]; then
    fail "cancel window was not printed"
  fi
  if [[ "$out" != *"m7 cleanup:"* ]]; then
    fail "cleanup summary was not printed"
  fi
  if ! grep -q "delete cluster --name factory-m7" "$dir/kind.log"; then
    fail "cleanup did not delete the kind cluster"
  fi
  rm -rf "$dir"
}

case_apply_order() {
  local dir text
  dir="$(mktemp -d)"
  text="$(
    python3 "$ROOT/spike/m7/apply_order.py" "$dir" <<'YAML'
apiVersion: factory.dev/v1alpha1
kind: Factory
metadata:
  name: m7
---
apiVersion: factory.dev/v1alpha1
kind: Runner
metadata:
  name: local
---
apiVersion: factory.dev/v1alpha1
kind: FactoryAgent
metadata:
  name: implementer
spec:
  agentType: IMPLEMENT
---
apiVersion: factory.dev/v1alpha1
kind: FactoryAgent
metadata:
  name: foreman
spec:
  agentType: FOREMAN
YAML
  )"
  [[ "$text" == *"01-runners"* ]]
  [[ "$text" == *"05-factory"* ]]
  [[ "$text" == *"06-foreman"* ]]
  [[ "$text" == *"07-agents"* ]]
  if [[ "$(find "$dir" -name '*.yaml' -printf '%f\n' | sort | head -1)" != "01-runners.yaml" ]]; then
    fail "runners were not first"
  fi
  rm -rf "$dir"
}

case_planted_token() {
  local dir
  dir="$(mktemp -d)"
  mkdir -p "$dir/evidence"
  printf '%s\n' 'log ghp_abcdefghijklmnopqrstuvwxyz012345' >"$dir/evidence/worker.log"
  set +e
  python3 "$ROOT/spike/lib/redact_evidence.py" --scan-only "$dir/evidence" >"$dir/out" 2>"$dir/err"
  local code=$?
  set -e
  if [[ "$code" -eq 0 ]]; then
    fail "planted token was not caught"
  fi
  if [[ -d "$dir/evidence" ]]; then
    fail "scan failure left the evidence directory"
  fi
  if grep -q 'ghp_abcdefghijklmnopqrstuvwxyz012345' "$dir/err"; then
    fail "scanner printed the planted token"
  fi
  rm -rf "$dir"
}

case_remote() {
  local work
  work="$(mktemp -d "$ROOT/spike/m7/.work/remote.XXXXXX")"
  M7_CASE_WORK="$work" python3 "$ROOT/spike/m7/remote_cases.py" preflight
  M7_CASE_WORK="$work" python3 "$ROOT/spike/m7/remote_cases.py" ledger
  M7_CASE_WORK="$work" python3 "$ROOT/spike/m7/remote_cases.py" rewrite
  M7_CASE_WORK="$work" python3 "$ROOT/spike/m7/remote_cases.py" probe
  M7_CASE_WORK="$work" python3 "$ROOT/spike/m7/remote_cases.py" record
  M7_CASE_WORK="$work" python3 "$ROOT/spike/m7/remote_cases.py" cleanup-low-speed
  M7_CASE_WORK="$work" python3 "$ROOT/spike/m7/remote_cases.py" cleanup-timeout
  local summary
  summary="$(M7_LEDGER="$work/remote-ledger.json" M7_WORK="$work" m7_cleanup_summary)"
  if [[ "$summary" != "m7 cleanup: refs deleted=3 left=0 branches deleted=3 left=0 prs deleted=1 left=0" ]]; then
    fail "cleanup summary was ${summary}"
  fi
  M7_CASE_WORK="$work" python3 "$ROOT/spike/m7/remote_cases.py" sweep
  rm -rf "$work"
}

case_router() {
  PYTHONPATH="$ROOT/spike/lib${PYTHONPATH:+:$PYTHONPATH}" \
    python3 "$ROOT/spike/m7/router_cases.py"
}

case_pull_url() {
  local got
  got="$(m7_trim_pull_url 'https://github.com/acme/app/pull/9.')"
  if [[ "$got" != "https://github.com/acme/app/pull/9" ]]; then
    fail "trailing period was kept"
  fi
  got="$(m7_trim_pull_url 'https://pr.invalid/m7/factory-m7-target/pull/2),')"
  if [[ "$got" != "https://pr.invalid/m7/factory-m7-target/pull/2" ]]; then
    fail "trailing punctuation was kept"
  fi
}

case_pr_budget() {
  local got
  unset M7_PR_WAIT_SECONDS
  got="$(m7_pr_wait_seconds)"
  if [[ "$got" != "480" ]]; then
    fail "default pr budget was ${got}"
  fi
  if M7_PR_WAIT_SECONDS=1 m7_pr_wait_seconds >/dev/null 2>&1; then
    fail "pr budget of 1 was accepted"
  fi
}

case_served_agents() {
  local dir
  dir="$(mktemp -d "$ROOT/spike/m7/.work/agents.XXXXXX")"
  printf '%s\n' '[{"name":"foreman"},{"name":"hold"}]' >"$dir/agents.json"
  if m7_agent_names_ready "$dir/agents.json" 2>"$dir/err"; then
    fail "a list without implementer was accepted"
  fi
  if [[ "$(cat "$dir/err")" != "factoryd agents missing implementer" ]]; then
    fail "missing implementer was not reported"
  fi
  printf '%s\n' 'not-json' >"$dir/agents.json"
  if m7_agent_names_ready "$dir/agents.json" 2>"$dir/err"; then
    fail "an unreadable agent list was accepted"
  fi
  printf '%s\n' '[{"name":"implementer"},{"name":"hold"}]' >"$dir/agents.json"
  if ! m7_agent_names_ready "$dir/agents.json"; then
    fail "implementer and hold were rejected"
  fi
  rm -rf "$dir"
}

case_hold_agent() {
  local agent run
  agent="$ROOT/deploy/m7-factory/agents/hold/agent.md"
  run="$ROOT/spike/m7/agentrun-hold.yaml"
  if [[ "$(grep -c '^model: m7-hold$' "$agent")" -ne 1 ]]; then
    fail "hold agent model was not m7-hold"
  fi
  if grep -q 'git clone' "$agent" || grep -q 'GITHUB_API_BASE' "$agent"; then
    fail "hold agent can clone or open a pull request"
  fi
  if ! grep -q 'name: hold' "$run"; then
    fail "hold AgentRun does not name the hold agent"
  fi
  if grep -q 'name: implementer' "$run"; then
    fail "hold AgentRun still names the implementer"
  fi
}

case_live_before_apply() {
  local dir
  dir="$(mktemp -d "$ROOT/spike/m7/.work/order.XXXXXX")"
  mkdir -p "$dir/bin" "$dir/definition"
  cat >"$dir/bin/go" <<'EOF'
#!/bin/bash
dest=""
while [[ $# -gt 0 ]]; do
  if [[ "$1" == "-o" ]]; then
    dest="$2"
    shift 2
    continue
  fi
  shift
done
if [[ -z "$dest" ]]; then
  exit 0
fi
cat >"$dest" <<'CTL'
#!/bin/bash
printf 'convert %s\n' "$2" >> "$M7_ORDER_LOG"
printf '%s\n' 'apiVersion: factory.dev/v1alpha1' 'kind: Factory' 'metadata:' '  name: m7'
CTL
chmod 755 "$dest"
EOF
  cat >"$dir/bin/kubectl" <<'EOF'
#!/bin/bash
for arg in "$@"; do
  if [[ "$arg" == "apply" ]]; then
    printf 'apply\n' >> "$M7_ORDER_LOG"
  fi
done
exit 0
EOF
  cat >"$dir/remote" <<'EOF'
#!/bin/bash
printf '%s\n' "$1" >> "$M7_ORDER_LOG"
if [[ "$1" == "seed" ]]; then
  mkdir -p "$M7_WORK/remote-seed"
fi
exit 0
EOF
  chmod 755 "$dir/bin/go" "$dir/bin/kubectl" "$dir/remote"
  : >"$dir/order.log"
  PATH="$dir/bin:${PATH}" \
    MODE=live-ci \
    M7_WORK="$dir" \
    M7_REMOTE_STUB="$dir/remote" \
    M7_ORDER_LOG="$dir/order.log" \
    KUBECTL="$dir/bin/kubectl" \
    bash -c 'source "$1"; m7_apply_definition "$WORK/definition"' _ "$ROOT/spike/m7/run.sh"
  python3 - "$dir/order.log" <<'PY'
import sys
from pathlib import Path

lines = Path(sys.argv[1]).read_text(encoding="utf-8").splitlines()
want = ["preflight", "seed"]
if lines[:2] != want:
    raise SystemExit(f"preflight and seed were not first: {lines}")
converts = [line for line in lines if line.startswith("convert ")]
if len(converts) != 1 or not converts[0].endswith("/remote-seed"):
    raise SystemExit(f"convert did not use the seeded definition: {lines}")
if lines[2:] != [converts[0], "apply"]:
    raise SystemExit(f"apply did not follow seed: {lines}")
PY
  rm -rf "$dir"
}

case_reviewer_stamp() {
  local dir text
  dir="$(mktemp -d "$ROOT/spike/m7/.work/reviewer.XXXXXX")"
  mkdir -p "$dir/agents/implementer"
  cp "$ROOT/deploy/m7-factory/agents/implementer/agent.md" "$dir/agents/implementer/agent.md"
  if FACTORY_REVIEWERS_ADMINS='not a login' m7_stamp_reviewer "$dir" 2>"$dir/err"; then
    fail "rejected login was accepted"
  fi
  if [[ "$(cat "$dir/err")" != "reviewer was not accepted" ]]; then
    fail "rejected login did not fail closed"
  fi
  if grep -q 'not a login' "$dir/agents/implementer/agent.md" "$dir/err"; then
    fail "rejected login was written"
  fi
  FACTORY_REVIEWERS_ADMINS='polds' m7_stamp_reviewer "$dir"
  text="$(cat "$dir/agents/implementer/agent.md")"
  if [[ "$text" == *'{{M7_REVIEWER}}'* ]]; then
    fail "reviewer placeholder was left in place"
  fi
  if [[ "$text" != *'{"reviewers":["polds"]}'* ]]; then
    fail "reviewer login was not stamped"
  fi
  rm -rf "$dir"
}

case_offline_refs() {
  local dir text
  dir="$(mktemp -d "$ROOT/spike/m7/.work/refs.XXXXXX")"
  mkdir -p "$dir/agents/implementer"
  cp "$ROOT/deploy/m7-factory/agents/implementer/agent.md" "$dir/agents/implementer/agent.md"
  MODE=live M7_RUN_ID=off-abcd1234 m7_bind_offline_refs "$dir"
  if ! grep -q 'against the run base ref' "$dir/agents/implementer/agent.md"; then
    fail "live rewrote the implementer steps"
  fi
  MODE=offline M7_RUN_ID=off-abcd1234 m7_bind_offline_refs "$dir"
  text="$(cat "$dir/agents/implementer/agent.md")"
  if [[ "$text" != *'check out m7-run/off-abcd1234/main'* ]]; then
    fail "offline steps missed the base ref"
  fi
  if [[ "$text" != *'against m7-run/off-abcd1234/main'* ]]; then
    fail "offline steps missed the pull request base"
  fi
  if [[ "$text" == *'run base ref'* ]]; then
    fail "offline left the unbound base step"
  fi
  rm -rf "$dir"
}

case_evidence() {
  local dir secret
  dir="$(mktemp -d "$ROOT/spike/m7/.work/evidence.XXXXXX")"
  secret="planted-evidence-secret"
  umask 077
  printf '%s' "$secret" >"$dir/api-key"
  chmod 600 "$dir/api-key"
  cat >"$dir/kubectl" <<'EOF'
#!/bin/bash
if [[ "${1:-}" == "--kubeconfig" ]]; then
  shift 2
fi
cmd="${1:-}"
shift || true
case "$cmd" in
  get)
    resource="${1:-}"
    shift || true
    case "$resource" in
      pods)
        if [[ "$*" == *jsonpath* ]]; then
          printf '%s\n' 'oz-task-run-abc'
        fi
        ;;
      jobs)
        if [[ "$*" == *jsonpath* ]]; then
          printf '%s\n' 'oz-task-run-abc'
        fi
        ;;
      agentruns)
        if [[ "$*" == *jsonpath* ]]; then
          printf '%s\n' 'run-evidence-1'
        fi
        ;;
    esac
    ;;
  logs)
    if [[ "$*" == *--previous* ]]; then
      exit 1
    fi
    if [[ "$*" == *m7-router* ]]; then
      printf '%s\n' 'INFO POST /v1/chat/completions'
      exit 0
    fi
    if [[ "$*" == *oz-task-run-abc* ]]; then
      printf '%s\n' "task saw ${M7_PLANTED_SECRET}"
      exit 0
    fi
    printf 'log\n'
    ;;
  describe)
    printf 'describe %s\n' "$*"
    ;;
  *)
    exit 0
    ;;
esac
EOF
  cat >"$dir/events.py" <<'PY'
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

SSE = """event: terminal
data: {"status":"succeeded","summary":"done"}

event: run.terminal
data: {"status":"failed","failure_reason":"no_work_product","summary":"agent finished without opening a pull request"}

"""


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/healthz":
            body = b"ok"
        elif self.path.endswith("/events"):
            body = SSE.encode()
        else:
            self.send_response(404)
            self.end_headers()
            return
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        return


server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
print(server.server_address[1], flush=True)
server.serve_forever()
PY
  chmod 755 "$dir/kubectl"
  (
    WORK="$dir"
    EVIDENCE="$dir/evidence"
    KUBECTL="$dir/kubectl"
    kubeconfig="$dir/kubeconfig"
    touch "$kubeconfig"
    export M7_PLANTED_SECRET="$secret"
    python3 "$dir/events.py" >"$dir/port" &
    py=$!
    trap 'kill "$py" >/dev/null 2>&1 || true; wait "$py" >/dev/null 2>&1 || true' EXIT
    ready=""
    for _ in 1 2 3 4 5 6 7 8 9 10; do
      if [[ -s "$dir/port" ]]; then
        ready=1
        break
      fi
      sleep 0.1
    done
    if [[ -z "$ready" ]]; then
      fail "events server did not start"
    fi
    M7_EVENTS_PORT="$(cat "$dir/port")"
    text="$(collect_evidence)"
    if [[ "$text" == *"$secret"* ]]; then
      fail "model call line contained a secret"
    fi
    if [[ "$text" != "m7 model calls 1 harness status=succeeded summary=done run status=failed summary=agent finished without opening a pull request" ]]; then
      fail "model call line was not the harness and run status"
    fi
    for path in \
      "$EVIDENCE/task-pods/oz-task-run-abc.log" \
      "$EVIDENCE/task-describe/oz-task-run-abc.pods.txt" \
      "$EVIDENCE/task-describe/oz-task-run-abc.jobs.txt" \
      "$EVIDENCE/router.log" \
      "$EVIDENCE/run-events/run-evidence-1.sse"; do
      if [[ ! -s "$path" ]]; then
        fail "missing evidence $path"
      fi
    done
    if [[ -e "$EVIDENCE/task-pods/oz-task-run-abc.previous.log" ]]; then
      fail "empty previous log was kept"
    fi
    if ! grep -q 'POST /v1/chat/completions' "$EVIDENCE/router.log"; then
      fail "router log missed the model call"
    fi
    if ! grep -q 'no_work_product' "$EVIDENCE/run-events/run-evidence-1.sse"; then
      fail "run events missed the terminal reason"
    fi
    scan_evidence
    if grep -R -q -F -- "$secret" "$EVIDENCE"; then
      fail "evidence kept a secret"
    fi
    if ! grep -q '\[REDACTED\]' "$EVIDENCE/task-pods/oz-task-run-abc.log"; then
      fail "task pod log was not redacted"
    fi
  )
  rm -rf "$dir"
}

case_pr_phase_settled() {
  if m7_pr_phase_settled succeeded ""; then
    fail "succeeded without a pull request was final"
  fi
  if ! m7_pr_phase_settled succeeded "https://example.invalid/pr/1"; then
    fail "succeeded with a pull request was not final"
  fi
  if ! m7_pr_phase_settled failed ""; then
    fail "failed was not final"
  fi
  if m7_pr_phase_settled running ""; then
    fail "running was final"
  fi
  if m7_pr_phase_settled running "https://example.invalid/pr/1"; then
    fail "running with a pull request was final"
  fi
}

case_inference_headers() {
  local dir text mode bits
  dir="$(mktemp -d "$ROOT/spike/m7/.work/headers.XXXXXX")"
  for mode in live live-ci; do
    text="$(
      MODE="$mode" \
        M7_WORK="$dir" \
        M1_OPENAI_EXTRA_HEADERS='x-litellm-api-key: sk-planted-header-value' \
        bash -c 'source "$1"; m7_prepare_inference_headers' _ "$ROOT/spike/m7/run.sh"
    )"
    if [[ "$text" == *sk-planted-header-value* ]]; then
      fail "${mode} printed the header value"
    fi
    if [[ "$text" != "factoryd inference extra headers set" ]]; then
      fail "${mode} header notice"
    fi
    text="$(
      MODE="$mode" \
        M7_WORK="$dir" \
        M1_OPENAI_EXTRA_HEADERS='x-litellm-api-key: sk-planted-header-value' \
        bash -x -c 'source "$1"; m7_prepare_inference_headers' _ "$ROOT/spike/m7/run.sh" 2>&1
    )"
    if [[ "$text" == *sk-planted-header-value* ]]; then
      fail "${mode} bash -x printed the header value"
    fi
    if [[ ! -f "$dir/openai-extra-headers" ]]; then
      fail "${mode} did not write the header file"
    fi
    bits="$(python3 -c 'import os,sys; print(oct(os.stat(sys.argv[1]).st_mode & 0o777))' "$dir/openai-extra-headers")"
    if [[ "$bits" != "0o600" ]]; then
      fail "${mode} header file mode ${bits}"
    fi
    if ! grep -q -F -x -- 'x-litellm-api-key: sk-planted-header-value' "$dir/openai-extra-headers"; then
      fail "${mode} header file mismatch"
    fi
  done
  text="$(
    MODE=offline \
      M7_WORK="$dir" \
      M1_OPENAI_EXTRA_HEADERS='x-litellm-api-key: sk-planted-header-value' \
      bash -c 'source "$1"; m7_prepare_inference_headers' _ "$ROOT/spike/m7/run.sh"
  )"
  if [[ -n "$text" ]]; then
    fail "offline printed a header notice"
  fi
  if [[ -f "$dir/openai-extra-headers" ]]; then
    fail "offline kept the header file"
  fi
  printf 'stale' >"$dir/openai-extra-headers"
  text="$(
    MODE=live \
      M7_WORK="$dir" \
      M1_OPENAI_EXTRA_HEADERS='' \
      bash -c 'source "$1"; m7_prepare_inference_headers' _ "$ROOT/spike/m7/run.sh"
  )"
  if [[ -n "$text" || -f "$dir/openai-extra-headers" ]]; then
    fail "empty live headers were kept"
  fi
  rm -rf "$dir"
}

case_cleanup_on_exit() {
  local dir code
  dir="$(mktemp -d "$ROOT/spike/m7/.work/exit.XXXXXX")"
  cat >"$dir/remote" <<'EOF'
#!/bin/bash
printf '%s\n' "$*" >> "$M7_REMOTE_LOG"
exit 0
EOF
  chmod 755 "$dir/remote"
  : >"$dir/remote.log"
  set +e
  MODE=live-ci \
    M7_WORK="$dir" \
    M7_REMOTE_STUB="$dir/remote" \
    M7_REMOTE_LOG="$dir/remote.log" \
    bash -c 'source "$1"; cluster_up=0; trap "cleanup EXIT" EXIT; die boom' _ "$ROOT/spike/m7/run.sh"
  code=$?
  set -e
  if [[ "$code" -eq 0 ]]; then
    fail "die did not fail the driver"
  fi
  if ! grep -qx 'cleanup' "$dir/remote.log"; then
    fail "EXIT did not run remote cleanup"
  fi
  rm -rf "$dir"
}

m7_launch() {
  set -m
  "$@" &
  M7_LAUNCH_PID=$!
  set +m
}

# Wait here. A substitution cannot wait on this shell's child, and set -e
# exits on status 130.
m7_wait_pid() {
  local pid="$1" limit="$2" start=$SECONDS
  M7_WAIT_EC=0
  while kill -0 "$pid" 2>/dev/null; do
    if [[ $((SECONDS - start)) -ge $limit ]]; then
      kill -KILL "$pid" 2>/dev/null || true
      set +e
      wait "$pid" 2>/dev/null
      set -e
      M7_WAIT_EC=124
      return 0
    fi
    sleep 0.05
  done
  set +e
  wait "$pid" 2>/dev/null
  M7_WAIT_EC=$?
  set -e
}

m7_proof_tree() {
  local dir="$1" pause="${2:-0}"
  cat >"$dir/kind" <<EOF
#!/bin/bash
printf '%s\n' "\$*" >> "\$M7_KIND_LOG"
[[ "\$*" == *delete* ]] && sleep $pause
exit 0
EOF
  cat >"$dir/remote" <<'EOF'
#!/bin/bash
printf '%s\n' "$*" >> "$M7_REMOTE_LOG"
[[ "$1" == cleanup ]] || exit 0
python3 - "$M7_WORK/remote-ledger.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
if p.is_file():
    d = json.loads(p.read_text())
    for e in d.get("entries") or []:
        if isinstance(e, dict):
            e["phase"] = "cleaned"
    p.write_text(json.dumps(d) + "\n")
PY
EOF
  cat >"$dir/child.sh" <<EOF
#!/bin/bash
set -euo pipefail
source "$ROOT/spike/m7/run.sh"
cluster_up=1
m7_install_traps
if [[ "\${1:-hold}" == sleep ]]; then
  sleep 30 &
  m7_hold_pid=\$!
  wait "\$m7_hold_pid" || true
else
  m7_cancel_proof_hold "https://github.com/polds/factory-m7-target/pull/3"
fi
printf 'M7 LIVE PASS wall 1s\n'
EOF
  chmod 755 "$dir/kind" "$dir/remote" "$dir/child.sh"
  : >"$dir/kind.log"
  : >"$dir/remote.log"
  : >"$dir/trap.count"
}

m7_start() {
  m7_launch env MODE=live-ci M7_CANCEL_PROOF="$2" M7_CANCEL_PROOF_HOLD_SECONDS="$3" \
    M7_WORK="$1" M7_REMOTE_STUB="$1/remote" M7_KIND_LOG="$1/kind.log" \
    M7_REMOTE_LOG="$1/remote.log" M7_TRAP_COUNT="$1/trap.count" \
    M7_KIND_CLUSTER=factory-m7-test KIND="$1/kind" M7_LEDGER="$1/remote-ledger.json" \
    bash "$1/child.sh" "${4:-hold}" >"$1/out" 2>"$1/err"
}

m7_seen() {
  local i
  for i in $(seq 1 80); do
    grep -q "$2" "$1" 2>/dev/null && return 0
    sleep 0.05
  done
  return 1
}

m7_reap() { kill -KILL -- -"$1" 2>/dev/null || true; }

m7_one_trap() {
  [[ $(wc -l <"$1/trap.count") -eq 1 ]] || fail "trap count in $1"
}

case_cancel_proof_flag() {
  local n
  M7_CANCEL_PROOF=false m7_cancel_proof_enabled && fail "false enabled cancel proof"
  M7_CANCEL_PROOF='' m7_cancel_proof_enabled && fail "empty enabled cancel proof"
  M7_CANCEL_PROOF=true m7_cancel_proof_enabled || fail "true did not enable cancel proof"
  n="$(M7_CANCEL_PROOF_HOLD_SECONDS=900 m7_cancel_proof_hold_seconds)"
  [[ "$n" == 600 ]] || fail "hold cap was $n"
  n="$(M7_CANCEL_PROOF_HOLD_SECONDS=0 m7_cancel_proof_hold_seconds)"
  [[ "$n" == 600 ]] || fail "zero hold was $n"
}

case_cancel_proof_int() {
  local dir=$ROOT/spike/m7/.work/proof-int-$$ start end
  rm -rf "$dir"
  mkdir -p "$dir"
  m7_proof_tree "$dir" 0
  m7_start "$dir" true 30 hold
  m7_seen "$dir/out" "m7 cancel-proof: holding 30s pr=https://github.com/polds/factory-m7-target/pull/3" || fail "holding marker missing"
  start=$(date +%s.%N)
  kill -INT "$M7_LAUNCH_PID"
  m7_wait_pid "$M7_LAUNCH_PID" 8
  end=$(date +%s.%N)
  m7_reap "$M7_LAUNCH_PID"
  python3 -c 'import sys; raise SystemExit(0 if float(sys.argv[2])-float(sys.argv[1]) < 2 else 1)' "$start" "$end" || fail "INT slower than 2s"
  [[ "$M7_WAIT_EC" == 130 ]] || fail "INT exit $M7_WAIT_EC"
  m7_one_trap "$dir"
  grep -q PASS "$dir/out" "$dir/err" && fail "PASS on INT"
  grep -q cleanup "$dir/remote.log" && fail "remote cleanup on proof INT"
  grep -q "delete cluster --name factory-m7-test" "$dir/kind.log" || fail "kind not deleted"
  rm -rf "$dir"
}

case_cancel_proof_kind_ignores_term() {
  local dir=$ROOT/spike/m7/.work/proof-kind-ignore-$$ start end
  rm -rf "$dir"
  mkdir -p "$dir"
  m7_proof_tree "$dir" 0
  cat >"$dir/kind" <<'EOF'
#!/bin/bash
trap '' TERM INT
printf '%s\n' "$*" >> "$M7_KIND_LOG"
[[ "$*" == *delete* ]] && sleep 30
exit 0
EOF
  chmod 755 "$dir/kind"
  m7_start "$dir" true 30 hold
  m7_seen "$dir/out" "m7 cancel-proof: holding" || fail "holding marker missing"
  start=$(date +%s.%N)
  kill -INT "$M7_LAUNCH_PID"
  m7_seen "$dir/kind.log" "delete cluster" || fail "kind delete did not start"
  kill -TERM "$M7_LAUNCH_PID" 2>/dev/null || true
  m7_wait_pid "$M7_LAUNCH_PID" 12
  end=$(date +%s.%N)
  m7_reap "$M7_LAUNCH_PID"
  python3 -c 'import sys; raise SystemExit(0 if float(sys.argv[2])-float(sys.argv[1]) < 9 else 1)' "$start" "$end" || fail "ignore-TERM kind slower than 9s"
  [[ "$M7_WAIT_EC" == 130 ]] || fail "ignore-TERM kind exit $M7_WAIT_EC"
  m7_one_trap "$dir"
  grep -q PASS "$dir/out" "$dir/err" && fail "PASS on ignore-TERM kind"
  rm -rf "$dir"
}

case_cancel_proof_term() {
  local dir=$ROOT/spike/m7/.work/proof-term-$$
  rm -rf "$dir"
  mkdir -p "$dir"
  m7_proof_tree "$dir" 1.2
  m7_start "$dir" true 30 hold
  m7_seen "$dir/out" "m7 cancel-proof: holding" || fail "holding marker missing"
  kill -TERM "$M7_LAUNCH_PID"
  m7_seen "$dir/kind.log" "delete cluster" || fail "kind delete did not start"
  kill -TERM "$M7_LAUNCH_PID" 2>/dev/null || true
  m7_wait_pid "$M7_LAUNCH_PID" 8
  m7_reap "$M7_LAUNCH_PID"
  [[ "$M7_WAIT_EC" == 143 ]] || fail "TERM exit $M7_WAIT_EC"
  m7_one_trap "$dir"
  grep -q PASS "$dir/out" "$dir/err" && fail "PASS on TERM"
  rm -rf "$dir"
}

case_cancel_proof_expire() {
  local dir=$ROOT/spike/m7/.work/proof-expire-$$
  rm -rf "$dir"
  mkdir -p "$dir"
  m7_proof_tree "$dir" 0
  m7_start "$dir" true 1 hold
  m7_wait_pid "$M7_LAUNCH_PID" 8
  m7_reap "$M7_LAUNCH_PID"
  [[ "$M7_WAIT_EC" != 0 && "$M7_WAIT_EC" != 124 ]] || fail "expired hold exit $M7_WAIT_EC"
  grep -q "m7 cancel-proof: hold expired without cancel" "$dir/err" || fail "hold expired line missing"
  grep -q PASS "$dir/out" "$dir/err" && fail "PASS on expiry"
  rm -rf "$dir"
}

case_normal_int_no_resume() {
  local dir=$ROOT/spike/m7/.work/normal-int-$$
  rm -rf "$dir"
  mkdir -p "$dir"
  m7_proof_tree "$dir" 0
  m7_start "$dir" false 30 sleep
  sleep 0.2
  kill -INT "$M7_LAUNCH_PID"
  m7_wait_pid "$M7_LAUNCH_PID" 8
  m7_reap "$M7_LAUNCH_PID"
  [[ "$M7_WAIT_EC" == 130 ]] || fail "normal INT exit $M7_WAIT_EC"
  grep -q PASS "$dir/out" && fail "normal INT resumed"
  grep -qx cleanup "$dir/remote.log" || fail "normal INT skipped remote cleanup"
  grep -q "delete cluster --name factory-m7-test" "$dir/kind.log" || fail "kind not deleted"
  rm -rf "$dir"
}

case_cleanup_remote_after_proof() {
  local dir=$ROOT/spike/m7/.work/proof-clean-$$ text phase
  rm -rf "$dir"
  mkdir -p "$dir"
  m7_proof_tree "$dir" 0
  python3 - "$dir/remote-ledger.json" <<'PY'
import json, sys
from pathlib import Path
Path(sys.argv[1]).write_text(json.dumps({"version": 1, "entries": [
    {"kind": "ref", "name": "m7-run/ci-proof/main", "phase": "created"},
    {"kind": "ref", "name": "m7-run/ci-proof/change", "phase": "created"},
    {"kind": "ref", "name": "m7-run/ci-proof/extra", "phase": "created"},
    {"kind": "pr", "name": "3", "phase": "created"},
]}) + "\n")
PY
  m7_start "$dir" true 30 hold
  m7_seen "$dir/out" "m7 cancel-proof: holding" || fail "holding marker missing"
  kill -INT "$M7_LAUNCH_PID"
  m7_wait_pid "$M7_LAUNCH_PID" 8
  m7_reap "$M7_LAUNCH_PID"
  [[ "$M7_WAIT_EC" == 130 ]] || fail "proof INT exit $M7_WAIT_EC"
  [[ -f "$dir/remote-ledger.json" ]] || fail "INT deleted the ledger"
  phase=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["entries"][0]["phase"])' "$dir/remote-ledger.json")
  [[ "$phase" != cleaned ]] || fail "INT cleaned the ledger"
  text=$(
    MODE=live-ci M7_CANCEL_PROOF=true M7_WORK="$dir" M7_REMOTE_STUB="$dir/remote" \
      M7_REMOTE_LOG="$dir/remote.log" M1_OPENAI_BASE_URL=http://127.0.0.1:9 \
      M1_OPENAI_API_KEY=offline-not-a-key M6_GITHUB_APP_ID=1 \
      M6_GITHUB_APP_PRIVATE_KEY=offline-not-a-key FACTORY_REVIEWERS_ADMINS=polds \
      "$ROOT/spike/m7/run.sh" --cleanup-remote
  )
  [[ "$text" == *"m7 cleanup: refs deleted=3 left=0 branches deleted=3 left=0 prs deleted=1 left=0"* ]] || fail "cleanup-remote summary was $text"
  rm -rf "$dir"
}

mkdir -p "$ROOT/spike/m7/.work"
case_secrets
case_cleanup_refused
case_cancel_window
case_apply_order
case_planted_token
case_remote
case_router
case_pull_url
case_pr_budget
case_served_agents
case_hold_agent
case_live_before_apply
case_reviewer_stamp
case_offline_refs
case_evidence
case_pr_phase_settled
case_inference_headers
case_cleanup_on_exit
case_cancel_proof_flag
case_cancel_proof_int
case_cancel_proof_kind_ignores_term
case_cancel_proof_term
case_cancel_proof_expire
case_normal_int_no_resume
case_cleanup_remote_after_proof
echo "m7 run_test ok"
