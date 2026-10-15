#!/usr/bin/env bash
# M4 laptop profile — labeled intake, Slack progress, and a thread reply.
#
#   MODE=offline ./spike/m4/run.sh    # default; ends with "M4 OFFLINE PASS"
#   MODE=live    ./spike/m4/run.sh    # GitHub App + Slack + a local model
#   MODE=live-ci ./spike/m4/run.sh    # signed local delivery, real PR and Slack
#
# Offline posts a signed issues.labeled delivery at the fakes in profile m4.
# A second copy of that delivery is deduped. A different issue is unmatched.
# The building model records one commit, a signed pull_request opens the
# check run, and a signed Slack reply starts a second building run.
#
# Task containers are on the host Docker bridge. They reach published ports
# through the bridge gateway on Linux and through host.docker.internal on
# Docker Desktop. The live model URL from a container is
# http://host.docker.internal:8317/v1. This script does not rewrite a
# loopback model URL.
#
# Live reads deploy/.env when that file exists (KEY=VALUE text, not
# executed). A non-empty shell variable wins. Secrets stay in that file.
# The App private key stays at spike/m4/secrets/github-app.pem. Live copies
# it to spike/m4/.work/live/github-app.pem (mode 644) and mounts the copy.
# live-ci reads M4_GITHUB_APP_PRIVATE_KEY (PEM text) and writes it to
# spike/m4/.work/live-ci/github-app.pem (mode 644). It does not start a
# tunnel. It signs a synthetic issues.labeled delivery with
# FACTORY_GITHUB_WEBHOOK_SECRET, generating that secret when it is empty,
# and posts the delivery to the local ingress. The sender is
# M4_LIVE_CI_SENDER, or the repo owner when that is empty, and its type
# is User. On exit the trap closes that issue, closes the draft pull
# request, and deletes the pull request branch. Nothing in this script
# prints token or key material.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
WORK="$HERE/.work"
COMPOSE_BASE="$ROOT/deploy/docker-compose.yml"
COMPOSE_OVERLAY="$HERE/compose.yml"
PROJECT="factory-m4"
FACTORY_NAME="m4"
SRC_DEF="$ROOT/deploy/m4-factory"

HARNESS_IMAGE="factory-harness:dev"
TASK_IMAGE="factory-m1-task:dev"
MODE="${MODE:-offline}"

OFFLINE_DUMMY_KEY="m4-offline-dummy-7f3c9a"
OFFLINE_GH="m4-offline-gh-7f3c9a2e"
OFFLINE_WEBHOOK="m4-offline-gh-whsec-7f3c9a"
OFFLINE_SLACK_BOT="xoxb-m4-offline-7f3c9a2e1b4d"
OFFLINE_SLACK_SIGN="m4-offline-slack-sign-7f3c9a"
OFFLINE_APP_ID="99032"
OFFLINE_TEAM="T0M4FACTORY"
OFFLINE_CHANNEL="C0M4FACTORY"
OFFLINE_USER="U0M4HUMAN"
OFFLINE_API_KEY="m4-api-7f3c9a2e1b4d"
OFFLINE_WORKER_KEY="m4-worker-7f3c9a2e1b4d"
OFFLINE_S3="m4s3-secret-7f3c9a2e1b4d"
PLANTED_DUMMY="m4-planted-dummy-7f3c9a"
LIVE_MODEL_URL="http://host.docker.internal:8317/v1"

ISSUE_NUMBER=17
PR_NUMBER=77
PR_URL="https://github.com/polds/factory/pull/77"
DELIVERY_ISSUE="delivery-issue-1"
DELIVERY_UNMATCHED="delivery-unmatched-1"
DELIVERY_PR="delivery-pr-1"
SLACK_EVENT_ID="EvM4REPLY1"
M4_FAKE_GITHUB_PORT="${M4_FAKE_GITHUB_PORT:-18080}"
M4_FAKE_SLACK_PORT="${M4_FAKE_SLACK_PORT:-18081}"

ROUTER_PID=""
COMPOSE_STARTED=0
SECRETS=()
LEAK_ANNOUNCED=0
TASK_ID=""
OCI_PLATFORM=""
GOARCH_TARGET=""
GW=""
TASK_HOST=""
DEF=""
PR_POSTED=0

# shellcheck disable=SC2317,SC2329 # invoked by the EXIT/INT/TERM trap
cleanup() {
  local code=$? pid=""
  trap - EXIT INT TERM ERR
  set +e
  close_live_ci || true
  if [[ -n "${ROUTER_PID}" ]]; then
    kill "${ROUTER_PID}" 2>/dev/null || true
  fi
  if [[ -f "$WORK/stub.pids" ]]; then
    while read -r pid; do
      if [[ -n "$pid" ]]; then
        kill "$pid" 2>/dev/null || true
      fi
    done <"$WORK/stub.pids"
  fi
  wait 2>/dev/null || true
  if [[ "${COMPOSE_STARTED}" -eq 1 ]]; then
    save_compose_logs || true
    compose --profile m4 --profile tunnel down -v --timeout 20 >/dev/null 2>&1 ||
      compose --profile m4 --profile tunnel down -v --timeout 20 || true
    COMPOSE_STARTED=0
  fi
  if ! scan_logs_for_secrets; then
    announce_leak
    code=1
  fi
  exit "${code}"
}

say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }

die() { fail "$*"; }

on_err() {
  local code="$1" line="$2"
  trap - ERR
  fail "command failed at line ${line} (exit ${code})"
}

run_capture() {
  local __var="$1"
  shift
  local __errexit=0
  case $- in
    *e*) __errexit=1 ;;
  esac
  trap - ERR
  set +e
  "$@"
  local __code=$?
  if [[ "$__errexit" -eq 1 ]]; then
    set -e
  fi
  trap 'on_err $? $LINENO' ERR
  printf -v "$__var" '%s' "$__code"
}

harness_contract_exit() {
  case "$1" in
    0 | 10 | 20 | 21 | 30) return 0 ;;
    *) return 1 ;;
  esac
}

normalize_docker_platform() {
  case "$1" in
    linux/aarch64 | linux/arm64) printf '%s\n' linux/arm64 ;;
    linux/x86_64 | linux/amd64) printf '%s\n' linux/amd64 ;;
    *) return 1 ;;
  esac
}

goarch_for_platform() {
  case "$1" in
    linux/arm64) printf '%s\n' arm64 ;;
    linux/amd64) printf '%s\n' amd64 ;;
    *) return 1 ;;
  esac
}

uname_for_platform() {
  case "$1" in
    linux/arm64) printf '%s\n' aarch64 ;;
    linux/amd64) printf '%s\n' x86_64 ;;
    *) return 1 ;;
  esac
}

