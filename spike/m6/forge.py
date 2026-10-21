#!/usr/bin/env python3
"""Pull-request front in front of the unmodified M4 GitHub fake.

Every other route is proxied. This process stores pull requests,
reviewers, and labels. It captures the installation token from the
proxied access_tokens response and requires that bearer on pull-request
routes. It does not log tokens or request bodies.
"""

from __future__ import annotations

import argparse
import hmac
import json
import os
import re
import sys
import threading
import urllib.error
import urllib.parse
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PULLS = re.compile(r"^/repos/([^/]+)/([^/]+)/pulls$")
REVIEWERS = re.compile(r"^/repos/([^/]+)/([^/]+)/pulls/(\d+)/requested_reviewers$")
LABELS = re.compile(r"^/repos/([^/]+)/([^/]+)/issues/(\d+)/labels$")

_lock = threading.Lock()
_pulls: list[dict] = []
_token = ""


def message(text: str) -> bytes:
    return json.dumps({"message": text}).encode()


def capture(payload: bytes) -> None:
    global _token
    try:
        doc = json.loads(payload)
    except json.JSONDecodeError:
        return
    if not isinstance(doc, dict):
        return
    token = doc.get("token")
    if not isinstance(token, str) or token == "":
        return
    with _lock:
        _token = token
    path = os.environ.get("M6_TOKEN_FILE", "")
    if path == "":
        return
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as fh:
        fh.write(token)
        fh.write("\n")


def authorized(header: str) -> bool:
    if not header.startswith("Bearer "):
        return False
    got = header[7:].strip()
    with _lock:
        want = _token
    if want == "" or got == "":
        return False
    return hmac.compare_digest(got, want)


def snapshot() -> dict:
    with _lock:
        pulls = json.loads(json.dumps(_pulls))
    checks: list = []
    upstream = os.environ.get("M6_UPSTREAM_GITHUB", "").rstrip("/")
    if upstream:
        try:
            with urllib.request.urlopen(upstream + "/_spike/state", timeout=5) as res:
                doc = json.loads(res.read())
            if isinstance(doc, dict) and isinstance(doc.get("checks"), list):
                checks = doc["checks"]
        except (OSError, urllib.error.URLError, json.JSONDecodeError):
            checks = []
    return {"pulls": pulls, "checks": checks}


