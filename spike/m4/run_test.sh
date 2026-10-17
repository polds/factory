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
  unset M4_TASK_HOST
  assert_eq "$(host_for_tasks "Docker Desktop" "172.17.0.1" "Darwin")" "host.docker.internal"
}

case_host_orbstack() {
  unset M4_TASK_HOST
  assert_eq "$(host_for_tasks "OrbStack" "192.168.215.1" "Darwin")" "host.docker.internal" || return 1
  assert_eq "$(host_for_tasks "OrbStack" "192.168.215.1" "Linux")" "host.docker.internal"
}

case_host_darwin_unknown() {
  unset M4_TASK_HOST
  assert_eq "$(host_for_tasks "colima" "192.168.64.1" "Darwin")" "host.docker.internal"
}

case_host_linux() {
  unset M4_TASK_HOST
  assert_eq "$(host_for_tasks "Ubuntu 24.04.1 LTS" "172.17.0.1" "Linux")" "172.17.0.1"
}

case_host_override() {
  local saved="${M4_TASK_HOST:-}" rc=0
  M4_TASK_HOST="10.9.8.7"
  assert_eq "$(host_for_tasks "Ubuntu 24.04.1 LTS" "172.17.0.1" "Linux")" "10.9.8.7" || rc=1
  assert_eq "$(host_for_tasks "OrbStack" "192.168.215.1" "Darwin" "host.docker.internal")" "10.9.8.7" || rc=1
  if [[ -n "$saved" ]]; then
    M4_TASK_HOST="$saved"
  else
    unset M4_TASK_HOST
  fi
  return "$rc"
}

case_host_follows_server_url() {
  unset M4_TASK_HOST
  assert_eq "$(host_for_tasks "OrbStack" "192.168.215.1" "Darwin" "host.docker.internal")" "host.docker.internal" || return 1
  assert_eq "$(host_for_tasks "Ubuntu 24.04.1 LTS" "172.17.0.1" "Linux" "host.docker.internal")" "host.docker.internal" || return 1
  assert_eq "$(host_for_tasks "Ubuntu 24.04.1 LTS" "172.17.0.1" "Linux" "")" "172.17.0.1"
}

case_host_router_probe() {
  assert_eq "$(host_router_probe_url "172.17.0.1" 9090)" "http://172.17.0.1:9090/healthz" || return 1
  assert_eq "$(host_router_probe_url "192.168.215.1")" "http://192.168.215.1:9090/healthz" || return 1
  local name=""
  name="$(host_router_probe_url "host.docker.internal" 9090)"
  if [[ -n "$name" ]]; then
    echo "host probe would dial ${name}" >&2
    return 1
  fi
  if grep -F -n "wait_http \"http://\${TASK_HOST}" "$SCRIPT"; then
    echo "host wait_http dials TASK_HOST" >&2
    return 1
  fi
}

case_task_endpoint_consistent() {
  local saved_url="${FACTORY_TASK_SERVER_URL:-}" saved_host="${M4_TASK_HOST:-}"
  local dir="" log="" rc=0
  dir="$(mktemp -d)"
  unset M4_TASK_HOST
  FACTORY_TASK_SERVER_URL="http://host.docker.internal:8080"
  if ! resolve_task_endpoint "OrbStack" "192.168.215.1" "Darwin" >"$dir/preset.out"; then
    rc=1
  fi
  log="$(cat "$dir/preset.out")"
  assert_eq "$TASK_HOST" "host.docker.internal" || rc=1
  assert_eq "${FACTORY_TASK_SERVER_URL:-}" "http://host.docker.internal:8080" || rc=1
  [[ "$log" == *"task host: host.docker.internal"* ]] || rc=1
  [[ "$log" == *"FACTORY_TASK_SERVER_URL host: host.docker.internal:8080"* ]] || rc=1

  unset FACTORY_TASK_SERVER_URL
  if ! resolve_task_endpoint "Ubuntu 24.04.1 LTS" "172.17.0.1" "Linux" >"$dir/linux.out"; then
    rc=1
  fi
  assert_eq "$TASK_HOST" "172.17.0.1" || rc=1
  assert_eq "${FACTORY_TASK_SERVER_URL:-}" "http://172.17.0.1:8080" || rc=1

  unset FACTORY_TASK_SERVER_URL
  if resolve_task_endpoint "OrbStack" "192.168.215.1" "Darwin" >"$dir/out" 2>"$dir/err"; then
    echo "OrbStack without FACTORY_TASK_SERVER_URL was accepted" >&2
    rc=1
  fi
  if ! grep -F -q "FACTORY_TASK_SERVER_URL=http://host.docker.internal:8080" "$dir/out" "$dir/err"; then
    echo "OrbStack refusal did not name the task server URL" >&2
    rc=1
  fi

  unset FACTORY_TASK_SERVER_URL
  if resolve_task_endpoint "colima" "192.168.64.1" "Darwin" >"$dir/darwin.out" 2>"$dir/darwin.err"; then
    echo "Darwin without FACTORY_TASK_SERVER_URL was accepted" >&2
    rc=1
  fi

  M4_TASK_HOST="10.9.8.7"
  unset FACTORY_TASK_SERVER_URL
  resolve_task_endpoint "Ubuntu 24.04.1 LTS" "172.17.0.1" "Linux" >"$dir/override.out" || rc=1
  assert_eq "$TASK_HOST" "10.9.8.7" || rc=1
  assert_eq "${FACTORY_TASK_SERVER_URL:-}" "http://10.9.8.7:8080" || rc=1

  FACTORY_TASK_SERVER_URL="http://host.docker.internal:8080"
  if resolve_task_endpoint "Ubuntu 24.04.1 LTS" "172.17.0.1" "Linux" >"$dir/mismatch.out" 2>"$dir/mismatch.err"; then
    echo "M4_TASK_HOST was allowed to diverge from FACTORY_TASK_SERVER_URL" >&2
    rc=1
  fi

  if [[ -n "$saved_url" ]]; then
    FACTORY_TASK_SERVER_URL="$saved_url"
  else
    unset FACTORY_TASK_SERVER_URL
  fi
  if [[ -n "$saved_host" ]]; then
    M4_TASK_HOST="$saved_host"
  else
    unset M4_TASK_HOST
  fi
  rm -rf "$dir"
  return "$rc"
}

case_health_probe_bounds() {
  local start="" end="" elapsed="" body="" line=""
  body="$(awk '/^wait_http\(\)/,/^}/' "$SCRIPT")"
  [[ "$body" == *"--connect-timeout 2"* && "$body" == *"-m 5"* ]] || {
    echo "wait_http curl is unbounded" >&2
    return 1
  }
  while IFS= read -r line; do
    case "$line" in
      *wait_http*) continue ;;
    esac
    case "$line" in
      *http*healthz*)
        case "$line" in
          *curl* | *-fsS*)
            [[ "$line" == *"--connect-timeout 2"* && "$line" == *"-m 5"* ]] || {
              echo "unbounded health probe: ${line}" >&2
              return 1
            }
            ;;
        esac
        ;;
    esac
  done <"$SCRIPT"
  start="$(date +%s)"
  if curl -fsS --connect-timeout 2 -m 5 "http://192.0.2.1:9090/healthz" >/dev/null 2>&1; then
    echo "blackhole health probe succeeded" >&2
    return 1
  fi
  end="$(date +%s)"
  elapsed="$((end - start))"
  if [[ "$elapsed" -gt 8 ]]; then
    echo "health probe took ${elapsed}s" >&2
    return 1
  fi
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
  [[ "$text" == *'FACTORY_PUBLIC_URL}/api/v1/ingress/github'* ]] || return 1
  [[ "$text" == *"Issues (required for the labeled intake)"* ]] || return 1
  [[ "$text" == *"Issue comment"* ]] || return 1
  [[ "$text" == *$'\n       Pull request\n'* ]] || return 1
  [[ "$text" == *"Pull request review"* ]] || return 1
  [[ "$text" == *$'\n       Push\n'* ]] || return 1
  [[ "$text" == *"Check run"* ]] || return 1
  [[ "$text" == *"Check suite"* ]] || return 1
  [[ "$text" == *"A trycloudflare quick tunnel changes URL on every restart."* ]] || return 1
  [[ "$text" == *"Set M4_LIVE_MODEL to an id the server lists."* ]] || return 1
  [[ "$text" == *"This machine lists models on 127.0.0.1 at that port."* ]] || return 1
  [[ "$text" == *"M4_LIVE_MODEL_TRIAGE"* ]] || return 1
  [[ "$text" == *"M4_LIVE_MODEL_PLANNING"* ]] || return 1
  [[ "$text" == *"M4_LIVE_MODEL_BUILDING"* ]] || return 1
  [[ "$text" == *"M4_LIVE_MODEL_REVIEWING"* ]] || return 1
}

case_mode_parse() {
  accept_mode offline || return 1
  accept_mode live || return 1
  accept_mode live-ci || return 1
  if accept_mode liveci; then
    return 1
  fi
  if accept_mode ""; then
    return 1
  fi
  if accept_mode LIVE; then
    return 1
  fi
}

