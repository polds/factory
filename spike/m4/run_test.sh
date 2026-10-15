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
run_case mode_parse case_mode_parse
run_case live_ci_delivery_sign case_live_ci_delivery_sign
run_case live_ci_title_cleanup case_live_ci_title_cleanup
run_case live_ci_webhook_secret case_live_ci_webhook_secret
run_case live_ci_pem case_live_ci_pem
run_case live_ci_env_names case_live_ci_env_names
run_case no_gnu_flags case_no_gnu_flags

echo
echo "${pass} passed, ${fail} failed"
if [[ "$fail" -ne 0 ]]; then
  exit 1
fi
