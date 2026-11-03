#!/usr/bin/env python3
"""Offline checks for the M7 hold model and run-scoped pull refs."""

from __future__ import annotations

import io
import json
import os
import sys
import threading
from contextlib import redirect_stderr
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import router


def fail(text: str) -> None:
    sys.stderr.write(text + "\n")
    raise SystemExit(1)


def command_of(payload: dict) -> str:
    message = payload["choices"][0]["message"]
    raw = message["tool_calls"][0]["function"]["arguments"]
    return str(json.loads(raw)["command"])


def test_hold() -> None:
    os.environ.pop("M7_UPSTREAM_BASE", None)
    status, payload = router.answer({"model": "m7-hold", "messages": []})
    if status != 200:
        fail(f"hold status {status}")
    command = command_of(payload)
    if command != "sleep 600 && echo M7_HOLDING":
        fail("hold command was not the fixed sleep")
    for needle in ("git", "curl", "pull", "clone"):
        if needle in command:
            fail(f"hold command contained {needle}")


def test_refs() -> None:
    os.environ["M7_RUN_ID"] = "off-abcd1234"
    os.environ["SPIKE_RUN_REF_PREFIX"] = "m7-run"
    os.environ["M7_DEFINITION_GIT_URL"] = (
        "git://m7-git.factory.svc.cluster.local/target.git"
    )
    os.environ["M7_GITHUB_API_BASE"] = "http://m7-forge.factory.svc.cluster.local:8080"
    os.environ.pop("M7_UPSTREAM_BASE", None)
    status, payload = router.answer(
        {
            "model": "m7-agent",
            "messages": [{"role": "user", "content": "M7_PUSHED"}],
        }
    )
    if status != 200:
        fail(f"push status {status}")
    command = command_of(payload)
    for needle in (
        "m7-run/off-abcd1234/change",
        "m7-run/off-abcd1234/main",
    ):
        if needle not in command:
            fail(f"push command missed {needle}")
    if "m7-change" in command or '"base": "main"' in command:
        fail("push command used an unscoped ref")


def test_missing_run() -> None:
    os.environ.pop("M7_RUN_ID", None)
    os.environ["M7_DEFINITION_GIT_URL"] = "git://example.invalid/target.git"
    os.environ["M7_GITHUB_API_BASE"] = "http://forge.invalid"
    _, payload = router.answer(
        {
            "model": "m7-agent",
            "messages": [{"role": "user", "content": "M7_CLONED"}],
        }
    )
    text = json.dumps(payload)
    if "missing m7 run id" not in text:
        fail("invalid run id did not stop the push")
    if "m7-change" in text or '"base": "main"' in text:
        fail("invalid run id fell back to main")


class Sink(BaseHTTPRequestHandler):
    hits = 0
    auth = ""

    def log_message(self, fmt: str, *args: object) -> None:
        del fmt, args

    def do_POST(self) -> None:
        length = int(self.headers.get("Content-Length", "0") or "0")
        if length:
            self.rfile.read(length)
        Sink.hits += 1
        Sink.auth = self.headers.get("Authorization", "")
        body = b'{"choices":[{"message":{"content":"ok"}}]}'
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


class Bag:
    def __init__(self, pairs: list[tuple[str, str]]) -> None:
        self._pairs = pairs

    def items(self) -> list[tuple[str, str]]:
        return list(self._pairs)


def test_dispatch() -> None:
    Sink.hits = 0
    Sink.auth = ""
    server = ThreadingHTTPServer(("127.0.0.1", 0), Sink)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    port = server.server_address[1]
    key = "sk-forward-test-m7"
    try:
        os.environ["M7_UPSTREAM_BASE"] = f"http://127.0.0.1:{port}/v1"
        raw = json.dumps({"model": "m7-hold", "messages": []}).encode()
        status, payload, _ctype = router.dispatch(json.loads(raw), raw, Bag([]))
        if status != 200 or b"sleep 600" not in payload or Sink.hits != 0:
            fail("hold reached the upstream")
        raw = json.dumps(
            {
                "model": "nous/claude-sonnet-5.5",
                "messages": [{"role": "user", "content": "hi"}],
            }
        ).encode()
        captured = io.StringIO()
        with redirect_stderr(captured):
            status, _payload, _ctype = router.dispatch(
                json.loads(raw),
                raw,
                Bag([("Authorization", "Bearer " + key), ("Host", "skip")]),
            )
        if status != 200:
            fail(f"forward status {status}")
        if Sink.auth != "Bearer " + key:
            fail("forward dropped Authorization")
        if key in captured.getvalue():
            fail("forward logged the key")
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=2)


def main() -> None:
    test_hold()
    test_refs()
    test_missing_run()
    test_dispatch()
    print("m7 router cases ok")


if __name__ == "__main__":
    main()