# load_env_file reads KEY=VALUE lines. It does not expand or execute values.
# Empty values are ignored. A variable that is already non-empty is kept.
load_env_file() {
  local file="$1" line="" n=0 key="" val=""
  local -a keys=() vals=()
  [[ -f "$file" ]] || return 1
  while IFS= read -r line || [[ -n "$line" ]]; do
    n=$((n + 1))
    line="${line%$'\r'}"
    line="${line#"${line%%[![:space:]]*}"}"
    [[ -z "$line" || "$line" == \#* ]] && continue
    if [[ "$line" == export\ * ]]; then
      line="${line#export }"
      line="${line#"${line%%[![:space:]]*}"}"
    fi
    if [[ ! "$line" =~ ^([A-Za-z_][A-Za-z0-9_]*)=(.*)$ ]]; then
      echo "deploy/.env:${n}: invalid line" >&2
      return 1
    fi
    key="${BASH_REMATCH[1]}"
    val="${BASH_REMATCH[2]}"
    if [[ ${#val} -ge 2 && "$val" == \"*\" && "$val" == *\" ]]; then
      val="${val:1:${#val}-2}"
    elif [[ ${#val} -ge 2 && "$val" == \'*\' && "$val" == *\' ]]; then
      val="${val:1:${#val}-2}"
    fi
    if [[ -z "$val" || -n "${!key:-}" ]]; then
      continue
    fi
    keys+=("$key")
    vals+=("$val")
  done <"$file"
  local i
  for i in "${!keys[@]}"; do
    printf -v "${keys[$i]}" '%s' "${vals[$i]}"
    export "${keys[$i]}"
  done
}

copy_if_empty() {
  local dest="$1" src="$2"
  if [[ -z "${!dest:-}" && -n "${!src:-}" ]]; then
    printf -v "$dest" '%s' "${!src}"
    export "${dest?}"
  fi
}

apply_live_env_aliases() {
  copy_if_empty OPENAI_BASE_URL FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL
  copy_if_empty OPENAI_BASE_URL M1_OPENAI_BASE_URL
  copy_if_empty OPENAI_API_KEY FACTORY_INFERENCE_OPENAI_API_KEY
  copy_if_empty OPENAI_API_KEY M1_OPENAI_API_KEY
  copy_if_empty GH_TOKEN FACTORY_SECRET_GH_TOKEN
  copy_if_empty FACTORY_SLACK_BOT_TOKEN M4_SLACK_BOT_TOKEN
  copy_if_empty M4_SLACK_BOT_TOKEN FACTORY_SLACK_BOT_TOKEN
  copy_if_empty FACTORY_GITHUB_APP_ID M4_GITHUB_APP_ID
  copy_if_empty M4_GITHUB_APP_ID FACTORY_GITHUB_APP_ID
}

accept_mode() {
  case "${1:-}" in
    offline | live | live-ci) return 0 ;;
    *) return 1 ;;
  esac
}

configure_live_ci_webhook() {
  if [[ -n "${FACTORY_GITHUB_WEBHOOK_SECRET:-}" ]]; then
    return 0
  fi
  FACTORY_GITHUB_WEBHOOK_SECRET="$(openssl rand -hex 32)"
  export FACTORY_GITHUB_WEBHOOK_SECRET
}

# stage_app_key_from_env writes M4_GITHUB_APP_PRIVATE_KEY to dest at mode 644.
# It does not print the PEM.
stage_app_key_from_env() {
  local dest="$1"
  if [[ -z "${M4_GITHUB_APP_PRIVATE_KEY:-}" ]]; then
    echo "M4_GITHUB_APP_PRIVATE_KEY is empty" >&2
    return 1
  fi
  if [[ -z "$dest" ]]; then
    echo "App key destination is empty" >&2
    return 1
  fi
  mkdir -p "$(dirname "$dest")"
  M4_KEY_DEST="$dest" M4_GITHUB_APP_PRIVATE_KEY="$M4_GITHUB_APP_PRIVATE_KEY" python3 - <<'PY'
import os

pem = os.environ.get("M4_GITHUB_APP_PRIVATE_KEY", "")
dest = os.environ["M4_KEY_DEST"]
if pem and not pem.endswith("\n"):
    pem += "\n"
with open(dest, "w", encoding="utf-8") as handle:
    handle.write(pem)
PY
  chmod 644 "$dest"
  [[ "$(file_mode "$dest")" == "644" ]] || return 1
  head -n 1 "$dest" | grep -q '^-----BEGIN '
}

live_app_key_source() {
  if [[ -n "${FACTORY_GITHUB_APP_PRIVATE_KEY_FILE:-}" ]]; then
    printf '%s\n' "$FACTORY_GITHUB_APP_PRIVATE_KEY_FILE"
    return 0
  fi
  printf '%s\n' "$HERE/secrets/github-app.pem"
}

file_mode() {
  python3 -c 'import os, sys; print(format(os.stat(sys.argv[1]).st_mode & 0o777, "03o"))' "$1"
}

# stage_live_app_key copies src to dest at mode 644 and leaves src unchanged.
# factoryd is uid 65532 and cannot read a mode-600 mount. The caller mounts
# dest and keeps it off the offline key path, which openssl replaces.
stage_live_app_key() {
  local src="$1" dest="$2" mode=""
  if [[ ! -f "$src" ]]; then
    return 1
  fi
  if [[ "$src" -ef "$dest" ]]; then
    return 1
  fi
  mkdir -p "$(dirname "$dest")"
  install -m 644 "$src" "$dest"
  mode="$(file_mode "$dest")"
  [[ "$mode" == "644" ]] || return 1
  cmp -s "$src" "$dest"
}

url_log_host() {
  RAW="$1" python3 - <<'PY'
import os
from urllib.parse import urlparse
parts = urlparse(os.environ.get("RAW", ""))
host = parts.hostname or ""
if parts.port:
    host = f"{host}:{parts.port}"
print(host)
PY
}

assert_linux_elf() {
  local bin="$1" arch="$2"
  python3 - "$bin" "$arch" <<'PY'
import struct
import sys

path, arch = sys.argv[1], sys.argv[2]
want = {"amd64": 62, "arm64": 183}
data = open(path, "rb").read(20)
if len(data) < 20 or data[:4] != b"\x7fELF":
    sys.stderr.write("%s: not a linux ELF binary\n" % path)
    sys.exit(1)
if data[4] != 2 or data[5] != 1:
    sys.stderr.write("%s: want a 64-bit little-endian ELF\n" % path)
    sys.exit(1)
machine = struct.unpack_from("<H", data, 18)[0]
if arch not in want or machine != want[arch]:
    sys.stderr.write("%s: ELF machine %s, want %s for %s\n" % (path, machine, want.get(arch), arch))
    sys.exit(1)
PY
}

assert_image_platform() {
  local image="$1" want="$2" got=""
  got="$(docker image inspect --format '{{.Os}}/{{.Architecture}}' "$image" 2>/dev/null || true)"
  if [[ "$got" != "$want" ]]; then
    echo "image ${image} platform is ${got:-unknown}, want ${want}" >&2
    return 1
  fi
}

compose() {
  docker compose -p "$PROJECT" -f "$COMPOSE_BASE" -f "$COMPOSE_OVERLAY" "$@"
}

remember() {
  local value="$1"
  if [[ -n "$value" ]]; then
    SECRETS+=("$value")
  fi
}

scan_files() {
  local secret files=()
  while [[ $# -gt 0 ]]; do
    if [[ -f "$1" ]]; then
      files+=("$1")
    fi
    shift
  done
  if [[ ${#files[@]} -eq 0 ]]; then
    return 0
  fi
  for secret in "${SECRETS[@]+"${SECRETS[@]}"}"; do
    if [[ -z "$secret" ]]; then
      continue
    fi
    if grep -F -q -e "$secret" "${files[@]}"; then
      return 1
    fi
  done
  if ! scan_patterns "${files[@]}"; then
    return 1
  fi
  return 0
}

scan_patterns() {
  python3 - "$@" <<'PY'
import re
import sys

pattern = re.compile(
    r"ghp_|ghs_|github_pat_|xox[bpa]-|sk-|lin_api_|BEGIN .*PRIVATE KEY"
)
for path in sys.argv[1:]:
    try:
        text = open(path, "r", errors="replace").read()
    except OSError:
        continue
    if pattern.search(text):
        sys.exit(1)
sys.exit(0)
PY
}

log_files() {
  local f="" nullglob=0
  if shopt -q nullglob; then
    nullglob=1
  fi
  shopt -s nullglob
  LOG_FILES=(
    "$WORK/compose.log"
    "$WORK/factoryctl-list.err"
    "$WORK/factoryctl-get.err"
    "$WORK/factoryctl-deliveries.err"
    "$WORK/factoryctl-schedules.err"
    "$WORK/image-exec.err"
    "$WORK/model-router.log"
    "$WORK/github-state.json"
    "$WORK/slack-state.json"
    "$WORK/apptoken.err"
    "$WORK/live-issue.json"
    "$WORK/live-pr.json"
    "$WORK/live-slack.json"
  )
  for f in "$WORK"/model-stub-*.log "$WORK"/evidence/* "$WORK"/live-ci-cleanup-*.err; do
    if [[ -f "$f" ]]; then
      LOG_FILES+=("$f")
    fi
  done
  if [[ "$nullglob" -eq 0 ]]; then
    shopt -u nullglob
  fi
}

scan_logs_for_secrets() {
  log_files
  scan_files "${LOG_FILES[@]+"${LOG_FILES[@]}"}"
}

discard_captured_logs() {
  rm -rf "$WORK/evidence"
  rm -f \
    "$WORK/compose.log" \
    "$WORK/factoryctl-list.err" \
    "$WORK/factoryctl-get.err" \
    "$WORK/factoryctl-deliveries.err" \
    "$WORK/factoryctl-schedules.err" \
    "$WORK/image-exec.err" \
    "$WORK/model-router.log" \
    "$WORK/github-state.json" \
    "$WORK/slack-state.json" \
    "$WORK/apptoken.err" \
    "$WORK/live-issue.json" \
    "$WORK/live-pr.json" \
    "$WORK/live-slack.json" \
    "$WORK"/model-stub-*.log
}

announce_leak() {
  discard_captured_logs
  if [[ "$LEAK_ANNOUNCED" -eq 0 ]]; then
    echo "M4 FAIL — a secret value appeared in the logs or captured state; output withheld"
    LEAK_ANNOUNCED=1
  fi
}

prove_scanner() {
  local proof="$WORK/planted-secret.txt" pattern="$WORK/planted-pattern.txt"
  local saved=() rejected=0
  mkdir -p "$WORK"
  printf 'planted %s\n' "$PLANTED_DUMMY" >"$proof"
  printf 'ghs_planted\n' >"$pattern"
  saved=("${SECRETS[@]+"${SECRETS[@]}"}")
  SECRETS=("$PLANTED_DUMMY")
  if ! scan_files "$proof"; then
    rejected=1
  fi
  SECRETS=("${saved[@]+"${saved[@]}"}")
  if scan_patterns "$pattern"; then
    rejected=0
  fi
  rm -f "$proof" "$pattern"
  if [[ -e "$proof" || -e "$pattern" ]]; then
    die "planted dummy file was not removed"
  fi
  if [[ "$rejected" -ne 1 ]]; then
    die "secret scanner did not fail on a planted value"
  fi
  echo "secret scanner rejects a planted dummy"
}

save_compose_logs() {
  if [[ "${COMPOSE_STARTED}" -ne 1 ]]; then
    return 0
  fi
  mkdir -p "$WORK"
  compose logs --no-color >"$WORK/compose.log" 2>&1 || true
}

dump_failure() {
  set +e
  echo
  echo "── compose log (tail) ────────────────────────────────────────"
  if [[ -f "$WORK/compose.log" ]]; then
    tail -n 120 "$WORK/compose.log"
  else
    echo "(no compose log)"
  fi
  local err=""
  for err in \
    "$WORK/factoryctl-list.err" \
    "$WORK/factoryctl-get.err" \
    "$WORK/factoryctl-deliveries.err" \
    "$WORK/factoryctl-schedules.err" \
    "$WORK/image-exec.err" \
    "$WORK/model-router.log" \
    "$WORK/apptoken.err"; do
    if [[ -f "$err" ]]; then
      echo "── $(basename "$err") (tail) ────────────────────────────────"
      tail -n 40 "$err"
    fi
  done
  local stub=""
  for stub in "$WORK"/model-stub-*.log; do
    if [[ -f "$stub" ]]; then
      echo "── $(basename "$stub") (tail) ───────────────────────────────"
      tail -n 20 "$stub"
    fi
  done
  return 0
}

fail() {
  local reason="$*"
  trap - ERR
  set +e
  save_compose_logs
  if ! scan_logs_for_secrets; then
    announce_leak
    exit 1
  fi
  dump_failure
  echo
  echo "M4 FAIL — ${reason}"
  exit 1
}

bridge_gateway() {
  local gw
  gw="$(docker network inspect bridge --format '{{(index .IPAM.Config 0).Gateway}}')"
  if [[ ! "$gw" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    die "could not determine an IPv4 gateway for the docker bridge (got ${gw:-empty})"
  fi
  printf '%s\n' "$gw"
}

# host_for_tasks is the host name a bridge container uses for published ports.
# Docker Desktop publishes them on host.docker.internal. Linux uses the gateway.
host_for_tasks() {
  local os="$1" gw="$2"
  case "$os" in
    *Docker\ Desktop*) printf '%s\n' "host.docker.internal" ;;
    *) printf '%s\n' "$gw" ;;
  esac
}

distinct_models() {
  local seen="" item=""
  if [[ "$#" -eq 0 ]]; then
    echo "no model ids" >&2
    return 1
  fi
  for item in "$@"; do
    if [[ -z "$item" ]]; then
      echo "empty model id" >&2
      return 1
    fi
    if [[ ! "$item" =~ ^[A-Za-z0-9_.:/+-]+$ ]]; then
      echo "model id has characters this script will not write into the definition" >&2
      return 1
    fi
    case " ${seen} " in
      *" ${item} "*)
        echo "duplicate model id" >&2
        return 1
        ;;
    esac
    seen="${seen} ${item}"
  done
}

read_model() {
  awk '/^model: / { print $2; exit }' "$1"
}

require_id() {
  local value="$1" prefix="$2"
  if [[ ! "$value" =~ ^${prefix}[0-9A-Z]{26}$ ]]; then
    echo "bad ${prefix} id" >&2
    return 1
  fi
}

psql_at() {
  compose exec -T postgres \
    psql -U factory -d factory -v ON_ERROR_STOP=1 -At -c "$1"
}

sign_github() {
  local secret="$1" file="$2"
  python3 - "$secret" "$file" <<'PY'
import hashlib
import hmac
import sys

secret, path = sys.argv[1], sys.argv[2]
body = open(path, "rb").read()
print("sha256=" + hmac.new(secret.encode(), body, hashlib.sha256).hexdigest())
PY
}

sign_slack() {
  local secret="$1" ts="$2" file="$3"
  python3 - "$secret" "$ts" "$file" <<'PY'
import hashlib
import hmac
import sys

secret, ts, path = sys.argv[1], sys.argv[2], sys.argv[3]
body = open(path, "rb").read()
mac = hmac.new(secret.encode(), b"v0:" + ts.encode() + b":" + body, hashlib.sha256)
print("v0=" + mac.hexdigest())
PY
}

ack_duplicate() {
  python3 - "$1" <<'PY'
import json
import sys

doc = json.load(open(sys.argv[1], encoding="utf-8"))
if "duplicate" not in doc or not isinstance(doc["duplicate"], bool):
    sys.exit(2)
print("true" if doc["duplicate"] else "false")
PY
}

post_github() {
  local event="$1" delivery="$2" body="$3" dest="$4" sig=""
  sig="$(sign_github "$FACTORY_GITHUB_WEBHOOK_SECRET" "$body")"
  curl -sS -o "$dest" -w '%{http_code}' \
    -X POST "http://127.0.0.1:8080/api/v1/ingress/github" \
    -H "Content-Type: application/json" \
    -H "X-GitHub-Event: ${event}" \
    -H "X-GitHub-Delivery: ${delivery}" \
    -H "X-Hub-Signature-256: ${sig}" \
    --data-binary @"$body"
}

post_slack() {
  local body="$1" dest="$2" ts="" sig=""
  ts="$(date +%s)"
  sig="$(sign_slack "$FACTORY_SLACK_SIGNING_SECRET" "$ts" "$body")"
  curl -sS -o "$dest" -w '%{http_code}' \
    -X POST "http://127.0.0.1:8080/api/v1/ingress/slack" \
    -H "Content-Type: application/json" \
    -H "X-Slack-Request-Timestamp: ${ts}" \
    -H "X-Slack-Signature: ${sig}" \
    --data-binary @"$body"
}

write_labeled_issue() {
  local dest="$1" number="$2" label="$3" title="$4"
  python3 - "$dest" "$number" "$label" "$title" <<'PY'
import json
import sys

dest, number, label, title = sys.argv[1], int(sys.argv[2]), sys.argv[3], sys.argv[4]
doc = {
    "action": "labeled",
    "label": {"name": label},
    "issue": {
        "id": 17000 + number,
        "number": number,
        "title": title,
        "body": "Change the fixture greeting. Do not print tokens or API keys.",
        "html_url": "https://github.com/polds/factory/issues/%d" % number,
        "updated_at": "2026-10-15T00:00:00Z",
        "labels": [{"name": label}],
    },
    "repository": {
        "id": 32,
        "name": "factory",
        "full_name": "polds/factory",
        "owner": {"login": "polds", "id": 7, "type": "User"},
    },
    "sender": {"login": "octocat", "id": 1, "type": "User"},
}
with open(dest, "w", encoding="utf-8") as fh:
    json.dump(doc, fh)
    fh.write("\n")
PY
}

# live_ci_sender_for prints the human login for a synthetic delivery.
# M4_LIVE_CI_SENDER wins. Otherwise the login is the owner of repo.
# The intake trigger rejects authors in not_in, and the engine drops [bot].
live_ci_sender_for() {
  local repo="$1" sender="${M4_LIVE_CI_SENDER:-}"
  if [[ ! "$repo" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
    echo "repo must be owner/name" >&2
    return 1
  fi
  if [[ -z "$sender" ]]; then
    sender="${repo%%/*}"
  fi
  python3 - "$sender" "$ROOT/deploy/m4-factory/automations/labeled-intake/automation.md" <<'PY'
import re
import sys

login, path = sys.argv[1], sys.argv[2]
if "[bot]" in login.lower():
    raise SystemExit("sender is not a human login the intake trigger accepts")
if not re.fullmatch(r"[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?", login):
    raise SystemExit("sender is not a human login the intake trigger accepts")
text = open(path, encoding="utf-8").read()
match = re.search(r"not_in:\s*\[([^\]]*)\]", text)
excluded = []
if match:
    excluded = [part.strip().strip("'\"") for part in match.group(1).split(",") if part.strip()]
if login in excluded:
    raise SystemExit("sender is not a human login the intake trigger accepts")
print(login)
PY
}

resolve_live_ci_sender() {
  local sender=""
  sender="$(live_ci_sender_for "$M4_REPO")" || return 1
  M4_LIVE_CI_SENDER="$sender"
  export M4_LIVE_CI_SENDER
}

write_live_ci_delivery_from_issue() {
  local dest="$1" issue_json="$2" repo="$3" sender=""
  if [[ ! "$repo" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
    echo "repo must be owner/name" >&2
    return 1
  fi
  sender="$(live_ci_sender_for "$repo")" || return 1
  python3 - "$dest" "$issue_json" "$repo" "$sender" <<'PY'
import json
import re
import sys

dest, src, repo, sender = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
if "[bot]" in sender.lower() or not re.fullmatch(r"[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?", sender):
    raise SystemExit("sender is not a human login the intake trigger accepts")
issue = json.load(open(src, encoding="utf-8"))
number = issue.get("number")
if not isinstance(number, int) or number <= 0:
    raise SystemExit("issue number")
owner, name = repo.split("/", 1)
doc = {
    "action": "labeled",
    "label": {"name": "factory-ready"},
    "issue": {
        "id": issue.get("id") or (17000 + number),
        "number": number,
        "title": issue.get("title") or "",
        "body": issue.get("body") or "",
        "html_url": issue.get("html_url") or ("https://github.com/%s/issues/%d" % (repo, number)),
        "updated_at": issue.get("updated_at") or "2026-10-15T00:00:00Z",
        "labels": [{"name": "factory-ready"}],
    },
    "repository": {
        "id": 32,
        "name": name,
        "full_name": repo,
        "owner": {"login": owner, "id": 7, "type": "User"},
    },
    "sender": {"login": sender, "id": 1, "type": "User"},
}
with open(dest, "w", encoding="utf-8") as fh:
    json.dump(doc, fh)
    fh.write("\n")
PY
}

write_opened_pull() {
  local dest="$1" sha="$2"
  python3 - "$dest" "$sha" "$PR_URL" "$ISSUE_NUMBER" "$PR_NUMBER" <<'PY'
import json
import sys

dest, sha, url, issue, number = sys.argv[1], sys.argv[2], sys.argv[3], int(sys.argv[4]), int(sys.argv[5])
doc = {
    "action": "opened",
    "number": number,
    "pull_request": {
        "id": 7700,
        "number": number,
        "title": "Emphasize Hello",
        "body": "Closes #%d" % issue,
        "html_url": url,
        "merged": False,
        "updated_at": "2026-10-15T00:10:00Z",
        "head": {"ref": "m4-hello", "sha": sha},
        "base": {"ref": "main", "sha": "def456"},
    },
    "repository": {
        "id": 32,
        "name": "factory",
        "full_name": "polds/factory",
        "owner": {"login": "polds", "id": 7, "type": "User"},
    },
    "sender": {"login": "octocat", "id": 1, "type": "User"},
}
with open(dest, "w", encoding="utf-8") as fh:
    json.dump(doc, fh)
    fh.write("\n")
PY
}

write_slack_reply() {
  local dest="$1" root_ts="$2"
  python3 - "$dest" "$root_ts" "$OFFLINE_TEAM" "$OFFLINE_CHANNEL" "$OFFLINE_USER" "$SLACK_EVENT_ID" <<'PY'
import json
import sys
import time

dest, root, team, channel, user, event_id = sys.argv[1:]
now = int(time.time())
doc = {
    "type": "event_callback",
    "team_id": team,
    "event_id": event_id,
    "event_time": now,
    "event": {
        "type": "message",
        "user": user,
        "text": "also add a test for an empty name",
        "ts": "1700000099.000200",
        "thread_ts": root,
        "channel": channel,
        "channel_type": "channel",
        "event_ts": "1700000099.000200",
    },
}
with open(dest, "w", encoding="utf-8") as fh:
    json.dump(doc, fh)
    fh.write("\n")
PY
}

fetch_github_state() {
  curl -fsS "http://127.0.0.1:${M4_FAKE_GITHUB_PORT}/_spike/state" >"$WORK/github-state.json"
}

fetch_slack_state() {
  curl -fsS "http://127.0.0.1:${M4_FAKE_SLACK_PORT}/_spike/state" >"$WORK/slack-state.json"
}

commit_count() {
  python3 - "$WORK/github-state.json" <<'PY'
import json
import sys

doc = json.load(open(sys.argv[1], encoding="utf-8"))
print(len(doc["pull"]["commits"]))
PY
}

commit_sha() {
  local n="$1"
  python3 - "$WORK/github-state.json" "$n" <<'PY'
import json
import sys

doc = json.load(open(sys.argv[1], encoding="utf-8"))
n = int(sys.argv[2])
commits = doc["pull"]["commits"]
if len(commits) < n or doc["pull"]["number"] != 77:
    sys.exit(1)
print(commits[n - 1]["sha"])
PY
}

checks_ready() {
  python3 - "$WORK/github-state.json" <<'PY'
import json
import sys

doc = json.load(open(sys.argv[1], encoding="utf-8"))
checks = doc["checks"]
created = any(c["op"] == "create" and c["status"] == "in_progress" for c in checks)
updated = any(
    c["op"] == "update" and c["status"] == "completed" and c["conclusion"] == "success"
    for c in checks
)
minted = doc["token_mints"] >= 1 and any(
    "factory" in (row.get("repositories") or []) for row in doc["mints"]
)
sys.exit(0 if created and updated and minted else 1)
PY
}

slack_root_ts() {
  python3 - "$WORK/slack-state.json" "$PR_URL" <<'PY'
import json
import sys

doc = json.load(open(sys.argv[1], encoding="utf-8"))
pr = sys.argv[2]
msgs = doc["messages"]
roots = [m for m in msgs if m["op"] == "post" and m["thread_ts"] == ""]
updates = [m for m in msgs if m["op"] == "update"]
milestone = [
    m
    for m in msgs
    if m["op"] == "post" and "Pull request opened:" in m["text"] and pr in m["text"]
]
blob = "\n".join(m["text"] for m in msgs)
if len(roots) == 1 and updates and milestone and "implementer" in blob and "m4-" in blob:
    print(roots[0]["ts"])
    sys.exit(0)
sys.exit(1)
PY
}

filter_task_lines() {
  python3 -c 'import re,sys
pat = re.compile(r"^task_[0-9A-Z]{26}$")
for line in sys.stdin:
    field = line.split(" ", 1)[0].strip()
    if pat.fullmatch(field):
        print(field)'
}

task_ids() {
  "$FACTORYCTL" task list --factory "$FACTORY_NAME" 2>>"$WORK/factoryctl-list.err" |
    filter_task_lines
}

one_task_id() {
  local ids="" n=0
  ids="$(task_ids)"
  if [[ -z "$ids" ]]; then
    return 1
  fi
  n="$(printf '%s\n' "$ids" | awk 'NF { c++ } END { print c+0 }')"
  if [[ "$n" -ne 1 ]]; then
    echo "expected one work item, saw ${n}" >&2
    return 1
  fi
  printf '%s\n' "$ids"
}

handoff_complete() {
  local line=""
  line="$("$FACTORYCTL" task get --factory "$FACTORY_NAME" "$TASK_ID" 2>>"$WORK/factoryctl-get.err" || true)"
  [[ "$line" == *"stage=human_handoff"* && "$line" == *"status=complete"* ]]
}

building_conversations() {
  psql_at "SELECT conversation_id FROM runs WHERE work_item_id = '${TASK_ID}' AND stage = 'building' ORDER BY created_at"
}

apply_offline_secrets() {
  local host="$1" name="" value=""
  local -a names=(
    FACTORY_GITHUB_WEBHOOK_SECRET
    FACTORY_GITHUB_APP_PRIVATE_KEY_FILE
    FACTORY_SLACK_BOT_TOKEN
    FACTORY_SLACK_SIGNING_SECRET
    FACTORY_LINEAR_API_KEY
    FACTORY_LINEAR_WEBHOOK_SECRET
    FACTORY_SECRET_GH_TOKEN
    FACTORY_INFERENCE_OPENAI_API_KEY
    GH_TOKEN
    OPENAI_API_KEY
    SLACK_TEST_USER_TOKEN
  )
  for name in "${names[@]}"; do
    value="${!name:-}"
    if [[ -n "$value" ]]; then
      remember "$value"
    fi
  done
  unset GH_TOKEN OPENAI_API_KEY OPENAI_BASE_URL SLACK_TEST_USER_TOKEN
  export FACTORY_GITHUB_APP_ID="$OFFLINE_APP_ID"
  export FACTORY_GITHUB_TOKEN_MODE="app"
  export FACTORY_GITHUB_WEBHOOK_SECRET="$OFFLINE_WEBHOOK"
  export FACTORY_GITHUB_API_BASE="http://fake-github:8080"
  export FACTORY_SLACK_BOT_TOKEN="$OFFLINE_SLACK_BOT"
  export FACTORY_SLACK_SIGNING_SECRET="$OFFLINE_SLACK_SIGN"
  export FACTORY_SLACK_TEAM_ID="$OFFLINE_TEAM"
  export FACTORY_SLACK_API_BASE="http://fake-slack:8080"
  export FACTORY_LINEAR_API_KEY=""
  export FACTORY_LINEAR_WEBHOOK_SECRET=""
  export FACTORY_LINEAR_API_BASE=""
  export FACTORY_DELIVERY_RETENTION="168h"
  export FACTORY_SCHEDULE_CATCHUP="1h"
  export FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL="http://${host}:9090"
  export FACTORY_INFERENCE_OPENAI_API_KEY="$OFFLINE_DUMMY_KEY"
  export FACTORY_SECRET_GH_TOKEN="$OFFLINE_GH"
  export FACTORY_API_KEY="$OFFLINE_API_KEY"
  export FACTORY_WORKER_API_KEY="$OFFLINE_WORKER_KEY"
  export S3_SECRET_KEY="$OFFLINE_S3"
  export FACTORY_EXTERNAL_URL="http://127.0.0.1:8080"
  remember "$OFFLINE_WEBHOOK"
  remember "$OFFLINE_SLACK_BOT"
  remember "$OFFLINE_SLACK_SIGN"
  remember "$OFFLINE_DUMMY_KEY"
  remember "$OFFLINE_GH"
  remember "$OFFLINE_API_KEY"
  remember "$OFFLINE_WORKER_KEY"
  remember "$OFFLINE_S3"
}

prepare_definition() {
  local dest="$1"
  rm -rf "$dest"
  mkdir -p "$dest"
  cp -a "$SRC_DEF/." "$dest/"
  DEF_ROOT="$dest" DEF_MODE="$MODE" python3 - <<'PY'
import os
import re
from pathlib import Path

root = Path(os.environ["DEF_ROOT"])
mode = os.environ["DEF_MODE"]
if mode == "offline":
    path = root / "automations/hourly/automation.md"
    text = path.read_text(encoding="utf-8")
    old = "enabled: false\n"
    if text.count(old) != 1:
        raise SystemExit("hourly automation does not have one disabled line")
    path.write_text(text.replace(old, "enabled: true\n", 1), encoding="utf-8")
elif mode in ("live", "live-ci"):
    channel = os.environ["M4_SLACK_CHANNEL"]
    user = os.environ.get("M4_SLACK_ALLOWED_USER", "")
    if not re.fullmatch(r"[CG][A-Z0-9]+", channel):
        raise SystemExit("slack channel is not a safe id")
    if mode == "live" and not re.fullmatch(r"U[A-Z0-9]+", user or ""):
        raise SystemExit("slack user is not a safe id")
    if user and not re.fullmatch(r"U[A-Z0-9]+", user):
        raise SystemExit("slack user is not a safe id")
    path = root / "factory.yaml"
    text = path.read_text(encoding="utf-8")
    text2 = text.replace("channel: C0M4FACTORY\n", "channel: %s\n" % channel, 1)
    if user:
        text2 = text2.replace("- U0M4HUMAN\n", "- %s\n" % user, 1)
    if text2 == text or "C0M4FACTORY" in text2:
        raise SystemExit("factory.yaml slack channel was not replaced")
    if user and "U0M4HUMAN" in text2:
        raise SystemExit("factory.yaml slack user was not replaced")
    path.write_text(text2, encoding="utf-8")
else:
    raise SystemExit("unknown mode")
PY
  chmod -R a+rX "$dest"
}

write_final_script() {
  local dest="$1" text="$2"
  python3 - "$dest" "$text" <<'PY'
import json
import sys

path, text = sys.argv[1], sys.argv[2]
with open(path, "w", encoding="utf-8") as fh:
    json.dump([{"final": text}], fh)
    fh.write("\n")
PY
}

write_building_script() {
  local dest="$1" url="$2"
  if [[ ! "$url" =~ ^http://[A-Za-z0-9._-]+:[0-9]+/_spike/commits$ ]]; then
    echo "commit URL is not a host and port this script will write" >&2
    return 1
  fi
  python3 - "$dest" "$url" <<'PY'
import json
import sys

path, url = sys.argv[1], sys.argv[2]


def curl(message, dest, pause):
    payload = json.dumps({"message": message})
    command = (
        "curl -fsS -m 20 -o "
        + dest
        + " -X POST "
        + url
        + ' -H "Authorization: Bearer ${GH_TOKEN}"'
        + ' -H "Content-Type: application/json"'
        + " -d "
        + json.dumps(payload)
    )
    if pause:
        # The intake run uses this model too. The pause keeps the first
        # commit visible until the driver posts the pull_request delivery.
        command += " && sleep 5"
    return command


steps = [
    {"final": "intake: scope recorded"},
    {"tool": "bash", "args": {"command": curl("m4 commit", "/tmp/m4-commit.json", True)}},
    {"final": "building: first change recorded"},
    {"tool": "bash", "args": {"command": curl("m4 follow-up", "/tmp/m4-follow.json", False)}},
    {"final": "building: follow-up recorded"},
]
with open(path, "w", encoding="utf-8") as fh:
    json.dump(steps, fh)
    fh.write("\n")
PY
}

start_stub() {
  local name="$1" port="$2" script="$3"
  "$WORK/model-stub" -addr "127.0.0.1:${port}" -script "$script" \
    >"$WORK/model-stub-${name}.log" 2>&1 &
  echo "$!" >>"$WORK/stub.pids"
}

wait_http() {
  local url="$1" _
  for _ in $(seq 1 50); do
    if curl -fsS "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.2
  done
  return 1
}

write_router() {
  cat >"$WORK/model-router.py" <<'PY'
"""Route each stage model id to its own model-stub. Do not log bodies."""

import json
import os
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

ROUTES = {}
with open(os.environ["M4_ROUTES_FILE"], encoding="utf-8") as handle:
    for raw in handle:
        line = raw.strip()
        if not line:
            continue
        model, url = line.split("|", 1)
        ROUTES[model] = url.rstrip("/")


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        sys.stderr.write("model-stub %s\n" % (fmt % args))

    def do_GET(self):
        if self.path.split("?", 1)[0] == "/healthz":
            body = b"ok\n"
            self.send_response(200)
            self.send_header("Content-Type", "text/plain")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        self.send_error(404)

    def do_POST(self):
        path = self.path.split("?", 1)[0]
        if path not in ("/chat/completions", "/v1/chat/completions"):
            self.send_error(404)
            return
        length = int(self.headers.get("Content-Length", "0"))
        if length < 0 or length > 1_000_000:
            self.send_error(413)
            return
        body = self.rfile.read(length)
        try:
            payload = json.loads(body)
        except json.JSONDecodeError:
            self._json(400, {"error": {"message": "invalid json"}})
            return
        model = payload.get("model")
        if not isinstance(model, str) or model not in ROUTES:
            sys.stderr.write("model-stub route unknown\n")
            self._json(400, {"error": {"message": "unknown model"}})
            return
        target = ROUTES[model] + path
        req = Request(target, data=body, method="POST")
        req.add_header("Content-Type", "application/json")
        try:
            with urlopen(req, timeout=30) as resp:
                raw = resp.read()
                status = resp.status
        except HTTPError as exc:
            raw = exc.read()
            status = exc.code
        except URLError:
            self._json(502, {"error": {"message": "model stub unavailable"}})
            return
        if status == 200:
            raw = inject_usage(raw)
        sys.stderr.write("model-stub route model=%s status=%s\n" % (model, status))
        self._bytes(status, raw)

    def _json(self, status, obj):
        self._bytes(status, json.dumps(obj).encode("utf-8"))

    def _bytes(self, status, raw):
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)


def inject_usage(raw):
    try:
        parsed = json.loads(raw)
    except json.JSONDecodeError:
        return raw
    if isinstance(parsed, dict) and "usage" not in parsed:
        parsed["usage"] = {"prompt_tokens": 1, "completion_tokens": 1}
        return json.dumps(parsed).encode("utf-8")
    return raw


addr = os.environ["M4_ROUTER_ADDR"]
host, port = addr.rsplit(":", 1)


class Server(ThreadingHTTPServer):
    allow_reuse_address = True


Server((host, int(port)), Handler).serve_forever()
PY
}

print_live_checklist() {
  cat <<EOF

Live checklist
  1. Start a tunnel to factoryd on this machine:
       cloudflared tunnel --url http://localhost:8080
     Or start the compose tunnel profile yourself. Its URL is not stable.
  2. Set FACTORY_PUBLIC_URL to that https origin.
  3. GitHub App webhook: \${FACTORY_PUBLIC_URL}/api/v1/ingress/github
     Slack request URL: \${FACTORY_PUBLIC_URL}/api/v1/ingress/slack
  4. Point the model server at ${LIVE_MODEL_URL}
     (FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL). Containers on Docker
     Desktop reach the Mac through host.docker.internal.
  5. Put the GitHub App private key at spike/m4/secrets/github-app.pem.
     The script copies it to spike/m4/.work/live/github-app.pem (mode 644)
     and mounts that copy. The original file is left unchanged.
  6. After the draft PR opens, reply in the Slack thread.
     Suggested text: also add a test for an empty name
     Set REPLY_TEXT and SLACK_TEST_USER_TOKEN to post that reply as a user.

EOF
  if [[ -n "${FACTORY_PUBLIC_URL:-}" ]]; then
    echo "GitHub App webhook: ${FACTORY_PUBLIC_URL}/api/v1/ingress/github"
    echo "Slack request URL: ${FACTORY_PUBLIC_URL}/api/v1/ingress/slack"
  fi
}

require_live_ids() {
  [[ "${FACTORY_SLACK_TEAM_ID:-}" =~ ^T[A-Z0-9]+$ ]] || die "FACTORY_SLACK_TEAM_ID must be a Slack team id"
  [[ "${M4_SLACK_CHANNEL:-}" =~ ^[CG][A-Z0-9]+$ ]] || die "M4_SLACK_CHANNEL must be a Slack channel id"
  [[ "${M4_SLACK_ALLOWED_USER:-}" =~ ^U[A-Z0-9]+$ ]] || die "M4_SLACK_ALLOWED_USER must be a Slack user id"
  [[ "${M4_REPO}" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || die "M4_REPO must be owner/name"
  if [[ -z "${FACTORY_PUBLIC_URL:-}" ]]; then
    print_live_checklist
    die "FACTORY_PUBLIC_URL is empty. Set it to the tunnel origin and run again."
  fi
  if [[ "$FACTORY_PUBLIC_URL" != https://* || "$FACTORY_PUBLIC_URL" == *"@"* ]]; then
    die "FACTORY_PUBLIC_URL must be an https origin without userinfo"
  fi
  FACTORY_PUBLIC_URL="${FACTORY_PUBLIC_URL%/}"
  export FACTORY_PUBLIC_URL
  export FACTORY_EXTERNAL_URL="$FACTORY_PUBLIC_URL"
}

live_ci_run_id() {
  local id="${GITHUB_RUN_ID:-local}"
  if [[ "$id" == local || "$id" =~ ^[0-9]+$ ]]; then
    printf '%s\n' "$id"
    return 0
  fi
  echo "GITHUB_RUN_ID is not a number" >&2
  return 1
}

live_ci_issue_title() {
  local id=""
  id="$(live_ci_run_id)" || return 1
  printf '[m4-live-ci %s] M4: emphasize Hello\n' "$id"
}

live_ci_marker() {
  local id=""
  id="$(live_ci_run_id)" || return 1
  printf 'm4-live-ci-marker: %s\n' "$id"
}

live_ci_definition_repo() {
  python3 - "$ROOT/deploy/m4-factory/automations/labeled-intake/automation.md" <<'PY'
import re
import sys

text = open(sys.argv[1], encoding="utf-8").read()
owners = re.findall(r"owner: ([A-Za-z0-9_.-]+), name: ([A-Za-z0-9_.-]+)", text)
filters = re.findall(r"repos: \[([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)\]", text)
slugs = {"%s/%s" % pair for pair in owners}
slugs.update(filters)
if len(slugs) != 1:
    raise SystemExit("labeled-intake does not name one repo")
print(next(iter(slugs)))
PY
}

assert_live_ci_repo() {
  local defined=""
  defined="$(live_ci_definition_repo)" || return 1
  [[ "$M4_REPO" == "$defined" ]]
}

live_ci_cleanup_request() {
  local kind="$1"
  [[ "${M4_REPO:-}" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || return 1
  case "$kind" in
    issue)
      [[ "${ISSUE_LIVE:-}" =~ ^[1-9][0-9]*$ ]] || return 1
      printf 'PATCH /repos/%s/issues/%s state=closed\n' "$M4_REPO" "$ISSUE_LIVE"
      ;;
    pull)
      [[ "${PR_LIVE_NUMBER:-}" =~ ^[1-9][0-9]*$ ]] || return 1
      printf 'PATCH /repos/%s/pulls/%s state=closed\n' "$M4_REPO" "$PR_LIVE_NUMBER"
      ;;
    branch)
      [[ "${PR_LIVE_BRANCH:-}" =~ ^[A-Za-z0-9._/-]+$ ]] || return 1
      case "$PR_LIVE_BRANCH" in
        main | master | */main | */master | *..*) return 1 ;;
      esac
      printf 'DELETE /repos/%s/git/refs/heads/%s\n' "$M4_REPO" "$PR_LIVE_BRANCH"
      ;;
    *) return 1 ;;
  esac
}

select_draft_pull() {
  python3 - "$1" "$2" <<'PY'
import json
import re
import sys

path, number = sys.argv[1], sys.argv[2]
if not re.fullmatch(r"[1-9][0-9]*", number):
    raise SystemExit(1)
pattern = re.compile(r"(?<!\w)Closes #%s(?!\d)" % re.escape(number))
rows = json.load(open(path, encoding="utf-8"))
for row in rows:
    if row.get("isDraft") and pattern.search(row.get("body") or ""):
        print(row.get("url") or "")
        print(row.get("headRefOid") or "")
        print(row.get("number") or "")
        print(row.get("headRefName") or "")
        raise SystemExit(0)
raise SystemExit(1)
PY
}

record_live_ci_evidence() {
  [[ "${MODE:-}" == live-ci ]] || return 0
  mkdir -p "$WORK/evidence"
  if [[ -n "${ISSUE_LIVE:-}" ]]; then
    printf '%s\n' "https://github.com/${M4_REPO}/issues/${ISSUE_LIVE}" >"$WORK/evidence/issue.txt"
  fi
  if [[ -n "${PR_LIVE_URL:-}" ]]; then
    printf '%s\n' "$PR_LIVE_URL" >"$WORK/evidence/pull-request.txt"
  fi
  if [[ -n "${PR_LIVE_BRANCH:-}" ]]; then
    printf '%s\n' "$PR_LIVE_BRANCH" >"$WORK/evidence/branch.txt"
  fi
  if [[ -n "${PERMALINK:-}" ]]; then
    printf '%s\n' "$PERMALINK" >"$WORK/evidence/slack.txt"
  fi
}

# close_live_ci closes the issue and draft PR, then deletes the PR branch.
# Failures are logged without response bodies, so a token in an error stays off the console.
close_live_ci() {
  local kind="" spec="" method="" path=""
  [[ "${MODE:-}" == live-ci ]] || return 0
  record_live_ci_evidence || true
  [[ -n "${LIVE_TOKEN:-}" ]] || return 0
  mkdir -p "$WORK"
  for kind in issue pull branch; do
    spec="$(live_ci_cleanup_request "$kind" 2>/dev/null)" || continue
    method="${spec%% *}"
    path="${spec#* }"
    path="${path%% *}"
    if [[ "$method" == DELETE ]]; then
      GH_TOKEN="$LIVE_TOKEN" gh api --method DELETE \
        -H "Accept: application/vnd.github+json" \
        "$path" >/dev/null 2>"$WORK/live-ci-cleanup-${kind}.err" ||
        echo "live-ci cleanup: could not delete the pull request branch" >&2
    else
      GH_TOKEN="$LIVE_TOKEN" gh api --method PATCH \
        -H "Accept: application/vnd.github+json" \
        "$path" -f state=closed >/dev/null 2>"$WORK/live-ci-cleanup-${kind}.err" ||
        echo "live-ci cleanup: could not close the ${kind}" >&2
    fi
  done
}

mint_installation_token() {
  (cd "$ROOT" && CGO_ENABLED=0 go build -o "$WORK/apptoken" ./spike/m4/apptoken)
  LIVE_TOKEN="$("$WORK/apptoken" -repo "$M4_REPO" 2>"$WORK/apptoken.err" || true)"
  if [[ -z "$LIVE_TOKEN" ]]; then
    return 1
  fi
  remember "$LIVE_TOKEN"
  export GH_TOKEN="$LIVE_TOKEN"
  export FACTORY_SECRET_GH_TOKEN="$LIVE_TOKEN"
}

create_live_issue() {
  local title="M4: emphasize Hello"
  if [[ "$MODE" == live-ci ]]; then
    title="$(live_ci_issue_title)" || return 1
  fi
  GH_TOKEN="$LIVE_TOKEN" gh api --method POST \
    -H "Accept: application/vnd.github+json" \
    "/repos/${M4_REPO}/issues" \
    -f title="$title" \
    -f body="$(live_issue_body 0)" \
    >"$WORK/live-issue.json"
  ISSUE_LIVE="$(
    python3 - "$WORK/live-issue.json" <<'PY'
import json
import sys

doc = json.load(open(sys.argv[1], encoding="utf-8"))
number = doc.get("number")
if not isinstance(number, int) or number <= 0:
    sys.exit(1)
print(number)
PY
  )" || return 1
  python3 - "$WORK/live-issue-body.txt" "$ISSUE_LIVE" "$M4_REPO" <<'PY'
import sys

path, number, repo = sys.argv[1], sys.argv[2], sys.argv[3]
text = """Repository: https://github.com/%s

Change spike/m1/fixture/greet/greet.go so Hello returns the concatenation of "Hello, ", name, and "!".
Update spike/m1/fixture/greet/greet_test.go so the expected greeting for M1 is Hello, M1!.
Run go test ./... in spike/m1/fixture.
GH_TOKEN is already in the environment. Run gh auth setup-git before git push.
Open a draft pull request whose body contains Closes #%s.
Do not print GH_TOKEN or OPENAI_API_KEY. Do not merge.
""" % (repo, number)
open(path, "w", encoding="utf-8").write(text)
PY
  GH_TOKEN="$LIVE_TOKEN" gh api --method PATCH \
    -H "Accept: application/vnd.github+json" \
    "/repos/${M4_REPO}/issues/${ISSUE_LIVE}" \
    -f body="$(cat "$WORK/live-issue-body.txt")" >/dev/null
  GH_TOKEN="$LIVE_TOKEN" gh api \
    -H "Accept: application/vnd.github+json" \
    "/repos/${M4_REPO}/issues/${ISSUE_LIVE}" \
    >"$WORK/live-issue.json"
}

wait_draft_pull() {
  local parsed=""
  PR_LIVE_URL=""
  PR_LIVE_SHA=""
  PR_LIVE_NUMBER=""
  PR_LIVE_BRANCH=""
  for _ in $(seq 1 360); do
    GH_TOKEN="$LIVE_TOKEN" gh pr list --repo "$M4_REPO" --state open \
      --json number,isDraft,url,body,headRefOid,headRefName >"$WORK/live-pr.json" || true
    parsed="$(select_draft_pull "$WORK/live-pr.json" "$ISSUE_LIVE")" &&
      PR_LIVE_URL="$(printf '%s\n' "$parsed" | awk 'NR==1')" &&
      PR_LIVE_SHA="$(printf '%s\n' "$parsed" | awk 'NR==2')" &&
      PR_LIVE_NUMBER="$(printf '%s\n' "$parsed" | awk 'NR==3')" &&
      PR_LIVE_BRANCH="$(printf '%s\n' "$parsed" | awk 'NR==4')" &&
      [[ -n "$PR_LIVE_URL" ]] && return 0
    sleep 10
  done
  return 1
}

read_slack_permalink() {
  local needle="${1:-M4: emphasize Hello}"
  curl -fsS -H "Authorization: Bearer ${FACTORY_SLACK_BOT_TOKEN}" \
    "https://slack.com/api/conversations.history?channel=${M4_SLACK_CHANNEL}&limit=50" \
    >"$WORK/live-slack.json" || return 1
  LIVE_ROOT="$(
    python3 - "$WORK/live-slack.json" "$needle" <<'PY'
import json
import sys

doc = json.load(open(sys.argv[1], encoding="utf-8"))
needle = sys.argv[2]
if not doc.get("ok"):
    sys.exit(1)
for msg in doc.get("messages") or []:
    text = msg.get("text") or ""
    if "Work item" in text and needle in text:
        print(msg.get("ts") or "")
        sys.exit(0)
sys.exit(1)
PY
  )" || return 1
  PERMALINK="https://slack.com/archives/${M4_SLACK_CHANNEL}/p${LIVE_ROOT//./}"
}

write_live_ci_evidence() {
  mkdir -p "$WORK/evidence"
  if [[ -f "$WORK/issue.json" ]]; then
    cp "$WORK/issue.json" "$WORK/evidence/delivery.json"
  fi
  if [[ -f "$WORK/ack-issue.json" ]]; then
    cp "$WORK/ack-issue.json" "$WORK/evidence/ack.json"
  fi
  record_live_ci_evidence
}

live_issue_body() {
  local number="$1"
  cat <<EOF
Repository: https://github.com/${M4_REPO}

Change spike/m1/fixture/greet/greet.go so Hello returns the concatenation of "Hello, ", name, and "!".
Update spike/m1/fixture/greet/greet_test.go so the expected greeting for M1 is Hello, M1!.
Run go test ./... in spike/m1/fixture.
GH_TOKEN is already in the environment. Run gh auth setup-git before git push.
Open a draft pull request whose body contains Closes #${number}.
Do not print GH_TOKEN or OPENAI_API_KEY. Do not merge.
EOF
  if [[ "$MODE" == live-ci ]]; then
    live_ci_marker
  fi
}

# Sourced by spike/m4/run_test.sh. Executing the file runs the laptop stack.
if [[ "${BASH_SOURCE[0]}" != "$0" ]]; then
  return 0
fi

trap cleanup EXIT INT TERM
trap 'on_err $? $LINENO' ERR

say "Preflight"
if ! accept_mode "$MODE"; then
  die "MODE must be offline, live, or live-ci (got ${MODE})"
fi

command -v docker >/dev/null || die "docker not found"
command -v go >/dev/null || die "go not found"
command -v curl >/dev/null || die "curl not found"
command -v python3 >/dev/null || die "python3 not found"
command -v openssl >/dev/null || die "openssl not found"
docker info >/dev/null 2>&1 || die "docker daemon is not reachable"

platform="$(docker info --format '{{.OSType}}/{{.Architecture}}' 2>/dev/null || true)"
case "$platform" in
  linux/aarch64 | linux/arm64 | linux/x86_64 | linux/amd64) ;;
  *)
    die "unsupported docker platform: $platform"
    ;;
esac
OCI_PLATFORM="$(normalize_docker_platform "$platform")" || die "unsupported docker platform: ${platform}"
GOARCH_TARGET="$(goarch_for_platform "$OCI_PLATFORM")" || die "unsupported docker platform: ${platform}"
export DOCKER_DEFAULT_PLATFORM="$OCI_PLATFORM"
echo "docker platform: $platform"
echo "image platform: $OCI_PLATFORM"
echo "go: $(go version)"
echo "mode: $MODE"

if [[ -f "$ROOT/deploy/.env" ]]; then
  load_env_file "$ROOT/deploy/.env" || die "deploy/.env is invalid"
fi
apply_live_env_aliases

M4_REPO="${M4_REPO:-polds/factory}"
if [[ "$MODE" == live ]]; then
  command -v gh >/dev/null || die "gh not found (required for MODE=live)"
  print_live_checklist
  require_live_ids
  [[ -n "${OPENAI_BASE_URL:-}" ]] || die "OPENAI_BASE_URL or FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL is required for MODE=live"
  [[ -n "${FACTORY_GITHUB_APP_ID:-}" ]] || die "FACTORY_GITHUB_APP_ID is required for MODE=live"
  [[ -n "${FACTORY_GITHUB_WEBHOOK_SECRET:-}" ]] || die "FACTORY_GITHUB_WEBHOOK_SECRET is required for MODE=live"
  [[ -n "${FACTORY_SLACK_BOT_TOKEN:-}" ]] || die "FACTORY_SLACK_BOT_TOKEN is required for MODE=live"
  [[ -n "${FACTORY_SLACK_SIGNING_SECRET:-}" ]] || die "FACTORY_SLACK_SIGNING_SECRET is required for MODE=live"
  remember "${FACTORY_GITHUB_WEBHOOK_SECRET}"
  remember "${FACTORY_SLACK_BOT_TOKEN}"
  remember "${FACTORY_SLACK_SIGNING_SECRET}"
  remember "${GH_TOKEN:-}"
  remember "${OPENAI_API_KEY:-}"
  remember "${SLACK_TEST_USER_TOKEN:-}"
  export FACTORY_GITHUB_TOKEN_MODE="${FACTORY_GITHUB_TOKEN_MODE:-app}"
fi
if [[ "$MODE" == live-ci ]]; then
  command -v gh >/dev/null || die "gh not found (required for MODE=live-ci)"
  [[ -n "${OPENAI_BASE_URL:-}" ]] || die "M1_OPENAI_BASE_URL or OPENAI_BASE_URL is required for MODE=live-ci"
  [[ "${M4_GITHUB_APP_ID:-}" =~ ^[1-9][0-9]*$ ]] || die "M4_GITHUB_APP_ID must be a positive integer"
  [[ -n "${M4_GITHUB_APP_PRIVATE_KEY:-}" ]] || die "M4_GITHUB_APP_PRIVATE_KEY is empty"
  [[ -n "${FACTORY_SLACK_BOT_TOKEN:-}" ]] || die "M4_SLACK_BOT_TOKEN is empty"
  [[ "${M4_SLACK_CHANNEL:-}" =~ ^[CG][A-Z0-9]+$ ]] || die "M4_SLACK_CHANNEL must be a Slack channel id"
  [[ "${M4_REPO}" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || die "M4_REPO must be owner/name"
  assert_live_ci_repo || die "M4_REPO does not match the labeled-intake repo in deploy/m4-factory"
  resolve_live_ci_sender || die "M4_LIVE_CI_SENDER is not a human login the intake trigger accepts"
  live_ci_issue_title >/dev/null || die "GITHUB_RUN_ID is not a number"
  configure_live_ci_webhook || die "could not configure FACTORY_GITHUB_WEBHOOK_SECRET"
  remember "${FACTORY_GITHUB_WEBHOOK_SECRET}"
  remember "${FACTORY_SLACK_BOT_TOKEN}"
  remember "${M4_GITHUB_APP_PRIVATE_KEY}"
  remember "${OPENAI_API_KEY:-}"
  export FACTORY_GITHUB_APP_ID="$M4_GITHUB_APP_ID"
  export FACTORY_GITHUB_TOKEN_MODE="app"
  export FACTORY_EXTERNAL_URL="${FACTORY_EXTERNAL_URL:-http://127.0.0.1:8080}"
  echo "live-ci: local signed delivery, no inbound tunnel"
fi

mkdir -p "$WORK" "$WORK/evidence"
: >"$WORK/stub.pids"
prove_scanner

GW="$(bridge_gateway)"
echo "docker bridge gateway: $GW"
DOCKER_OS="$(docker info --format '{{.OperatingSystem}}' 2>/dev/null || true)"
TASK_HOST="$(host_for_tasks "$DOCKER_OS" "$GW")"
echo "task host: $TASK_HOST"
if [[ -n "${FACTORY_TASK_SERVER_URL:-}" ]]; then
  echo "FACTORY_TASK_SERVER_URL host: $(url_log_host "$FACTORY_TASK_SERVER_URL")"
else
  case "$DOCKER_OS" in
    *Docker\ Desktop*)
      die "Docker Desktop: set FACTORY_TASK_SERVER_URL=http://host.docker.internal:8080. This script does not rewrite it."
      ;;
  esac
  export FACTORY_TASK_SERVER_URL="http://${GW}:8080"
  echo "FACTORY_TASK_SERVER_URL=http://${GW}:8080"
fi

export POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-factory}"
export S3_ACCESS_KEY="${S3_ACCESS_KEY:-factoryadmin}"
export FACTORY_LOG_LEVEL="${FACTORY_LOG_LEVEL:-info}"
export M4_FAKE_GITHUB_PORT M4_FAKE_SLACK_PORT

if [[ "$MODE" == offline ]]; then
  apply_offline_secrets "$TASK_HOST"
  openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out "$WORK/github-app.pem" >/dev/null 2>&1
  chmod 644 "$WORK/github-app.pem"
  export M4_GITHUB_APP_KEY_HOST="$WORK/github-app.pem"
  printf '%s\n' "$OFFLINE_SLACK_BOT" >"$WORK/slack-bot-token"
  chmod 644 "$WORK/slack-bot-token"
  export M4_SLACK_TOKEN_FILE="$WORK/slack-bot-token"
else
  live_host="$(url_log_host "$OPENAI_BASE_URL")"
  case "$live_host" in
    localhost | localhost:* | 127.0.0.1 | 127.0.0.1:* | "::1" | "::1:"*)
      die "OPENAI_BASE_URL points at a loopback host. Task containers cannot reach it. Set FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL=${LIVE_MODEL_URL}. This script does not rewrite that URL."
      ;;
  esac
  echo "task OPENAI_BASE_URL host: ${live_host}"
  if [[ "$MODE" == live-ci ]]; then
    key_copy="$WORK/live-ci/github-app.pem"
    if ! stage_app_key_from_env "$key_copy"; then
      die "Could not stage the GitHub App private key for factoryd."
    fi
  else
    key_src="$(live_app_key_source)"
    key_copy="$WORK/live/github-app.pem"
    if [[ ! -f "$key_src" ]]; then
      die "GitHub App private key file is missing (${key_src}). Keep it at spike/m4/secrets/github-app.pem."
    fi
    if [[ "$key_src" -ef "$WORK/github-app.pem" ]]; then
      die "The live App key must not be spike/m4/.work/github-app.pem. Offline overwrites that file. Keep the live key at spike/m4/secrets/github-app.pem."
    fi
    if ! stage_live_app_key "$key_src" "$key_copy"; then
      die "Could not stage a mode-644 copy of the GitHub App private key for factoryd."
    fi
  fi
  export M4_GITHUB_APP_KEY_HOST="$key_copy"
  export FACTORY_GITHUB_APP_PRIVATE_KEY_FILE="$key_copy"
  export FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL="$OPENAI_BASE_URL"
  export FACTORY_INFERENCE_OPENAI_API_KEY="${OPENAI_API_KEY:-}"
  export FACTORY_API_KEY="${FACTORY_API_KEY:-m4-api-7f3c9a2e1b4d}"
  export FACTORY_WORKER_API_KEY="${FACTORY_WORKER_API_KEY:-m4-worker-7f3c9a2e1b4d}"
  export S3_SECRET_KEY="${S3_SECRET_KEY:-m4s3-secret-7f3c9a2e1b4d}"
  remember "$FACTORY_API_KEY"
  remember "$FACTORY_WORKER_API_KEY"
  remember "$S3_SECRET_KEY"
fi

say "Build factoryctl and the offline binaries"
(cd "$ROOT" && CGO_ENABLED=0 go build -o "$WORK/factoryctl" ./cmd/factoryctl)
FACTORYCTL="$WORK/factoryctl"
"$FACTORYCTL" validate "$SRC_DEF"
say "Prepare the mounted definition"
prepare_definition "$WORK/definition"
DEF="$WORK/definition"
"$FACTORYCTL" validate "$DEF"

MODEL_TRIAGE="$(read_model "$DEF/agents/triage/agent.md")"
MODEL_PLANNING="$(read_model "$DEF/agents/spec/agent.md")"
MODEL_BUILDING="$(read_model "$DEF/agents/implementer/agent.md")"
MODEL_REVIEWING="$(read_model "$DEF/agents/reviewer/agent.md")"
distinct_models "$MODEL_TRIAGE" "$MODEL_PLANNING" "$MODEL_BUILDING" "$MODEL_REVIEWING" ||
  die "definition model ids must be four distinct values"
echo "models: triage=${MODEL_TRIAGE} planning=${MODEL_PLANNING} building=${MODEL_BUILDING} reviewing=${MODEL_REVIEWING}"

export FACTORY_DEFINITION_HOST_PATH="$DEF"
export FACTORY_DEFINITION_MOUNT="/definitions/m4-factory"
export FACTORY_DEFINITION_PATH="/definitions/m4-factory"

if [[ "$MODE" == offline ]]; then
  say "Build fake GitHub and fake Slack for linux/${GOARCH_TARGET}"
  (cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH_TARGET" \
    go build -o "$WORK/fake-github" ./spike/m4/fakes/github)
  (cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH_TARGET" \
    go build -o "$WORK/fake-slack" ./spike/m4/fakes/slack)
  assert_linux_elf "$WORK/fake-github" "$GOARCH_TARGET" ||
    fail "fake-github is not a linux/${GOARCH_TARGET} ELF"
  assert_linux_elf "$WORK/fake-slack" "$GOARCH_TARGET" ||
    fail "fake-slack is not a linux/${GOARCH_TARGET} ELF"
  chmod 755 "$WORK/fake-github" "$WORK/fake-slack"
  export M4_FAKE_GITHUB_BIN="$WORK/fake-github"
  export M4_FAKE_SLACK_BIN="$WORK/fake-slack"
fi

say "Build the harness image and the task image"
(cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH_TARGET" \
  go build -o harness/generic-openai/factory-harness ./cmd/factory-harness)
assert_linux_elf "$ROOT/harness/generic-openai/factory-harness" "$GOARCH_TARGET" ||
  fail "harness binary is not a linux/${GOARCH_TARGET} ELF"
docker build --platform "$OCI_PLATFORM" -q -t "$HARNESS_IMAGE" "$ROOT/harness/generic-openai" >/dev/null
assert_image_platform "$HARNESS_IMAGE" "$OCI_PLATFORM" ||
  fail "harness image platform does not match ${OCI_PLATFORM}"
docker build --platform "$OCI_PLATFORM" -q -t "$TASK_IMAGE" "$ROOT/spike/m1/task-image" >/dev/null
assert_image_platform "$TASK_IMAGE" "$OCI_PLATFORM" ||
  fail "task image platform does not match ${OCI_PLATFORM}"
HARNESS_EXEC_CODE=0
run_capture HARNESS_EXEC_CODE docker run --rm --platform "$OCI_PLATFORM" \
  --entrypoint /factory-harness "$HARNESS_IMAGE" \
  >"$WORK/image-exec.out" 2>"$WORK/image-exec.err"
if ! harness_contract_exit "$HARNESS_EXEC_CODE"; then
  fail "harness image cannot execute on ${OCI_PLATFORM} (exit ${HARNESS_EXEC_CODE})"
fi
rm -f "$WORK/image-exec.out" "$WORK/image-exec.err"
TASK_UNAME_CODE=0
run_capture TASK_UNAME_CODE docker run --rm --platform "$OCI_PLATFORM" \
  --entrypoint /bin/sh "$TASK_IMAGE" -c 'uname -m && git --version >/dev/null && go version >/dev/null && command -v gh >/dev/null && command -v curl >/dev/null' \
  >"$WORK/task-uname.txt" 2>"$WORK/image-exec.err"
TASK_UNAME="$(tr -d '[:space:]' <"$WORK/task-uname.txt" 2>/dev/null || true)"
want_uname="$(uname_for_platform "$OCI_PLATFORM")"
if [[ "$TASK_UNAME_CODE" -ne 0 || "$TASK_UNAME" != "$want_uname" ]]; then
  fail "task image failed on ${OCI_PLATFORM} (uname ${TASK_UNAME:-empty}, exit ${TASK_UNAME_CODE}, want ${want_uname})"
fi
rm -f "$WORK/task-uname.txt" "$WORK/image-exec.err"

if [[ "$MODE" == offline ]]; then
  say "Route model-stub responses by model id on ${TASK_HOST}:9090"
  (cd "$ROOT" && CGO_ENABLED=0 go build -o "$WORK/model-stub" ./spike/m1/model-stub)
  write_final_script "$WORK/script-triage.json" "triage: scope is the Hello exclamation change"
  write_final_script "$WORK/script-planning.json" "planning: add an exclamation mark in Hello and update the test"
  write_building_script "$WORK/script-building.json" "http://${TASK_HOST}:${M4_FAKE_GITHUB_PORT}/_spike/commits"
  write_final_script "$WORK/script-reviewing.json" "reviewing: no findings"
  start_stub triage 9091 "$WORK/script-triage.json"
  start_stub planning 9092 "$WORK/script-planning.json"
  start_stub building 9093 "$WORK/script-building.json"
  start_stub reviewing 9094 "$WORK/script-reviewing.json"
  {
    printf '%s|http://127.0.0.1:9091\n' "$MODEL_TRIAGE"
    printf '%s|http://127.0.0.1:9092\n' "$MODEL_PLANNING"
    printf '%s|http://127.0.0.1:9093\n' "$MODEL_BUILDING"
    printf '%s|http://127.0.0.1:9094\n' "$MODEL_REVIEWING"
  } >"$WORK/routes.txt"
  write_router
  M4_ROUTES_FILE="$WORK/routes.txt" M4_ROUTER_ADDR="0.0.0.0:9090" \
    python3 "$WORK/model-router.py" >"$WORK/model-router.log" 2>&1 &
  ROUTER_PID=$!
  if ! wait_http "http://127.0.0.1:9090/healthz"; then
    fail "model router did not become reachable"
  fi
  if ! wait_http "http://${TASK_HOST}:9090/healthz"; then
    fail "model router is not reachable from the task host"
  fi
fi

say "Start the laptop stack"
COMPOSE_STARTED=1
compose build
assert_image_platform factoryd:dev "$OCI_PLATFORM" ||
  fail "factoryd image platform does not match ${OCI_PLATFORM}"
assert_image_platform factory-upstream-worker:dev "$OCI_PLATFORM" ||
  fail "worker image platform does not match ${OCI_PLATFORM}"
compose up -d --wait postgres
compose --profile migrate run --rm migrate
if [[ "$MODE" == offline ]]; then
  compose --profile m4 up -d --build --wait
else
  compose up -d --build --wait
fi

for _ in $(seq 1 30); do
  if curl -fsS "http://127.0.0.1:8080/healthz" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
if ! curl -fsS "http://127.0.0.1:8080/healthz" >/dev/null 2>&1; then
  fail "factoryd /healthz did not become ready"
fi
echo "factoryd healthy on :8080"
export FACTORY_URL="http://127.0.0.1:8080"

if [[ "$MODE" == offline ]]; then
  if ! wait_http "http://127.0.0.1:${M4_FAKE_GITHUB_PORT}/healthz"; then
    fail "fake-github /healthz did not become ready"
  fi
  if ! wait_http "http://127.0.0.1:${M4_FAKE_SLACK_PORT}/healthz"; then
    fail "fake-slack /healthz did not become ready"
  fi
  docker run --rm --network bridge --entrypoint curl "$TASK_IMAGE" \
    -fsS "http://${TASK_HOST}:${M4_FAKE_GITHUB_PORT}/healthz" >/dev/null ||
    fail "fake-github is not reachable from a bridge container at ${TASK_HOST}:${M4_FAKE_GITHUB_PORT}"
  docker run --rm --network bridge --entrypoint curl "$TASK_IMAGE" \
    -fsS "http://${TASK_HOST}:9090/healthz" >/dev/null ||
    fail "model router is not reachable from a bridge container at ${TASK_HOST}:9090"
  echo "fakes and model router reachable from a bridge container"

  say "Signed issues.labeled delivery"
  write_labeled_issue "$WORK/issue.json" "$ISSUE_NUMBER" "factory-ready" "Emphasize Hello"
  code="$(post_github issues "$DELIVERY_ISSUE" "$WORK/issue.json" "$WORK/ack-issue.json")"
  dup="$(ack_duplicate "$WORK/ack-issue.json" || true)"
  if [[ "$code" != "200" || "$dup" != "false" ]]; then
    fail "first labeled delivery was not accepted (http ${code}, duplicate ${dup:-missing})"
  fi
  code="$(post_github issues "$DELIVERY_ISSUE" "$WORK/issue.json" "$WORK/ack-issue-again.json")"
  dup="$(ack_duplicate "$WORK/ack-issue-again.json" || true)"
  if [[ "$code" != "200" || "$dup" != "true" ]]; then
    fail "redelivery was not deduped (http ${code}, duplicate ${dup:-missing})"
  fi
  echo "redelivery deduped"

  for _ in $(seq 1 60); do
    if TASK_ID="$(one_task_id)"; then
      break
    fi
    TASK_ID=""
    sleep 1
  done
  if [[ -z "$TASK_ID" ]]; then
    fail "labeled delivery did not create exactly one work item"
  fi
  require_id "$TASK_ID" "task_" || fail "work item id is not a task id"
  echo "work item: $TASK_ID"
  if [[ "$(task_ids | awk 'NF { c++ } END { print c+0 }')" -ne 1 ]]; then
    fail "redelivery created another work item"
  fi

  say "Unmatched delivery"
  write_labeled_issue "$WORK/unmatched.json" 99 "docs" "Documentation only"
  code="$(post_github issues "$DELIVERY_UNMATCHED" "$WORK/unmatched.json" "$WORK/ack-unmatched.json")"
  dup="$(ack_duplicate "$WORK/ack-unmatched.json" || true)"
  if [[ "$code" != "200" || "$dup" != "false" ]]; then
    fail "unmatched delivery was not accepted (http ${code}, duplicate ${dup:-missing})"
  fi
  unmatched_seen=0
  for _ in $(seq 1 30); do
    if "$FACTORYCTL" deliveries list --factory "$FACTORY_NAME" --unmatched \
      >"$WORK/unmatched.txt" 2>>"$WORK/factoryctl-deliveries.err"; then
      if grep -F -q "delivery=${DELIVERY_UNMATCHED}" "$WORK/unmatched.txt"; then
        unmatched_seen=1
        break
      fi
    fi
    sleep 1
  done
  if [[ "$unmatched_seen" -ne 1 ]]; then
    fail "factoryctl deliveries list --unmatched did not show ${DELIVERY_UNMATCHED}"
  fi
  echo "unmatched delivery listed"
  if [[ "$(task_ids | awk 'NF { c++ } END { print c+0 }')" -ne 1 ]]; then
    fail "unmatched delivery created a work item"
  fi

  say "Wait for the first commit, the check run, and Slack"
  HEAD_SHA=""
  ROOT_TS=""
  ready=0
  for _ in $(seq 1 900); do
    fetch_github_state || true
    commits="$(commit_count 2>/dev/null || echo 0)"
    if [[ "$PR_POSTED" -eq 0 && "$commits" -ge 1 ]]; then
      HEAD_SHA="$(commit_sha 1)"
      write_opened_pull "$WORK/pull.json" "$HEAD_SHA"
      code="$(post_github pull_request "$DELIVERY_PR" "$WORK/pull.json" "$WORK/ack-pr.json")"
      dup="$(ack_duplicate "$WORK/ack-pr.json" || true)"
      if [[ "$code" != "200" || "$dup" != "false" ]]; then
        fail "pull_request delivery was not accepted (http ${code}, duplicate ${dup:-missing})"
      fi
      PR_POSTED=1
      echo "pull request delivery posted"
    fi
    fetch_slack_state || true
    if [[ "$PR_POSTED" -eq 1 ]] && handoff_complete && checks_ready; then
      ROOT_TS="$(slack_root_ts || true)"
      if [[ -n "$ROOT_TS" ]]; then
        ready=1
        break
      fi
    fi
    sleep 1
  done
  if [[ "$ready" -ne 1 ]]; then
    fail "pipeline did not reach handoff with a check run and Slack thread"
  fi
  echo "handoff complete; slack root ${ROOT_TS}"

  schedules="$("$FACTORYCTL" schedules list --factory "$FACTORY_NAME" 2>>"$WORK/factoryctl-schedules.err" || true)"
  if [[ "$schedules" != *"name=hourly"* || "$schedules" != *'cron="@every 1h"'* ]]; then
    fail "hourly schedule was not loaded"
  fi
  echo "hourly schedule loaded"

  CONV="$(psql_at "SELECT conversation_id FROM work_items WHERE id = '${TASK_ID}'" | tr -d '[:space:]')"
  require_id "$CONV" "conv_" || fail "work item has no conversation id"
  before="$(building_conversations | awk 'NF { c++ } END { print c+0 }')"
  if [[ "$before" -ne 1 ]]; then
    fail "expected one building run before the reply (saw ${before})"
  fi
  if [[ "$(building_conversations | tr -d '[:space:]')" != "$CONV" ]]; then
    fail "building run conversation does not match the work item"
  fi

  say "Signed Slack thread reply"
  write_slack_reply "$WORK/reply.json" "$ROOT_TS"
  code="$(post_slack "$WORK/reply.json" "$WORK/ack-reply.json")"
  dup="$(ack_duplicate "$WORK/ack-reply.json" || true)"
  if [[ "$code" != "200" || "$dup" != "false" ]]; then
    fail "slack reply was not accepted (http ${code}, duplicate ${dup:-missing})"
  fi
  followed=0
  for _ in $(seq 1 600); do
    fetch_github_state || true
    commits="$(commit_count 2>/dev/null || echo 0)"
    rows="$(building_conversations || true)"
    count="$(printf '%s\n' "$rows" | awk 'NF { c++ } END { print c+0 }')"
    distinct="$(printf '%s\n' "$rows" | awk 'NF { print }' | sort -u | awk 'NF { c++ } END { print c+0 }')"
    if [[ "$count" -ge 2 && "$distinct" -eq 1 && "$commits" -ge 2 && "$rows" == *"$CONV"* ]]; then
      followed=1
      break
    fi
    sleep 1
  done
  if [[ "$followed" -ne 1 ]]; then
    fail "slack reply did not start a second building run on the same conversation"
  fi
  echo "continuation recorded a second commit"

  save_compose_logs
  if ! scan_logs_for_secrets; then
    announce_leak
    exit 1
  fi
  echo
  echo "M4 OFFLINE PASS"
  exit 0
fi

say "Mint an installation token"
if ! mint_installation_token; then
  fail "apptoken did not mint an installation token"
fi

if [[ "$MODE" == live-ci ]]; then
  say "Open an issue and inject a signed labeled delivery"
  create_live_issue || fail "issue create did not return a number"
  write_live_ci_delivery_from_issue "$WORK/issue.json" "$WORK/live-issue.json" "$M4_REPO" ||
    fail "could not build the synthetic delivery"
  code="$(post_github issues "live-ci-${ISSUE_LIVE}" "$WORK/issue.json" "$WORK/ack-issue.json")"
  dup="$(ack_duplicate "$WORK/ack-issue.json" || true)"
  mkdir -p "$WORK/evidence"
  cp "$WORK/issue.json" "$WORK/evidence/delivery.json"
  cp "$WORK/ack-issue.json" "$WORK/evidence/ack.json"
  if [[ "$code" != "200" || "$dup" != "false" ]]; then
    fail "labeled delivery was not accepted (http ${code}, duplicate ${dup:-missing})"
  fi
  echo "synthetic labeled delivery accepted for issue #${ISSUE_LIVE}"

  say "Wait for a draft pull request"
  if ! wait_draft_pull; then
    fail "draft pull request did not open"
  fi
  echo "draft pull request: ${PR_LIVE_URL}"

  say "Slack thread"
  read_slack_permalink "$(live_ci_issue_title)" || fail "slack history has no factory root for this issue"
  echo "slack thread: ${PERMALINK}"
  write_live_ci_evidence
  save_compose_logs
  if ! scan_logs_for_secrets; then
    announce_leak
    exit 1
  fi
  echo
  echo "M4 LIVE-CI PASS"
  exit 0
fi

say "Label the issue"
create_live_issue || fail "issue create did not return a number"
GH_TOKEN="$LIVE_TOKEN" gh api --method POST \
  -H "Accept: application/vnd.github+json" \
  "/repos/${M4_REPO}/issues/${ISSUE_LIVE}/labels" \
  -f "labels[]=factory-ready" >/dev/null
echo "labeled issue #${ISSUE_LIVE}"

say "Wait for a draft pull request"
if ! wait_draft_pull; then
  fail "draft pull request did not open"
fi
echo "draft pull request: ${PR_LIVE_URL}"

say "Slack thread"
read_slack_permalink || fail "slack history has no factory root for this issue"
permalink="$PERMALINK"
echo "slack thread: ${permalink}"

if [[ -n "${REPLY_TEXT:-}" && -n "${SLACK_TEST_USER_TOKEN:-}" ]]; then
  python3 - "$WORK/live-reply.json" "$M4_SLACK_CHANNEL" "$LIVE_ROOT" "$REPLY_TEXT" <<'PY'
import json
import sys

path, channel, thread, text = sys.argv[1:]
with open(path, "w", encoding="utf-8") as fh:
    json.dump({"channel": channel, "thread_ts": thread, "text": text}, fh)
PY
  curl -fsS -H "Authorization: Bearer ${SLACK_TEST_USER_TOKEN}" \
    -H "Content-Type: application/json" \
    --data-binary @"$WORK/live-reply.json" \
    https://slack.com/api/chat.postMessage >"$WORK/live-reply-ack.json" ||
    fail "slack test user reply failed"
  echo "posted REPLY_TEXT into the thread"
else
  echo "Reply in the Slack thread. Suggested text: also add a test for an empty name"
fi

say "Wait for a new commit on the pull request"
changed=0
for _ in $(seq 1 360); do
  GH_TOKEN="$LIVE_TOKEN" gh pr view --repo "$M4_REPO" "$PR_LIVE_URL" --json headRefOid \
    >"$WORK/live-head.json" || true
  now="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("headRefOid") or "")' "$WORK/live-head.json" 2>/dev/null || true)"
  if [[ -n "$now" && -n "$PR_LIVE_SHA" && "$now" != "$PR_LIVE_SHA" ]]; then
    changed=1
    break
  fi
  sleep 10
done
if [[ "$changed" -ne 1 ]]; then
  fail "pull request head did not change after the Slack reply"
fi
save_compose_logs
if ! scan_logs_for_secrets; then
  announce_leak
  exit 1
fi
echo
echo "M4 LIVE PASS"
exit 0