case_live_ci_delivery_sign() {
  local dir="" sig="" want="" saved_secret="" saved_sender=""
  dir="$(mktemp -d)"
  python3 - "$dir/issue.json" <<'PY'
import json
import sys

doc = {
    "id": 17017,
    "number": 17,
    "title": "[m4-live-ci local] M4: emphasize Hello",
    "body": "Change spike/m1/fixture/greet/greet.go so Hello returns Hello, name!.\nm4-live-ci-marker: local\n",
    "html_url": "https://github.com/polds/factory/issues/17",
    "user": {"login": "factory-dev[bot]", "id": 99, "type": "Bot"},
}
with open(sys.argv[1], "w", encoding="utf-8") as handle:
    json.dump(doc, handle)
    handle.write("\n")
PY
  saved_sender="${M4_LIVE_CI_SENDER:-}"
  unset M4_LIVE_CI_SENDER
  write_live_ci_delivery_from_issue "$dir/delivery.json" "$dir/issue.json" "polds/factory" || return 1
  saved_secret="${FACTORY_GITHUB_WEBHOOK_SECRET:-}"
  FACTORY_GITHUB_WEBHOOK_SECRET="m4-test-secret"
  sig="$(sign_github "$FACTORY_GITHUB_WEBHOOK_SECRET" "$dir/delivery.json")"
  want="$(
    python3 - "$FACTORY_GITHUB_WEBHOOK_SECRET" "$dir/delivery.json" <<'PY'
import hashlib
import hmac
import sys

secret, path = sys.argv[1], sys.argv[2]
payload = open(path, "rb").read()
print("sha256=" + hmac.new(secret.encode(), payload, hashlib.sha256).hexdigest())
PY
  )"
  assert_eq "$sig" "$want" || return 1
  if [[ "$(sign_github "other-secret" "$dir/delivery.json")" == "$sig" ]]; then
    echo "a different webhook secret produced the same signature" >&2
    return 1
  fi
  python3 - "$dir/delivery.json" "$dir/issue.json" <<'PY' || return 1
import json
import sys

doc = json.load(open(sys.argv[1], encoding="utf-8"))
source = json.load(open(sys.argv[2], encoding="utf-8"))
sender = doc.get("sender") or {}
login = sender.get("login") or ""
if sender.get("type") != "User":
    raise SystemExit("sender type")
if "[bot]" in login.lower() or login == (source.get("user") or {}).get("login"):
    raise SystemExit("sender copied the bot")
if login != "polds":
    raise SystemExit("default sender is the repo owner")
if (source.get("user") or {}).get("type") != "Bot":
    raise SystemExit("fixture")
if doc.get("action") != "labeled":
    raise SystemExit("action")
if (doc.get("label") or {}).get("name") != "factory-ready":
    raise SystemExit("label")
issue = doc.get("issue") or {}
if issue.get("number") != 17 or "m4-live-ci-marker: local" not in (issue.get("body") or ""):
    raise SystemExit("issue")
repo = doc.get("repository") or {}
if repo.get("full_name") != "polds/factory":
    raise SystemExit("repo")
PY
  M4_LIVE_CI_SENDER="factory-dev[bot]"
  if write_live_ci_delivery_from_issue "$dir/bot.json" "$dir/issue.json" "polds/factory" 2>/dev/null; then
    echo "a bot sender was accepted" >&2
    return 1
  fi
  M4_LIVE_CI_SENDER="dependabot"
  if write_live_ci_delivery_from_issue "$dir/dep.json" "$dir/issue.json" "polds/factory" 2>/dev/null; then
    echo "dependabot was accepted" >&2
    return 1
  fi
  if [[ -n "$saved_sender" ]]; then
    M4_LIVE_CI_SENDER="$saved_sender"
  else
    unset M4_LIVE_CI_SENDER
  fi
  if [[ -n "$saved_secret" ]]; then
    FACTORY_GITHUB_WEBHOOK_SECRET="$saved_secret"
  else
    unset FACTORY_GITHUB_WEBHOOK_SECRET
  fi
  rm -rf "$dir"
}

case_live_ci_title_cleanup() {
  local saved_run="${GITHUB_RUN_ID:-}" saved_repo="${M4_REPO:-}"
  local saved_issue="${ISSUE_LIVE:-}" saved_pr="${PR_LIVE_NUMBER:-}" saved_branch="${PR_LIVE_BRANCH:-}"
  GITHUB_RUN_ID=4242
  assert_eq "$(live_ci_issue_title)" "[m4-live-ci 4242] M4: emphasize Hello" || return 1
  assert_eq "$(live_ci_marker)" "m4-live-ci-marker: 4242" || return 1
  unset GITHUB_RUN_ID
  assert_eq "$(live_ci_issue_title)" "[m4-live-ci local] M4: emphasize Hello" || return 1
  M4_REPO=polds/factory
  ISSUE_LIVE=12
  PR_LIVE_NUMBER=34
  PR_LIVE_BRANCH=m4-hello-4242
  assert_eq "$(live_ci_cleanup_request issue)" "PATCH /repos/polds/factory/issues/12 state=closed" || return 1
  assert_eq "$(live_ci_cleanup_request pull)" "PATCH /repos/polds/factory/pulls/34 state=closed" || return 1
  assert_eq "$(live_ci_cleanup_request branch)" "DELETE /repos/polds/factory/git/refs/heads/m4-hello-4242" || return 1
  PR_LIVE_BRANCH=main
  if live_ci_cleanup_request branch >/dev/null; then
    echo "refused to protect main" >&2
    return 1
  fi
  local dir=""
  dir="$(mktemp -d)"
  printf '%s\n' '[{"isDraft":true,"url":"https://github.com/polds/factory/pull/9","headRefOid":"abc","number":9,"headRefName":"m4-hello","body":"Closes #123"}]' \
    >"$dir/wide.json"
  if select_draft_pull "$dir/wide.json" 12 >/dev/null; then
    echo "Closes #123 matched issue 12" >&2
    return 1
  fi
  printf '%s\n' '[{"isDraft":true,"url":"https://github.com/polds/factory/pull/9","headRefOid":"abc","number":9,"headRefName":"m4-hello","body":"Please Closes #12 now"}]' \
    >"$dir/exact.json"
  assert_eq "$(select_draft_pull "$dir/exact.json" 12 | awk 'NR==1')" "https://github.com/polds/factory/pull/9" || return 1
  M4_REPO=other/factory
  if assert_live_ci_repo; then
    echo "a repo outside the definition was accepted" >&2
    return 1
  fi
  M4_REPO=polds/factory
  assert_live_ci_repo || return 1
  if [[ -n "$saved_run" ]]; then
    GITHUB_RUN_ID="$saved_run"
  else
    unset GITHUB_RUN_ID
  fi
  M4_REPO="$saved_repo"
  ISSUE_LIVE="$saved_issue"
  PR_LIVE_NUMBER="$saved_pr"
  PR_LIVE_BRANCH="$saved_branch"
  rm -rf "$dir"
}

case_live_ci_webhook_secret() {
  local saved="${FACTORY_GITHUB_WEBHOOK_SECRET:-}" generated=""
  FACTORY_GITHUB_WEBHOOK_SECRET="configured-secret"
  configure_live_ci_webhook || return 1
  assert_eq "$FACTORY_GITHUB_WEBHOOK_SECRET" "configured-secret" || return 1
  unset FACTORY_GITHUB_WEBHOOK_SECRET
  configure_live_ci_webhook || return 1
  generated="$FACTORY_GITHUB_WEBHOOK_SECRET"
  [[ "$generated" =~ ^[0-9a-f]{64}$ ]] || return 1
  if [[ -n "$saved" ]]; then
    FACTORY_GITHUB_WEBHOOK_SECRET="$saved"
  else
    unset FACTORY_GITHUB_WEBHOOK_SECRET
  fi
}

case_live_ci_pem() {
  local dir="" dest="" pem="" out="" mode=""
  dir="$(mktemp -d)"
  dest="$dir/github-app.pem"
  pem=$'-----BEGIN PRIVATE KEY-----\nYQ==\n-----END PRIVATE KEY-----\n'
  M4_GITHUB_APP_PRIVATE_KEY="$pem"
  out="$(umask 077 && stage_app_key_from_env "$dest" 2>"$dir/err")" || return 1
  [[ -z "$out" ]] || return 1
  if [[ -s "$dir/err" ]]; then
    echo "staging the App key wrote to stderr" >&2
    return 1
  fi
  if grep -F -q "BEGIN PRIVATE KEY" <<<"$out"; then
    echo "staging the App key printed the PEM" >&2
    return 1
  fi
  mode="$(file_mode "$dest")"
  assert_eq "$mode" "644" || return 1
  cmp -s <(printf '%s' "$pem") "$dest" || return 1
  git -C "$ROOT" check-ignore -q -- spike/m4/.work/live-ci/github-app.pem || return 1
  unset M4_GITHUB_APP_PRIVATE_KEY
  if stage_app_key_from_env "$dir/missing.pem" >"$dir/empty.out" 2>"$dir/empty.err"; then
    echo "empty PEM was accepted" >&2
    return 1
  fi
  if grep -F -q "BEGIN" "$dir/empty.err"; then
    echo "empty-PEM error included key material" >&2
    return 1
  fi
  rm -rf "$dir"
}

