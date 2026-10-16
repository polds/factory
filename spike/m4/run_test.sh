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

case_prepare_definition_offline() {
  local dest="" saved_mode="$MODE" rc=0
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
  if [[ -n "${M4_SLACK_CHANNEL+x}" ]]; then
    had_channel=1
    saved_channel="$M4_SLACK_CHANNEL"
  fi
  if [[ -n "${M4_SLACK_ALLOWED_USER+x}" ]]; then
    had_user=1
    saved_user="$M4_SLACK_ALLOWED_USER"
  fi
  dest="$(mktemp -d)"
  base="$(mktemp -d)"
  export M4_SLACK_CHANNEL=C0C9TEST
  export M4_SLACK_ALLOWED_USER=U0TEST
  for mode in live live-ci; do
    MODE="$mode"
    SRC_DEF="$ROOT/deploy/m4-factory"
    if ! prepare_definition "$dest"; then
      rc=1
      break
    fi
    if ! python3 - "$ROOT/deploy/m4-factory/factory.yaml" "$dest/factory.yaml" C0C9TEST U0TEST <<'PY'
import sys
from pathlib import Path

source = Path(sys.argv[1]).read_text(encoding="utf-8").splitlines()
prepared = Path(sys.argv[2]).read_text(encoding="utf-8").splitlines()
channel, user = sys.argv[3], sys.argv[4]
rewritten = []
n_channel = 0
n_user = 0
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
    else:
        rewritten.append(line)
if n_channel != 1 or n_user != 1:
    raise SystemExit("source placeholders channel=%s user=%s" % (n_channel, n_user))
if prepared != rewritten:
    raise SystemExit("prepared factory.yaml is not one channel line and one user line")
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
  rm -rf "$dest" "$base"
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
run_case no_gnu_flags case_no_gnu_flags

echo
echo "${pass} passed, ${fail} failed"
if [[ "$fail" -ne 0 ]]; then
  exit 1
fi
