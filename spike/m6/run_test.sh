#!/usr/bin/env bash
# Shell checks for spike/m6/run.sh. No live network.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=spike/m6/run.sh
source "$ROOT/spike/m6/run.sh"

fails=0

run_case() {
  local name="$1"
  shift
  local err="$ROOT/spike/m6/.work/case-${name}.err"
  mkdir -p "$ROOT/spike/m6/.work"
  if "$@" >"$ROOT/spike/m6/.work/case-${name}.out" 2>"$err"; then
    printf 'ok   %s\n' "$name"
  else
    printf 'FAIL %s\n' "$name"
    cat "$err" >&2 || true
    fails=$((fails + 1))
  fi
}

expect_fail() {
  local name="$1" needle="$2"
  shift 2
  local err="$ROOT/spike/m6/.work/case-${name}.err"
  mkdir -p "$ROOT/spike/m6/.work"
  if "$@" >"$ROOT/spike/m6/.work/case-${name}.out" 2>"$err"; then
    printf 'FAIL %s (expected failure)\n' "$name"
    fails=$((fails + 1))
    return 0
  fi
  if ! grep -F "$needle" "$err" >/dev/null; then
    printf 'FAIL %s (stderr missing %s)\n' "$name" "$needle"
    cat "$err" >&2 || true
    fails=$((fails + 1))
    return 0
  fi
  printf 'ok   %s\n' "$name"
}

case_missing_live_url() {
  (
    unset FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL
    MODE=live
    require_mode_secrets
  )
}

case_missing_live_model() {
  (
    FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL="http://models.example/v1"
    unset M6_LIVE_MODEL
    MODE=live
    require_mode_secrets
  )
}

case_missing_live_definition() {
  (
    FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL="http://models.example/v1"
    M6_LIVE_MODEL="listed-model"
    unset M6_DEFINITION_GIT_URL
    MODE=live
    require_mode_secrets
  )
}

case_missing_live_app() {
  (
    FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL="http://models.example/v1"
    M6_LIVE_MODEL="listed-model"
    M6_DEFINITION_GIT_URL="https://example.invalid/m6/factory.git"
    unset FACTORY_GITHUB_APP_ID
    MODE=live
    require_mode_secrets
  )
}

case_missing_live_key() {
  (
    FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL="http://models.example/v1"
    M6_LIVE_MODEL="listed-model"
    M6_DEFINITION_GIT_URL="https://example.invalid/m6/factory.git"
    FACTORY_GITHUB_APP_ID="17"
    unset FACTORY_GITHUB_APP_PRIVATE_KEY_FILE
    MODE=live
    require_mode_secrets
  )
}

case_missing_ci_url() {
  (
    unset M1_OPENAI_BASE_URL
    MODE=live-ci
    require_mode_secrets
  )
}

case_missing_ci_key() {
  (
    M1_OPENAI_BASE_URL="http://models.example/v1"
    unset M1_OPENAI_API_KEY
    MODE=live-ci
    require_mode_secrets
  )
}

case_missing_ci_model() {
  (
    M1_OPENAI_BASE_URL="http://models.example/v1"
    M1_OPENAI_API_KEY="present"
    unset M6_LIVE_MODEL
    MODE=live-ci
    require_mode_secrets
  )
}

case_loopback_rejected() {
  reject_loopback "http://127.0.0.1:9/v1"
}

case_loopback_allows_gateway() {
  reject_loopback "http://172.17.0.1:9/v1"
}

case_port_prebound() {
  local dir="$ROOT/spike/m6/.work/prebound-$$" port="" pid=""
  mkdir -p "$dir"
  python3 - "$dir/port" <<'PY' &
import socket
import sys
import time

sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
sock.bind(("127.0.0.1", 0))
sock.listen(1)
with open(sys.argv[1], "w", encoding="utf-8") as fh:
    fh.write(str(sock.getsockname()[1]))
while True:
    time.sleep(0.2)
PY
  pid=$!
  local i
  for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
    if [[ -s "$dir/port" ]]; then
      break
    fi
    sleep 0.1
  done
  port="$(tr -d '[:space:]' <"$dir/port")"
  local code=0
  set +e
  pick_port M6_ROUTER_PORT "$port" >/dev/null
  code=$?
  set -e
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  rm -rf "$dir"
  if [[ "$code" -eq 0 ]]; then
    echo "prebound listener was accepted" >&2
    return 1
  fi
}

