# shellcheck shell=bash
# Shared spike driver helpers. spike/m5/run.sh and spike/m6/run.sh source this
# file. It has no top-level side effects. Names below are resolved in the
# caller when a function runs:
#   ROOT WORK EVIDENCE SIGNAL MODE FACTORY_API_KEY PLAYWRIGHT_IMAGE
#   reviewing_model_id watch_signals WATCH_PID
# Defaults keep the m5 driver behavior. m6 overrides them with env vars.
# shellcheck disable=SC2154

phase_report() {
  local name="$1" start="$2"
  local prefix="${PHASE_PREFIX:-m5}"
  printf '%s phase %s %ss\n' "$prefix" "$name" "$((SECONDS - start))"
}

# detect_platform sets OCI_PLATFORM and GOARCH_TARGET from `docker info`.
# Callers export nothing else. goarch_for_platform comes from spike/lib/common.sh.
detect_platform() {
  local arch=""
  arch="$(docker info --format '{{.Architecture}}')"
  case "$arch" in
    aarch64 | arm64) OCI_PLATFORM="linux/arm64" ;;
    x86_64 | amd64) OCI_PLATFORM="linux/amd64" ;;
    *)
      echo "unsupported docker architecture ${arch}" >&2
      return 1
      ;;
  esac
  GOARCH_TARGET="$(goarch_for_platform "$OCI_PLATFORM")"
  export OCI_PLATFORM GOARCH_TARGET
}

random_hex() {
  openssl rand -hex 24
}

wait_http() {
  local url="$1" attempt
  for attempt in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30; do
    if curl -fsS --connect-timeout 2 -m 5 "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  # The index only bounds the retries. Callers see the status.
  [[ -n "$attempt" ]]
  return 1
}

url_hostname() {
  python3 - "$1" <<'PY'
import sys
from urllib.parse import urlparse
print(urlparse(sys.argv[1]).hostname or "")
PY
}

host_for_tasks() {
  local os="$1" gw="$2" kernel="${3:-}" server_host="${4:-}"
  if [[ -z "$kernel" ]]; then
    kernel="$(uname -s)"
  fi
  if [[ -n "${M6_TASK_HOST:-}" ]]; then
    printf '%s\n' "$M6_TASK_HOST"
    return 0
  fi
  if [[ -n "${M5_TASK_HOST:-}" ]]; then
    printf '%s\n' "$M5_TASK_HOST"
    return 0
  fi
  if [[ -n "$server_host" ]]; then
    printf '%s\n' "$server_host"
    return 0
  fi
  case "$os" in
    *Docker\ Desktop* | *OrbStack*)
      printf '%s\n' "host.docker.internal"
      return 0
      ;;
  esac
  if [[ "$kernel" == "Darwin" ]]; then
    printf '%s\n' "host.docker.internal"
    return 0
  fi
  printf '%s\n' "$gw"
}

host_catalog_probe_url() {
  local raw="$1" task_host="${2:-}"
  python3 - "$raw" "$task_host" <<'PY'
import sys
from urllib.parse import urlparse, urlunparse
raw, task_host = sys.argv[1], sys.argv[2]
parts = urlparse(raw)
host = parts.hostname or ""
if host != "host.docker.internal" and not (task_host and host == task_host):
    print(raw)
    raise SystemExit(0)
netloc = "127.0.0.1"
if parts.port is not None:
    netloc = "127.0.0.1:%d" % parts.port
print(urlunparse((parts.scheme, netloc, parts.path, parts.params, parts.query, "")))
PY
}

apply_live_inference_env() {
  if [[ "${MODE:-}" != live-ci ]]; then
    return 0
  fi
  export FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL="$M1_OPENAI_BASE_URL"
  export FACTORY_INFERENCE_OPENAI_API_KEY="$M1_OPENAI_API_KEY"
  if [[ -n "${M1_OPENAI_EXTRA_HEADERS:-}" ]]; then
    export FACTORY_INFERENCE_OPENAI_COMPATIBLE_EXTRA_HEADERS="$M1_OPENAI_EXTRA_HEADERS"
  else
    unset FACTORY_INFERENCE_OPENAI_COMPATIBLE_EXTRA_HEADERS
  fi
}

