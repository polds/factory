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
      bash -c 'source "$1"; cluster_up=1; cleanup INT' _ "$ROOT/spike/m7/run.sh"
  )"
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
  rm -rf "$work"
}

mkdir -p "$ROOT/spike/m7/.work"
case_secrets
case_cleanup_refused
case_cancel_window
case_apply_order
case_planted_token
case_remote
echo "m7 run_test ok"