case_live_ci_env_names() {
  local name=""
  for name in \
    M4_GITHUB_APP_ID \
    M4_GITHUB_APP_PRIVATE_KEY \
    M4_SLACK_BOT_TOKEN \
    M4_SLACK_CHANNEL \
    M1_OPENAI_BASE_URL \
    M1_OPENAI_API_KEY \
    M4_LIVE_MODEL \
    M4_LIVE_CI_SENDER \
    FACTORY_GITHUB_WEBHOOK_SECRET; do
    grep -F -q "$name" "$SCRIPT" || {
      echo "run.sh does not mention ${name}" >&2
      return 1
    }
    grep -F -q "$name" "$ROOT/.github/workflows/m4-live.yml" || {
      echo "m4-live.yml does not mention ${name}" >&2
      return 1
    }
  done
}

case_prepare_definition_offline() {
  local dest="" saved_mode="$MODE" rc=0 rel=""
  dest="$(mktemp -d)"
  MODE=offline
  prepare_definition "$dest" || rc=1
  if [[ "$rc" -eq 0 ]] && ! cmp -s "$ROOT/deploy/m4-factory/factory.yaml" "$dest/factory.yaml"; then
    echo "offline prepare_definition changed factory.yaml" >&2
    rc=1
  fi
  if [[ "$rc" -eq 0 ]] && ! python3 - "$dest/automations/hourly/automation.md" <<'PY'
import sys
from pathlib import Path

text = Path(sys.argv[1]).read_text(encoding="utf-8")
if text.count("enabled: true\n") != 1 or "enabled: false" in text:
    raise SystemExit("offline did not enable the hourly automation once")
PY
  then
    rc=1
  fi
  if [[ "$rc" -eq 0 ]]; then
    for rel in \
      agents/triage/agent.md \
      agents/spec/agent.md \
      agents/implementer/agent.md \
      agents/reviewer/agent.md; do
      if ! cmp -s "$ROOT/deploy/m4-factory/$rel" "$dest/$rel"; then
        echo "offline prepare_definition changed ${rel}" >&2
        rc=1
      fi
    done
  fi
  MODE="$saved_mode"
  rm -rf "$dest"
  return "$rc"
}

expect_placeholder_failure() {
  local kind="$1" message="$2" dest="$3" base="$4"
  cp -a "$ROOT/deploy/m4-factory/." "$base/" || return 1
  python3 - "$base/factory.yaml" "$kind" <<'PY' || return 1
import sys
from pathlib import Path

path, kind = Path(sys.argv[1]), sys.argv[2]
kept = []
seen = 0
for line in path.read_text(encoding="utf-8").splitlines(keepends=True):
    body = line.strip()
    if kind == "channel" and body == "channel: C0M4FACTORY":
        seen += 1
        continue
    if kind == "user" and body == "- U0M4HUMAN":
        seen += 1
        continue
    if kind == "dup-channel" and body == "channel: C0M4FACTORY":
        seen += 1
        kept.append(line)
        kept.append(line)
        continue
    if kind == "dup-user" and body == "- U0M4HUMAN":
        seen += 1
        kept.append(line)
        kept.append(line)
        continue
    kept.append(line)
if seen != 1:
    raise SystemExit("fixture edit saw %s placeholder lines" % seen)
path.write_text("".join(kept), encoding="utf-8")
PY
  SRC_DEF="$base"
  MODE=live
  if prepare_definition "$dest" >"$base/out" 2>"$base/err"; then
    echo "prepare_definition accepted a ${kind} placeholder fixture" >&2
    return 1
  fi
  if ! grep -F -q "$message" "$base/err"; then
    echo "prepare_definition ${kind} failure was not clear" >&2
    cat "$base/err" >&2
    return 1
  fi
}

case_prepare_definition_placeholders() {
  local dest="" base="" mode="" rc=0
  local saved_mode="$MODE" saved_src="$SRC_DEF"
  local saved_channel="" saved_user="" had_channel=0 had_user=0
  local saved_model="" had_model=0
  local saved_triage="" saved_planning="" saved_building="" saved_reviewing=""
  local had_triage=0 had_planning=0 had_building=0 had_reviewing=0
  if [[ -n "${M4_SLACK_CHANNEL+x}" ]]; then
    had_channel=1
    saved_channel="$M4_SLACK_CHANNEL"
  fi
  if [[ -n "${M4_SLACK_ALLOWED_USER+x}" ]]; then
    had_user=1
    saved_user="$M4_SLACK_ALLOWED_USER"
  fi
  if [[ -n "${M4_LIVE_MODEL+x}" ]]; then
    had_model=1
    saved_model="$M4_LIVE_MODEL"
  fi
  if [[ -n "${M4_LIVE_MODEL_TRIAGE+x}" ]]; then
    had_triage=1
    saved_triage="$M4_LIVE_MODEL_TRIAGE"
  fi
  if [[ -n "${M4_LIVE_MODEL_PLANNING+x}" ]]; then
    had_planning=1
    saved_planning="$M4_LIVE_MODEL_PLANNING"
  fi
  if [[ -n "${M4_LIVE_MODEL_BUILDING+x}" ]]; then
    had_building=1
    saved_building="$M4_LIVE_MODEL_BUILDING"
  fi
  if [[ -n "${M4_LIVE_MODEL_REVIEWING+x}" ]]; then
    had_reviewing=1
    saved_reviewing="$M4_LIVE_MODEL_REVIEWING"
  fi
  dest="$(mktemp -d)"
  base="$(mktemp -d)"
  export M4_SLACK_CHANNEL=C0C9TEST
  export M4_SLACK_ALLOWED_USER=U0TEST
  export M4_LIVE_MODEL=claude-sonnet-5-5
  unset M4_LIVE_MODEL_TRIAGE M4_LIVE_MODEL_PLANNING M4_LIVE_MODEL_BUILDING M4_LIVE_MODEL_REVIEWING
  for mode in live live-ci; do
    MODE="$mode"
    SRC_DEF="$ROOT/deploy/m4-factory"
    if ! prepare_definition "$dest"; then
      rc=1
      break
    fi
    if ! python3 - "$ROOT/deploy/m4-factory/factory.yaml" "$dest/factory.yaml" C0C9TEST U0TEST claude-sonnet-5-5 <<'PY'
import sys
from pathlib import Path

source = Path(sys.argv[1]).read_text(encoding="utf-8").splitlines()
prepared = Path(sys.argv[2]).read_text(encoding="utf-8").splitlines()
channel, user, model = sys.argv[3], sys.argv[4], sys.argv[5]
rewritten = []
n_channel = 0
n_user = 0
n_model = 0
for line in source:
    indent = line[: len(line) - len(line.lstrip())]
    body = line.strip()
    if body.startswith("#") and ("C0M4FACTORY" in line or "U0M4HUMAN" in line):
        raise SystemExit("placeholder token remains in a comment")
    if body == "channel: C0M4FACTORY":
        n_channel += 1
        rewritten.append(indent + "channel: " + channel)
    elif body == "- U0M4HUMAN":
        n_user += 1
        rewritten.append(indent + "- " + user)
    elif body == "model: m4-triage":
        n_model += 1
        rewritten.append(indent + "model: " + model)
    else:
        rewritten.append(line)
if n_channel != 1 or n_user != 1 or n_model != 1:
    raise SystemExit(
        "source placeholders channel=%s user=%s model=%s" % (n_channel, n_user, n_model)
    )
if prepared != rewritten:
    raise SystemExit("prepared factory.yaml is not one channel line, one user line, and one model line")
PY
    then
      rc=1
      break
    fi
    if ! cmp -s "$ROOT/deploy/m4-factory/automations/hourly/automation.md" \
      "$dest/automations/hourly/automation.md"; then
      echo "live prepare_definition changed the hourly automation" >&2
      rc=1
      break
    fi
  done
  if [[ "$rc" -eq 0 ]]; then
    expect_placeholder_failure channel "factory.yaml slack channel was not replaced" "$dest" "$base" || rc=1
  fi
  if [[ "$rc" -eq 0 ]]; then
    expect_placeholder_failure user "factory.yaml slack user was not replaced" "$dest" "$base" || rc=1
  fi
  if [[ "$rc" -eq 0 ]]; then
    expect_placeholder_failure dup-channel "factory.yaml slack channel was not replaced" "$dest" "$base" || rc=1
  fi
  if [[ "$rc" -eq 0 ]]; then
    expect_placeholder_failure dup-user "factory.yaml slack user was not replaced" "$dest" "$base" || rc=1
  fi
  MODE="$saved_mode"
  SRC_DEF="$saved_src"
  if [[ "$had_channel" -eq 1 ]]; then
    export M4_SLACK_CHANNEL="$saved_channel"
  else
    unset M4_SLACK_CHANNEL
  fi
  if [[ "$had_user" -eq 1 ]]; then
    export M4_SLACK_ALLOWED_USER="$saved_user"
  else
    unset M4_SLACK_ALLOWED_USER
  fi
  if [[ "$had_model" -eq 1 ]]; then
    export M4_LIVE_MODEL="$saved_model"
  else
    unset M4_LIVE_MODEL
  fi
  if [[ "$had_triage" -eq 1 ]]; then
    export M4_LIVE_MODEL_TRIAGE="$saved_triage"
  else
    unset M4_LIVE_MODEL_TRIAGE
  fi
  if [[ "$had_planning" -eq 1 ]]; then
    export M4_LIVE_MODEL_PLANNING="$saved_planning"
  else
    unset M4_LIVE_MODEL_PLANNING
  fi
  if [[ "$had_building" -eq 1 ]]; then
    export M4_LIVE_MODEL_BUILDING="$saved_building"
  else
    unset M4_LIVE_MODEL_BUILDING
  fi
  if [[ "$had_reviewing" -eq 1 ]]; then
    export M4_LIVE_MODEL_REVIEWING="$saved_reviewing"
  else
    unset M4_LIVE_MODEL_REVIEWING
  fi
  rm -rf "$dest" "$base"
  return "$rc"
}

