#!/usr/bin/env bash
# M3 laptop profile — four stage agents, one blocking spec gate.
#
#   MODE=offline ./spike/m3/run.sh    # default; ends with "M3 OFFLINE PASS"
#   MODE=live    ./spike/m3/run.sh    # real models + GitHub; prints the PR URL
#
# Offline starts one model-stub per stage model id and a router on the
# Docker bridge gateway. Task containers reach factoryd at
# FACTORY_TASK_SERVER_URL. When that variable is empty, Linux uses the
# bridge gateway. Docker Desktop must set
# http://host.docker.internal:8080 in deploy/.env. This script does not
# rewrite a loopback model URL.
#
# Live reads deploy/.env when that file exists (KEY=VALUE text, not
# executed). A non-empty shell variable wins. OPENAI_BASE_URL, GH_TOKEN,
# M3_MODEL_TRIAGE, M3_MODEL_PLANNING, M3_MODEL_BUILDING,
# M3_MODEL_REVIEWING, and optional OPENAI_API_KEY and M3_REPO are the
# shell names. FACTORY_INFERENCE_* and FACTORY_SECRET_GH_TOKEN are the
# same inference and GitHub values. Nothing in this script prints those
# values.
#
# Live calls `factoryctl approvals decide` only when APPROVAL_DECISION is
# exactly approve. Any other set value is refused. When the variable is
# unset, the script prints the approval id and waits for an operator.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
WORK="$HERE/.work"
COMPOSE_FILE="$ROOT/deploy/docker-compose.yml"
PROJECT="factory-m3"
FACTORY_NAME="m3"
SRC_DEF="$ROOT/deploy/m3-factory"

HARNESS_IMAGE="factory-harness:dev"
TASK_IMAGE="factory-m1-task:dev"
MODE="${MODE:-offline}"

OFFLINE_DUMMY_KEY="m3-offline-dummy-7f3c9a"
OFFLINE_GH="m3-offline-gh-7f3c9a2e"
PLANTED_DUMMY="m3-planted-dummy-7f3c9a"

ROUTER_PID=""
WATCH_PID=""
COMPOSE_STARTED=0
SECRETS=()
LEAK_ANNOUNCED=0
TASK_ID=""
APPROVAL_ID=""
OCI_PLATFORM=""
GOARCH_TARGET=""
GW=""
DEF=""

# shellcheck disable=SC2317,SC2329 # invoked by the EXIT/INT/TERM trap
cleanup() {
  local code=$? pid=""
  trap - EXIT INT TERM ERR
  set +e
  if [[ -n "${WATCH_PID}" ]]; then
    kill "${WATCH_PID}" 2>/dev/null || true
  fi
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
    docker compose -p "$PROJECT" -f "$COMPOSE_FILE" down -v --timeout 20 >/dev/null 2>&1 ||
      docker compose -p "$PROJECT" -f "$COMPOSE_FILE" down -v --timeout 20 || true
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
  copy_if_empty OPENAI_API_KEY FACTORY_INFERENCE_OPENAI_API_KEY
  copy_if_empty GH_TOKEN FACTORY_SECRET_GH_TOKEN
}

# Hostname and port only. Userinfo in the URL is a secret and is not printed.
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
  docker compose -p "$PROJECT" -f "$COMPOSE_FILE" "$@"
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
  return 0
}

