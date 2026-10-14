#!/usr/bin/env bash
# Unit tests for spike/m3/run.sh and the m3 definition. No docker stack.
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
  local got="$1" want="$2" label="$3"
  if [[ "$got" != "$want" ]]; then
    echo "${label}: got $(printf '%q' "$got") want $(printf '%q' "$want")" >&2
    return 1
  fi
}

source_run() {
  # shellcheck source=spike/m3/run.sh
  source "$SCRIPT"
}

test_live_approval_action() {
  local got
  got="$(
    source_run
    APPROVAL_DECISION=""
    live_approval_action
  )"
  assert_eq "$got" wait "unset" || return 1
  got="$(
    source_run
    unset APPROVAL_DECISION
    live_approval_action
  )"
  assert_eq "$got" wait "missing" || return 1
  got="$(
    source_run
    APPROVAL_DECISION=approve
    live_approval_action
  )"
  assert_eq "$got" decide "approve" || return 1
  got="$(
    source_run
    APPROVAL_DECISION=reject
    live_approval_action
  )"
  assert_eq "$got" refuse "reject" || return 1
  got="$(
    source_run
    APPROVAL_DECISION=Approve
    live_approval_action
  )"
  assert_eq "$got" refuse "wrong case" || return 1
  got="$(
    source_run
    APPROVAL_DECISION="approve "
    live_approval_action
  )"
  assert_eq "$got" refuse "trailing space" || return 1
}

test_distinct_models() {
  local rc
  (
    source_run
    distinct_models m3-triage m3-planning m3-building m3-reviewing
  ) || return 1
  set +e
  (
    source_run
    distinct_models m3-triage m3-planning m3-triage m3-reviewing
  )
  rc=$?
  set -e
  if [[ "$rc" -eq 0 ]]; then
    echo "duplicate model ids were accepted" >&2
    return 1
  fi
  set +e
  (
    source_run
    distinct_models m3-triage m3-planning "" m3-reviewing
  )
  rc=$?
  set -e
  if [[ "$rc" -eq 0 ]]; then
    echo "empty model id was accepted" >&2
    return 1
  fi
}

test_definition_shape() {
  local factory="$ROOT/deploy/m3-factory/factory.yaml"
  local runner="$ROOT/deploy/m3-factory/runners/local.yaml"
  local models="" model=""
  [[ -f "$factory" ]] || {
    echo "missing $factory" >&2
    return 1
  }
  [[ -f "$runner" ]] || {
    echo "missing $runner" >&2
    return 1
  }
  grep -F -q 'name: m3' "$factory" || return 1
  grep -F -q 'stages: [triage, planning, building, reviewing]' "$factory" || return 1
  grep -F -q 'triage: triage' "$factory" || return 1
  grep -F -q 'planning: spec' "$factory" || return 1
  grep -F -q 'building: implementer' "$factory" || return 1
  grep -F -q 'reviewing: reviewer' "$factory" || return 1
  grep -F -q 'required: true' "$factory" || return 1
  grep -F -q 'approvers: "role:editor"' "$factory" || return 1
  grep -F -q 'revisionLimit: 3' "$factory" || return 1
  grep -F -q 'GH_TOKEN' "$factory" || return 1
  grep -F -q 'factory-m1-task:dev' "$runner" || return 1
  for agent in triage spec implementer reviewer; do
    [[ -f "$ROOT/deploy/m3-factory/agents/${agent}/agent.md" ]] || {
      echo "missing agent ${agent}" >&2
      return 1
    }
    model="$(awk '/^model: / { print $2; exit }' "$ROOT/deploy/m3-factory/agents/${agent}/agent.md")"
    [[ -n "$model" ]] || {
      echo "${agent} has no model" >&2
      return 1
    }
    models="${models} ${model}"
  done
  # shellcheck disable=SC2086
  (
    source_run
    # Word-splitting is the model list.
    distinct_models ${models}
  )
}

test_no_host_rewrite() {
  if grep -n -E 'task_base_url|127\.0\.0\.1.*host\.docker\.internal|localhost.*host\.docker\.internal' "$SCRIPT" >/dev/null; then
    echo "run.sh reimplements the Docker Desktop host rewrite" >&2
    return 1
  fi
}

if [[ ! -f "$SCRIPT" ]]; then
  echo "FAIL run.sh missing"
  exit 1
fi

run_case live_approval_action test_live_approval_action
run_case distinct_models test_distinct_models
run_case definition_shape test_definition_shape
run_case no_host_rewrite test_no_host_rewrite

echo
echo "${pass} passed, ${fail} failed"
[[ "$fail" -eq 0 ]]