case_port_reserved() {
  pick_port M6_ROUTER_PORT 8318
}

case_port_picked() {
  local port=""
  port="$(pick_port M6_ROUTER_PORT "")"
  [[ "$port" =~ ^[1-9][0-9]*$ ]]
  [[ "$port" != 8317 && "$port" != 8318 ]]
}

case_rewrite_copy_only() {
  local dir="$ROOT/spike/m6/.work/rewrite-$$" before="" after=""
  mkdir -p "$dir"
  before="$(mktemp)"
  after="$(mktemp)"
  (cd "$ROOT/deploy/m6-factory" && find . -type f -print | sort | while IFS= read -r path; do
    printf '%s ' "$path"
    cksum <"$ROOT/deploy/m6-factory/$path"
  done) >"$before"
  cp -R "$ROOT/deploy/m6-factory/." "$dir/copy/"
  rewrite_models "$dir/copy" "m6-agent" "m6-judge"
  (cd "$ROOT/deploy/m6-factory" && find . -type f -print | sort | while IFS= read -r path; do
    printf '%s ' "$path"
    cksum <"$ROOT/deploy/m6-factory/$path"
  done) >"$after"
  cmp -s "$before" "$after"
  grep -F "m6-agent" "$dir/copy/factory.yaml" >/dev/null
  if grep -R -F "m6-agent" "$ROOT/deploy/m6-factory" >/dev/null; then
    echo "rewrite touched deploy/m6-factory" >&2
    return 1
  fi
  rm -rf "$dir" "$before" "$after"
}

case_router_routes() {
  python3 - "$ROOT/spike/m6/router.py" <<'PY'
import importlib.util
import json
import os
import sys

os.environ["M6_DEFINITION_GIT_URL"] = "git://172.17.0.1:9/m6/factory.git"
os.environ["M6_GITHUB_API_BASE"] = "http://172.17.0.1:9"
path = sys.argv[1]
spec = importlib.util.spec_from_file_location("m6router", path)
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)

def chat(model, messages):
    status, payload = mod.answer({"model": model, "messages": messages})
    return status, payload

status, payload = chat("m6-agent", [{"role": "user", "content": "Summarize the change."}])
if status != 200 or "CONVENTION: cite-sources" in payload["choices"][0]["message"]["content"]:
    raise SystemExit("baseline agent invented the convention")
status, payload = chat(
    "m6-agent",
    [{"role": "system", "content": "CONVENTION: cite-sources"}, {"role": "user", "content": "Summarize"}],
)
if "CONVENTION: cite-sources" not in payload["choices"][0]["message"]["content"]:
    raise SystemExit("candidate agent dropped the convention")
status, payload = chat("m6-agent", [{"role": "user", "content": "m6-infra-trial"}])
if status != 500:
    raise SystemExit("infra trial was not an error")
status, payload = chat(
    "m6-judge",
    [{"role": "user", "content": 'You are the scorer "cite-sources".\n## Work item\nm6-bad-label\n## Transcript\nDone.\n'}],
)
body = json.loads(payload["choices"][0]["message"]["content"])
if body["label"] != "not-a-label":
    raise SystemExit("bad label was accepted")
first = body["reasoning"]
status, payload = chat(
    "m6-judge",
    [{"role": "user", "content": 'You are the scorer "correctness".\n## Success criteria\nCONVENTION: cite-sources\n## Transcript\nDone.\n'}],
)
body = json.loads(payload["choices"][0]["message"]["content"])
if body["label"] != "fail" or body["reasoning"] == first:
    raise SystemExit("correctness judged the success criteria or reused reasoning")
status, payload = chat(
    "m6-agent",
    [{"role": "user", "content": "You are improving the factory definition.\nbranch: improve/batch1\n"}],
)
command = json.loads(payload["choices"][0]["message"]["tool_calls"][0]["function"]["arguments"])["command"]
if "M6_CLONED" not in command or "GH_TOKEN" in command:
    raise SystemExit("first improve step was not a clone")