case_live_label_sender() {
  local dir="" bin="" log="" text="" rc=0 login_rc=0 post_rc=0
  local saved_path="$PATH" saved_issue="${ISSUE_LIVE:-}" saved_repo="${M4_REPO:-}"
  local saved_token="" had_token=0 saved_live="${LIVE_TOKEN:-}" had_live=0
  local saved_mode="$MODE"
  dir="$(mktemp -d)"
  bin="$dir/bin"
  log="$dir/gh.log"
  mkdir -p "$bin"
  cat >"$bin/gh" <<'EOF'
#!/bin/sh
if [ -n "${GH_TOKEN+x}" ]; then
  printf 'GH_TOKEN=set\n' >> "$FAKE_GH_LOG"
else
  printf 'GH_TOKEN=unset\n' >> "$FAKE_GH_LOG"
fi
printf '%s\n' "$*" >> "$FAKE_GH_LOG"
if [ "$1" = api ] && [ "$2" = user ]; then
  if [ "$FAKE_GH_USER" = fail ]; then
    exit 1
  fi
  printf '{"type":"%s"}\n' "$FAKE_GH_USER"
  exit 0
fi
if [ "$FAKE_GH_LABEL" = fail ]; then
  exit 1
fi
exit 0
EOF
  chmod +x "$bin/gh"
  if [[ -n "${GH_TOKEN+x}" ]]; then
    had_token=1
    saved_token="$GH_TOKEN"
  fi
  if [[ -n "${LIVE_TOKEN+x}" ]]; then
    had_live=1
  fi
  export GH_TOKEN="ghs_app_token_sentinel"
  export LIVE_TOKEN="ghs_app_token_sentinel"
  export FAKE_GH_LOG="$log"
  export FAKE_GH_USER=User
  export FAKE_GH_LABEL=ok
  export PATH="$bin:$PATH"
  M4_REPO=polds/factory
  ISSUE_LIVE=77
  : >"$log"
  if ! label_live_issue >"$dir/user.out" 2>"$dir/user.err"; then
    echo "a User login was not allowed to label" >&2
    cat "$dir/user.err" >&2
    rc=1
  fi
  if [[ "$rc" -eq 0 ]] && grep -F -q "GH_TOKEN=set" "$log"; then
    echo "label call saw GH_TOKEN" >&2
    rc=1
  fi
  if [[ "$rc" -eq 0 ]] && ! grep -F -q "api user" "$log"; then
    echo "label path did not check gh api user" >&2
    rc=1
  fi
  if [[ "$rc" -eq 0 ]] && ! grep -F -q "/repos/polds/factory/issues/77/labels" "$log"; then
    echo "label path did not post factory-ready" >&2
    rc=1
  fi
  if [[ "$rc" -eq 0 ]] && grep -F -q "ghs_app_token_sentinel" "$log"; then
    echo "app token was passed to gh" >&2
    rc=1
  fi
  if [[ "$GH_TOKEN" != "ghs_app_token_sentinel" ]]; then
    echo "label call cleared the App token" >&2
    rc=1
  fi
  FAKE_GH_USER=Bot
  : >"$log"
  if label_live_issue >"$dir/bot.out" 2>"$dir/bot.err"; then
    echo "a Bot login was allowed to label" >&2
    rc=1
  elif ! grep -F -q "gh auth login" "$dir/bot.err"; then
    echo "bot rejection did not mention gh auth login" >&2
    cat "$dir/bot.err" >&2
    rc=1
  elif grep -F -q "/labels" "$log"; then
    echo "bot rejection still applied the label" >&2
    rc=1
  fi
  FAKE_GH_USER=fail
  : >"$log"
  label_live_issue >"$dir/fail.out" 2>"$dir/fail.err"
  login_rc=$?
  if [[ "$login_rc" -ne 1 ]]; then
    echo "a missing gh login returned ${login_rc}, want 1" >&2
    rc=1
  elif ! grep -F -q "gh auth login" "$dir/fail.err"; then
    echo "missing login did not mention gh auth login" >&2
    cat "$dir/fail.err" >&2
    rc=1
  elif grep -F -q "/labels" "$log"; then
    echo "missing login still applied the label" >&2
    rc=1
  fi
  FAKE_GH_USER=User
  FAKE_GH_LABEL=fail
  : >"$log"
  label_live_issue >"$dir/post.out" 2>"$dir/post.err"
  post_rc=$?
  if [[ "$post_rc" -ne 2 ]]; then
    echo "a failed label POST returned ${post_rc}, want 2" >&2
    cat "$dir/post.err" >&2
    rc=1
  elif grep -F -q "gh auth login" "$dir/post.err"; then
    echo "label POST failure was reported as a login failure" >&2
    rc=1
  elif ! grep -F -q "could not apply the factory-ready label" "$dir/post.err"; then
    echo "label POST failure was not reported" >&2
    cat "$dir/post.err" >&2
    rc=1
  elif ! grep -F -q "/repos/polds/factory/issues/77/labels" "$log"; then
    echo "label POST was not attempted" >&2
    rc=1
  fi
  text="$(live_wait_hint)"
  if [[ "$text" != $'https://github.com/polds/factory/issues/77\nno work item yet: check webhook delivery and sender' ]]; then
    echo "wait hint was:" >&2
    printf '%s\n' "$text" >&2
    rc=1
  fi
  MODE=live
  if ! live_wait_should_hint 1 || ! live_wait_should_hint 6; then
    echo "live mode did not schedule the stall hint" >&2
    rc=1
  fi
  if live_wait_should_hint 2; then
    echo "live mode hinted on a quiet poll" >&2
    rc=1
  fi
  MODE=live-ci
  if live_wait_should_hint 1 || live_wait_should_hint 6; then
    echo "live-ci scheduled the webhook stall hint" >&2
    rc=1
  fi
  MODE="$saved_mode"
  MODE=live
  if ! live_wait_note 1 none; then
    echo "a missing work item did not schedule the stall hint" >&2
    rc=1
  fi
  if live_wait_note 1 open; then
    echo "an existing work item scheduled the webhook stall hint" >&2
    rc=1
  fi
  if live_wait_note 1 failed; then
    echo "a failed work item scheduled the webhook stall hint" >&2
    rc=1
  fi
  if live_wait_note 1 unknown; then
    echo "an unreadable factoryd response scheduled the webhook stall hint" >&2
    rc=1
  fi
  MODE=live-ci
  if live_wait_note 1 none; then
    echo "live-ci scheduled the webhook stall hint" >&2
    rc=1
  fi
  MODE="$saved_mode"
  if ! declare -f wait_draft_pull | grep -F -q live_wait_note; then
    echo "wait_draft_pull does not gate the stall hint" >&2
    rc=1
  fi
  if ! declare -f wait_draft_pull | grep -A2 -F 'failed)' | grep -F -q 'return 2'; then
    echo "wait_draft_pull does not stop when the work item failed" >&2
    rc=1
  fi
  text="$(print_live_checklist)"
  if [[ "$text" != *"gh auth login"* ]]; then
    echo "live checklist does not mention gh auth login" >&2
    rc=1
  fi
  PATH="$saved_path"
  M4_REPO="$saved_repo"
  ISSUE_LIVE="$saved_issue"
  if [[ "$had_token" -eq 1 ]]; then
    export GH_TOKEN="$saved_token"
  else
    unset GH_TOKEN
  fi
  if [[ "$had_live" -eq 1 ]]; then
    LIVE_TOKEN="$saved_live"
  else
    unset LIVE_TOKEN
  fi
  unset FAKE_GH_LOG FAKE_GH_USER FAKE_GH_LABEL
  rm -rf "$dir"
  return "$rc"
}

