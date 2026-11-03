#!/usr/bin/env python3
"""Stateless offline model router for the M6 exit loop.

Each request is answered from its own messages. A process-wide counter
is the only memory, and it only changes judge reasoning so a rescore
does not repeat the previous text. The router does not log prompts or
tokens.

The improver pushes the branch and stops. Factoryd opens the pull
request. Set M6_IMPROVER_OPENS_PR=1 to force the live-only branch
where the improver opens the request first.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
import threading
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

CONVENTION = "CONVENTION: cite-sources"
CONTRADICTION_LINE = (
    "Do not invent a required final line that this skill does not contain."
)
INSTRUCTION = (
    "Include the following line exactly, on its own line, in the final message:"
)
RULE_PARTS = (
    "explicit, imperative instruction",
    "Remove or rewrite any existing instruction",
    "contradicts",
    "bare appended marker line",
)
BRANCH_RE = re.compile(r"branch: (improve/[A-Za-z0-9_-]+)")
UNSAFE_URL = re.compile(r"[\s'\"$`\\]")
RUN_ID_RE = re.compile(r"^[a-z0-9-]{1,80}$")
REF_PREFIX_RE = re.compile(r"^[a-z0-9-]{1,40}$")
REF_RE = re.compile(r"^[a-z0-9][a-z0-9./-]{0,160}$")
MODELS = ("m6-agent", "m6-judge")
LOCAL_MODELS = {"m6-agent", "m6-judge", "m7-agent", "m7-hold"}
HOP_HEADERS = {
    "host",
    "content-length",
    "connection",
    "transfer-encoding",
    "keep-alive",
    "te",
    "trailers",
    "upgrade",
}

_reason_lock = threading.Lock()
_reason_n = 0


def next_reasoning() -> str:
    global _reason_n
    with _reason_lock:
        _reason_n += 1
        return f"r{_reason_n}"


def message_text(message: dict) -> str:
    content = message.get("content")
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        parts: list[str] = []
        for item in content:
            if isinstance(item, dict) and isinstance(item.get("text"), str):
                parts.append(item["text"])
        return "\n".join(parts)
    return ""


def blob(body: dict) -> str:
    parts: list[str] = []
    for message in body.get("messages") or []:
        if isinstance(message, dict):
            parts.append(message_text(message))
    return "\n".join(parts)


def system_text(body: dict) -> str:
    parts: list[str] = []
    for message in body.get("messages") or []:
        if isinstance(message, dict) and message.get("role") == "system":
            parts.append(message_text(message))
    return "\n".join(parts)


def section(text: str, title: str) -> str:
    marker = f"## {title}\n"
    start = text.find(marker)
    if start < 0:
        return ""
    rest = text[start + len(marker) :]
    nxt = rest.find("\n## ")
    if nxt >= 0:
        rest = rest[:nxt]
    return rest


def completion(text: str, tool: dict | None) -> dict:
    message: dict = {"role": "assistant"}
    finish = "stop"
    if tool is None:
        message["content"] = text
    else:
        message["tool_calls"] = [tool]
        finish = "tool_calls"
    return {
        "id": "chatcmpl-m6",
        "object": "chat.completion",
        "choices": [{"index": 0, "message": message, "finish_reason": finish}],
    }


def bash_call(command: str) -> dict:
    return completion(
        "",
        {
            "id": "call_m6",
            "type": "function",
            "function": {
                "name": "bash",
                "arguments": json.dumps({"command": command}),
            },
        },
    )


def shell_url(value: str) -> str:
    text = value.strip()
    if text == "" or UNSAFE_URL.search(text):
        return ""
    return text


def factoryd_prompt(text: str) -> bool:
    """A factoryd improvement prompt carries the definition-tree heading.

    The driver unit test sends a short prompt that only names the branch.
    That path stays on the old steps so those checks keep passing.
    """
    return "## Definition tree" in text or "# Instructions" in text


def improve_instructions(text: str) -> bool:
    return "DEFINITION_GIT_URL" in text and BRANCH_RE.search(text) is not None


def contradiction_rule(text: str) -> bool:
    return all(part in text for part in RULE_PARTS)


def failing_evidence(text: str) -> bool:
    """The prompt quotes a scorer rubric and at least one assistant tail."""
    rubrics = section(text, "Scorer rubrics")
    examples = section(text, "Failing examples")
    if "scorer " not in rubrics or "rubric:" not in rubrics:
        return False
    body = rubrics.split("rubric:", 1)[1].strip()
    if body == "" or body == "(none)":
        return False
    if "reasoning:" not in examples:
        return False
    marker = "assistant output:"
    for line in examples.splitlines():
        at = line.find(marker)
        if at < 0:
            continue
        tail = line[at + len(marker) :].strip()
        if tail and tail != "(none)":
            return True
    return False


def grounded(text: str) -> bool:
    return contradiction_rule(text) and failing_evidence(text)


def failed_by_agent(summary: str) -> dict:
    return completion(
        json.dumps({"status": "failed_by_agent", "summary": summary}),
        None,
    )


def shell_quote(value: str) -> str:
    return "'" + value.replace("'", "'\"'\"'") + "'"


def skill_edit(branch: str) -> str:
    """Rewrite the skill. Deleting the contradicting line is the point:
    appending a marker beside it leaves the agent on the old instruction.
    """
    path = "skills/cite-sources/SKILL.md"
    return (
        f"cd /workspace && git checkout -b {branch} && "
        f"grep -v -F -x {shell_quote(CONTRADICTION_LINE)} {path} > /tmp/m6-skill.md && "
        f"mv /tmp/m6-skill.md {path} && "
        "printf '%s\\n' '' "
        f"{shell_quote(INSTRUCTION)} '' {shell_quote(CONVENTION)} >> {path} && "
        "echo M6_EDITED"
    )


def improve(body: dict) -> dict:
    text = blob(body)
    if factoryd_prompt(text) and not improve_instructions(text):
        return failed_by_agent(
            "improvement prompt is missing DEFINITION_GIT_URL or the improve branch"
        )
    if factoryd_prompt(text) and not grounded(text):
        return failed_by_agent(
            "improvement prompt is missing failing evidence or the contradiction rule"
        )
    if "M6_PR_HTTP" in text:
        return completion("Improvement branch is pushed.", None)
    branch = ""
    match = BRANCH_RE.search(text)
    if match:
        branch = match.group(1)
    git_url = shell_url(os.environ.get("M6_DEFINITION_GIT_URL", ""))
    api = shell_url(os.environ.get("M6_GITHUB_API_BASE", "")).rstrip("/")
    if branch == "" or git_url == "" or api == "":
        return completion("missing improve target", None)
    if "M6_PUSHED" in text:
        if os.environ.get("M6_IMPROVER_OPENS_PR") != "1":
            return completion("Improvement branch is pushed.", None)
        payload = json.dumps(
            {
                "title": "m6 improver",
                "head": branch,
                "base": "main",
                "body": "Opened by the offline improver.",
            }
        )
        command = (
            "code=$(curl -sS -o /tmp/pr.json -w '%{http_code}' "
            "--connect-timeout 2 -m 15 -X POST "
            f"'{api}/repos/m6/factory/pulls' "
            '-H "Authorization: Bearer $GH_TOKEN" '
            "-H 'Content-Type: application/json' "
            f"-d '{payload}') && echo \"M6_PR_HTTP $code\""
        )
        return bash_call(command)
    if "M6_EDITED" in text:
        command = (
            "cd /workspace && git add skills/cite-sources/SKILL.md && "
            "git commit -m 'cite sources' && "
            f"git push origin HEAD:refs/heads/{branch} && echo M6_PUSHED"
        )
        return bash_call(command)
    if "M6_CLONED" in text:
        return bash_call(skill_edit(branch))
    # /workspace is the task directory and starts empty. Clone into that
    # exact path with full history: a shallow or single-branch clone cannot
    # check out the live base m6-run/<id>/main.
    command = (
        "cd /tmp && git config --global --add safe.directory /workspace && "
        "GIT_TERMINAL_PROMPT=0 "
        f"git clone '{git_url}' /workspace && echo M6_CLONED"
    )
    return bash_call(command)


def judge(body: dict) -> dict:
    text = blob(body)
    transcript = section(text, "Transcript")
    work = section(text, "Work item")
    cited = CONVENTION in transcript
    if 'You are the scorer "cite-sources"' in text:
        if "m6-bad-label" in work or "m6-bad-label" in transcript:
            label = "not-a-label"
        elif cited:
            label = "followed"
        else:
            label = "missed"
    elif cited:
        label = "pass"
    else:
        label = "fail"
    payload = json.dumps({"label": label, "reasoning": next_reasoning()})
    return completion(payload, None)


def ask_call(question: str) -> dict:
    return completion(
        "",
        {
            "id": "call_m6_ask",
            "type": "function",
            "function": {
                "name": "ask_input",
                "arguments": json.dumps({"text": question}),
            },
        },
    )


def agent(body: dict) -> dict:
    # m6-ask-input is the offline stub for a work item that parks. The
    # loop spec's concrete prompts do not contain it.
    if "m6-ask-input" in blob(body):
        return ask_call("Which one-sentence summary should this item use?")
    if CONVENTION in system_text(body):
        return completion(f"Done.\n{CONVENTION}", None)
    return completion("Done.", None)


def m7_target() -> tuple[str, str]:
    git_url = shell_url(os.environ.get("M7_DEFINITION_GIT_URL", ""))
    api = shell_url(os.environ.get("M7_GITHUB_API_BASE", "")).rstrip("/")
    return git_url, api


def m7_refs() -> tuple[str, str] | None:
    """Run-scoped head and base. Unset or invalid ids do not fall back to main."""
    run = os.environ.get("M7_RUN_ID", "").strip()
    prefix = os.environ.get("SPIKE_RUN_REF_PREFIX", "m7-run").strip()
    if RUN_ID_RE.fullmatch(run) is None or REF_PREFIX_RE.fullmatch(prefix) is None:
        return None
    head = f"{prefix}/{run}/change"
    base = f"{prefix}/{run}/main"
    if REF_RE.fullmatch(head) is None or REF_RE.fullmatch(base) is None:
        return None
    return head, base


def m7_hold_turn(body: dict) -> dict:
    """Sleep until cancel. The command is fixed; the model is not asked."""
    del body
    return bash_call("sleep 600 && echo M7_HOLDING")


def upstream_base() -> str:
    return os.environ.get("M7_UPSTREAM_BASE", "").strip().rstrip("/")


def forward_completion(raw: bytes, headers: object) -> tuple[int, bytes, str]:
    """Forward one completion. Header values are not logged."""
    base = upstream_base()
    if base == "":
        return 400, b'{"error":{"message":"unknown model"}}\n', "application/json"
    req = urllib.request.Request(base + "/chat/completions", data=raw, method="POST")
    items = getattr(headers, "items", None)
    if callable(items):
        for key, value in items():
            if str(key).lower() in HOP_HEADERS:
                continue
            req.add_header(str(key), str(value))
    try:
        with urllib.request.urlopen(req, timeout=90) as res:
            ctype = res.headers.get("Content-Type", "application/json")
            return res.status, res.read(), ctype.split(";", 1)[0].strip()
    except urllib.error.HTTPError as err:
        ctype = "application/json"
        if err.headers is not None:
            ctype = err.headers.get("Content-Type", ctype).split(";", 1)[0].strip()
        return err.code, err.read(), ctype or "application/json"
    except urllib.error.URLError:
        return (
            502,
            b'{"error":{"message":"upstream unreachable"}}\n',
            "application/json",
        )


def dispatch(body: dict, raw: bytes, headers: object) -> tuple[int, bytes, str]:
    model = body.get("model")
    if model in LOCAL_MODELS or upstream_base() == "":
        status, payload = answer(body)
        return status, json.dumps(payload).encode(), "application/json"
    return forward_completion(raw, headers)


def m7_turn(body: dict) -> dict:
    """Offline m7-agent. The hold model is m7-hold and never reaches here.

    m6-agent and m6-judge are unchanged. m7-agent is not listed in MODELS.
    Pull requests use m7-run/<id>/change against m7-run/<id>/main.

    The run-scoped base ref has to be in the prompt. A direct run that
    never received the implementer instructions does not get a scripted
    clone or pull request.
    """
    text = blob(body)
    if "m7-hold" in text and "M7_HOLDING" not in text:
        return m7_hold_turn(body)
    if "m7-hold" in text:
        return completion("holding", None)
    refs = m7_refs()
    if refs is not None and refs[1] not in text:
        return completion("missing implementer instructions", None)
    if "M7_PR_HTTP" in text:
        return completion("pull request opened", None)
    git_url, api = m7_target()
    if "M7_PUSHED" in text:
        if git_url == "" or api == "" or refs is None:
            return completion("missing m7 target", None)
        head, base = refs
        payload = json.dumps(
            {
                "title": "m7",
                "head": head,
                "base": base,
                "body": "m7 offline",
            }
        )
        command = (
            "code=$(curl -sS -o /tmp/pr.json -w '%{http_code}' "
            "--connect-timeout 2 -m 15 -X POST "
            f"'{api}/repos/polds/factory-m7-target/pulls' "
            '-H "Authorization: Bearer $GH_TOKEN" '
            "-H 'Content-Type: application/json' "
            f"-d '{payload}') && "
            """awk -F'"' '{for(i=1;i<=NF;i++) if($i=="html_url") print $(i+2)}' /tmp/pr.json && """
            'echo "M7_PR_HTTP $code"'
        )
        return bash_call(command)
    if "M7_CLONED" in text:
        if refs is None:
            return completion("missing m7 run id", None)
        head, base = refs
        command = (
            f"cd /workspace && git checkout -B '{head}' && "
            "printf '%s\\n' m7 >> README.md && git add README.md && "
            "git -c user.email=m7@example.invalid -c user.name=m7 "
            "commit -m m7 && "
            f"git -c protocol.allow=always push origin 'HEAD~1:refs/heads/{base}' && "
            f"git -c protocol.allow=always push origin 'HEAD:refs/heads/{head}' && "
            "echo M7_PUSHED"
        )
        return bash_call(command)
    if git_url == "":
        return completion("missing m7 target", None)
    command = (
        "cd /tmp && git config --global --add safe.directory /workspace && "
        "git config --global protocol.allow always && "
        "GIT_TERMINAL_PROMPT=0 "
        f"git -c protocol.allow=always clone '{git_url}' /workspace && echo M7_CLONED"
    )
    return bash_call(command)


def answer(body: dict) -> tuple[int, dict]:
    model = body.get("model")
    text = blob(body)
    if model == "m6-agent" and "m6-infra-trial" in text:
        return 500, {"error": {"message": "m6 infra trial"}}
    if model == "m6-agent" and "You are improving the factory definition" in text:
        return 200, improve(body)
    if model == "m6-agent":
        return 200, agent(body)
    if model == "m6-judge":
        return 200, judge(body)
    if model == "m7-hold":
        return 200, m7_hold_turn(body)
    if model == "m7-agent":
        return 200, m7_turn(body)
    return 400, {"error": {"message": "unknown model"}}


class Router(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt: str, *args: object) -> None:
        del fmt, args
        sys.stderr.write(f"{self.command} {self.path.split('?', 1)[0]}\n")

    def do_GET(self) -> None:
        if self.path in ("/healthz", "/v1/healthz"):
            self._send(200, b"ok\n", "text/plain")
            return
        if self.path in ("/v1/models", "/models"):
            raw = json.dumps(
                {
                    "object": "list",
                    "data": [{"id": model, "object": "model"} for model in MODELS],
                }
            ).encode()
            self._send(200, raw, "application/json")
            return
        self._send(404, b'{"error":{"message":"not found"}}\n', "application/json")

    def do_POST(self) -> None:
        if self.path not in ("/v1/chat/completions", "/chat/completions"):
            self._send(404, b'{"error":{"message":"not found"}}\n', "application/json")
            return
        length = int(self.headers.get("Content-Length", "0") or "0")
        if length > 1 << 20:
            self._send(
                413, b'{"error":{"message":"payload too large"}}\n', "application/json"
            )
            return
        raw = self.rfile.read(length)
        try:
            body = json.loads(raw)
        except json.JSONDecodeError:
            self._send(
                400, b'{"error":{"message":"bad request"}}\n', "application/json"
            )
            return
        if not isinstance(body, dict):
            self._send(
                400, b'{"error":{"message":"bad request"}}\n', "application/json"
            )
            return
        status, payload, content_type = dispatch(body, raw, self.headers)
        self._send(status, payload, content_type)

    def _send(self, status: int, payload: bytes, content_type: str) -> None:
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--addr", default="127.0.0.1:0")
    args = parser.parse_args()
    host, _, port_text = args.addr.rpartition(":")
    server = ThreadingHTTPServer((host or "127.0.0.1", int(port_text)), Router)
    server.serve_forever()


if __name__ == "__main__":
    main()
