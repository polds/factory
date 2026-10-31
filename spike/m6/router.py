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
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

CONVENTION = "CONVENTION: cite-sources"
BRANCH_RE = re.compile(r"branch: (improve/[A-Za-z0-9_-]+)")
UNSAFE_URL = re.compile(r"[\s'\"$`\\]")
MODELS = ("m6-agent", "m6-judge")

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


def failed_by_agent(summary: str) -> dict:
    return completion(
        json.dumps({"status": "failed_by_agent", "summary": summary}),
        None,
    )


def improve(body: dict) -> dict:
    text = blob(body)
    if factoryd_prompt(text) and not improve_instructions(text):
        return failed_by_agent(
            "improvement prompt is missing DEFINITION_GIT_URL or the improve branch"
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
        command = (
            f"cd /workspace && git checkout -b {branch} && "
            "cat >> skills/cite-sources/SKILL.md <<'EOF'\n"
            "\n## Required line\n\n"
            f"{CONVENTION}\n"
            "EOF\necho M6_EDITED"
        )
        return bash_call(command)
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


def agent(body: dict) -> dict:
    if CONVENTION in system_text(body):
        return completion(f"Done.\n{CONVENTION}", None)
    return completion("Done.", None)


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
        status, payload = answer(body)
        self._send(status, json.dumps(payload).encode(), "application/json")

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
