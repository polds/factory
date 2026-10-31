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

case_missing_live_api() {
  (
    FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL="http://models.example/v1"
    M6_LIVE_MODEL="listed-model"
    M6_DEFINITION_GIT_URL="https://example.invalid/m6/factory.git"
    unset M6_GITHUB_API_BASE
    MODE=live
    require_mode_secrets
  )
}

case_missing_live_app() {
  (
    FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL="http://models.example/v1"
    M6_LIVE_MODEL="listed-model"
    M6_DEFINITION_GIT_URL="https://example.invalid/m6/factory.git"
    M6_GITHUB_API_BASE="https://api.example.invalid"
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
    M6_GITHUB_API_BASE="https://api.example.invalid"
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

case_missing_ci_definition() {
  (
    M1_OPENAI_BASE_URL="http://models.example/v1"
    M1_OPENAI_API_KEY="present"
    M6_LIVE_MODEL="listed-model"
    unset M6_DEFINITION_GIT_URL
    MODE=live-ci
    require_mode_secrets
  )
}

case_missing_ci_api() {
  (
    M1_OPENAI_BASE_URL="http://models.example/v1"
    M1_OPENAI_API_KEY="present"
    M6_LIVE_MODEL="listed-model"
    M6_DEFINITION_GIT_URL="https://example.invalid/m6/factory.git"
    unset M6_GITHUB_API_BASE
    MODE=live-ci
    require_mode_secrets
  )
}

case_missing_ci_app() {
  (
    M1_OPENAI_BASE_URL="http://models.example/v1"
    M1_OPENAI_API_KEY="present"
    M6_LIVE_MODEL="listed-model"
    M6_DEFINITION_GIT_URL="https://example.invalid/m6/factory.git"
    M6_GITHUB_API_BASE="https://api.example.invalid"
    unset FACTORY_GITHUB_APP_ID
    MODE=live-ci
    require_mode_secrets
  )
}

case_missing_ci_keyfile() {
  (
    M1_OPENAI_BASE_URL="http://models.example/v1"
    M1_OPENAI_API_KEY="present"
    M6_LIVE_MODEL="listed-model"
    M6_DEFINITION_GIT_URL="https://example.invalid/m6/factory.git"
    M6_GITHUB_API_BASE="https://api.example.invalid"
    FACTORY_GITHUB_APP_ID="17"
    unset FACTORY_GITHUB_APP_PRIVATE_KEY_FILE
    MODE=live-ci
    require_mode_secrets
  )
}

case_app_key_mount() {
  local mode="$1" dir="" rendered=""
  dir="$ROOT/spike/m6/.work/app-key-${mode}-$$"
  mkdir -p "$dir"
  printf 'not-a-real-key\n' >"$dir/app.pem"
  rendered="$dir/config.yml"
  (
    MODE="$mode"
    WORK="$dir"
    FACTORY_GITHUB_APP_PRIVATE_KEY_FILE="$dir/app.pem"
    assign_app_key
    if [[ "$M6_APP_KEY_FILE" != "$dir/app.pem" ]]; then
      echo "M6_APP_KEY_FILE is ${M6_APP_KEY_FILE}" >&2
      exit 1
    fi
    export M6_APP_KEY_FILE
    docker compose -f "$ROOT/deploy/docker-compose.yml" -f "$ROOT/spike/m6/compose.yml" --profile m6 config >"$rendered"
  )
  if ! awk -v path="$dir/app.pem" '
    index($0, "source: " path) { found = 1 }
    found && index($0, "target: /run/secrets/github-app.pem") { ok = 1 }
    END { exit ok ? 0 : 1 }
  ' "$rendered"; then
    echo "${mode} compose mount is not the checked key file" >&2
    rm -rf "$dir"
    return 1
  fi
  rm -rf "$dir"
}

case_live_targets() {
  (
    MODE=live
    M6_DEFINITION_GIT_URL="https://example.invalid/m6/factory.git"
    M6_GITHUB_API_BASE="https://api.example.invalid/"
    GIT_PORT=9
    FORGE_PORT=9
    TASK_HOST="172.17.0.1"
    apply_definition_targets
    if [[ "$M6_FACTORYD_GIT_URL" != "https://example.invalid/m6/factory.git" ||
      "$M6_TASK_GIT_URL" != "https://example.invalid/m6/factory.git" ||
      "$M6_FACTORYD_GITHUB_API" != "https://api.example.invalid" ||
      "$M6_TASK_GITHUB_API" != "https://api.example.invalid" ]]; then
      echo "live targets did not use the operator URLs" >&2
      exit 1
    fi
  )
}

case_offline_targets_ignore_operator() {
  (
    # shellcheck disable=SC2030 # each case sets MODE in its own subshell
    MODE=offline
    M6_DEFINITION_GIT_URL="https://example.invalid/m6/factory.git"
    M6_GITHUB_API_BASE="https://api.example.invalid"
    GIT_PORT=9420
    FORGE_PORT=9421
    TASK_HOST="172.17.0.1"
    apply_definition_targets
    if [[ "$M6_FACTORYD_GIT_URL" != "git://host.docker.internal:9420/m6/factory.git" ||
      "$M6_TASK_GIT_URL" != "git://172.17.0.1:9420/m6/factory.git" ||
      "$M6_FACTORYD_GITHUB_API" != "http://host.docker.internal:9421" ||
      "$M6_TASK_GITHUB_API" != "http://172.17.0.1:9421" ]]; then
      echo "offline targets followed the operator URLs" >&2
      exit 1
    fi
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
if (
    "M6_CLONED" not in command
    or "GH_TOKEN" in command
    or "--depth" in command
    or "--single-branch" in command
    or "git clone" not in command
    or "/workspace" not in command
):
    raise SystemExit("first improve step was not a full clone into /workspace")

def grounded_prompt(extra=""):
    return (
        "You are improving the factory definition.\n"
        "# Instructions\n"
        "DEFINITION_GIT_URL\n"
        "## Scorer rubrics\n"
        "- scorer review\n"
        "  rubric:\n"
        "    Return fail when the marker is absent.\n"
        "## Failing examples\n"
        "- run run_1 scorer review label fail\n"
        "  reasoning: the skill omitted the required line\n"
        "  assistant output: Done.\n"
        "## Definition tree\n"
        "branch: improve/batch1\n"
        "State the required behaviour as an explicit, imperative instruction "
        "in the skill or instruction file you edit. Remove or rewrite any "
        "existing instruction in the edited files that contradicts it. "
        "A bare appended marker line is not enough.\n"
        + extra
    )

status, payload = chat("m6-agent", [{"role": "user", "content": grounded_prompt()}])
command = json.loads(payload["choices"][0]["message"]["tool_calls"][0]["function"]["arguments"])["command"]
if "git clone" not in command or "M6_CLONED" not in command:
    raise SystemExit("grounded improver prompt was not a clone")
ungrounded = grounded_prompt().replace(
    "State the required behaviour as an explicit, imperative instruction ",
    "",
).replace("assistant output: Done.", "assistant output: (none)")
status, payload = chat("m6-agent", [{"role": "user", "content": ungrounded}])
body = json.loads(payload["choices"][0]["message"]["content"])
if body.get("status") != "failed_by_agent" or "failing evidence" not in body.get("summary", ""):
    raise SystemExit("ungrounded improver prompt was accepted")
status, payload = chat(
    "m6-agent",
    [{"role": "user", "content": "You are improving the factory definition.\nbranch: improve/batch1\nM6_CLONED\n"}],
)
command = json.loads(payload["choices"][0]["message"]["tool_calls"][0]["function"]["arguments"])["command"]
if (
    "grep -v -F -x" not in command
    or "Do not invent a required final line that this skill does not contain." not in command
    or "Include the following line exactly, on its own line, in the final message:" not in command
    or "CONVENTION: cite-sources" not in command
    or "cat >>" in command
):
    raise SystemExit("improver edit did not remove the contradicting line")
os.environ.pop("M6_IMPROVER_OPENS_PR", None)
status, payload = chat(
    "m6-agent",
    [{"role": "user", "content": "You are improving the factory definition.\nbranch: improve/batch1\nM6_PUSHED\n"}],
)
message = payload["choices"][0]["message"]
if message.get("tool_calls") or "repos/m6/factory/pulls" in json.dumps(message):
    raise SystemExit("default improver opened a pull request")
if "Improvement branch is pushed." not in (message.get("content") or ""):
    raise SystemExit("default improver did not stop after the push")
os.environ["M6_IMPROVER_OPENS_PR"] = "1"
status, payload = chat(
    "m6-agent",
    [{"role": "user", "content": "You are improving the factory definition.\nbranch: improve/batch1\nM6_PUSHED\n"}],
)
command = json.loads(payload["choices"][0]["message"]["tool_calls"][0]["function"]["arguments"])["command"]
if "/repos/m6/factory/pulls" not in command or "Opened by the offline improver." not in command:
    raise SystemExit("forced improver did not open a pull request")
status, payload = chat(
    "m6-agent",
    [{"role": "user", "content": "m6-ask-input. Ask which one-sentence summary to write, then stop."}],
)
message = payload["choices"][0]["message"]
fn = message["tool_calls"][0]["function"]
if status != 200 or fn["name"] != "ask_input" or "CONVENTION: cite-sources" in json.dumps(message):
    raise SystemExit("stub did not ask without inventing the convention")
status, payload = chat(
    "m6-agent",
    [{"role": "user", "content": "The repository is already in the desired state. Do not call ask_input."}],
)
message = payload["choices"][0]["message"]
if message.get("tool_calls") or "CONVENTION: cite-sources" in (message.get("content") or ""):
    raise SystemExit("concrete prompt asked or invented the convention")
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
    forge.kill()
    forge.wait(timeout=5)
    forge_port = free_port()
    forge = subprocess.Popen(
        [sys.executable, os.path.join(root, "spike", "m6", "forge.py"), "--addr", "127.0.0.1:%d" % forge_port],
        env=env,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    wait_up("http://127.0.0.1:%d/_spike/m6" % forge_port)
    with urllib.request.urlopen("http://127.0.0.1:%d/_spike/m6" % forge_port, timeout=5) as res:
        fresh = json.load(res)
    if fresh["pulls"]:
        raise SystemExit("restarted forge kept pull state")
    status, restarted = call(
        "POST",
        "/repos/m6/factory/pulls",
        {"title": "again", "head": "improve/b", "base": "main", "body": "fresh"},
    )
    if status != 201 or restarted.get("number") != 1:
        raise SystemExit("restarted forge rejected the saved installation token")
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

# evidence_check_env is the live evidence list from m6_playwright_and_evidence.
# Parent secrets are cleared so the fixture is the only match.
evidence_check_env() {
  unset FACTORY_API_KEY FACTORY_SECRET_M5_WEBHOOK FACTORY_WORKER_API_KEY \
    S3_SECRET_KEY FACTORY_SECRET_M5_PLANTED FACTORY_INFERENCE_OPENAI_API_KEY \
    FACTORY_INFERENCE_OPENAI_COMPATIBLE_EXTRA_HEADERS M1_OPENAI_EXTRA_HEADERS \
    INSTALL_TOKEN M6_APP_TOKEN FACTORY_GITHUB_WEBHOOK_SECRET \
    EVIDENCE_HAR EVIDENCE_TRACE EVIDENCE_HOSTS
  FACTORY_SECRET_M6_PLANTED="planted-value-123"
  export FACTORY_SECRET_M6_PLANTED
  EVIDENCE_EXTRA_SECRETS="FACTORY_SECRET_M6_PLANTED FACTORY_GITHUB_WEBHOOK_SECRET M6_TASK_GIT_URL M6_TASK_GITHUB_API INSTALL_TOKEN M6_APP_TOKEN"
  export EVIDENCE_EXTRA_SECRETS
}

case_evidence_public_definition_url() {
  local dir="$ROOT/spike/m6/.work/evidence-public-$$"
  local repo="https://github.com/polds/factory-m6-definition"
  local code=0
  rm -rf "$dir"
  mkdir -p "$dir"
  printf '%s\n' \
    "$repo" \
    "${repo}/pull/7" \
    "https://api.github.com/repos/polds/factory-m6-definition/pulls/7" \
    >"$dir/dom-improvement.html"
  (
    MODE=live
    M6_DEFINITION_GIT_URL="$repo"
    M6_GITHUB_API_BASE="https://api.github.com"
    export MODE M6_DEFINITION_GIT_URL M6_GITHUB_API_BASE
    apply_definition_targets
    EVIDENCE="$dir"
    export EVIDENCE
    evidence_check_env
    [[ "$M6_TASK_GIT_URL" == "$repo" ]]
    [[ "$M6_TASK_GITHUB_API" == "https://api.github.com" ]]
    assert_evidence
  ) || code=$?
  rm -rf "$dir"
  return "$code"
}

case_evidence_public_url_keeps_planted_secret() {
  local dir="$ROOT/spike/m6/.work/evidence-public-secret-$$"
  local repo="https://github.com/polds/factory-m6-definition"
  local code=0
  rm -rf "$dir"
  mkdir -p "$dir"
  printf '%s\n' \
    "$repo" \
    "${repo}/pull/7" \
    "planted-value-123" \
    >"$dir/dom-improvement.html"
  (
    MODE=live
    M6_DEFINITION_GIT_URL="$repo"
    M6_GITHUB_API_BASE="https://api.github.com"
    export MODE M6_DEFINITION_GIT_URL M6_GITHUB_API_BASE
    apply_definition_targets
    EVIDENCE="$dir"
    export EVIDENCE
    evidence_check_env
    assert_evidence
  ) || code=$?
  rm -rf "$dir"
  return "$code"
}

case_evidence_credentialed_url() {
  local dir="$ROOT/spike/m6/.work/evidence-cred-$$"
  local token="ghs_aaaaaaaaaaaaaaaaaaaa"
  local url="https://x-access-token:${token}@github.com/polds/factory-m6-definition.git"
  local code=0
  rm -rf "$dir"
  mkdir -p "$dir"
  printf '%s\n' "$url" >"$dir/dom-improvement.html"
  (
    MODE=live
    M6_DEFINITION_GIT_URL="$url"
    M6_GITHUB_API_BASE="https://api.github.com"
    export MODE M6_DEFINITION_GIT_URL M6_GITHUB_API_BASE
    apply_definition_targets
    EVIDENCE="$dir"
    export EVIDENCE
    evidence_check_env
    assert_evidence
  ) || code=$?
  rm -rf "$dir"
  return "$code"
}

case_evidence_query_url() {
  local dir="$ROOT/spike/m6/.work/evidence-query-$$"
  local url="https://github.com/polds/factory-m6-definition?access=qqqqqqqqqqqqqqqqqqqqqqqq"
  local code=0
  rm -rf "$dir"
  mkdir -p "$dir"
  printf '%s\n' "$url" >"$dir/dom-improvement.html"
  (
    MODE=live
    M6_DEFINITION_GIT_URL="$url"
    M6_GITHUB_API_BASE="https://api.github.com"
    export MODE M6_DEFINITION_GIT_URL M6_GITHUB_API_BASE
    apply_definition_targets
    EVIDENCE="$dir"
    export EVIDENCE
    evidence_check_env
    assert_evidence
  ) || code=$?
  rm -rf "$dir"
  return "$code"
}

case_evidence_offline_task_url() {
  local dir="$ROOT/spike/m6/.work/evidence-offline-url-$$"
  local code=0
  rm -rf "$dir"
  mkdir -p "$dir"
  (
    # shellcheck disable=SC2030 # each case sets MODE in its own subshell
    MODE=offline
    TASK_HOST=172.17.0.1
    GIT_PORT=9420
    FORGE_PORT=9419
    export MODE TASK_HOST GIT_PORT FORGE_PORT
    apply_definition_targets
    printf '%s\n%s\n' "$M6_TASK_GIT_URL" "$M6_TASK_GITHUB_API" >"$dir/dom-improvement.html"
    EVIDENCE="$dir"
    export EVIDENCE
    evidence_check_env
    assert_evidence
  ) || code=$?
  rm -rf "$dir"
  return "$code"
}

# case_evidence_control_splice plants URLs that urlsplit would shorten by
# deleting tab, LF, or CR. The check must exit 1 and say planted secret.
# Names are printed. Values are not.
case_evidence_control_splice() {
  local dir="$ROOT/spike/m6/.work/evidence-splice-$$"
  local name="" err="" one=0
  rm -rf "$dir"
  mkdir -p "$dir"
  python3 - "$dir" <<'PY'
import sys
from pathlib import Path

root = Path(sys.argv[1])
origin = "https://api.github.com"
repo = "https://github.com/polds/factory-m6-definition"
ghs = "ghs_" + ("a" * 20)
ghp = "ghp_" + ("b" * 20)
tail = "c" * 24
cases = {
    "origin-lf": origin + "\n" + tail,
    "origin-cr": origin + "\r" + tail,
    "origin-tab": origin + "\t" + tail,
    "def-lf-ghs": repo + "\n" + ghs,
    "def-cr-ghp": repo + "\r" + ghp,
    "def-tab-ghs": repo + "\t" + ghs,
    "def-lf-pull": repo + "\n" + ghs + "/pull/3",
    "def-cr-pull": repo + "\r" + ghp + "/pull/3",
    "def-tab-pull": repo + "\t" + ghs + "/pull/3",
    "host-tab": "https://api.gith" + "\t" + "ub.com",
}
for name, value in cases.items():
    (root / name).write_text(value, encoding="utf-8")
    evidence = root / (name + ".d")
    evidence.mkdir()
    (evidence / "dom-improvement.html").write_text(value, encoding="utf-8")
PY
  for name in origin-lf origin-cr origin-tab def-lf-ghs def-cr-ghp def-tab-ghs def-lf-pull def-cr-pull def-tab-pull host-tab; do
    err="$dir/$name.err"
    one=0
    (
      local value=""
      IFS= read -r -d '' value <"$dir/$name" || true
      # shellcheck disable=SC2030 # each case sets MODE in its own subshell
      MODE=live
      M6_TASK_GIT_URL="$value"
      unset M6_TASK_GITHUB_API
      export MODE M6_TASK_GIT_URL
      EVIDENCE="$dir/$name.d"
      export EVIDENCE
      evidence_check_env
      assert_evidence
    ) >"$dir/$name.out" 2>"$err" || one=$?
    if [[ "$one" -eq 0 ]]; then
      printf 'spliced value was treated as public: %s\n' "$name" >&2
      rm -rf "$dir"
      return 1
    fi
    if ! grep -F 'planted secret' "$err" >/dev/null; then
      printf 'spliced value did not report planted secret: %s\n' "$name" >&2
      rm -rf "$dir"
      return 1
    fi
    if grep -E 'ghs_|ghp_|cccccccc' "$err" >/dev/null; then
      printf 'evidence check printed the planted value: %s\n' "$name" >&2
      rm -rf "$dir"
      return 1
    fi
  done
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
    token_probe "http://127.0.0.1:9/v1" "listed-model" >"$dir/out" 2>"$dir/err"
  )
  grep -F 'max_tokens' "$dir/args" >/dev/null
  if grep -F 'secret-key-value' "$dir/out" "$dir/err" >/dev/null; then
    echo "token probe printed the key" >&2
    return 1
  fi
  if ! grep -F 'headers Authorization' "$dir/err" >/dev/null; then
    echo "token probe did not name the Authorization header" >&2
    return 1
  fi
  rm -rf "$dir"
}

case_probe_redacts_401() {
  local dir="$ROOT/spike/m6/.work/probe-401-$$"
  mkdir -p "$dir"
  cat >"$dir/curl" <<'EOF'
#!/bin/bash
set -euo pipefail
printf '%s\n' "$@" >>"${CURL_ARGS_FILE:?}"
dest=""
prev=""
for arg in "$@"; do
  if [[ "$prev" == "-o" ]]; then
    dest="$arg"
  fi
  prev="$arg"
done
if [[ -n "$dest" ]]; then
  printf '%s\n' '{"error":"bad key secret-key-value extra sk-planted-extra-header-value litellm litellm-key-value-xyz"}' >"$dest"
fi
printf '401'
EOF
  chmod 755 "$dir/curl"
  : >"$dir/args"
  local catalog_code=0 token_code=0
  FACTORY_INFERENCE_OPENAI_API_KEY="secret-key-value" \
    M1_OPENAI_API_KEY="litellm-key-value-xyz" \
    FACTORY_INFERENCE_OPENAI_COMPATIBLE_EXTRA_HEADERS='x-litellm-api-key: Bearer sk-planted-extra-header-value' \
    CATALOG_CURL="$dir/curl" \
    CURL_ARGS_FILE="$dir/args" \
    catalog_probe "http://127.0.0.1:9/v1" "$dir/catalog.json" >"$dir/catalog.out" 2>"$dir/catalog.err" || catalog_code=$?
  FACTORY_INFERENCE_OPENAI_API_KEY="secret-key-value" \
    M1_OPENAI_API_KEY="litellm-key-value-xyz" \
    FACTORY_INFERENCE_OPENAI_COMPATIBLE_EXTRA_HEADERS='x-litellm-api-key: Bearer sk-planted-extra-header-value' \
    TOKEN_CURL="$dir/curl" \
    CURL_ARGS_FILE="$dir/args" \
    token_probe "http://127.0.0.1:9/v1" "listed-model" >"$dir/token.out" 2>"$dir/token.err" || token_code=$?
  printf '%s\n' "$catalog_code" >"$dir/catalog.code"
  printf '%s\n' "$token_code" >"$dir/token.code"
  python3 - "$dir" <<'PY'
import sys
root = sys.argv[1]
catalog_code = open(root + "/catalog.code", encoding="utf-8").read().strip()
token_code = open(root + "/token.code", encoding="utf-8").read().strip()
secrets = (
    "secret-key-value",
    "sk-planted-extra-header-value",
    "litellm-key-value-xyz",
    "Bearer sk-planted-extra-header-value",
)
files = ["catalog.out", "catalog.err", "token.out", "token.err"]
blob = "\n".join(open(root + "/" + name, encoding="utf-8").read() for name in files)
for secret in secrets:
    if secret in blob:
        sys.stderr.write("probe printed a secret\n")
        raise SystemExit(1)
catalog = open(root + "/catalog.err", encoding="utf-8").read()
token = open(root + "/token.err", encoding="utf-8").read()
for text, label in ((catalog, "catalog"), (token, "token")):
    if "status 401" not in text or "headers " not in text or "Authorization" not in text:
        sys.stderr.write("%s probe missed status or Authorization\n" % label)
        raise SystemExit(1)
    if "x-litellm-api-key" not in text or "[REDACTED]" not in text:
        sys.stderr.write("%s probe missed the extra header or the redaction\n" % label)
        raise SystemExit(1)
if "listed-model" not in token:
    sys.stderr.write("token probe dropped the model id\n")
    raise SystemExit(1)
if catalog_code == "0" or token_code == "0":
    sys.stderr.write("401 probe returned success\n")
    raise SystemExit(1)
PY
  rm -rf "$dir"
}

case_compose_config() {
  docker compose -f "$ROOT/deploy/docker-compose.yml" --profile m6 config >/dev/null
  docker compose -f "$ROOT/deploy/docker-compose.yml" -f "$ROOT/spike/m6/compose.yml" --profile m6 config >/dev/null
}

case_remote() {
  local name="$1"
  local mode="${2:-}"
  local work
  work="$(mktemp -d "$ROOT/spike/m6/.work/case-remote.XXXXXX")"
  if [[ -n "$mode" ]]; then
    M6_CASE_WORK="$work" python3 "$ROOT/spike/m6/remote_cases.py" "$name" "$mode"
  else
    M6_CASE_WORK="$work" python3 "$ROOT/spike/m6/remote_cases.py" "$name"
  fi
  rm -rf "$work"
}

case_shell_offline_noop() {
  local dir="$ROOT/spike/m6/.work/shell-off"
  local stub="$dir/bin"
  rm -rf "$dir"
  mkdir -p "$stub"
  cat >"$stub/git" <<'EOF'
#!/bin/bash
printf '%s\n' "$*" >> "$M6_ARGV_LOG"
exit 99
EOF
  cp "$stub/git" "$stub/curl"
  chmod 755 "$stub/git" "$stub/curl"
  (
    export PATH="$stub:$PATH"
    # shellcheck disable=SC2031 # this subshell sets its own MODE
    export MODE=offline
    export M6_ARGV_LOG="$dir/argv"
    export M6_DEFINITION_REF=should-clear
    export M6_RUN_BASE=should-clear
    preflight_remote
    seed_remote
    cleanup_remote
    [[ -z "${M6_DEFINITION_REF:-}" ]]
    [[ -z "${M6_RUN_BASE:-}" ]]
  )
  if [[ -s "$dir/argv" ]]; then
    printf 'offline shell called git or curl\n' >&2
    return 1
  fi
  rm -rf "$dir"
}

case_cleanup_remote_offline() {
  MODE=offline "$ROOT/spike/m6/run.sh" --cleanup-remote
}

case_compose_definition_ref() {
  local literal base_render unset_render set_render sha_render
  mkdir -p "$ROOT/spike/m6/.work"
  # The no-network CI job is a shallow checkout, so the base commit is
  # not available there. A copy with the two interpolations written back
  # to literal main is the offline render. A full clone also compares
  # that render to the base commit when the object exists.
  M6_COMPOSE_BACKUP="$ROOT/spike/m6/.work/compose.backup.yml"
  literal="$(mktemp)"
  base_render="$(mktemp)"
  unset_render="$(mktemp)"
  set_render="$(mktemp)"
  sha_render="$(mktemp)"
  cp "$ROOT/deploy/docker-compose.yml" "$M6_COMPOSE_BACKUP"
  # shellcheck disable=SC2329 # invoked by the RETURN trap
  restore_compose() {
    if [[ -f "${M6_COMPOSE_BACKUP:-}" ]]; then
      cp "$M6_COMPOSE_BACKUP" "$ROOT/deploy/docker-compose.yml"
    fi
  }
  trap restore_compose RETURN
  python3 - "$M6_COMPOSE_BACKUP" "$literal" <<'PY'
import sys

src, dest = sys.argv[1:]
text = open(src, encoding="utf-8").read()
needle = "${M6_DEFINITION_REF:-main}"
if text.count(needle) != 2:
    raise SystemExit("expected two m6 ref interpolations")
open(dest, "w", encoding="utf-8").write(text.replace(needle, "main"))
PY
  cp "$literal" "$ROOT/deploy/docker-compose.yml"
  if ! env -u M6_DEFINITION_REF docker compose \
    -f "$ROOT/deploy/docker-compose.yml" \
    -f "$ROOT/spike/m6/compose.yml" \
    --profile m6 config >"$base_render"; then
    printf 'literal main render failed\n' >&2
    return 1
  fi
  cp "$M6_COMPOSE_BACKUP" "$ROOT/deploy/docker-compose.yml"
  if ! env -u M6_DEFINITION_REF docker compose \
    -f "$ROOT/deploy/docker-compose.yml" \
    -f "$ROOT/spike/m6/compose.yml" \
    --profile m6 config >"$unset_render"; then
    printf 'unset M6_DEFINITION_REF render failed\n' >&2
    return 1
  fi
  if ! M6_DEFINITION_REF="m6-run/local-ab/main" docker compose \
    -f "$ROOT/deploy/docker-compose.yml" \
    -f "$ROOT/spike/m6/compose.yml" \
    --profile m6 config >"$set_render"; then
    printf 'set M6_DEFINITION_REF render failed\n' >&2
    return 1
  fi
  if ! cmp -s "$base_render" "$unset_render"; then
    printf 'unset M6_DEFINITION_REF render differs from literal main\n' >&2
    return 1
  fi
  if git -C "$ROOT" cat-file -e 317157cb7be4e6b5020002cdd1796979a303c585:deploy/docker-compose.yml 2>/dev/null; then
    cp "$literal" "$ROOT/deploy/docker-compose.yml"
    git -C "$ROOT" show 317157cb7be4e6b5020002cdd1796979a303c585:deploy/docker-compose.yml \
      >"$ROOT/deploy/docker-compose.yml"
    if ! env -u M6_DEFINITION_REF docker compose \
      -f "$ROOT/deploy/docker-compose.yml" \
      -f "$ROOT/spike/m6/compose.yml" \
      --profile m6 config >"$sha_render"; then
      printf 'base commit render failed\n' >&2
      return 1
    fi
    cp "$M6_COMPOSE_BACKUP" "$ROOT/deploy/docker-compose.yml"
    if ! cmp -s "$unset_render" "$sha_render"; then
      printf 'unset render differs from the base commit\n' >&2
      return 1
    fi
  fi
  if ! grep -F 'FACTORY_DEFINITION_GIT_REF: m6-run/local-ab/main' "$set_render" >/dev/null; then
    printf 'set ref did not render the run base\n' >&2
    return 1
  fi
  if ! grep -F 'FACTORY_DEFINITION_GIT_PRODUCTION_REF: m6-run/local-ab/main' "$set_render" >/dev/null; then
    printf 'set ref did not render the production ref\n' >&2
    return 1
  fi
  trap - RETURN
  restore_compose
  rm -f "$literal" "$base_render" "$unset_render" "$set_render" "$sha_render" "$M6_COMPOSE_BACKUP"
  unset M6_COMPOSE_BACKUP
}

expect_fail missing-live-url "missing FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL" case_missing_live_url
expect_fail missing-live-model "missing M6_LIVE_MODEL" case_missing_live_model
expect_fail missing-live-definition "missing M6_DEFINITION_GIT_URL" case_missing_live_definition
expect_fail missing-live-api "missing M6_GITHUB_API_BASE" case_missing_live_api
expect_fail missing-live-app "missing FACTORY_GITHUB_APP_ID" case_missing_live_app
expect_fail missing-live-key "missing FACTORY_GITHUB_APP_PRIVATE_KEY_FILE" case_missing_live_key
expect_fail missing-ci-url "missing M1_OPENAI_BASE_URL" case_missing_ci_url
expect_fail missing-ci-key "missing M1_OPENAI_API_KEY" case_missing_ci_key
expect_fail missing-ci-model "missing M6_LIVE_MODEL" case_missing_ci_model
expect_fail missing-ci-definition "missing M6_DEFINITION_GIT_URL" case_missing_ci_definition
expect_fail missing-ci-api "missing M6_GITHUB_API_BASE" case_missing_ci_api
expect_fail missing-ci-app "missing FACTORY_GITHUB_APP_ID" case_missing_ci_app
expect_fail missing-ci-keyfile "missing FACTORY_GITHUB_APP_PRIVATE_KEY_FILE" case_missing_ci_keyfile
run_case live-app-key-mount case_app_key_mount live
run_case live-ci-app-key-mount case_app_key_mount live-ci
run_case live-targets case_live_targets
run_case offline-targets case_offline_targets_ignore_operator
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
run_case evidence-public-definition-url case_evidence_public_definition_url
expect_fail evidence-public-url-keeps-planted-secret "planted secret" case_evidence_public_url_keeps_planted_secret
expect_fail evidence-credentialed-url "planted secret" case_evidence_credentialed_url
expect_fail evidence-query-url "planted secret" case_evidence_query_url
expect_fail evidence-offline-task-url "planted secret" case_evidence_offline_task_url
run_case evidence-control-splice case_evidence_control_splice
run_case token-probe case_token_probe_shape
run_case probe-401 case_probe_redacts_401
run_case compose-config case_compose_config
run_case shell-offline-noop case_shell_offline_noop
expect_fail cleanup-remote-offline "cleanup-remote is for live" case_cleanup_remote_offline
run_case compose-definition-ref case_compose_definition_ref
for remote_mode in live live-ci; do
  run_case "opaque-token-${remote_mode}" case_remote opaque-token "$remote_mode"
  run_case "preflight-all-${remote_mode}" case_remote preflight-all "$remote_mode"
  run_case "url-shape-${remote_mode}" case_remote url-shape "$remote_mode"
  run_case "ls-remote-${remote_mode}" case_remote ls-remote "$remote_mode"
  run_case "mint-ok-${remote_mode}" case_remote mint-ok "$remote_mode"
  run_case "mint-deny-${remote_mode}" case_remote mint-deny "$remote_mode"
  run_case "collaborators-${remote_mode}" case_remote collaborators "$remote_mode"
  run_case "seed-move-${remote_mode}" case_remote seed-move "$remote_mode"
  run_case "watch-stale-${remote_mode}" case_remote watch-stale "$remote_mode"
  run_case "cleanup-${remote_mode}" case_remote cleanup "$remote_mode"
  run_case "allow-${remote_mode}" case_remote allow "$remote_mode"
  run_case "token-argv-${remote_mode}" case_remote token-argv "$remote_mode"
  run_case "close-guard-${remote_mode}" case_remote close-guard "$remote_mode"
  run_case "watch-mismatch-${remote_mode}" case_remote watch-mismatch "$remote_mode"
  run_case "ledger-run-${remote_mode}" case_remote ledger-run "$remote_mode"
  run_case "ledger-corrupt-${remote_mode}" case_remote ledger-corrupt "$remote_mode"
  run_case "cleanup-bound-git-${remote_mode}" case_remote cleanup-bound-git "$remote_mode"
  run_case "cleanup-bound-mint-${remote_mode}" case_remote cleanup-bound-mint "$remote_mode"
done
run_case offline-remote case_remote offline
run_case forge-lookup case_remote forge-lookup

# shellcheck source=spike/m6/run_test_t76b.sh
. "$ROOT/spike/m6/run_test_t76b.sh"

# expect_fail calls the case from an if, which suppresses set -e in this
# process and in subshells it starts. A new bash is the run.sh context.
m6_fresh() {
  local body="$1"
  shift
  # shellcheck disable=SC2016 # $1 and $2 are the child bash's arguments
  env "$@" bash -c '
    set -euo pipefail
    source "$1"
    eval "$2"
  ' bash "$ROOT/spike/m6/run.sh" "$body"
}

# Each set -e step prints M6 FAIL — <step>: exit <n> and returns non-zero.
case_step_build_images() {
  local dir="$ROOT/spike/m6/.work/step-build"
  rm -rf "$dir"
  mkdir -p "$dir/bin"
  cat >"$dir/bin/docker" <<'EOF'
#!/bin/bash
if [[ "${1:-}" == build ]]; then
  exit 7
fi
exit 0
EOF
  chmod 755 "$dir/bin/docker"
  body=$(
    cat <<'EOF'
export PATH="${M6_STUB_BIN}:${PATH}"
m6_arm_err
m6_step=build_images
spike_run_init m6
spike_fg build_images
EOF
  )
  m6_fresh "$body" M6_STUB_BIN="$dir/bin" OCI_PLATFORM=linux/amd64 GOARCH_TARGET=amd64 SPIKE_REG="$dir/spike-run"
}

case_step_prepare_definition() {
  local dir="$ROOT/spike/m6/.work/step-prepare"
  rm -rf "$dir"
  mkdir -p "$dir"
  : >"$dir/notdir"
  body=$(
    cat <<'EOF'
m6_arm_err
m6_step=prepare_definition
WORK="$M6_CASE_WORK"
prepare_definition
EOF
  )
  m6_fresh "$body" M6_CASE_WORK="$dir/notdir"
}

case_step_seed_git() {
  local dir="$ROOT/spike/m6/.work/step-seed"
  rm -rf "$dir"
  mkdir -p "$dir"
  body=$(
    cat <<'EOF'
m6_arm_err
m6_step=seed_git
WORK="$M6_CASE_WORK"
seed_git
EOF
  )
  m6_fresh "$body" M6_CASE_WORK="$dir"
}

case_step_start_git() {
  local dir="$ROOT/spike/m6/.work/step-start-git"
  rm -rf "$dir"
  mkdir -p "$dir/bin"
  cat >"$dir/bin/openssl" <<'EOF'
#!/bin/bash
exit 3
EOF
  chmod 755 "$dir/bin/openssl"
  body=$(
    cat <<'EOF'
export PATH="${M6_STUB_BIN}:${PATH}"
m6_arm_err
m6_step=start_git
WORK="$M6_CASE_WORK"
GIT_PORT=9
unset SPIKE_REG
start_git
EOF
  )
  m6_fresh "$body" M6_STUB_BIN="$dir/bin" M6_CASE_WORK="$dir"
}

case_step_start_forge() {
  local dir="$ROOT/spike/m6/.work/step-start-forge"
  rm -rf "$dir"
  mkdir -p "$dir/bin"
  cat >"$dir/bin/python3" <<'EOF'
#!/bin/bash
exit 9
EOF
  chmod 755 "$dir/bin/python3"
  body=$(
    cat <<'EOF'
export PATH="${M6_STUB_BIN}:${PATH}"
m6_arm_err
m6_step=start_forge
start_forge
EOF
  )
  m6_fresh "$body" M6_STUB_BIN="$dir/bin"
}

case_step_start_router() {
  local dir="$ROOT/spike/m6/.work/step-start-router"
  rm -rf "$dir"
  mkdir -p "$dir/bin"
  cat >"$dir/bin/openssl" <<'EOF'
#!/bin/bash
exit 3
EOF
  chmod 755 "$dir/bin/openssl"
  body=$(
    cat <<'EOF'
export PATH="${M6_STUB_BIN}:${PATH}"
m6_arm_err
m6_step=start_router
WORK="$M6_CASE_WORK"
ROUTER_PORT=9
M6_TASK_GIT_URL="git://example.invalid/m6/factory.git"
M6_TASK_GITHUB_API="http://example.invalid"
unset SPIKE_REG
start_router
EOF
  )
  m6_fresh "$body" M6_STUB_BIN="$dir/bin" M6_CASE_WORK="$dir"
}

case_die_message() {
  local err="$ROOT/spike/m6/.work/die-message.err"
  mkdir -p "$ROOT/spike/m6/.work"
  if (
    m6_arm_err
    m6_step=should-not-appear
    die "live preflight failed"
  ) >"$ROOT/spike/m6/.work/die-message.out" 2>"$err"; then
    printf 'die returned 0\n' >&2
    return 1
  fi
  [[ "$(cat "$err")" == "M6 FAIL — live preflight failed" ]]
}

case_forge_json_atomic() {
  local dir="$ROOT/spike/m6/.work/forge-atomic"
  local dest="$dir/forge.json"
  local writer=""
  rm -rf "$dir"
  mkdir -p "$dir"
  printf '%s\n' '{"n":0}' >"$dest"
  # shellcheck disable=SC2317,SC2329 # RETURN trap reap
  stop_writer() {
    if [[ -n "${writer:-}" ]]; then
      kill "$writer" 2>/dev/null || true
      wait "$writer" 2>/dev/null || true
      writer=""
    fi
  }
  trap stop_writer RETURN
  (
    i=0
    while true; do
      i=$((i + 1))
      m6_publish_stdout "$dest" printf '%s\n' "{\"n\":${i}}"
      if [[ "$i" -ge 200 && -f "$dir/reader.done" ]]; then
        printf '%s\n' "$i" >"$dir/writes"
        break
      fi
    done
  ) &
  writer=$!
  i=0
  while [[ "$i" -lt 200 ]]; do
    i=$((i + 1))
    python3 -c 'import json,sys;json.load(open(sys.argv[1]))' "$dest" || return 1
  done
  touch "$dir/reader.done"
  wait "$writer" || return 1
  writer=""
  [[ "$(cat "$dir/writes")" -ge 200 ]] || return 1
  if m6_publish_stdout "$dest" false; then
    printf 'failed publish returned 0\n' >&2
    return 1
  fi
  python3 -c 'import json,sys;json.load(open(sys.argv[1]))' "$dest" || return 1
  [[ ! -e "${dest}.tmp" ]] || return 1
  # shellcheck disable=SC2016 # literal writer call, not an expansion
  forge_needle='m6_publish_stdout "$EVIDENCE/forge.json"'
  # shellcheck disable=SC2016 # literal writer call, not an expansion
  skill_needle='m6_publish_stdout "$EVIDENCE/skill.diff"'
  grep -F "$forge_needle" "$ROOT/spike/m6/run.sh" >/dev/null || return 1
  grep -F "$skill_needle" "$ROOT/spike/m6/run.sh" >/dev/null || return 1
  trap - RETURN
  rm -rf "$dir"
}

# set +e around spike_fg must not print an ERR line. Playwright's status
# still reaches die() after save_logs.
case_playwright_set_plus_e() {
  local dir="$ROOT/spike/m6/.work/playwright-set-plus-e"
  local err="$dir/stderr" out="$dir/stdout"
  rm -rf "$dir"
  mkdir -p "$dir"
  body=$(
    cat <<'EOF'
m6_arm_err
m6_step=playwright
WORK="$M6_CASE_WORK"
EVIDENCE="$WORK/evidence"
SIGNAL="$WORK/signal"
MODE=offline
export WORK EVIDENCE SIGNAL MODE
mkdir -p "$EVIDENCE" "$SIGNAL"
spike_run_init m6
watch_candidate() {
  while [[ ! -f "$SIGNAL/stop" ]]; do
    sleep 0.05
  done
}
run_playwright() {
  exit 3
}
compose() {
  printf '1\n' >>"$M6_SAVE_MARK"
  printf '%s\n' "$PINNED_SHA"
}
m6_playwright_and_evidence
EOF
  )
  m6_fresh "$body" \
    M6_CASE_WORK="$dir" \
    M6_SAVE_MARK="$dir/save-logs-called" \
    SPIKE_REG="$dir/spike-run" \
    >"$out" 2>"$err"
  local rc=$?
  [[ "$rc" -ne 0 ]] || return 1
  [[ "$(cat "$err")" == "M6 FAIL — playwright exit 3" ]] || return 1
  [[ -s "$dir/save-logs-called" ]] || return 1
  [[ -s "$dir/evidence/compose.log" ]] || return 1
  grep -F "$PINNED_SHA" "$dir/evidence/compose.log" >/dev/null || return 1
  rm -rf "$dir"
}

# Playwright's own exit and excerpt win when the pinned SHA is also absent.
case_playwright_failure_before_sha() {
  local dir="$ROOT/spike/m6/.work/playwright-before-sha"
  local err="$dir/stderr" out="$dir/stdout"
  rm -rf "$dir"
  mkdir -p "$dir"
  body=$(
    cat <<'EOF'
m6_arm_err
m6_step=playwright
WORK="$M6_CASE_WORK"
EVIDENCE="$WORK/evidence"
SIGNAL="$WORK/signal"
MODE=offline
export WORK EVIDENCE SIGNAL MODE
mkdir -p "$EVIDENCE" "$SIGNAL"
spike_run_init m6
watch_candidate() {
  while [[ ! -f "$SIGNAL/stop" ]]; do
    sleep 0.05
  done
}
run_playwright() {
  printf '  1) loop.spec.ts:188 › m6 exit loop\n'
  printf '    Error: timed out waiting for the page\n'
  exit 3
}
compose() {
  printf '1\n' >>"$M6_SAVE_MARK"
  printf '%s\n' 'logs without the pinned sha'
}
m6_playwright_and_evidence
EOF
  )
  m6_fresh "$body" \
    M6_CASE_WORK="$dir" \
    M6_SAVE_MARK="$dir/save-logs-called" \
    SPIKE_REG="$dir/spike-run" \
    >"$out" 2>"$err"
  local rc=$?
  [[ "$rc" -ne 0 ]] || return 1
  grep -F 'M6 FAIL — playwright exit 3' "$err" >/dev/null || return 1
  grep -F '== Playwright failures' "$err" >/dev/null || return 1
  grep -F 'Error: timed out waiting for the page' "$err" >/dev/null || return 1
  if grep -F 'pinned startingRepoRefs' "$err" >/dev/null; then
    printf 'pinned SHA check masked the playwright failure\n' >&2
    return 1
  fi
  [[ -s "$dir/save-logs-called" ]] || return 1
  if grep -F "$PINNED_SHA" "$dir/evidence/compose.log" >/dev/null; then
    printf 'compose log unexpectedly had the pinned sha\n' >&2
    return 1
  fi
  rm -rf "$dir"
}

case_redact_evidence() {
  python3 -m unittest discover -s "$ROOT/spike/m6" -p 'redact_evidence_test.py' -v
}

# A failed Playwright run still scrubs evidence before the process exits.
case_playwright_failure_redacts() {
  local dir="$ROOT/spike/m6/.work/playwright-redact"
  local err="$dir/stderr" out="$dir/stdout"
  local secret="c0ffeefakeconsolekey0123456789abcdef"
  local jwt="eyJhbGciOiJub25lIn0.eyJzdWIiOiJmYWtlIn0.ZmFrZXNpZ25hdHVyZQ"
  rm -rf "$dir"
  mkdir -p "$dir/evidence"
  printf 'Authorization: Bearer %s\n' "$secret" >"$dir/evidence/driver.log"
  python3 - "$dir/evidence/loop-trace.zip" "$jwt" <<'PY'
import sys, zipfile
with zipfile.ZipFile(sys.argv[1], "w") as zout:
    zout.writestr("trace.network", '{"name":"authorization","value":"Bearer ' + sys.argv[2] + '"}\n')
PY
  body=$(
    cat <<'EOF'
m6_arm_err
m6_step=playwright
WORK="$M6_CASE_WORK"
EVIDENCE="$WORK/evidence"
SIGNAL="$WORK/signal"
MODE=offline
export WORK EVIDENCE SIGNAL MODE
mkdir -p "$EVIDENCE" "$SIGNAL"
spike_run_init m6
watch_candidate() {
  while [[ ! -f "$SIGNAL/stop" ]]; do
    sleep 0.05
  done
}
run_playwright() {
  exit 3
}
compose() {
  printf '1\n' >>"$M6_SAVE_MARK"
  printf '%s\n' "$PINNED_SHA"
}
m6_playwright_and_evidence
EOF
  )
  set +e
  m6_fresh "$body" \
    M6_CASE_WORK="$dir" \
    M6_SAVE_MARK="$dir/save-logs-called" \
    SPIKE_REG="$dir/spike-run" \
    FACTORY_API_KEY="$secret" \
    >"$out" 2>"$err"
  local rc=$?
  set -e
  [[ "$rc" -ne 0 ]] || return 1
  grep -F 'M6 FAIL — playwright exit 3' "$err" >/dev/null || return 1
  [[ -f "$dir/evidence/driver.log" ]] || return 1
  if grep -F "$secret" "$dir/evidence/driver.log" >/dev/null; then
    printf 'console key survived driver.log\n' >&2
    return 1
  fi
  if grep -F "$secret" "$err" >/dev/null || grep -F "$jwt" "$err" >/dev/null; then
    printf 'redaction printed a secret\n' >&2
    return 1
  fi
  python3 - "$dir/evidence" "$jwt" "$secret" <<'PY'
import sys, zipfile
from pathlib import Path
root = Path(sys.argv[1])
jwt, secret = sys.argv[2], sys.argv[3]
blob = (root / "driver.log").read_text(encoding="utf-8")
if "[REDACTED]" not in blob or secret in blob:
    raise SystemExit("driver.log was not scrubbed")
with zipfile.ZipFile(root / "loop-trace.zip") as zin:
    text = zin.read("trace.network").decode("utf-8")
if jwt in text or secret in text or "[REDACTED]" not in text:
    raise SystemExit("trace was not scrubbed")
PY
  rm -rf "$dir"
}

# Mirrors the "Upload M6 evidence" step in .github/workflows/m6-live.yml.
# A stateless installation token that is still in the tree must fail the
# step before it can write ready=true. The redact path may write ready=true
# only after that token's payload is gone.
case_stateless_publish_gate() {
  local dir="$ROOT/spike/m6/.work/stateless-publish"
  local err="$dir/stderr" out="$dir/stdout" output="$dir/github_output"
  rm -rf "$dir"
  mkdir -p "$dir/scan/evidence" "$dir/full/evidence"
  python3 - "$dir" "$ROOT/spike/m6" <<'PY'
import sys
from pathlib import Path

sys.path.insert(0, sys.argv[2])
import redact_evidence_test as tests

base = Path(sys.argv[1])
tests.plant_stateless_tree(base / "scan" / "evidence")
tests.plant_stateless_tree(base / "full" / "evidence")
(base / "full" / "github-app-token").write_text(tests.stateless_token() + "\n", encoding="utf-8")
PY
  # A fresh bash, not this function. run_case invokes the function from
  # if, which turns errexit off for the whole call, including subshells.
  # GitHub Actions runs the step as its own shell, where set -e sticks.
  set +e
  bash -c '
set -euo pipefail
export GITHUB_OUTPUT="$3"
python3 "$1" --scan-only "$2"
if [[ -d "$2" ]]; then
  echo "ready=true" >> "$GITHUB_OUTPUT"
else
  echo "ready=false" >> "$GITHUB_OUTPUT"
fi
' bash "$ROOT/spike/m6/redact_evidence.py" "$dir/scan/evidence" "$output" >"$out" 2>"$err"
  local rc=$?
  set -e
  [[ "$rc" -ne 0 ]] || {
    printf 'scan gate exited 0\n' >&2
    return 1
  }
  if [[ -f "$output" ]] && grep -q 'ready=true' "$output"; then
    printf 'scan gate wrote ready=true\n' >&2
    return 1
  fi
  if [[ -d "$dir/scan/evidence" ]]; then
    printf 'scan gate left the evidence directory\n' >&2
    return 1
  fi
  python3 - "$err" "$ROOT/spike/m6" <<'PY'
import sys
from pathlib import Path

sys.path.insert(0, sys.argv[2])
import redact_evidence_test as tests

text = Path(sys.argv[1]).read_text(encoding="utf-8")
if tests.leaked_stateless(text):
    raise SystemExit("scan gate printed the token")
PY
  output="$dir/full_output"
  err="$dir/full_stderr"
  set +e
  bash -c '
set -euo pipefail
export GITHUB_OUTPUT="$4"
work="$3"
if [[ -f "$work/github-app-token" ]]; then
  M6_APP_TOKEN="$(tr -d "[:space:]" < "$work/github-app-token")"
  export M6_APP_TOKEN
fi
if [[ -f "$work/install-token" ]]; then
  INSTALL_TOKEN="$(tr -d "[:space:]" < "$work/install-token")"
  export INSTALL_TOKEN
fi
export EVIDENCE_EXTRA_SECRETS="INSTALL_TOKEN M6_APP_TOKEN"
python3 "$1" --key-file "$work/missing-api-key" "$2"
if [[ -d "$2" ]]; then
  echo "ready=true" >> "$GITHUB_OUTPUT"
else
  echo "ready=false" >> "$GITHUB_OUTPUT"
fi
' bash "$ROOT/spike/m6/redact_evidence.py" "$dir/full/evidence" "$dir/full" "$output" >"$dir/full_stdout" 2>"$err"
  rc=$?
  set -e
  [[ "$rc" -eq 0 ]] || {
    printf 'redact step exited %s\n' "$rc" >&2
    return 1
  }
  grep -q 'ready=true' "$output" || {
    printf 'redact step did not write ready=true\n' >&2
    return 1
  }
  python3 - "$dir/full/evidence" "$err" "$ROOT/spike/m6" <<'PY'
import sys
from pathlib import Path

sys.path.insert(0, sys.argv[3])
import redact_evidence_test as tests

root = Path(sys.argv[1])
if tests.leaked_stateless(tests.evidence_text(root)):
    raise SystemExit("redacted evidence still holds the installation token")
err = Path(sys.argv[2]).read_text(encoding="utf-8")
if tests.leaked_stateless(err):
    raise SystemExit("redact step printed the token")
PY
  rm -rf "$dir"
}

expect_fail step-build-images "M6 FAIL — build_images: exit 7" case_step_build_images
expect_fail step-prepare-definition "M6 FAIL — prepare_definition: exit 1" case_step_prepare_definition
expect_fail step-seed-git "M6 FAIL — seed_git: exit 1" case_step_seed_git
expect_fail step-start-git "M6 FAIL — start_git: exit 3" case_step_start_git
expect_fail step-start-forge "M6 FAIL — start_forge: exit 9" case_step_start_forge
expect_fail step-start-router "M6 FAIL — start_router: exit 3" case_step_start_router
run_case die-message case_die_message
run_case forge-json-atomic case_forge_json_atomic
run_case playwright-set-plus-e case_playwright_set_plus_e
run_case playwright-failure-before-sha case_playwright_failure_before_sha
run_case redact-evidence case_redact_evidence
run_case playwright-failure-redacts case_playwright_failure_redacts
run_case stateless-publish-gate case_stateless_publish_gate

if [[ "$fails" -ne 0 ]]; then
  printf '%s failed\n' "$fails" >&2
  exit 1
fi
printf 'spike/m6/run_test.sh ok\n'