write_catalog_headers() {
  local dest="$1" code=0
  python3 - "$dest" <<'PY' || code=$?
import os, re, sys, unicodedata

dest = sys.argv[1]
env_name = "FACTORY_INFERENCE_OPENAI_COMPATIBLE_EXTRA_HEADERS"
name_re = re.compile(r"^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$")

def fail(msg):
    sys.stderr.write(msg + "\n")
    raise SystemExit(1)

def bad_value(value):
    for ch in value:
        if ord(ch) < 32 or ord(ch) == 127 or unicodedata.category(ch) == "Cc":
            return True
    return False

key = os.environ.get("FACTORY_INFERENCE_OPENAI_API_KEY", "")
raw = os.environ.get(env_name, "")
headers = []
index = {}

def put(name, value):
    key_name = name.lower()
    if key_name in index:
        headers[index[key_name]] = (name, value)
        return
    index[key_name] = len(headers)
    headers.append((name, value))

if key:
    if "\r" in key or "\n" in key:
        fail("FACTORY_INFERENCE_OPENAI_API_KEY: CR or LF inside a header value")
    put("Authorization", "Bearer " + key)

if "\r" in raw:
    fail(env_name + ": CR or LF inside a header value")
text = raw.strip()
if text:
    for n, line in enumerate(text.split("\n"), 1):
        if line.strip() == "" or ":" not in line:
            fail("%s: line %d is malformed" % (env_name, n))
        name, value = line.split(":", 1)
        name = name.strip()
        value = value.strip()
        if not name_re.fullmatch(name) or not value or bad_value(value):
            fail("%s: line %d is malformed" % (env_name, n))
        put(name, value)

body = "\n".join("%s: %s" % (name, value) for name, value in headers)
fd = os.open(dest, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
with os.fdopen(fd, "w", encoding="utf-8") as fh:
    if body:
        fh.write(body)
PY
  if [[ "$code" != 0 ]]; then
    return "$code"
  fi
  chmod 600 "$dest"
}

# probe_report prints a failed model-endpoint response. The arguments
# after the model id are header names. Values are not printed. The body
# is the first 200 characters after every configured inference secret
# is replaced: the API key, the LiteLLM key, and every extra-header value.
probe_report() {
  local kind="$1" status="$2" body_file="$3" model="$4"
  shift 4
  local joined="" name=""
  for name in "$@"; do
    if [[ -n "$joined" ]]; then
      joined+=","
    fi
    joined+="$name"
  done
  PROBE_KIND="$kind" PROBE_STATUS="$status" PROBE_BODY_FILE="$body_file" \
    PROBE_MODEL="$model" PROBE_HEADERS="$joined" python3 - <<'PY'
import os
import sys

kind = os.environ.get("PROBE_KIND", "probe")
status = os.environ.get("PROBE_STATUS", "")
model = os.environ.get("PROBE_MODEL", "")
headers = os.environ.get("PROBE_HEADERS", "")
path = os.environ.get("PROBE_BODY_FILE", "")
try:
    with open(path, encoding="utf-8", errors="replace") as fh:
        body = fh.read(8192)
except OSError:
    body = ""

def add(secrets, value):
    text = value.strip()
    if len(text) >= 4:
        secrets.append(text)
    if text.lower().startswith("bearer "):
        token = text[7:].strip()
        if len(token) >= 4:
            secrets.append(token)

secrets = []
for env_name in ("FACTORY_INFERENCE_OPENAI_API_KEY", "M1_OPENAI_API_KEY"):
    add(secrets, os.environ.get(env_name, ""))
for env_name in (
    "FACTORY_INFERENCE_OPENAI_COMPATIBLE_EXTRA_HEADERS",
    "M1_OPENAI_EXTRA_HEADERS",
):
    raw = os.environ.get(env_name, "")
    for line in raw.split("\n"):
        if ":" not in line:
            continue
        add(secrets, line.split(":", 1)[1])
for secret in sorted(set(secrets), key=len, reverse=True):
    body = body.replace(secret, "[REDACTED]")
body = body[:200].replace("\n", " ").replace("\r", " ")
label = kind if model == "" else "%s %s" % (kind, model)
sys.stderr.write("%s status %s headers %s body %s\n" % (label, status, headers, body))
PY
}

# catalog_probe GETs $base/models. A 2xx leaves the body in dest and
# prints nothing. Any other result prints the HTTP status, the header
# names, and a redacted body, then returns 1. CATALOG_CURL is the test
# seam. A stub that exits 0 and prints no status is success, so older
# stubs keep working. curl itself always prints the status because of -w.
catalog_probe() {
  local base="$1" dest="$2" hdr="" line="" name="" code="" status=0
  local -a args=(-sS --connect-timeout 2 -m 5 -o "$dest" -w '%{http_code}')
  local -a names=()
  hdr="$(mktemp)"
  if ! write_catalog_headers "$hdr"; then
    rm -f "$hdr"
    return 1
  fi
  while IFS= read -r line || [[ -n "$line" ]]; do
    if [[ -n "$line" ]]; then
      args+=(-H "$line")
      name="${line%%:*}"
      name="${name#"${name%%[![:space:]]*}"}"
      names+=("$name")
    fi
  done <"$hdr"
  rm -f "$hdr"
  args+=("${base%/}/models")
  code="$("${CATALOG_CURL:-curl}" "${args[@]}")" || status=$?
  code="${code//$'\n'/}"
  code="${code//$'\r'/}"
  if [[ "$status" -eq 0 && -z "$code" ]]; then
    return 0
  fi
  if [[ "$status" -eq 0 && "$code" == 2* ]]; then
    return 0
  fi
  if [[ -z "$code" ]]; then
    code="curl-${status}"
  fi
  probe_report "catalog probe" "$code" "$dest" "" "${names[@]}"
  : >"$dest"
  return 1
}

catalog_contains_id() {
  local file="$1" id="$2"
  python3 - "$file" "$id" <<'PY'
import json, sys
path, want = sys.argv[1], sys.argv[2]
with open(path, encoding="utf-8") as fh:
    data = json.load(fh)
ids = []
if isinstance(data, dict):
    for row in data.get("data") or []:
        if isinstance(row, dict) and row.get("id"):
            ids.append(row["id"])
raise SystemExit(0 if want in ids else 1)
PY
}

reject_unlisted_model() {
  local file="$1" id="$2"
  if catalog_contains_id "$file" "$id"; then
    return 0
  fi
  echo "model id ${id} is not in the catalog" >&2
  return 1
}

compose() {
  local project="${COMPOSE_PROJECT:-factory-m5}"
  local overlay="${COMPOSE_OVERLAY:-$ROOT/spike/m5/compose.yml}"
  local profile="${COMPOSE_PROFILE:-m5}"
  docker compose -p "$project" \
    -f "$ROOT/deploy/docker-compose.yml" \
    -f "$overlay" \
    --profile "$profile" \
    "$@"
}

resolve_wipe_path() {
  python3 - "$WORK" "${1-}" <<'PY'
import os, sys

work = os.path.realpath(sys.argv[1])
raw = sys.argv[2]
if raw == "":
    sys.stderr.write("wipe_dir: empty path\n")
    raise SystemExit(1)
if work in ("", "/"):
    sys.stderr.write("wipe_dir: refusing work root %s\n" % work)
    raise SystemExit(1)
resolved = os.path.realpath(raw)
try:
    inside = os.path.commonpath([work, resolved]) == work
except ValueError:
    inside = False
if resolved in ("", "/") or resolved == work or not inside:
    sys.stderr.write("wipe_dir: refusing %s\n" % resolved)
    raise SystemExit(1)
print(resolved)
PY
}

wipe_dir() {
  local path="${1-}" resolved=""
  if ! resolved="$(resolve_wipe_path "$path")"; then
    return 1
  fi
  if [[ ! -e "$resolved" ]]; then
    return 0
  fi
  if rm -rf "$resolved" 2>/dev/null; then
    return 0
  fi
  # Bind the resolved directory itself. A parent mount would let the
  # root find walk sideways out of the tree we agreed to delete.
  # The label is opt-in. A caller that has not run spike_run_init leaves
  # SPIKE_RUN_LABEL unset, so its argv stays the two-flag form above the image.
  if [[ -n "${SPIKE_RUN_LABEL:-}" ]]; then
    docker run --rm --label "spike-run=${SPIKE_RUN_LABEL}" --user 0 \
      -v "$resolved:/wipe" golang:1.26-alpine \
      sh -c 'find /wipe -mindepth 1 -depth -exec rm -rf {} +'
  else
    docker run --rm --user 0 -v "$resolved:/wipe" golang:1.26-alpine \
      sh -c 'find /wipe -mindepth 1 -depth -exec rm -rf {} +'
  fi
}

copy_e2e() {
  local src="${E2E_SRC:-$ROOT/spike/m5/e2e}"
  wipe_dir "$WORK/e2e"
  wipe_dir "$SIGNAL"
  wipe_dir "$EVIDENCE"
  mkdir -p "$WORK/e2e" "$EVIDENCE" "$SIGNAL"
  chmod 777 "$EVIDENCE" "$SIGNAL"
  cp "$src/package.json" \
    "$src/package-lock.json" \
    "$src/playwright.config.ts" \
    "$src"/*.spec.ts \
    "$WORK/e2e/"
}

run_playwright() {
  copy_e2e
  local reviewing="" network factory_url factory_git_url headed_url headed_git name
  local -a extra_env=()
  if declare -F reviewing_model_id >/dev/null; then
    reviewing="$(reviewing_model_id)"
    if [[ -z "$reviewing" ]]; then
      echo "M5_REVIEWING_MODEL is empty" >&2
      return 1
    fi
    export M5_REVIEWING_MODEL="$reviewing"
  fi
  if [[ -n "${PLAYWRIGHT_EXTRA_ENV:-}" ]]; then
    # Word-splitting is the separator between variable names.
    # shellcheck disable=SC2086
    for name in ${PLAYWRIGHT_EXTRA_ENV}; do
      extra_env+=(-e "${name}=${!name-}")
    done
  fi
  network="${PLAYWRIGHT_NETWORK:-factory-m5_default}"
  factory_url="${PLAYWRIGHT_FACTORY_URL:-http://factoryd:8080}"
  factory_git_url="${PLAYWRIGHT_FACTORY_GIT_URL:-http://factoryd-git:8080}"
  headed_url="${PLAYWRIGHT_HEADED_URL:-http://127.0.0.1:8080}"
  headed_git="${PLAYWRIGHT_HEADED_GIT_URL:-http://127.0.0.1:8081}"
  : >"$EVIDENCE/driver.log"
  if declare -F watch_signals >/dev/null; then
    # PLAYWRIGHT_WATCH_IN_PARENT=1: the caller starts watch_signals in
    # the parent shell. A pipeline would otherwise hide WATCH_PID.
    if [[ "${PLAYWRIGHT_WATCH_IN_PARENT:-}" != 1 ]]; then
      watch_signals &
      # Callers (m5 and m6) kill this pid from their own cleanup.
      WATCH_PID=$!
      export WATCH_PID
    fi
  fi
  if [[ "${HEADED:-}" == 1 ]]; then
    (cd "$WORK/e2e" && npm ci &&
      M5_API_KEY="${FACTORY_API_KEY}" \
        M5_MODE="${MODE}" \
        M5_REVIEWING_MODEL="${M5_REVIEWING_MODEL:-}" \
        M5_SIGNAL="$SIGNAL" \
        M5_EVIDENCE="$EVIDENCE" \
        M6_API_KEY="${M6_API_KEY:-}" \
        M6_MODE="${M6_MODE:-${MODE}}" \
        M6_SIGNAL="$SIGNAL" \
        M6_EVIDENCE="$EVIDENCE" \
        FACTORY_URL="$headed_url" \
        FACTORY_GIT_URL="$headed_git" \
        npx playwright test --reporter=list)
    return $?
  fi
  # PLAYWRIGHT_DOCKER_ARGS is empty unless a caller opts in (--init, label, name).
  # An empty value must leave this argv identical to the historical form.
  local -a pw_prefix=()
  pw_prefix=(run --rm)
  if [[ -n "${PLAYWRIGHT_DOCKER_ARGS:-}" ]]; then
    local -a pw_extra=()
    # shellcheck disable=SC2206 # opt-in flags are separate words
    pw_extra=(${PLAYWRIGHT_DOCKER_ARGS})
    pw_prefix+=("${pw_extra[@]}")
  fi
  docker "${pw_prefix[@]}" \
    --network "$network" \
    -v "$WORK/e2e:/work" \
    -v "$EVIDENCE:/evidence" \
    -v "$SIGNAL:/signal" \
    -e "M5_API_KEY=${FACTORY_API_KEY}" \
    -e "M5_MODE=${MODE}" \
    -e "M5_REVIEWING_MODEL=${M5_REVIEWING_MODEL:-}" \
    -e M5_SIGNAL=/signal \
    -e M5_EVIDENCE=/evidence \
    -e "M6_API_KEY=${M6_API_KEY:-}" \
    -e "M6_MODE=${M6_MODE:-${MODE}}" \
    -e M6_SIGNAL=/signal \
    -e M6_EVIDENCE=/evidence \
    -e "FACTORY_URL=${factory_url}" \
    -e "FACTORY_GIT_URL=${factory_git_url}" \
    "${extra_env[@]}" \
    -w /work \
    "$PLAYWRIGHT_IMAGE" \
    bash -lc 'npm ci && npx playwright test --reporter=list'
}

save_logs() {
  mkdir -p "$EVIDENCE"
  compose logs --no-color >"$EVIDENCE/compose.log" 2>&1 || true
  if [[ -f "$WORK/router.log" ]]; then
    cp "$WORK/router.log" "$EVIDENCE/router.log"
  fi
  if [[ -f "$WORK/forge.log" ]]; then
    cp "$WORK/forge.log" "$EVIDENCE/forge.log"
  fi
  if [[ -f "$WORK/git-daemon.log" ]]; then
    cp "$WORK/git-daemon.log" "$EVIDENCE/git-daemon.log"
  fi
}

assert_evidence() {
  PYTHONPATH="${ROOT}/spike/m6${PYTHONPATH:+:$PYTHONPATH}" python3 - "$EVIDENCE" <<'PY'
import json, os, sys, zipfile
from pathlib import Path
from urllib.parse import urlparse

from redact_evidence import public_https_url

root = Path(sys.argv[1])
planted = []
for name in (
    "FACTORY_SECRET_M5_WEBHOOK",
    "FACTORY_WORKER_API_KEY",
    "S3_SECRET_KEY",
    "FACTORY_SECRET_M5_PLANTED",
    "FACTORY_INFERENCE_OPENAI_API_KEY",
):
    value = os.environ.get(name, "")
    if len(value) >= 8:
        planted.append(value.encode())

# public_https_url is redact_evidence's rule. Live M6_TASK_GIT_URL and
# M6_TASK_GITHUB_API are public https values that the redactor keeps, so
# they are not planted secrets. A control character urlsplit would delete
# keeps the value on the list. Offline git:// and http:// values, tokens,
# and URLs with userinfo or a query stay on the list. The fixed names above
# are unchanged.
for name in os.environ.get("EVIDENCE_EXTRA_SECRETS", "").split():
    value = os.environ.get(name, "")
    if len(value) >= 8 and not public_https_url(value):
        planted.append(value.encode())

def add_planted(value):
    if len(value) >= 8:
        planted.append(value.encode())

for raw_name in (
    "FACTORY_INFERENCE_OPENAI_COMPATIBLE_EXTRA_HEADERS",
    "M1_OPENAI_EXTRA_HEADERS",
):
    raw = os.environ.get(raw_name, "")
    if "\r" in raw:
        continue
    for line in raw.split("\n"):
        if ":" not in line:
            continue
        value = line.split(":", 1)[1].strip()
        add_planted(value)
        if value.lower().startswith("bearer "):
            add_planted(value[7:].strip())
api_key = os.environ.get("FACTORY_API_KEY", "").encode()

def har_mime(entry):
    content = (entry.get("response") or {}).get("content") or {}
    return str(content.get("mimeType") or "").split(";", 1)[0].strip().lower()

def har_static_asset(path):
    if path.startswith("/assets/"):
        return True
    name = path.rsplit("/", 1)[-1].lower()
    if name in {"favicon.ico", "favicon.png", "favicon.svg"}:
        return True
    return path.lower().endswith((".woff", ".woff2", ".ttf", ".otf", ".eot"))

# har_allowed_without_type classifies a HAR entry whose _resourceType
# is missing or empty. /api/v1 is any method. Static console assets and
# HTML documents are GET only.
def har_allowed_without_type(method, path, entry):
    if path.startswith("/api/v1/"):
        return True
    if method == "GET" and har_static_asset(path):
        return True
    if method == "GET" and har_mime(entry) == "text/html":
        return True
    return False

def walk_files():
    for path in root.rglob("*"):
        if path.is_file():
            yield path

for path in walk_files():
    data = path.read_bytes()
    for secret in planted:
        if secret in data:
            sys.stderr.write("planted secret in %s\n" % path.name)
            raise SystemExit(1)

for path in walk_files():
    if path.suffix not in {".html", ".log", ".txt"}:
        continue
    if api_key and api_key in path.read_bytes():
        sys.stderr.write("api key in %s\n" % path.name)
        raise SystemExit(1)

har_name = os.environ.get("EVIDENCE_HAR", "day.har")
trace_name = os.environ.get("EVIDENCE_TRACE", "day-trace.zip")
host_text = os.environ.get("EVIDENCE_HOSTS", "").strip()
if host_text:
    allowed_hosts = set(host_text.split())
else:
    allowed_hosts = {"factoryd", "factoryd-git", "127.0.0.1", "localhost"}
har_path = root / har_name
if har_path.is_file():
    har = json.loads(har_path.read_text(encoding="utf-8"))
    for entry in har.get("log", {}).get("entries", []):
        req = entry.get("request") or {}
        url = req.get("url") or ""
        host = urlparse(url).hostname or ""
        if host not in allowed_hosts:
            sys.stderr.write("off-origin %s\n" % url)
            raise SystemExit(1)
        rtype = str(entry.get("_resourceType") or "").strip()
        path = urlparse(url).path or ""
        method = str(req.get("method") or "").upper()
        if rtype:
            if rtype in {"xhr", "fetch", "eventsource", "websocket"} and not path.startswith("/api/v1/"):
                sys.stderr.write("non-api %s %s\n" % (rtype, path))
                raise SystemExit(1)
        elif not har_allowed_without_type(method, path, entry):
            sys.stderr.write("non-api %s %s\n" % (method, path))
            raise SystemExit(1)
        content = ((entry.get("response") or {}).get("content") or {}).get("text") or ""
        if api_key and api_key.decode() in content:
            sys.stderr.write("api key in a response body\n")
            raise SystemExit(1)
        for header in req.get("headers") or []:
            if str(header.get("name", "")).lower() == "authorization":
                header["value"] = "REDACTED"
        raw = json.dumps(entry)
        if api_key and api_key.decode() in raw:
            pass
    text = json.dumps(har)
    if api_key:
        text = text.replace(api_key.decode(), "REDACTED")
    rewritten = har_path.with_name(har_path.name + ".next")
    rewritten.write_text(text, encoding="utf-8")
    os.replace(rewritten, har_path)

trace = root / trace_name
if trace.is_file() and api_key:
    blob = trace.read_bytes()
    for secret in planted:
        if secret in blob:
            sys.stderr.write("planted secret in %s\n" % trace.name)
            raise SystemExit(1)
    out = trace.with_suffix(".zip.next")
    with zipfile.ZipFile(trace) as zin, zipfile.ZipFile(out, "w") as zout:
        for info in zin.infolist():
            data = zin.read(info.filename)
            for secret in planted:
                if secret in data:
                    sys.stderr.write("planted secret in trace entry\n")
                    raise SystemExit(1)
            data = data.replace(api_key, b"REDACTED")
            zout.writestr(info, data)
    out.replace(trace)
PY
}

fetch_origin_quiet() {
  local root="$1"
  python3 - "$root" <<'PY'
import os, subprocess, sys
root = sys.argv[1]
env = os.environ.copy()
env["GIT_TERMINAL_PROMPT"] = "0"
try:
    subprocess.run(
        ["git", "-C", root, "fetch", "--quiet", "origin"],
        env=env,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        timeout=15,
        check=True,
    )
except (subprocess.SubprocessError, OSError):
    raise SystemExit(1)
PY
}

warn_stale_checkout() {
  local root="${1:-$ROOT}" behind=""
  if [[ "${MODE:-}" == "live-ci" || -n "${GITHUB_ACTIONS:-}" ]]; then
    return 0
  fi
  if ! command -v git >/dev/null 2>&1; then
    return 0
  fi
  if ! git -C "$root" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    return 0
  fi
  if ! git -C "$root" remote get-url origin >/dev/null 2>&1; then
    return 0
  fi
  if ! fetch_origin_quiet "$root"; then
    return 0
  fi
  if ! git -C "$root" rev-parse --verify --quiet refs/remotes/origin/main >/dev/null 2>&1; then
    return 0
  fi
  behind="$(git -C "$root" rev-list --count HEAD..origin/main 2>/dev/null || true)"
  if [[ "$behind" == "1" ]]; then
    printf 'WARNING: HEAD is 1 commit behind origin/main. Run git pull to update this checkout.\n' >&2
    return 0
  fi
  if [[ "$behind" =~ ^[1-9][0-9]*$ ]]; then
    printf 'WARNING: HEAD is %s commits behind origin/main. Run git pull to update this checkout.\n' "$behind" >&2
  fi
  return 0
}

print_playwright_failures() {
  local log="${1:-}"
  if [[ ! -f "$log" ]]; then
    return 0
  fi
  if ! grep -E '^[[:space:]]+[0-9]+\) ' "$log" >/dev/null 2>&1; then
    return 0
  fi
  printf '\n== Playwright failures\n' >&2
  awk '
    /^[[:space:]]+[0-9]+\) / { show = 1 }
    show { print }
  ' "$log" >&2
}

# post_signed POSTs a body with an HMAC-SHA256 GitHub-style signature.
# POST_SIGNED_SECRET overrides the default m5 webhook secret so another
# driver can sign with its own value without copying this function.
post_signed() {
  local url="$1" dest="$2"
  shift 2
  local sig="" code="" secret=""
  secret="${POST_SIGNED_SECRET:-${FACTORY_SECRET_M5_WEBHOOK:-}}"
  sig="$(openssl dgst -sha256 -hmac "$secret" "$dest" | awk '{print $NF}')"
  code="$(curl --connect-timeout 2 -m 5 -sS -o "$WORK/deliver-body.txt" -w '%{http_code}' \
    -X POST "$url" \
    -H "Content-Type: application/json" \
    -H "X-Hub-Signature-256: sha256=${sig}" \
    "$@" \
    --data-binary @"$dest" || true)"
  printf '%s' "$code"
}

# stack_env_write records the named variables and the stack-class children
# under $WORK/stack.env (mode 600). It does nothing until spike_run_init.
stack_env_write() {
  local tmp="" name="" value=""
  if [[ -z "${SPIKE_RUN_LABEL:-}" ]]; then
    return 0
  fi
  umask 077
  tmp="$(mktemp "${WORK}/stack.env.XXXXXX")"
  {
    for name in "$@"; do
      eval "value=\${${name}-}"
      printf '%s=%q\n' "$name" "$value"
    done
    spike_emit_children
  } >"$tmp"
  chmod 600 "$tmp"
  mv -f "$tmp" "$WORK/stack.env"
}

stack_env_load() {
  # shellcheck disable=SC1091
  . "$WORK/stack.env"
  export SPIKE_RUN_ID SPIKE_RUN_LABEL
  SPIKE_REG="${WORK}/spike-run"
  export SPIKE_REG
  spike_load_children_from_env
}

# stack_refuse_live exits when a recorded stack pid is still alive.
# A stale file (every pid dead) is removed so the default path can run.
stack_refuse_live() {
  local prefix="$1" cmd="$2"
  if [[ ! -f "$WORK/stack.env" ]]; then
    return 0
  fi
  if (
    set +u
    # shellcheck disable=SC1091
    . "$WORK/stack.env"
    i=1
    n="${SPIKE_CHILD_COUNT:-0}"
    while [[ "$i" -le "$n" ]]; do
      eval "pid=\${SPIKE_CHILD_${i}_PID-}"
      eval "class=\${SPIKE_CHILD_${i}_CLASS-}"
      if [[ "$class" == stack && -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
        exit 0
      fi
      i=$((i + 1))
    done
    exit 1
  ); then
    echo "${prefix} FAIL — KEEP_UP stack is still up (${cmd} down or ${cmd} specs)" >&2
    exit 1
  fi
  rm -f "$WORK/stack.env"
}

stack_pids_match() {
  local prefix="$1" dir="" name="" pid=""
  for dir in "$SPIKE_REG"/children/*; do
    [[ -d "$dir" ]] || continue
    name="$(cat "$dir/name")"
    pid="$(cat "$dir/pid")"
    if ! spike_pid_identity_ok "$dir"; then
      echo "${prefix} FAIL — stale stack: ${name} pid ${pid} is gone" >&2
      return 1
    fi
  done
}

stack_compose_ready() {
  local prefix="$1" services="" joined="" svc=""
  shift
  joined=""
  for svc in "$@"; do
    if [[ -z "$joined" ]]; then
      joined="$svc"
    else
      joined="${joined} and ${svc}"
    fi
  done
  services="$(compose ps --status running --services 2>/dev/null || true)"
  for svc in "$@"; do
    if ! printf '%s\n' "$services" | grep -qx "$svc"; then
      echo "${prefix} FAIL — compose services ${joined} are not running" >&2
      return 1
    fi
  done
}

# Each argument is name=url. The failure line keeps the caller's service name.
stack_health_once() {
  local prefix="$1" pair="" name="" url=""
  shift
  for pair in "$@"; do
    name="${pair%%=*}"
    url="${pair#*=}"
    if ! curl -fsS --connect-timeout 2 -m 5 "$url" >/dev/null; then
      echo "${prefix} FAIL — ${name} /healthz did not answer" >&2
      return 1
    fi
  done
}

# stack_restart_processes stops stack-class children and starts them again
# from SPIKE_CHILD_<n>_*. The ready callback runs after the respawn.
stack_restart_processes() {
  local ready="${1:-}" i=1 n=0 class=""
  n="${SPIKE_CHILD_COUNT:-0}"
  if [[ "$n" -eq 0 ]]; then
    return 0
  fi
  KEEP_UP=0
  spike_stop_processes || true
  KEEP_UP=1
  rm -rf "$SPIKE_REG/children"
  mkdir -p "$SPIKE_REG/children"
  printf '%s\n' 0 >"$SPIKE_REG/seq"
  while [[ "$i" -le "$n" ]]; do
    eval "class=\${SPIKE_CHILD_${i}_CLASS-}"
    if [[ "$class" == stack ]]; then
      spike_respawn_child "$i"
    fi
    i=$((i + 1))
  done
  if [[ -n "$ready" ]]; then
    "$ready"
  fi
}

stack_down() {
  if [[ ! -f "$WORK/stack.env" ]]; then
    printf 'nothing to stop\n'
    exit 0
  fi
  stack_env_load
  KEEP_UP=0
  export KEEP_UP
  # The caller's cleanup trap reads COMPOSE_STARTED after this function exits.
  # shellcheck disable=SC2034
  COMPOSE_STARTED=1
  trap 'cleanup EXIT' EXIT
  trap 'cleanup INT' INT
  trap 'cleanup TERM' TERM
  exit 0
}

# spike_playwright_pipeline is the foreground body spike_fg waits on.
# PLAY_LOG is the tee target. The status is Playwright's, not tee's.
spike_playwright_pipeline() {
  local code=0
  set +e
  run_playwright 2>&1 | tee "$PLAY_LOG"
  code="${PIPESTATUS[0]}"
  return "$code"
}
