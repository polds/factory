#!/usr/bin/env python3
"""Route offline chat completions to long-lived model-stub processes.

spike/m1/model-stub advances one script step per /chat/completions call
and then repeats its last final step. The question stub's script is
ask_input, then a final. This process forwards every m5-question call to
that stub and never restarts it, so the parked run and the resumed
execution share one script cursor.

Other routes:
- m5-agent-failure goes to the agent stub, whose only step is the
  failed_by_agent JSON object.
- m5-infra-kill is held open until the client disconnects so a docker
  kill lands during the model call.
- The m5-day building turn is held until the client disconnects so the
  console can interrupt it. The hold outlasts the harness steer long-poll
  that has to finish before the interrupt button can enable. A follow-up
  that contains m5-follow-up is forwarded to the building stub immediately.
"""

from __future__ import annotations

import argparse
import json
import os
import select
import socket
import sys
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import ClassVar

MODELS = ("m5-triage", "m5-planning", "m5-building", "m5-reviewing")
HOLD_SECONDS = 55
INFRA_HOLD_SECONDS = 50


def blob(body: dict) -> str:
    parts: list[str] = []
    for message in body.get("messages") or []:
        if not isinstance(message, dict):
            continue
        content = message.get("content")
        if isinstance(content, str):
            parts.append(content)
        elif isinstance(content, list):
            for item in content:
                if isinstance(item, dict) and isinstance(item.get("text"), str):
                    parts.append(item["text"])
    return "\n".join(parts)


def final_message(text: str) -> dict:
    return {
        "id": "chatcmpl-m5",
        "object": "chat.completion",
        "choices": [
            {
                "index": 0,
                "message": {"role": "assistant", "content": text},
                "finish_reason": "stop",
            }
        ],
    }


class Router(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    stubs: ClassVar[dict[str, str]] = {}

    def log_message(self, fmt: str, *args) -> None:
        sys.stderr.write("%s\n" % (fmt % args))

    def do_GET(self) -> None:
        if self.path in ("/healthz", "/v1/healthz"):
            self._send(200, b"ok\n", "text/plain")
            return
        if self.path in ("/v1/models", "/models"):
            data = {
                "object": "list",
                "data": [{"id": model, "object": "model"} for model in MODELS],
            }
            raw = json.dumps(data).encode()
            self._send(200, raw, "application/json")
            return
        self._send(404, b"not found\n", "text/plain")

    def do_POST(self) -> None:
        if self.path not in ("/v1/chat/completions", "/chat/completions"):
            self._send(404, b"not found\n", "text/plain")
            return
        length = int(self.headers.get("Content-Length", "0") or "0")
        raw = self.rfile.read(length) if length else b""
        try:
            body = json.loads(raw.decode() or "{}")
        except json.JSONDecodeError:
            self._send(400, b'{"error":{"message":"bad json"}}\n', "application/json")
            return
        if not isinstance(body, dict):
            self._send(400, b'{"error":{"message":"bad json"}}\n', "application/json")
            return
        text = blob(body)
        model = str(body.get("model") or "")
        route = self._route(model, text)
        sys.stderr.write(f"route model={model} choice={route}\n")
        if route == "hold-infra":
            self._hold(INFRA_HOLD_SECONDS)
            return
        if route == "hold-day":
            if self._hold(HOLD_SECONDS):
                self._send_json(final_message("building summary"))
            return
        stub = self.stubs.get(route)
        if not stub:
            self._send(500, b'{"error":{"message":"no stub"}}\n', "application/json")
            return
        self._forward(stub, raw)

    def _route(self, model: str, text: str) -> str:
        if "m5-question" in text:
            return "question"
        if "m5-agent-failure" in text:
            return "agent"
        if "m5-infra-kill" in text:
            return "hold-infra"
        if model == "m5-building" and "m5-day" in text and "m5-follow-up" not in text:
            return "hold-day"
        return {
            "m5-triage": "triage",
            "m5-planning": "planning",
            "m5-building": "building",
            "m5-reviewing": "reviewing",
        }.get(model, "triage")

    def _hold(self, seconds: float) -> bool:
        """Return True when the client is still connected after the wait."""
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            ready, _, _ = select.select([self.connection], [], [], 0.2)
            if not ready:
                continue
            try:
                peeked = self.connection.recv(1, socket.MSG_PEEK)
            except (BlockingIOError, InterruptedError, OSError):
                return False
            if peeked == b"":
                return False
            time.sleep(0.2)
        return True

    def _forward(self, base: str, raw: bytes) -> None:
        url = base.rstrip("/") + "/v1/chat/completions"
        req = urllib.request.Request(
            url,
            data=raw,
            headers={"Content-Type": "application/json"},
            method="POST",
        )
        try:
            with urllib.request.urlopen(req, timeout=30) as res:
                payload = res.read()
                status = res.status
        except urllib.error.HTTPError as exc:
            payload = exc.read()
            status = exc.code
        except urllib.error.URLError as exc:
            message = json.dumps({"error": {"message": str(exc.reason)}}).encode()
            self._send(502, message, "application/json")
            return
        self._send(status, payload, "application/json")

    def _send_json(self, payload: dict) -> None:
        self._send(200, json.dumps(payload).encode(), "application/json")

    def _send(self, status: int, payload: bytes, content_type: str) -> None:
        try:
            self.send_response(status)
            self.send_header("Content-Type", content_type)
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)
        except OSError:
            return


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--addr", default="0.0.0.0:8318")
    args = parser.parse_args()
    host, _, port = args.addr.rpartition(":")
    Router.stubs = {
        "question": os.environ["M5_STUB_QUESTION"],
        "agent": os.environ["M5_STUB_AGENT"],
        "triage": os.environ["M5_STUB_TRIAGE"],
        "planning": os.environ["M5_STUB_PLANNING"],
        "building": os.environ["M5_STUB_BUILDING"],
        "reviewing": os.environ["M5_STUB_REVIEWING"],
    }
    server = ThreadingHTTPServer((host or "0.0.0.0", int(port)), Router)
    sys.stderr.write(f"listening addr={args.addr}\n")
    server.serve_forever()


if __name__ == "__main__":
    main()