PY
}

case_forge_pulls() {
  python3 - "$ROOT" <<'PY'
import json
import os
import socket
import subprocess
import sys
import time
import urllib.request

root = sys.argv[1]
work = os.path.join(root, "spike", "m6", ".work", "forge-case")
os.makedirs(work, exist_ok=True)
fake_bin = os.path.join(work, "fake-github")
subprocess.run(
    ["go", "build", "-o", fake_bin, "./spike/m4/fakes/github"],
    cwd=root,
    check=True,
    stdout=subprocess.DEVNULL,
)

def free_port():
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.bind(("127.0.0.1", 0))
    port = sock.getsockname()[1]
    sock.close()
    return port

fake_port = free_port()
forge_port = free_port()
fake = subprocess.Popen(
    [fake_bin, "-addr", "127.0.0.1:%d" % fake_port],
    stdout=subprocess.DEVNULL,
    stderr=subprocess.DEVNULL,
)
env = os.environ.copy()
env["M6_UPSTREAM_GITHUB"] = "http://127.0.0.1:%d" % fake_port
env["M6_TOKEN_FILE"] = os.path.join(work, "token")
forge = subprocess.Popen(
    [sys.executable, os.path.join(root, "spike", "m6", "forge.py"), "--addr", "127.0.0.1:%d" % forge_port],
    env=env,
    stdout=subprocess.DEVNULL,
    stderr=subprocess.DEVNULL,
)

def wait_up(url):
    for _ in range(50):
        try:
            with urllib.request.urlopen(url, timeout=1) as res:
                if res.status == 200:
                    return
        except OSError:
            time.sleep(0.1)
    raise SystemExit("listener did not start")

try:
    wait_up("http://127.0.0.1:%d/healthz" % fake_port)
    wait_up("http://127.0.0.1:%d/_spike/m6" % forge_port)
    minted = urllib.request.Request(
        "http://127.0.0.1:%d/app/installations/1/access_tokens" % forge_port,
        data=b'{"repositories":["m6/factory"],"permissions":{"issues":"write","checks":"write","pull_requests":"write"}}',
        headers={"Authorization": "Bearer app", "Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(minted, timeout=5) as res:
        token = json.load(res)["token"]
    if not token:
        raise SystemExit("mint returned an empty token")

    def call(method, path, payload=None, auth=True):
        data = None if payload is None else json.dumps(payload).encode()
        headers = {"Content-Type": "application/json"}
        if auth:
            headers["Authorization"] = "Bearer " + token
        req = urllib.request.Request(
            "http://127.0.0.1:%d%s" % (forge_port, path),
            data=data,
            headers=headers,
            method=method,
        )
        try:
            with urllib.request.urlopen(req, timeout=5) as res:
                raw = res.read()
                return res.status, json.loads(raw) if raw else {}
        except urllib.error.HTTPError as err:
            raw = err.read()
            body = json.loads(raw) if raw else {}
            return err.code, body

    status, created = call(
        "POST",
        "/repos/m6/factory/pulls",
        {"title": "one", "head": "improve/a", "base": "main", "body": "first"},
    )
    if status != 201 or not str(created.get("html_url", "")).startswith("https://pr.invalid/"):
        raise SystemExit("create did not return an https pull request")
    status, _ = call(
        "POST",
        "/repos/m6/factory/pulls",
        {"title": "two", "head": "improve/a", "base": "main", "body": "## Regressions addressed\n\n- run r1\n\nLayer changed: skill\n"},
    )
    if status != 422:
        raise SystemExit("duplicate create was not 422")
    status, rows = call("GET", "/repos/m6/factory/pulls?state=open&head=m6:improve/a")
    if status != 200 or rows[0]["number"] != created["number"]:
        raise SystemExit("lookup missed the open pull request")
    status, _ = call("POST", "/repos/m6/factory/pulls/1/requested_reviewers", {"reviewers": ["m6-reviewer"]})
    if status != 200:
        raise SystemExit("reviewers were rejected")
    status, _ = call("POST", "/repos/m6/factory/issues/1/labels", {"labels": ["skill"]})
    if status != 200:
        raise SystemExit("labels were rejected")
    status, _ = call(
        "POST",
        "/repos/m6/factory/check-runs",
        {"name": "factory/config", "head_sha": "abc", "status": "completed", "conclusion": "success", "output": {"summary": "factory/config passed"}},
    )
    if status != 201:
        raise SystemExit("check run was rejected")
    with urllib.request.urlopen("http://127.0.0.1:%d/_spike/m6" % forge_port, timeout=5) as res:
        state = json.load(res)
    pull = state["pulls"][0]
    if "m6-reviewer" not in pull["reviewers"] or "skill" not in pull["labels"]:
        raise SystemExit("state dropped reviewers or labels")
    bodies = [row["body"] for row in pull["creates"]]
    if not any(body.startswith("## Regressions addressed") and "Layer changed: skill" in body for body in bodies):
        raise SystemExit("state dropped the product pull request body")
    if not any(row.get("summary") == "factory/config passed" for row in state["checks"]):
        raise SystemExit("state dropped the factory/config check")
    status, _ = call("POST", "/repos/m6/factory/pulls", {"title": "x", "head": "other", "base": "main"}, auth=False)
    if status != 401:
        raise SystemExit("unauthenticated create was accepted")
finally:
    forge.kill()
    fake.kill()
    forge.wait(timeout=5)
    fake.wait(timeout=5)
PY
}

case_git_shallow_sha() {
  local dir="$ROOT/spike/m6/.work/git-case-$$" port="" pid="" sha=""
  rm -rf "$dir"
  mkdir -p "$dir/src" "$dir/base"
  git -C "$dir/src" init -b main
  printf 'one\n' >"$dir/src/file"
  git -C "$dir/src" add file
  git -C "$dir/src" -c user.email=factory@example.com -c user.name=factory commit -m one
  sha="$(git -C "$dir/src" rev-parse HEAD)"
  printf 'two\n' >"$dir/src/file"
  git -C "$dir/src" add file
  git -C "$dir/src" -c user.email=factory@example.com -c user.name=factory commit -m two
  git clone --bare "$dir/src" "$dir/base/m6/factory.git"
  git --git-dir="$dir/base/m6/factory.git" config uploadpack.allowReachableSHA1InWant true
  port="$(pick_port M6_GIT_PORT "")"
  git daemon --reuseaddr --export-all --enable=receive-pack \
    --listen=127.0.0.1 --port="$port" \
    --base-path="$dir/base" "$dir/base" \
    >"$dir/daemon.log" 2>&1 &
  pid=$!
  wait_port "$port"
  git -C "$dir/src" remote add origin "git://127.0.0.1:${port}/m6/factory.git"
  printf 'three\n' >"$dir/src/file"
  git -C "$dir/src" add file
  git -C "$dir/src" -c user.email=factory@example.com -c user.name=factory commit -m three
  git -C "$dir/src" push origin HEAD:refs/heads/pushed
  mkdir -p "$dir/fetch"
  git -C "$dir/fetch" init -b main
  git -C "$dir/fetch" fetch --depth 1 "git://127.0.0.1:${port}/m6/factory.git" "$sha"
  [[ "$(git -C "$dir/fetch" rev-parse FETCH_HEAD)" == "$sha" ]]
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  rm -rf "$dir"
}

case_evidence_leak() {
  local dir="$ROOT/spike/m6/.work/evidence-leak-$$"
  mkdir -p "$dir"
  printf 'planted-value-123\n' >"$dir/leak.txt"
  (
    EVIDENCE="$dir"
    export EVIDENCE
    FACTORY_SECRET_M6_PLANTED="planted-value-123"
    export FACTORY_SECRET_M6_PLANTED
    EVIDENCE_EXTRA_SECRETS="FACTORY_SECRET_M6_PLANTED"
    export EVIDENCE_EXTRA_SECRETS
    assert_evidence
  )
}

case_evidence_clean() {
  local dir="$ROOT/spike/m6/.work/evidence-clean-$$"
  mkdir -p "$dir"
  printf 'nothing sensitive\n' >"$dir/note.txt"
  (
    EVIDENCE="$dir"
    export EVIDENCE
    FACTORY_SECRET_M6_PLANTED="planted-value-123"
    export FACTORY_SECRET_M6_PLANTED
    FACTORY_API_KEY="api-key-value-123"
    export FACTORY_API_KEY
    EVIDENCE_EXTRA_SECRETS="FACTORY_SECRET_M6_PLANTED"
    export EVIDENCE_EXTRA_SECRETS
    assert_evidence
  )
  rm -rf "$dir"
}

case_token_probe_shape() {
  local dir="$ROOT/spike/m6/.work/token-probe-$$"
  mkdir -p "$dir"
  cat >"$dir/curl" <<'EOF'
#!/bin/bash
set -euo pipefail
printf '%s\n' "$@" >"$CURL_ARGS_FILE"
printf '%s\n' '{"id":"ok"}' >"$2"
printf '200'
EOF
  # The stub's -o dest is not always $2. Record every argument instead.
  cat >"$dir/curl" <<'EOF'
#!/bin/bash
set -euo pipefail
printf '%s\n' "$@" >"$CURL_ARGS_FILE"
dest=""
prev=""
for arg in "$@"; do
  if [[ "$prev" == "-o" ]]; then
    dest="$arg"
  fi
  prev="$arg"
done
if [[ -n "$dest" ]]; then
  printf '%s\n' '{"id":"ok"}' >"$dest"
fi
printf '200'
EOF
  chmod 755 "$dir/curl"
  (
    export FACTORY_INFERENCE_OPENAI_API_KEY="secret-key-value"
    export TOKEN_CURL="$dir/curl"
    export CURL_ARGS_FILE="$dir/args"
    token_probe "http://127.0.0.1:9/v1" "listed-model" >"$dir/out"
  )
  grep -F 'max_tokens' "$dir/args" >/dev/null
  if grep -F 'secret-key-value' "$dir/out" >/dev/null; then
    echo "token probe printed the key" >&2
    return 1
  fi
  rm -rf "$dir"
}

case_compose_config() {
  docker compose -f "$ROOT/deploy/docker-compose.yml" --profile m6 config >/dev/null
  docker compose -f "$ROOT/deploy/docker-compose.yml" -f "$ROOT/spike/m6/compose.yml" --profile m6 config >/dev/null
}

expect_fail missing-live-url "missing FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL" case_missing_live_url
expect_fail missing-live-model "missing M6_LIVE_MODEL" case_missing_live_model
expect_fail missing-live-definition "missing M6_DEFINITION_GIT_URL" case_missing_live_definition
expect_fail missing-live-app "missing FACTORY_GITHUB_APP_ID" case_missing_live_app
expect_fail missing-live-key "missing FACTORY_GITHUB_APP_PRIVATE_KEY_FILE" case_missing_live_key
expect_fail missing-ci-url "missing M1_OPENAI_BASE_URL" case_missing_ci_url
expect_fail missing-ci-key "missing M1_OPENAI_API_KEY" case_missing_ci_key
expect_fail missing-ci-model "missing M6_LIVE_MODEL" case_missing_ci_model
expect_fail loopback-rejected "loopback host" case_loopback_rejected
run_case loopback-gateway case_loopback_allows_gateway
run_case port-prebound case_port_prebound
expect_fail port-reserved "router port 8318 is reserved" case_port_reserved
run_case port-picked case_port_picked
run_case rewrite-copy case_rewrite_copy_only
run_case router-routes case_router_routes
run_case forge-pulls case_forge_pulls
run_case git-shallow case_git_shallow_sha
expect_fail evidence-leak "planted secret" case_evidence_leak
run_case evidence-clean case_evidence_clean
run_case token-probe case_token_probe_shape
run_case compose-config case_compose_config

if [[ "$fails" -ne 0 ]]; then
  printf '%s failed\n' "$fails" >&2
  exit 1
fi
printf 'spike/m6/run_test.sh ok\n'