case_prepare_definition_models() {
  local dest="" base="" fixture="" mode="" rc=0
  local saved_mode="$MODE" saved_src="$SRC_DEF"
  local saved_channel="" saved_user="" had_channel=0 had_user=0
  local saved_model="" had_model=0
  local saved_triage="" saved_planning="" saved_building="" saved_reviewing=""
  local had_triage=0 had_planning=0 had_building=0 had_reviewing=0
  if [[ -n "${M4_SLACK_CHANNEL+x}" ]]; then
    had_channel=1
    saved_channel="$M4_SLACK_CHANNEL"
  fi
  if [[ -n "${M4_SLACK_ALLOWED_USER+x}" ]]; then
    had_user=1
    saved_user="$M4_SLACK_ALLOWED_USER"
  fi
  if [[ -n "${M4_LIVE_MODEL+x}" ]]; then
    had_model=1
    saved_model="$M4_LIVE_MODEL"
  fi
  if [[ -n "${M4_LIVE_MODEL_TRIAGE+x}" ]]; then
    had_triage=1
    saved_triage="$M4_LIVE_MODEL_TRIAGE"
  fi
  if [[ -n "${M4_LIVE_MODEL_PLANNING+x}" ]]; then
    had_planning=1
    saved_planning="$M4_LIVE_MODEL_PLANNING"
  fi
  if [[ -n "${M4_LIVE_MODEL_BUILDING+x}" ]]; then
    had_building=1
    saved_building="$M4_LIVE_MODEL_BUILDING"
  fi
  if [[ -n "${M4_LIVE_MODEL_REVIEWING+x}" ]]; then
    had_reviewing=1
    saved_reviewing="$M4_LIVE_MODEL_REVIEWING"
  fi
  dest="$(mktemp -d)"
  base="$(mktemp -d)"
  fixture="$(mktemp -d)"
  export M4_SLACK_CHANNEL=C0C9TEST
  export M4_SLACK_ALLOWED_USER=U0TEST
  export M4_LIVE_MODEL=claude-sonnet-5-5
  unset M4_LIVE_MODEL_TRIAGE M4_LIVE_MODEL_PLANNING M4_LIVE_MODEL_BUILDING M4_LIVE_MODEL_REVIEWING
  for mode in live live-ci; do
    MODE="$mode"
    SRC_DEF="$ROOT/deploy/m4-factory"
    if ! prepare_definition "$dest"; then
      echo "default model rewrite failed for ${mode}" >&2
      rc=1
      break
    fi
    if ! python3 - "$dest" claude-sonnet-5-5 claude-sonnet-5-5 claude-sonnet-5-5 claude-sonnet-5-5 <<'PY'
import sys
from pathlib import Path

root = Path(sys.argv[1])
triage, planning, building, reviewing = sys.argv[2:6]
expect = {
    "factory.yaml": ("m4-triage", triage),
    "agents/triage/agent.md": ("m4-triage", triage),
    "agents/spec/agent.md": ("m4-planning", planning),
    "agents/implementer/agent.md": ("m4-building", building),
    "agents/reviewer/agent.md": ("m4-reviewing", reviewing),
}
for rel, pair in expect.items():
    stub, model = pair
    lines = (root / rel).read_text(encoding="utf-8").splitlines()
    hits = [line for line in lines if line.strip() == "model: " + model]
    leftover = [line for line in lines if line.strip() == "model: " + stub and model != stub]
    model_lines = [line for line in lines if line.strip().startswith("model:")]
    if len(hits) != 1 or leftover or len(model_lines) != 1:
        raise SystemExit("%s model rewrite failed" % rel)
PY
    then
      rc=1
      break
    fi
  done
  if [[ "$rc" -eq 0 ]]; then
    export M4_LIVE_MODEL_TRIAGE=model-triage.1
    export M4_LIVE_MODEL_PLANNING="org/planning:v1"
    export M4_LIVE_MODEL_BUILDING="model-building+1"
    export M4_LIVE_MODEL_REVIEWING=model_reviewing
    MODE=live
    SRC_DEF="$ROOT/deploy/m4-factory"
    if ! prepare_definition "$dest"; then
      echo "role model overrides were rejected" >&2
      rc=1
    elif ! python3 - "$dest" model-triage.1 "org/planning:v1" "model-building+1" model_reviewing <<'PY'
import sys
from pathlib import Path

root = Path(sys.argv[1])
triage, planning, building, reviewing = sys.argv[2:6]
expect = {
    "factory.yaml": ("m4-triage", triage),
    "agents/triage/agent.md": ("m4-triage", triage),
    "agents/spec/agent.md": ("m4-planning", planning),
    "agents/implementer/agent.md": ("m4-building", building),
    "agents/reviewer/agent.md": ("m4-reviewing", reviewing),
}
for rel, pair in expect.items():
    stub, model = pair
    lines = (root / rel).read_text(encoding="utf-8").splitlines()
    hits = [line for line in lines if line.strip() == "model: " + model]
    leftover = [line for line in lines if line.strip() == "model: " + stub and model != stub]
    model_lines = [line for line in lines if line.strip().startswith("model:")]
    if len(hits) != 1 or leftover or len(model_lines) != 1:
        raise SystemExit("%s override rewrite failed" % rel)
PY
    then
      rc=1
    fi
  fi
  if [[ "$rc" -eq 0 ]]; then
    unset M4_LIVE_MODEL
    MODE=live
    SRC_DEF="$ROOT/deploy/m4-factory"
    if prepare_definition "$dest" >"$base/unset.out" 2>"$base/unset.err"; then
      echo "unset M4_LIVE_MODEL was accepted" >&2
      rc=1
    elif ! grep -F -q "M4_LIVE_MODEL is required for live mode" "$base/unset.err"; then
      echo "unset M4_LIVE_MODEL failure was not clear" >&2
      cat "$base/unset.err" >&2
      rc=1
    fi
  fi
  if [[ "$rc" -eq 0 ]]; then
    export M4_LIVE_MODEL=""
    if require_live_model >"$base/req.out" 2>"$base/req.err"; then
      echo "empty M4_LIVE_MODEL was accepted by require_live_model" >&2
      rc=1
    elif ! grep -F -q "M4_LIVE_MODEL is required for live mode" "$base/req.err"; then
      echo "require_live_model failure was not clear" >&2
      cat "$base/req.err" >&2
      rc=1
    fi
  fi
  if [[ "$rc" -eq 0 ]]; then
    export M4_LIVE_MODEL="bad model"
    if prepare_definition "$dest" >"$base/bad.out" 2>"$base/bad.err"; then
      echo "an unsafe model id was accepted" >&2
      rc=1
    elif ! grep -F -q "model id is not a safe id" "$base/bad.err"; then
      echo "unsafe model id failure was not clear" >&2
      cat "$base/bad.err" >&2
      rc=1
    fi
  fi
  if [[ "$rc" -eq 0 ]]; then
    export M4_LIVE_MODEL=claude-sonnet-5-5
    export M4_LIVE_MODEL_BUILDING="bad model"
    if prepare_definition "$dest" >"$base/override.out" 2>"$base/override.err"; then
      echo "an unsafe model override was accepted" >&2
      rc=1
    elif ! grep -F -q "model id is not a safe id" "$base/override.err"; then
      echo "unsafe model override failure was not clear" >&2
      cat "$base/override.err" >&2
      rc=1
    fi
  fi
  if [[ "$rc" -eq 0 ]]; then
    unset M4_LIVE_MODEL_TRIAGE M4_LIVE_MODEL_PLANNING M4_LIVE_MODEL_BUILDING M4_LIVE_MODEL_REVIEWING
    cp -a "$ROOT/deploy/m4-factory/." "$fixture/"
    python3 - "$fixture/factory.yaml" <<'PY' || rc=1
import sys
from pathlib import Path

path = Path(sys.argv[1])
kept = []
seen = 0
for line in path.read_text(encoding="utf-8").splitlines(keepends=True):
    if line.strip() == "model: m4-triage":
        seen += 1
        continue
    kept.append(line)
if seen != 1:
    raise SystemExit("fixture edit saw %s model lines" % seen)
path.write_text("".join(kept), encoding="utf-8")
PY
  fi
  if [[ "$rc" -eq 0 ]]; then
    SRC_DEF="$fixture"
    if prepare_definition "$dest" >"$base/missing.out" 2>"$base/missing.err"; then
      echo "a missing model line was accepted" >&2
      rc=1
    elif ! grep -F -q "factory.yaml model was not replaced" "$base/missing.err"; then
      echo "missing model line failure was not clear" >&2
      cat "$base/missing.err" >&2
      rc=1
    fi
  fi
  if [[ "$rc" -eq 0 ]]; then
    if ! safe_model_ids claude-sonnet-5-5 claude-sonnet-5-5 claude-sonnet-5-5 claude-sonnet-5-5; then
      echo "safe_model_ids rejected one shared id" >&2
      rc=1
    fi
  fi
  if [[ "$rc" -eq 0 ]] && distinct_models claude-sonnet-5-5 claude-sonnet-5-5 claude-sonnet-5-5 claude-sonnet-5-5 >/dev/null 2>&1; then
    echo "distinct_models accepted one shared id" >&2
    rc=1
  fi
  if [[ "$rc" -eq 0 ]]; then
    printf '%s\n' '{"data":[{"id":"claude-sonnet-5-5"},{"id":"org/planning:v1"}]}' >"$base/models.json"
    if ! assert_listed_models "$base/models.json" claude-sonnet-5-5 claude-sonnet-5-5 "org/planning:v1"; then
      echo "listed models were rejected" >&2
      rc=1
    fi
  fi
  if [[ "$rc" -eq 0 ]] && assert_listed_models "$base/models.json" claude-sonnet-5-5 missing-model >"$base/miss.out" 2>"$base/miss.err"; then
    echo "an unlisted model was accepted" >&2
    rc=1
  elif [[ "$rc" -eq 0 ]] && ! grep -F -q "model id missing-model is not listed" "$base/miss.err"; then
    echo "unlisted model failure was not clear" >&2
    cat "$base/miss.err" >&2
    rc=1
  fi
  if [[ "$rc" -eq 0 ]]; then
    printf '%s\n' '<html>forbidden</html>' >"$base/models.html"
    if assert_listed_models "$base/models.html" claude-sonnet-5-5 >"$base/html.out" 2>"$base/html.err"; then
      echo "an html model list was accepted" >&2
      rc=1
    elif ! grep -F -q "model list was not a catalog" "$base/html.err"; then
      echo "html model list failure was not clear" >&2
      cat "$base/html.err" >&2
      rc=1
    fi
  fi
  if [[ "$rc" -eq 0 ]]; then
    printf '%s\n' '{"object":"list"}' >"$base/nodata.json"
    if assert_listed_models "$base/nodata.json" claude-sonnet-5-5 >"$base/nodata.out" 2>"$base/nodata.err"; then
      echo "a catalog without data was accepted" >&2
      rc=1
    elif ! grep -F -q "model list was not a catalog" "$base/nodata.err"; then
      echo "catalog without data failure was not clear" >&2
      cat "$base/nodata.err" >&2
      rc=1
    fi
  fi
  MODE="$saved_mode"
  SRC_DEF="$saved_src"
  if [[ "$had_channel" -eq 1 ]]; then
    export M4_SLACK_CHANNEL="$saved_channel"
  else
    unset M4_SLACK_CHANNEL
  fi
  if [[ "$had_user" -eq 1 ]]; then
    export M4_SLACK_ALLOWED_USER="$saved_user"
  else
    unset M4_SLACK_ALLOWED_USER
  fi
  if [[ "$had_model" -eq 1 ]]; then
    export M4_LIVE_MODEL="$saved_model"
  else
    unset M4_LIVE_MODEL
  fi
  if [[ "$had_triage" -eq 1 ]]; then
    export M4_LIVE_MODEL_TRIAGE="$saved_triage"
  else
    unset M4_LIVE_MODEL_TRIAGE
  fi
  if [[ "$had_planning" -eq 1 ]]; then
    export M4_LIVE_MODEL_PLANNING="$saved_planning"
  else
    unset M4_LIVE_MODEL_PLANNING
  fi
  if [[ "$had_building" -eq 1 ]]; then
    export M4_LIVE_MODEL_BUILDING="$saved_building"
  else
    unset M4_LIVE_MODEL_BUILDING
  fi
  if [[ "$had_reviewing" -eq 1 ]]; then
    export M4_LIVE_MODEL_REVIEWING="$saved_reviewing"
  else
    unset M4_LIVE_MODEL_REVIEWING
  fi
  rm -rf "$dest" "$base" "$fixture"
  return "$rc"
}