class Forge(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt: str, *args: object) -> None:
        del fmt, args
        sys.stderr.write(f"{self.command} {self.path.split('?', 1)[0]}\n")

    def do_GET(self) -> None:
        self._route()

    def do_POST(self) -> None:
        self._route()

    def do_PATCH(self) -> None:
        self._route()

    def _route(self) -> None:
        length = int(self.headers.get("Content-Length", "0") or "0")
        if length > 1 << 20:
            self._send(413, message("payload too large"))
            return
        body = self.rfile.read(length) if length else b""
        path = self.path.split("?", 1)[0]
        if self.command == "GET" and path == "/_spike/m6":
            self._send(200, json.dumps(snapshot()).encode())
            return
        if self.command == "POST" and PULLS.match(path):
            self._create(body)
            return
        if self.command == "GET" and PULLS.match(path):
            self._lookup()
            return
        reviewers = REVIEWERS.match(path)
        if self.command == "POST" and reviewers:
            self._reviewers(int(reviewers.group(3)), body)
            return
        labels = LABELS.match(path)
        if self.command == "POST" and labels:
            self._labels(int(labels.group(3)), body)
            return
        self._proxy(body)

    def _require(self) -> bool:
        if authorized(self.headers.get("Authorization", "")):
            return True
        self._send(401, message("unauthorized"))
        return False

    def _create(self, body: bytes) -> None:
        if not self._require():
            return
        try:
            doc = json.loads(body or b"{}")
        except json.JSONDecodeError:
            self._send(400, message("bad request"))
            return
        if not isinstance(doc, dict):
            self._send(400, message("bad request"))
            return
        head = str(doc.get("head") or "")
        base = str(doc.get("base") or "")
        title = str(doc.get("title") or "")
        text = str(doc.get("body") or "")
        if head == "" or base == "" or title == "":
            self._send(422, message("head, base, and title are required"))
            return
        with _lock:
            for pull in _pulls:
                if pull["head"] == head and pull["base"] == base:
                    pull["creates"].append({"status": 422, "body": text})
                    self._send(422, message("a pull request already exists"))
                    return
            number = len(_pulls) + 1
            html = f"https://pr.invalid/m6/factory/pull/{number}"
            _pulls.append(
                {
                    "number": number,
                    "html_url": html,
                    "title": title,
                    "head": head,
                    "base": base,
                    "body": text,
                    "state": "open",
                    "reviewers": [],
                    "labels": [],
                    "creates": [{"status": 201, "body": text}],
                }
            )
        self._send(201, json.dumps({"number": number, "html_url": html}).encode())

    def _lookup(self) -> None:
        if not self._require():
            return
        query = urllib.parse.parse_qs(urllib.parse.urlparse(self.path).query)
        head = (query.get("head") or [""])[0]
        branch = head.split(":", 1)[-1]
        state = (query.get("state") or ["open"])[0]
        with _lock:
            rows = [
                {"number": pull["number"], "html_url": pull["html_url"]}
                for pull in _pulls
                if pull["head"] == branch and (state == "all" or pull["state"] == state)
            ]
        self._send(200, json.dumps(rows).encode())

    def _find(self, number: int) -> dict | None:
        for pull in _pulls:
            if pull["number"] == number:
                return pull
        return None

    def _reviewers(self, number: int, body: bytes) -> None:
        if not self._require():
            return
        try:
            doc = json.loads(body or b"{}")
        except json.JSONDecodeError:
            self._send(400, message("bad request"))
            return
        logins = doc.get("reviewers") if isinstance(doc, dict) else None
        if not isinstance(logins, list):
            self._send(400, message("bad request"))
            return
        with _lock:
            pull = self._find(number)
            if pull is None:
                self._send(404, message("not found"))
                return
            for login in logins:
                if isinstance(login, str) and login not in pull["reviewers"]:
                    pull["reviewers"].append(login)
        self._send(200, b"{}")

    def _labels(self, number: int, body: bytes) -> None:
        if not self._require():
            return
        try:
            doc = json.loads(body or b"{}")
        except json.JSONDecodeError:
            self._send(400, message("bad request"))
            return
        labels = doc.get("labels") if isinstance(doc, dict) else None
        if not isinstance(labels, list):
            self._send(400, message("bad request"))
            return
        with _lock:
            pull = self._find(number)
            if pull is None:
                self._send(404, message("not found"))
                return
            for label in labels:
                if isinstance(label, str) and label not in pull["labels"]:
                    pull["labels"].append(label)
        self._send(200, b"{}")

    def _proxy(self, body: bytes) -> None:
        upstream = os.environ.get("M6_UPSTREAM_GITHUB", "").rstrip("/")
        if upstream == "":
            self._send(502, message("upstream is not configured"))
            return
        req = urllib.request.Request(
            upstream + self.path, data=body or None, method=self.command
        )
        for name in (
            "Authorization",
            "Accept",
            "Content-Type",
            "User-Agent",
            "X-GitHub-Api-Version",
        ):
            value = self.headers.get(name)
            if value:
                req.add_header(name, value)
        try:
            with urllib.request.urlopen(req, timeout=15) as res:
                payload = res.read()
                status = res.status
                content_type = res.headers.get("Content-Type", "application/json")
        except urllib.error.HTTPError as err:
            payload = err.read()
            status = err.code
            content_type = (
                err.headers.get("Content-Type", "application/json")
                if err.headers
                else "application/json"
            )
        except (OSError, urllib.error.URLError):
            self._send(502, message("upstream unavailable"))
            return
        if self.command == "POST" and "/access_tokens" in self.path:
            capture(payload)
        self._send(status, payload, content_type)

    def _send(
        self, status: int, payload: bytes, content_type: str = "application/json"
    ) -> None:
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--addr", default="127.0.0.1:0")
    args = parser.parse_args()
    if os.environ.get("M6_UPSTREAM_GITHUB", "") == "":
        sys.stderr.write("missing M6_UPSTREAM_GITHUB\n")
        raise SystemExit(1)
    host, _, port_text = args.addr.rpartition(":")
    server = ThreadingHTTPServer((host or "127.0.0.1", int(port_text)), Forge)
    server.serve_forever()


if __name__ == "__main__":
    main()