scan_logs_for_secrets() {
  local files=() f="" nullglob=0
  if shopt -q nullglob; then
    nullglob=1
  fi
  shopt -s nullglob
  files=(
    "$WORK/compose.log"
    "$WORK/watch.ts"
    "$WORK/factoryctl-create.err"
    "$WORK/factoryctl-get.err"
    "$WORK/factoryctl-approvals.err"
    "$WORK/factoryctl-decide.err"
    "$WORK/factoryctl-watch.err"
    "$WORK/image-exec.err"
    "$WORK/model-router.log"
  )
  for f in "$WORK"/model-stub-*.log "$WORK"/evidence/*; do
    if [[ -f "$f" ]]; then
      files+=("$f")
    fi
  done
  if [[ "$nullglob" -eq 0 ]]; then
    shopt -u nullglob
  fi
  scan_files "${files[@]+"${files[@]}"}"
}

discard_captured_logs() {
  rm -rf "$WORK/evidence"
  rm -f \
    "$WORK/compose.log" \
    "$WORK/watch.ts" \
    "$WORK/factoryctl-create.err" \
    "$WORK/factoryctl-get.err" \
    "$WORK/factoryctl-approvals.err" \
    "$WORK/factoryctl-decide.err" \
    "$WORK/factoryctl-watch.err" \
    "$WORK/image-exec.err" \
    "$WORK/model-router.log" \
    "$WORK"/model-stub-*.log
}

announce_leak() {
  discard_captured_logs
  if [[ "$LEAK_ANNOUNCED" -eq 0 ]]; then
    echo "M3 FAIL — a secret value appeared in the logs, watch output, or archived events; output withheld"
    LEAK_ANNOUNCED=1
  fi
}

prove_scanner() {
  local proof="$WORK/planted-secret.txt"
  local saved=()
  mkdir -p "$WORK"
  printf 'planted %s\n' "$PLANTED_DUMMY" >"$proof"
  saved=("${SECRETS[@]+"${SECRETS[@]}"}")
  SECRETS=("$PLANTED_DUMMY")
  local rejected=0
  if ! scan_files "$proof"; then
    rejected=1
  fi
  SECRETS=("${saved[@]+"${saved[@]}"}")
  rm -f "$proof"
  if [[ -e "$proof" ]]; then
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
  echo "── task watch (tail) ─────────────────────────────────────────"
  if [[ -f "$WORK/watch.ts" ]]; then
    tail -n 80 "$WORK/watch.ts"
  else
    echo "(no watch transcript)"
  fi
  local err=""
  for err in \
    "$WORK/factoryctl-create.err" \
    "$WORK/factoryctl-get.err" \
    "$WORK/factoryctl-approvals.err" \
    "$WORK/factoryctl-decide.err" \
    "$WORK/factoryctl-watch.err" \
    "$WORK/image-exec.err" \
    "$WORK/model-router.log"; do
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
  echo "M3 FAIL — ${reason}"
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

# live_approval_action is decide only for the exact value approve.
# Unset waits. Any other value is refused and must not approve the gate.
live_approval_action() {
  case "${APPROVAL_DECISION:-}" in
    "") printf '%s\n' wait ;;
    approve) printf '%s\n' decide ;;
    *) printf '%s\n' refuse ;;
  esac
}

# distinct_models fails when any id is empty, duplicated, or unsafe to
# write as a YAML scalar and as a SQL-adjacent token.
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

redact_if_live() {
  local value="$1" replacement="$2"
  if [[ -n "$value" && "$value" != "$replacement" ]]; then
    SECRETS+=("$value")
  fi
}

apply_offline_secrets() {
  local gw="$1"
  local live_gh="${FACTORY_SECRET_GH_TOKEN:-}"
  local live_key="${FACTORY_INFERENCE_OPENAI_API_KEY:-}"
  local live_url="${FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL:-}"
  local live_shell_gh="${GH_TOKEN:-}"
  local live_shell_key="${OPENAI_API_KEY:-}"
  local stub_url="http://${gw}:9090"
  unset GH_TOKEN OPENAI_API_KEY OPENAI_BASE_URL
  export FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL="$stub_url"
  export FACTORY_INFERENCE_OPENAI_API_KEY="$OFFLINE_DUMMY_KEY"
  export FACTORY_SECRET_GH_TOKEN="$OFFLINE_GH"
  redact_if_live "$live_gh" "$OFFLINE_GH"
  redact_if_live "$live_key" "$OFFLINE_DUMMY_KEY"
  redact_if_live "$live_url" "$stub_url"
  redact_if_live "$live_shell_gh" "$OFFLINE_GH"
  redact_if_live "$live_shell_key" "$OFFLINE_DUMMY_KEY"
  SECRETS+=("$OFFLINE_DUMMY_KEY" "$OFFLINE_GH")
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
    psql -U factory -d factory -v ON_ERROR_STOP=1 -At -F '|' -c "$1"
}

api_get() {
  local path="$1" dest="$2"
  curl -fsS -H "Authorization: Bearer ${FACTORY_API_KEY}" \
    "http://127.0.0.1:8080${path}" >"$dest"
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

start_stub() {
  local name="$1" port="$2" text="$3"
  local script="$WORK/script-${name}.json"
  write_final_script "$script" "$text"
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

wait_for_pid() {
  local pid="$1" tries="$2" _
  for _ in $(seq 1 "$tries"); do
    if ! kill -0 "$pid" 2>/dev/null; then
      return 0
    fi
    sleep 1
  done
  return 1
}

patch_models() {
  local dest="$1"
  rm -rf "$dest"
  mkdir -p "$dest"
  cp -a "$SRC_DEF/." "$dest/"
  DEF_ROOT="$dest" python3 - <<'PY'
import os
from pathlib import Path

root = Path(os.environ["DEF_ROOT"])
pairs = {
    "agents/triage/agent.md": os.environ["M3_MODEL_TRIAGE"],
    "agents/spec/agent.md": os.environ["M3_MODEL_PLANNING"],
    "agents/implementer/agent.md": os.environ["M3_MODEL_BUILDING"],
    "agents/reviewer/agent.md": os.environ["M3_MODEL_REVIEWING"],
}
for rel, new in pairs.items():
    path = root / rel
    lines = path.read_text(encoding="utf-8").splitlines(keepends=True)
    out = []
    done = False
    for line in lines:
        if not done and line.startswith("model: "):
            out.append("model: %s\n" % new)
            done = True
        else:
            out.append(line)
    if not done:
        raise SystemExit("no model line in %s" % rel)
    path.write_text("".join(out), encoding="utf-8")
PY
  chmod -R a+rX "$dest"
}

building_count() {
  local n=""
  n="$(psql_at "SELECT count(*) FROM runs WHERE work_item_id = '${TASK_ID}' AND stage = 'building'" | tr -d '[:space:]')"
  if [[ ! "$n" =~ ^[0-9]+$ ]]; then
    echo "building count query failed" >&2
    return 1
  fi
  printf '%s\n' "$n"
}

wait_for_gate() {
  local tries="$1" line="" _
  for _ in $(seq 1 "$tries"); do
    line="$("$FACTORYCTL" task get --factory "$FACTORY_NAME" "$TASK_ID" 2>>"$WORK/factoryctl-get.err" || true)"
    if [[ "$line" == *" status=awaiting_approval "* && "$line" == *" gate=spec"* ]]; then
      printf '%s\n' "$line"
      return 0
    fi
    if [[ "$line" == *" status=complete"* || "$line" == *" status=cancelled"* ]]; then
      echo "work item reached a terminal status before the spec gate" >&2
      printf '%s\n' "$line" >&2
      return 1
    fi
    sleep 1
  done
  echo "timed out waiting for the spec gate" >&2
  printf '%s\n' "${line:-empty}" >&2
  return 1
}

work_body() {
  if [[ "$MODE" == live ]]; then
    cat <<EOF
Repository: https://github.com/${M3_REPO}
Branch: ${BRANCH}

Change spike/m1/fixture/greet/greet.go so Hello returns the concatenation of "Hello, ", name, and "!".
Update spike/m1/fixture/greet/greet_test.go so the expected greeting for M1 is Hello, M1!.
Run go test ./... in spike/m1/fixture.
Commit on branch ${BRANCH} and push it. Git author environment variables are already set.
GH_TOKEN is already in the environment. Run gh auth setup-git before git push.
Do not print GH_TOKEN or OPENAI_API_KEY.
Open a draft pull request and reply with its URL:
gh pr create --draft --repo ${M3_REPO} --head ${BRANCH} --title "M3: emphasize Hello" --body "Update the fixture greeting."
EOF
    return 0
  fi
  cat <<'EOF'
Change the fixture greeting so Hello adds an exclamation mark, and update its test.
The spec gate must block implementation until an operator approves.
Do not print tokens or API keys.
EOF
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
with open(os.environ["M3_ROUTES_FILE"], encoding="utf-8") as handle:
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


addr = os.environ["M3_ROUTER_ADDR"]
host, port = addr.rsplit(":", 1)


class Server(ThreadingHTTPServer):
    allow_reuse_address = True


Server((host, int(port)), Handler).serve_forever()
PY
}

# Sourced by spike/m3/run_test.sh. Executing the file runs the laptop stack.
if [[ "${BASH_SOURCE[0]}" != "$0" ]]; then
  return 0
fi

trap cleanup EXIT INT TERM
trap 'on_err $? $LINENO' ERR

say "Preflight"
case "$MODE" in
  offline | live) ;;
  *)
    die "MODE must be offline or live (got ${MODE})"
    ;;
esac

command -v docker >/dev/null || die "docker not found"
command -v go >/dev/null || die "go not found"
command -v git >/dev/null || die "git not found"
command -v curl >/dev/null || die "curl not found"
command -v python3 >/dev/null || die "python3 not found"
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

BRANCH=""
M3_REPO="${M3_REPO:-polds/factory}"
if [[ "$MODE" == live ]]; then
  command -v gh >/dev/null || die "gh not found (required for MODE=live)"
  [[ -n "${OPENAI_BASE_URL:-}" ]] || die "OPENAI_BASE_URL or FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL is required for MODE=live"
  [[ -n "${GH_TOKEN:-}" ]] || die "GH_TOKEN or FACTORY_SECRET_GH_TOKEN is required for MODE=live"
  [[ -n "${M3_MODEL_TRIAGE:-}" ]] || die "M3_MODEL_TRIAGE is required for MODE=live"
  [[ -n "${M3_MODEL_PLANNING:-}" ]] || die "M3_MODEL_PLANNING is required for MODE=live"
  [[ -n "${M3_MODEL_BUILDING:-}" ]] || die "M3_MODEL_BUILDING is required for MODE=live"
  [[ -n "${M3_MODEL_REVIEWING:-}" ]] || die "M3_MODEL_REVIEWING is required for MODE=live"
  distinct_models "$M3_MODEL_TRIAGE" "$M3_MODEL_PLANNING" "$M3_MODEL_BUILDING" "$M3_MODEL_REVIEWING" ||
    die "live model ids must be four distinct YAML-safe values"
  export M3_MODEL_TRIAGE M3_MODEL_PLANNING M3_MODEL_BUILDING M3_MODEL_REVIEWING
  echo "live repo: ${M3_REPO}"
fi

mkdir -p "$WORK" "$WORK/evidence"
: >"$WORK/stub.pids"
prove_scanner

GW="$(bridge_gateway)"
echo "docker bridge gateway: $GW"
DOCKER_OS="$(docker info --format '{{.OperatingSystem}}' 2>/dev/null || true)"
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
  echo "Docker Desktop should set FACTORY_TASK_SERVER_URL to http://host.docker.internal:8080."
fi

export POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-factory}"
export S3_ACCESS_KEY="${S3_ACCESS_KEY:-factoryadmin}"
export S3_SECRET_KEY="${S3_SECRET_KEY:-m3s3-secret-7f3c9a2e1b4d}"
export FACTORY_API_KEY="${FACTORY_API_KEY:-m3-api-7f3c9a2e1b4d}"
export FACTORY_WORKER_API_KEY="${FACTORY_WORKER_API_KEY:-m3-worker-7f3c9a2e1b4d}"
export FACTORY_EXTERNAL_URL="${FACTORY_EXTERNAL_URL:-http://127.0.0.1:8080}"
export FACTORY_LOG_LEVEL="${FACTORY_LOG_LEVEL:-info}"
SECRETS+=("$S3_SECRET_KEY" "$FACTORY_API_KEY" "$FACTORY_WORKER_API_KEY")

DEF="$SRC_DEF"
if [[ "$MODE" == offline ]]; then
  apply_offline_secrets "$GW"
else
  live_host="$(url_log_host "$OPENAI_BASE_URL")"
  case "$live_host" in
    localhost | localhost:* | 127.0.0.1 | 127.0.0.1:* | "::1" | "::1:"*)
      die "OPENAI_BASE_URL points at a loopback host. Task containers cannot reach it. On Docker Desktop use host.docker.internal. On Linux use the bridge gateway. This script does not rewrite that URL."
      ;;
  esac
  echo "task OPENAI_BASE_URL host: ${live_host}"
  TS="$(python3 -c 'from datetime import datetime, timezone; print(datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ"))')"
  BRANCH="m3/run-${TS}"
  say "Point the four stage models at the live ids"
  patch_models "$WORK/definition"
  DEF="$WORK/definition"
  export FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL="$OPENAI_BASE_URL"
  export FACTORY_INFERENCE_OPENAI_API_KEY="${OPENAI_API_KEY:-}"
  export FACTORY_SECRET_GH_TOKEN="$GH_TOKEN"
  SECRETS+=("$GH_TOKEN")
  if [[ -n "${OPENAI_API_KEY:-}" ]]; then
    SECRETS+=("$OPENAI_API_KEY")
  fi
fi

export FACTORY_DEFINITION_HOST_PATH="$DEF"
export FACTORY_DEFINITION_MOUNT="/definitions/m3-factory"
export FACTORY_DEFINITION_PATH="/definitions/m3-factory"

MODEL_TRIAGE="$(read_model "$DEF/agents/triage/agent.md")"
MODEL_PLANNING="$(read_model "$DEF/agents/spec/agent.md")"
MODEL_BUILDING="$(read_model "$DEF/agents/implementer/agent.md")"
MODEL_REVIEWING="$(read_model "$DEF/agents/reviewer/agent.md")"
distinct_models "$MODEL_TRIAGE" "$MODEL_PLANNING" "$MODEL_BUILDING" "$MODEL_REVIEWING" ||
  die "definition model ids must be four distinct values"
echo "models: triage=${MODEL_TRIAGE} planning=${MODEL_PLANNING} building=${MODEL_BUILDING} reviewing=${MODEL_REVIEWING}"

say "Build factoryctl, the harness image, and the task image"
(cd "$ROOT" && CGO_ENABLED=0 go build -o "$WORK/factoryctl" ./cmd/factoryctl)
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
  --entrypoint /bin/sh "$TASK_IMAGE" -c 'uname -m && git --version >/dev/null && go version >/dev/null && command -v gh >/dev/null' \
  >"$WORK/task-uname.txt" 2>"$WORK/image-exec.err"
TASK_UNAME="$(tr -d '[:space:]' <"$WORK/task-uname.txt" 2>/dev/null || true)"
want_uname="$(uname_for_platform "$OCI_PLATFORM")"
if [[ "$TASK_UNAME_CODE" -ne 0 || "$TASK_UNAME" != "$want_uname" ]]; then
  fail "task image failed on ${OCI_PLATFORM} (uname ${TASK_UNAME:-empty}, exit ${TASK_UNAME_CODE}, want ${want_uname})"
fi
rm -f "$WORK/task-uname.txt" "$WORK/image-exec.err"
FACTORYCTL="$WORK/factoryctl"
"$FACTORYCTL" validate "$DEF"

if [[ "$MODE" == offline ]]; then
  say "Route model-stub responses by model id on ${GW}:9090"
  (cd "$ROOT" && CGO_ENABLED=0 go build -o "$WORK/model-stub" ./spike/m1/model-stub)
  start_stub triage 9091 "triage: scope is the Hello exclamation change"
  start_stub planning 9092 "planning: add an exclamation mark in Hello and update the test"
  start_stub building 9093 "building: greeting change recorded"
  start_stub reviewing 9094 "reviewing: no findings"
  {
    printf '%s|http://127.0.0.1:9091\n' "$MODEL_TRIAGE"
    printf '%s|http://127.0.0.1:9092\n' "$MODEL_PLANNING"
    printf '%s|http://127.0.0.1:9093\n' "$MODEL_BUILDING"
    printf '%s|http://127.0.0.1:9094\n' "$MODEL_REVIEWING"
  } >"$WORK/routes.txt"
  write_router
  M3_ROUTES_FILE="$WORK/routes.txt" M3_ROUTER_ADDR="0.0.0.0:9090" \
    python3 "$WORK/model-router.py" >"$WORK/model-router.log" 2>&1 &
  ROUTER_PID=$!
  if ! wait_http "http://127.0.0.1:9090/healthz"; then
    fail "model router did not become reachable"
  fi
  if ! wait_http "http://${GW}:9090/healthz"; then
    fail "model router is not reachable on the bridge gateway"
  fi
  for pair in "triage:9091" "planning:9092" "building:9093" "reviewing:9094"; do
    if ! wait_http "http://127.0.0.1:${pair##*:}/healthz"; then
      fail "model-stub ${pair%%:*} did not become reachable"
    fi
  done
  docker run --rm --network bridge --entrypoint curl "$TASK_IMAGE" \
    -fsS "http://${GW}:9090/healthz" >/dev/null
  echo "model router reachable from a bridge container"
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
(
  cd "$ROOT/deploy"
  docker compose -p "$PROJECT" up -d --build --wait
)

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

say "factoryctl task create"
BODY="$(work_body)"
REF="m3-offline"
if [[ "$MODE" == live ]]; then
  REF="$BRANCH"
fi
TASK_ID="$("$FACTORYCTL" task create \
  --factory "$FACTORY_NAME" \
  --title "Emphasize Hello" \
  --body "$BODY" \
  --source issue \
  --ref "$REF" \
  2>"$WORK/factoryctl-create.err")"
TASK_ID="${TASK_ID//$'\r'/}"
TASK_ID="${TASK_ID//$'\n'/}"
require_id "$TASK_ID" "task_" || fail "task create did not return a task id"
echo "task id: $TASK_ID"

trap - ERR
set +e
"$FACTORYCTL" task watch --factory "$FACTORY_NAME" "$TASK_ID" \
  >"$WORK/watch.ts" 2>"$WORK/factoryctl-watch.err" &
WATCH_PID=$!
set -e
trap 'on_err $? $LINENO' ERR

say "Wait until the spec gate blocks building"
GATE_LINE="$(wait_for_gate 300)" || fail "spec gate did not open"
printf '%s\n' "$GATE_LINE" | tee "$WORK/evidence/gate.txt"
api_get "/api/v1/factory/${FACTORY_NAME}/tasks/${TASK_ID}" "$WORK/evidence/task-blocked.json"
python3 - "$WORK/evidence/task-blocked.json" "$TASK_ID" <<'PY'
import json
import sys

task = json.load(open(sys.argv[1], encoding="utf-8"))
want_id = sys.argv[2]
if task.get("id") != want_id:
    sys.exit("task id mismatch")
if task.get("status") != "awaiting_approval" or task.get("pending_gate") != "spec":
    sys.exit("pending_gate=%r status=%r" % (task.get("pending_gate"), task.get("status")))
if not task.get("conversation_id"):
    sys.exit("conversation_id is empty")
PY

APPROVALS="$("$FACTORYCTL" approvals list --factory "$FACTORY_NAME" 2>"$WORK/factoryctl-approvals.err")"
printf '%s\n' "$APPROVALS" >"$WORK/evidence/approvals.txt"
if [[ -z "$APPROVALS" ]]; then
  fail "approvals list was empty while the spec gate was open"
fi
APPROVAL_ID="${APPROVALS%% *}"
require_id "$APPROVAL_ID" "apr_" || fail "approvals list did not start with an approval id"
if [[ "$APPROVALS" != *" gate=spec"* || "$APPROVALS" != *" work_item=${TASK_ID}"* ]]; then
  fail "approvals list did not show the spec gate for this task"
fi
echo "approval id: $APPROVAL_ID"

for _ in $(seq 1 5); do
  count="$(building_count)" || fail "could not count building runs"
  if [[ "$count" != 0 ]]; then
    fail "building run existed while the spec gate was open (count ${count})"
  fi
  sleep 1
done
echo "no building run while the spec gate is open"

if [[ "$MODE" == live ]]; then
  action="$(live_approval_action)"
  case "$action" in
    decide)
      echo "APPROVAL_DECISION=approve; deciding the spec gate"
      ;;
    wait)
      echo "Spec gate ${APPROVAL_ID} is blocking building."
      echo "This script will not decide unless APPROVAL_DECISION=approve."
      echo "From another shell, with FACTORY_URL and FACTORY_API_KEY set:"
      echo "  factoryctl approvals decide ${APPROVAL_ID} --decision approve"
      ;;
    refuse)
      fail "APPROVAL_DECISION is set but is not approve. Unset it to wait, or set it to approve."
      ;;
    *)
      fail "unknown approval action"
      ;;
  esac
else
  action=decide
fi

if [[ "$action" == wait ]]; then
  decision=""
  for _ in $(seq 1 3600); do
    decision="$(psql_at "SELECT decision FROM approvals WHERE id = '${APPROVAL_ID}'" | tr -d '[:space:]')"
    count="$(building_count)" || fail "could not count building runs"
    if [[ -z "$decision" && "$count" != 0 ]]; then
      fail "building run started before the spec gate was decided"
    fi
    case "$decision" in
      approve) break ;;
      reject) fail "spec gate was rejected" ;;
      "") ;;
      *) fail "unexpected approval decision" ;;
    esac
    sleep 1
  done
  if [[ "$decision" != "approve" ]]; then
    fail "timed out waiting for an operator to decide ${APPROVAL_ID}"
  fi
  echo "operator approved ${APPROVAL_ID}"
else
  decided="$("$FACTORYCTL" approvals decide "$APPROVAL_ID" --decision approve --note "spec gate" 2>"$WORK/factoryctl-decide.err")"
  if [[ "$decided" != "${APPROVAL_ID} approve" ]]; then
    fail "approvals decide did not report approve"
  fi
  echo "decided ${APPROVAL_ID} approve"
fi

say "Wait for implement and review"
WAIT_TRIES=300
if [[ "$MODE" == live ]]; then
  WAIT_TRIES=2400
fi
if ! wait_for_pid "$WATCH_PID" "$WAIT_TRIES"; then
  kill "$WATCH_PID" 2>/dev/null || true
  fail "task watch timed out"
fi
WATCH_CODE=0
run_capture WATCH_CODE wait "$WATCH_PID"
WATCH_PID=""
if [[ "$WATCH_CODE" -ne 0 ]]; then
  fail "factoryctl task watch exited ${WATCH_CODE}, want 0"
fi

api_get "/api/v1/factory/${FACTORY_NAME}/tasks/${TASK_ID}" "$WORK/evidence/task-done.json"
api_get "/api/v1/factory/${FACTORY_NAME}/runs" "$WORK/evidence/runs.json"
psql_at "SELECT stage, id, COALESCE(parent_run_id, ''), model_id, status, COALESCE(work_item_id, ''), conversation_id FROM runs WHERE work_item_id = '${TASK_ID}' ORDER BY created_at ASC, id ASC" \
  >"$WORK/evidence/linkage.tsv"
order="$(psql_at "SELECT CASE WHEN min(r.created_at) > a.decided_at THEN 'after' ELSE 'before' END FROM runs r JOIN approvals a ON a.work_item_id = r.work_item_id WHERE r.work_item_id = '${TASK_ID}' AND r.stage = 'building' AND a.id = '${APPROVAL_ID}' GROUP BY a.decided_at" | tr -d '[:space:]')"
if [[ "$order" != "after" ]]; then
  fail "building run was not created after the spec gate decision (${order:-empty})"
fi
echo "building run was created after the spec gate decision"

EXPECT_MODELS="${MODEL_TRIAGE} ${MODEL_PLANNING} ${MODEL_BUILDING} ${MODEL_REVIEWING}" \
  TASK_ID="$TASK_ID" MODE="$MODE" python3 - \
  "$WORK/evidence/linkage.tsv" \
  "$WORK/evidence/runs.json" \
  "$WORK/evidence/task-done.json" \
  "$WORK/watch.ts" <<'PY'
import json
import os
import sys

linkage_path, runs_path, task_path, watch_path = sys.argv[1:]
task_id = os.environ["TASK_ID"]
expect = os.environ["EXPECT_MODELS"].split()
mode = os.environ["MODE"]
stages = ["triage", "planning", "building", "reviewing"]
texts = {
    "triage": "triage: scope is the Hello exclamation change",
    "planning": "planning: add an exclamation mark in Hello and update the test",
    "building": "building: greeting change recorded",
    "reviewing": "reviewing: no findings",
}

rows = []
for raw in open(linkage_path, encoding="utf-8"):
    line = raw.rstrip("\n")
    if not line:
        continue
    parts = line.split("|")
    if len(parts) != 7:
        sys.exit("linkage row has %d fields" % len(parts))
    rows.append(parts)
if [row[0] for row in rows] != stages:
    sys.exit("stages %s" % [row[0] for row in rows])
if rows[0][2] != "":
    sys.exit("triage parent is set")
for i in range(1, 4):
    if rows[i][2] != rows[i - 1][1]:
        sys.exit("%s parent is not the previous run" % rows[i][0])
models = [row[3] for row in rows]
if models != expect or len(set(models)) != 4:
    sys.exit("models %s" % models)
conversation = rows[0][6]
if not conversation:
    sys.exit("conversation_id is empty")
for row in rows:
    if row[4] != "succeeded":
        sys.exit("%s status %s" % (row[0], row[4]))
    if row[5] != task_id:
        sys.exit("%s work_item_id %s" % (row[0], row[5]))
    if row[6] != conversation:
        sys.exit("conversation mismatch")

runs = json.load(open(runs_path, encoding="utf-8"))
linked = [run for run in runs if run.get("work_item_id") == task_id]
if len(linked) != 4:
    sys.exit("API runs for the task: %d" % len(linked))
for run in linked:
    if not run.get("work_item_id"):
        sys.exit("API run omitted work_item_id")

task = json.load(open(task_path, encoding="utf-8"))
if task.get("status") != "complete" or task.get("stage") != "human_handoff":
    sys.exit("final task status=%r stage=%r" % (task.get("status"), task.get("stage")))
if task.get("pending_gate"):
    sys.exit("pending_gate still set: %r" % task.get("pending_gate"))
if task.get("conversation_id") != conversation:
    sys.exit("task conversation does not match the runs")

if mode == "offline":
    watch = open(watch_path, encoding="utf-8").read().splitlines()
    for row, stage in zip(rows, stages):
        run_id = row[1]
        model = row[3]
        prefix = "run=%s " % run_id
        lines = [line for line in watch if line.startswith(prefix)]
        if not any(texts[stage] in line for line in lines):
            sys.exit("run %s missing scripted %s response" % (run_id, stage))
        needle = "usage model_id=%s " % model
        if not any(needle in line for line in lines):
            sys.exit("run %s missing usage model_id=%s" % (run_id, model))
        if not any(line.startswith(prefix + "terminal status=succeeded") for line in lines):
            sys.exit("run %s missing succeeded terminal" % run_id)
PY

echo "four runs linked parent to child on distinct model ids"

if [[ "$MODE" == offline ]]; then
  save_compose_logs || true
  if ! scan_logs_for_secrets; then
    announce_leak
    exit 1
  fi
  echo
  echo "M3 OFFLINE PASS — spec gate blocked building until approvals decide, then four stage runs finished on distinct models."
  exit 0
fi

PR_URL="$(gh pr list -R "$M3_REPO" --head "$BRANCH" --json url -q '.[0].url')"
if [[ -z "$PR_URL" || "$PR_URL" == "null" ]]; then
  fail "no pull request for ${BRANCH} on ${M3_REPO}"
fi
save_compose_logs || true
if ! scan_logs_for_secrets; then
  announce_leak
  exit 1
fi
echo
echo "$PR_URL"
echo "M3 LIVE PASS — factoryd opened ${PR_URL}."
exit 0