case_host_catalog_probe() {
  local dir="" bin="" log="" rc=0
  local saved_path="$PATH" saved_task="${TASK_HOST:-}" had_task=0
  local saved_url="" saved_key="" saved_model=""
  local had_url=0 had_key=0 had_model=0
  local saved_triage="" saved_planning="" saved_building="" saved_reviewing=""
  local had_triage=0 had_planning=0 had_building=0 had_reviewing=0
  if [[ -n "${TASK_HOST+x}" ]]; then
    had_task=1
  fi
  if [[ -n "${OPENAI_BASE_URL+x}" ]]; then
    had_url=1
    saved_url="$OPENAI_BASE_URL"
  fi
  if [[ -n "${OPENAI_API_KEY+x}" ]]; then
    had_key=1
    saved_key="$OPENAI_API_KEY"
  fi
  if [[ -n "${M4_LIVE_MODEL+x}" ]]; then
    had_model=1
    saved_model="$M4_LIVE_MODEL"
  fi
  if [[ -n "${M4_LIVE_MODEL_TRIAGE+x}" ]]; then
    had_triage=1
    saved_triage="$M4_LIVE_MODEL_TRIAGE"
  fi
  if [[ -n "${M4_LIVE_MODEL_PLANNING+x}" ]]; then
    had_planning=1
    saved_planning="$M4_LIVE_MODEL_PLANNING"
  fi
  if [[ -n "${M4_LIVE_MODEL_BUILDING+x}" ]]; then
    had_building=1
    saved_building="$M4_LIVE_MODEL_BUILDING"
  fi
  if [[ -n "${M4_LIVE_MODEL_REVIEWING+x}" ]]; then
    had_reviewing=1
    saved_reviewing="$M4_LIVE_MODEL_REVIEWING"
  fi
  dir="$(mktemp -d)"
  bin="$dir/bin"
  log="$dir/curl.log"
  mkdir -p "$bin"
  cat >"$bin/curl" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_CURL_LOG"
out=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "-o" ]; then
    out=$arg
  fi
  prev=$arg
done
if [ -n "$out" ]; then
  printf '%s\n' '{"data":[{"id":"claude-sonnet-5-5"}]}' > "$out"
fi
if [ "$FAKE_CURL_FAIL" = 1 ]; then
  exit 1
fi
exit 0
EOF
  chmod +x "$bin/curl"
  export PATH="$bin:$PATH"
  export FAKE_CURL_LOG="$log"
  export FAKE_CURL_FAIL=0
  export M4_LIVE_MODEL=claude-sonnet-5-5
  unset M4_LIVE_MODEL_TRIAGE M4_LIVE_MODEL_PLANNING M4_LIVE_MODEL_BUILDING M4_LIVE_MODEL_REVIEWING
  unset OPENAI_API_KEY
  unset TASK_HOST
  assert_eq "$(host_catalog_probe_url "http://host.docker.internal:8317/v1" "")" "http://127.0.0.1:8317/v1" || rc=1
  assert_eq "$(host_catalog_probe_url "http://172.17.0.1:8317/v1" "172.17.0.1")" "http://127.0.0.1:8317/v1" || rc=1
  assert_eq "$(host_catalog_probe_url "https://models.example.com/v1" "host.docker.internal")" "https://models.example.com/v1" || rc=1
  export OPENAI_BASE_URL="http://host.docker.internal:8317/v1"
  : >"$log"
  if [[ "$rc" -eq 0 ]] && ! preflight_model_catalog; then
    echo "loopback catalog probe failed" >&2
    rc=1
  fi
  if [[ "$rc" -eq 0 ]] && ! grep -F -q "http://127.0.0.1:8317/v1/models" "$log"; then
    echo "catalog probe did not use 127.0.0.1:8317" >&2
    cat "$log" >&2
    rc=1
  fi
  if [[ "$rc" -eq 0 ]] && grep -F -q "host.docker.internal" "$log"; then
    echo "catalog probe dialed host.docker.internal" >&2
    cat "$log" >&2
    rc=1
  fi
  if [[ "$OPENAI_BASE_URL" != "http://host.docker.internal:8317/v1" ]]; then
    echo "catalog probe rewrote OPENAI_BASE_URL" >&2
    rc=1
  fi
  export OPENAI_API_KEY="sk-test-secret"
  : >"$log"
  if [[ "$rc" -eq 0 ]] && ! preflight_model_catalog; then
    echo "authorized catalog probe failed" >&2
    rc=1
  fi
  if [[ "$rc" -eq 0 ]] && ! grep -F -q "Authorization: Bearer sk-test-secret" "$log"; then
    echo "catalog probe dropped the Authorization header" >&2
    rc=1
  fi
  if [[ "$rc" -eq 0 ]] && ! grep -F -q "http://127.0.0.1:8317/v1/models" "$log"; then
    echo "authorized catalog probe did not use 127.0.0.1:8317" >&2
    rc=1
  fi
  export FAKE_CURL_FAIL=1
  : >"$log"
  if [[ "$rc" -eq 0 ]] && preflight_model_catalog >"$dir/fail.out" 2>"$dir/fail.err"; then
    echo "a failed catalog probe was accepted" >&2
    rc=1
  elif [[ "$rc" -eq 0 ]] && ! grep -F -q "could not list models from host.docker.internal:8317 (probed 127.0.0.1:8317)" "$dir/fail.err"; then
    echo "catalog probe failure did not name both hosts" >&2
    cat "$dir/fail.err" >&2
    rc=1
  elif [[ "$rc" -eq 0 ]] && grep -F -q "sk-test-secret" "$dir/fail.err"; then
    echo "catalog probe failure leaked the API key" >&2
    rc=1
  fi
  unset OPENAI_API_KEY
  export FAKE_CURL_FAIL=0
  export OPENAI_BASE_URL="https://models.example.com/v1"
  export TASK_HOST="host.docker.internal"
  : >"$log"
  if [[ "$rc" -eq 0 ]] && ! preflight_model_catalog; then
    echo "remote catalog probe failed" >&2
    rc=1
  fi
  if [[ "$rc" -eq 0 ]] && ! grep -F -q "https://models.example.com/v1/models" "$log"; then
    echo "remote catalog probe did not keep the container URL" >&2
    cat "$log" >&2
    rc=1
  fi
  if [[ "$rc" -eq 0 ]] && grep -F -q "127.0.0.1" "$log"; then
    echo "remote catalog probe dialed loopback" >&2
    cat "$log" >&2
    rc=1
  fi
  export FAKE_CURL_FAIL=1
  if [[ "$rc" -eq 0 ]] && preflight_model_catalog >"$dir/remote.out" 2>"$dir/remote.err"; then
    echo "a failed remote catalog probe was accepted" >&2
    rc=1
  elif [[ "$rc" -eq 0 ]] && ! grep -F -q "could not list models from models.example.com (probed models.example.com)" "$dir/remote.err"; then
    echo "remote catalog failure did not name both hosts" >&2
    cat "$dir/remote.err" >&2
    rc=1
  fi
  export FAKE_CURL_FAIL=0
  export OPENAI_BASE_URL="http://172.17.0.1:8317/v1"
  export TASK_HOST="172.17.0.1"
  : >"$log"
  if [[ "$rc" -eq 0 ]] && ! preflight_model_catalog; then
    echo "task-host catalog probe failed" >&2
    rc=1
  fi
  if [[ "$rc" -eq 0 ]] && ! grep -F -q "http://127.0.0.1:8317/v1/models" "$log"; then
    echo "task-host catalog probe did not use 127.0.0.1:8317" >&2
    cat "$log" >&2
    rc=1
  fi
  if [[ "$rc" -eq 0 ]] && grep -F -q "172.17.0.1" "$log"; then
    echo "task-host catalog probe dialed the container host" >&2
    cat "$log" >&2
    rc=1
  fi
  PATH="$saved_path"
  if [[ "$had_task" -eq 1 ]]; then
    TASK_HOST="$saved_task"
  else
    unset TASK_HOST
  fi
  if [[ "$had_url" -eq 1 ]]; then
    export OPENAI_BASE_URL="$saved_url"
  else
    unset OPENAI_BASE_URL
  fi
  if [[ "$had_key" -eq 1 ]]; then
    export OPENAI_API_KEY="$saved_key"
  else
    unset OPENAI_API_KEY
  fi
  if [[ "$had_model" -eq 1 ]]; then
    export M4_LIVE_MODEL="$saved_model"
  else
    unset M4_LIVE_MODEL
  fi
  if [[ "$had_triage" -eq 1 ]]; then
    export M4_LIVE_MODEL_TRIAGE="$saved_triage"
  else
    unset M4_LIVE_MODEL_TRIAGE
  fi
  if [[ "$had_planning" -eq 1 ]]; then
    export M4_LIVE_MODEL_PLANNING="$saved_planning"
  else
    unset M4_LIVE_MODEL_PLANNING
  fi
  if [[ "$had_building" -eq 1 ]]; then
    export M4_LIVE_MODEL_BUILDING="$saved_building"
  else
    unset M4_LIVE_MODEL_BUILDING
  fi
  if [[ "$had_reviewing" -eq 1 ]]; then
    export M4_LIVE_MODEL_REVIEWING="$saved_reviewing"
  else
    unset M4_LIVE_MODEL_REVIEWING
  fi
  unset FAKE_CURL_LOG FAKE_CURL_FAIL
  rm -rf "$dir"
  return "$rc"
}

case_live_work_failure() {
  local dir="" bin="" log="" rc=0 state="" message=""
  local saved_path="$PATH" saved_work="$WORK" saved_repo="${M4_REPO:-}"
  local saved_issue="${ISSUE_LIVE:-}" saved_key="" had_key=0 saved_name="${FACTORY_NAME:-}"
  if [[ -n "${FACTORY_API_KEY+x}" ]]; then
    had_key=1
    saved_key="$FACTORY_API_KEY"
  fi
  dir="$(mktemp -d)"
  bin="$dir/bin"
  log="$dir/curl.log"
  WORK="$dir/work"
  mkdir -p "$bin" "$WORK"
  cat >"$bin/curl" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_CURL_LOG"
out=""
prev=""
write_code=0
url=""
for arg in "$@"; do
  if [ "$prev" = "-o" ]; then
    out=$arg
  fi
  if [ "$arg" = "-w" ]; then
    write_code=1
  fi
  prev=$arg
  url=$arg
done
code=200
body=""
case "$url" in
  *run-by-external-reference*)
    if [ "$FAKE_WORK" = none ]; then
      code=404
      body='{"title":"not found"}'
    else
      body=$(cat "$FAKE_WORK_BODY")
    fi
    ;;
  */tasks/*)
    body=$(cat "$FAKE_TASK_BODY")
    ;;
  */events)
    body=$(cat "$FAKE_EVENTS_BODY")
    ;;
esac
if [ -n "$out" ]; then
  printf '%s' "$body" > "$out"
fi
if [ "$write_code" = 1 ]; then
  printf '%s' "$code"
fi
exit 0
EOF
  chmod +x "$bin/curl"
  export PATH="$bin:$PATH"
  export FAKE_CURL_LOG="$log"
  export FACTORY_API_KEY="sk-test-secret"
  export FACTORY_NAME="m4"
  M4_REPO="polds/factory"
  ISSUE_LIVE="77"
  printf '%s\n' '{"id":"wi_1","stage":"triage","status":"open"}' >"$dir/task.json"
  printf '%s\n' '{"provider":"github","external_ref":"polds/factory#77","work_item_id":"wi_1","run":{"id":"run_9","status":"running"}}' >"$dir/running.json"
  printf '%s\n' '{"provider":"github","external_ref":"polds/factory#77","work_item_id":"wi_1","run":{"id":"run_9","status":"failed","failure_reason":"container_exit"}}' >"$dir/failed.json"
  printf '%s\n' 'event: run.terminal
data: {"type":"run.terminal","status":"failed","failure_reason":"container_exit","summary":"provider HTTP 503: sk-test-secret overloaded"}
' >"$dir/events.txt"
  : >"$log"
  export FAKE_WORK=none
  poll_live_work >"$WORK/live-wait-state"
  state="$(cat "$WORK/live-wait-state")"
  if [[ "$state" != none ]]; then
    echo "missing work item state was ${state}" >&2
    rc=1
  fi
  if [[ "$rc" -eq 0 ]] && ! grep -F -q 'ref=polds%2Ffactory%2377' "$log"; then
    echo "work item probe did not encode the issue ref" >&2
    cat "$log" >&2
    rc=1
  fi
  if [[ "$rc" -eq 0 ]] && ! grep -F -q "Authorization: Bearer sk-test-secret" "$log"; then
    echo "work item probe dropped the API key header" >&2
    rc=1
  fi
  export FAKE_WORK=open
  export FAKE_WORK_BODY="$dir/running.json"
  export FAKE_TASK_BODY="$dir/task.json"
  : >"$log"
  poll_live_work >"$WORK/live-wait-state"
  state="$(cat "$WORK/live-wait-state")"
  if [[ "$state" != open ]]; then
    echo "running work item state was ${state}" >&2
    rc=1
  fi
  if [[ "$rc" -eq 0 ]] && grep -F -q '/events' "$log"; then
    echo "a running work item fetched terminal events" >&2
    rc=1
  fi
  export FAKE_WORK=failed
  export FAKE_WORK_BODY="$dir/failed.json"
  export FAKE_EVENTS_BODY="$dir/events.txt"
  : >"$log"
  poll_live_work >"$WORK/live-wait-state"
  state="$(cat "$WORK/live-wait-state")"
  if [[ "$state" != failed ]]; then
    echo "failed work item state was ${state}" >&2
    printf '%s\n' "$LIVE_WAIT_ERROR" >&2
    rc=1
  fi
  message="work item wi_1 escalated at stage triage: run run_9 failed: container_exit: provider HTTP 503: *** overloaded"
  if [[ "$rc" -eq 0 && "$LIVE_WAIT_ERROR" != "$message" ]]; then
    echo "failure reason was:" >&2
    printf '%s\n' "$LIVE_WAIT_ERROR" >&2
    rc=1
  fi
  if [[ "$rc" -eq 0 ]] && grep -F -q "sk-test-secret" <<<"$LIVE_WAIT_ERROR"; then
    echo "failure reason leaked the API key" >&2
    rc=1
  fi
  if [[ "$rc" -eq 0 ]] && ! grep -F -q '/api/v1/agent/runs/run_9/events' "$log"; then
    echo "failed work item did not read run events" >&2
    cat "$log" >&2
    rc=1
  fi
  printf '%s\n' 'factory-harness: provider HTTP 503: down' >"$dir/logs.txt"
  : >"$dir/empty-events.txt"
  : >"$dir/empty-task.json"
  state="$(read_live_work_state 200 "$dir/failed.json" "$dir/task.json" "$dir/empty-events.txt" "$dir/logs.txt" "$dir/message")"
  if [[ "$rc" -eq 0 && "$state" != failed ]]; then
    echo "log fallback state was ${state}" >&2
    rc=1
  fi
  if [[ "$rc" -eq 0 ]] && ! grep -F -q 'provider HTTP 503: down' "$dir/message"; then
    echo "log fallback did not keep the provider status line" >&2
    cat "$dir/message" >&2
    rc=1
  fi
  state="$(read_live_work_state 404 "$dir/failed.json" "$dir/task.json" "$dir/events.txt" "$dir/logs.txt" "$dir/none-message")"
  if [[ "$rc" -eq 0 && "$state" != none ]]; then
    echo "404 state was ${state}" >&2
    rc=1
  fi
  PATH="$saved_path"
  WORK="$saved_work"
  M4_REPO="$saved_repo"
  ISSUE_LIVE="$saved_issue"
  FACTORY_NAME="$saved_name"
  if [[ "$had_key" -eq 1 ]]; then
    export FACTORY_API_KEY="$saved_key"
  else
    unset FACTORY_API_KEY
  fi
  unset FAKE_CURL_LOG FAKE_WORK FAKE_WORK_BODY FAKE_TASK_BODY FAKE_EVENTS_BODY
  rm -rf "$dir"
  return "$rc"
}

case_select_draft_pull() {
  local dir="" mode="" rc=0 status=0 out="" saved_mode="${MODE:-}"
  local saved_url="${PR_LIVE_URL-}" saved_sha="${PR_LIVE_SHA-}"
  local saved_number="${PR_LIVE_NUMBER-}" saved_branch="${PR_LIVE_BRANCH-}"
  local saved_error="${DRAFT_PULL_ERROR-}"
  local want=""
  dir="$(mktemp -d)"
  cat >"$dir/none.json" <<'EOF'
[]
EOF
  cat >"$dir/one.json" <<'EOF'
[
  {"isDraft": false, "url": "https://github.com/polds/factory/pull/80", "headRefOid": "abc80", "number": 80, "headRefName": "ready", "body": "Closes #83"},
  {"isDraft": true, "url": "https://github.com/polds/factory/pull/84", "headRefOid": "abc84", "number": 84, "headRefName": "m4-84", "body": "Please Closes #83 now"}
]
EOF
  cat >"$dir/two.json" <<'EOF'
[
  {"isDraft": true, "url": "https://github.com/polds/factory/pull/84", "headRefOid": "aaa", "number": 84, "headRefName": "m4-a", "body": "Closes #83"},
  {"isDraft": false, "url": "https://github.com/polds/factory/pull/86", "headRefOid": "ccc", "number": 86, "headRefName": "m4-c", "body": "Closes #83"},
  {"isDraft": true, "url": "https://github.com/polds/factory/pull/85", "headRefOid": "bbb", "number": 85, "headRefName": "m4-b", "body": "Also Closes #83."},
  {"isDraft": true, "url": "https://github.com/polds/factory/pull/87", "headRefOid": "ddd", "number": 87, "headRefName": "m4-d", "body": "Closes #830"}
]
EOF
  want=$'more than one open draft pull request matches Closes #83:\nhttps://github.com/polds/factory/pull/84\nhttps://github.com/polds/factory/pull/85'
  for mode in offline live live-ci; do
    MODE="$mode"
    status=0
    out="$(select_draft_pull "$dir/none.json" 83 2>"$dir/none.err")" || status=$?
    if [[ "$status" -eq 0 || -n "$out" || -s "$dir/none.err" ]]; then
      echo "${mode}: zero matching drafts did not stay quiet (status ${status})" >&2
      rc=1
    fi
    status=0
    out="$(select_draft_pull "$dir/one.json" 83 2>"$dir/one.err")" || status=$?
    if [[ "$rc" -eq 0 && ("$status" -ne 0 || -s "$dir/one.err") ]]; then
      echo "${mode}: one matching draft was rejected" >&2
      rc=1
    fi
    if [[ "$rc" -eq 0 && "$out" != $'https://github.com/polds/factory/pull/84\nabc84\n84\nm4-84' ]]; then
      echo "${mode}: one matching draft printed:" >&2
      printf '%s\n' "$out" >&2
      rc=1
    fi
    status=0
    out="$(select_draft_pull "$dir/two.json" 83 2>"$dir/two.err")" || status=$?
    if [[ "$rc" -eq 0 && ("$status" -eq 0 || -n "$out") ]]; then
      echo "${mode}: two matching drafts were accepted" >&2
      rc=1
    fi
    if [[ "$rc" -eq 0 && "$status" -ne 3 ]]; then
      echo "${mode}: two matching drafts exited ${status}" >&2
      rc=1
    fi
    if [[ "$rc" -eq 0 && "$(cat "$dir/two.err")" != "$want" ]]; then
      echo "${mode}: ambiguous draft message was:" >&2
      cat "$dir/two.err" >&2
      rc=1
    fi
    PR_LIVE_URL=""
    PR_LIVE_SHA=""
    PR_LIVE_NUMBER=""
    PR_LIVE_BRANCH=""
    DRAFT_PULL_ERROR=""
    status=0
    take_selected_draft "$dir/two.json" 83 || status=$?
    if [[ "$rc" -eq 0 && "$status" -ne 3 ]]; then
      echo "${mode}: take_selected_draft status was ${status}" >&2
      rc=1
    fi
    if [[ "$rc" -eq 0 && "$DRAFT_PULL_ERROR" != "$want" ]]; then
      echo "${mode}: DRAFT_PULL_ERROR was:" >&2
      printf '%s\n' "$DRAFT_PULL_ERROR" >&2
      rc=1
    fi
    if [[ "$rc" -eq 0 && -n "$PR_LIVE_URL" ]]; then
      echo "${mode}: an ambiguous list still selected ${PR_LIVE_URL}" >&2
      rc=1
    fi
  done
  if ! declare -f wait_draft_pull | grep -F -q take_selected_draft; then
    echo "wait_draft_pull does not use the shared draft selector" >&2
    rc=1
  fi
  if ! declare -f finish_wait_draft_pull | grep -F -q DRAFT_PULL_ERROR; then
    echo "the draft-PR wait does not report an ambiguous match" >&2
    rc=1
  fi
  MODE="$saved_mode"
  PR_LIVE_URL="$saved_url"
  PR_LIVE_SHA="$saved_sha"
  PR_LIVE_NUMBER="$saved_number"
  PR_LIVE_BRANCH="$saved_branch"
  DRAFT_PULL_ERROR="$saved_error"
  rm -rf "$dir"
  return "$rc"
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
run_case host_orbstack case_host_orbstack
run_case host_darwin_unknown case_host_darwin_unknown
run_case host_linux case_host_linux
run_case host_override case_host_override
run_case host_follows_server_url case_host_follows_server_url
run_case host_router_probe case_host_router_probe
run_case task_endpoint_consistent case_task_endpoint_consistent
run_case health_probe_bounds case_health_probe_bounds
run_case task_lines case_task_lines
run_case building_script case_building_script
run_case live_key_copy case_live_key_copy
run_case live_model_url case_live_model_url
run_case mode_parse case_mode_parse
run_case live_ci_delivery_sign case_live_ci_delivery_sign
run_case live_ci_title_cleanup case_live_ci_title_cleanup
run_case live_ci_webhook_secret case_live_ci_webhook_secret
run_case live_ci_pem case_live_ci_pem
run_case live_ci_env_names case_live_ci_env_names
run_case prepare_definition_offline case_prepare_definition_offline
run_case prepare_definition_placeholders case_prepare_definition_placeholders
run_case prepare_definition_models case_prepare_definition_models
run_case live_label_sender case_live_label_sender
run_case host_catalog_probe case_host_catalog_probe
run_case live_work_failure case_live_work_failure
run_case select_draft_pull case_select_draft_pull
run_case no_gnu_flags case_no_gnu_flags

echo
echo "${pass} passed, ${fail} failed"
if [[ "$fail" -ne 0 ]]; then
  exit 1
fi
