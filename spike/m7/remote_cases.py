#!/usr/bin/env python3
"""Offline checks for the M7 remote preflight and ledger.

Reuses the M6 forge and the lifted remote module. Tokens here are fake.
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
import threading
from datetime import UTC, datetime, timedelta
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "spike" / "lib"))

TOKEN = "m7fake" + ("a" * 40)
RUN = "local-s1"


def fail(text: str) -> None:
    sys.stderr.write(text + "\n")
    raise SystemExit(1)


class GitHub(BaseHTTPRequestHandler):
    mode = "missing-repo"

    def log_message(self, fmt: str, *args: object) -> None:
        del fmt, args

    def do_GET(self) -> None:
        path = self.path.split("?", 1)[0]
        if path == "/repos/polds/factory-m7-target/installation":
            if self.mode == "not-installed":
                body = b'{"message":"Not Found polds/factory-m7-target"}'
                self._send(404, body)
                return
            self._send(200, b'{"id":7}')
            return
        if path.startswith("/repos/polds/factory-m7-target/collaborators/"):
            if self.mode == "not-collaborator":
                self._send(404, b'{"message":"not a collaborator"}')
                return
            self.send_response(204)
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        self._send(404, b'{"message":"missing"}')

    def do_POST(self) -> None:
        if "/access_tokens" in self.path:
            exp = (datetime.now(UTC) + timedelta(hours=1)).strftime(
                "%Y-%m-%dT%H:%M:%SZ"
            )
            self._send(201, json.dumps({"token": TOKEN, "expires_at": exp}).encode())
            return
        self._send(404, b'{"message":"missing"}')

    def _send(self, status: int, body: bytes) -> None:
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def serve(mode: str) -> tuple[ThreadingHTTPServer, int]:
    GitHub.mode = mode
    server = ThreadingHTTPServer(("127.0.0.1", 0), GitHub)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    return server, server.server_address[1]


def ensure_key(work: Path) -> Path:
    key = work / "app.pem"
    if not key.exists():
        subprocess.run(
            [
                "openssl",
                "genpkey",
                "-algorithm",
                "RSA",
                "-pkeyopt",
                "rsa_keygen_bits:2048",
                "-out",
                str(key),
            ],
            check=True,
            capture_output=True,
        )
        key.chmod(0o644)
    return key


def ensure_apptoken(work: Path) -> Path:
    binary = work / "apptoken"
    if not binary.exists():
        subprocess.run(
            ["go", "build", "-o", str(binary), "./spike/m4/apptoken"],
            cwd=ROOT,
            check=True,
            stdout=subprocess.DEVNULL,
        )
    return binary


def base_env(work: Path, mode: str) -> dict[str, str]:
    env = os.environ.copy()
    env["MODE"] = "live-ci"
    env["SPIKE_ENV_PREFIX"] = "M7"
    env["SPIKE_RUN_REF_PREFIX"] = "m7-run"
    env["M7_WORK"] = str(work)
    env["M7_RUN_ID"] = RUN
    env["M7_DEFINITION_GIT_URL"] = "https://github.com/polds/factory-m7-target.git"
    env["M7_GITHUB_API_BASE"] = f"http://127.0.0.1:{mode}"
    env["FACTORY_GITHUB_APP_ID"] = "12345"
    env["FACTORY_GITHUB_APP_PRIVATE_KEY_FILE"] = str(ensure_key(work))
    env["M7_APPTOKEN_BIN"] = str(ensure_apptoken(work))
    env["FACTORY_REVIEWERS_ADMINS"] = "m7-reviewer"
    env.pop("GITHUB_RUN_ID", None)
    env.pop("GITHUB_RUN_ATTEMPT", None)
    return env


def write_git(stub: Path, log: Path, *, missing: bool) -> None:
    stub.mkdir(parents=True, exist_ok=True)
    body = """#!/bin/bash
printf '%s\\n' "$*" >> "$M7_ARGV_LOG"
if [[ "$*" == *ls-remote* ]]; then
  if [[ "${M7_GIT_MISSING:-}" == 1 ]]; then
    echo "fatal: repository 'https://github.com/polds/factory-m7-target.git' not found" >&2
    exit 128
  fi
  exit 0
fi
if [[ "$*" == *push* ]]; then
  printf '*\\t:refs/heads/m7-run/local-s1/main\\n'
  exit 0
fi
exit 0
"""
    path = stub / "git"
    path.write_text(body, encoding="utf-8")
    path.chmod(0o755)
    del missing, log


def run_preflight(work: Path, server_mode: str, *, missing_repo: bool) -> str:
    server, port = serve(server_mode)
    try:
        stub = work / "bin"
        log = work / "argv"
        log.write_text("", encoding="utf-8")
        write_git(stub, log, missing=missing_repo)
        env = base_env(work, str(port))
        env["PATH"] = f"{stub}:{env['PATH']}"
        env["M7_ARGV_LOG"] = str(log)
        env["M7_GIT_MISSING"] = "1" if missing_repo else "0"
        proc = subprocess.run(
            [sys.executable, str(ROOT / "spike" / "lib" / "remote.py"), "preflight"],
            cwd=ROOT,
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )
        text = proc.stderr + proc.stdout
        fail_file = work / "remote-fail"
        if fail_file.is_file():
            text += fail_file.read_text(encoding="utf-8")
        if proc.returncode == 0:
            fail(f"{server_mode}: preflight succeeded")
        return text
    finally:
        server.shutdown()


def case_preflight(work: Path) -> None:
    missing = run_preflight(work, "missing-repo", missing_repo=True)
    if "polds/factory-m7-target" not in missing:
        fail("missing repo did not name polds/factory-m7-target")
    installed = run_preflight(work, "not-installed", missing_repo=False)
    if "metadata" not in installed or "404" not in installed:
        fail("app not installed did not name metadata or HTTP 404")
    if "polds/factory-m7-target" not in installed:
        fail("app not installed did not name polds/factory-m7-target")
    reviewer = run_preflight(work, "not-collaborator", missing_repo=False)
    if "collaborator m7-reviewer was not accepted" not in reviewer:
        fail("reviewer failure did not name m7-reviewer")


def start_forge(work: Path) -> tuple[subprocess.Popen[bytes], str]:
    port = 0
    # Bind by asking the OS via a one-shot in the child --addr 127.0.0.1:0
    # is not printed. Pick a free port here.
    import socket

    sock = socket.socket()
    sock.bind(("127.0.0.1", 0))
    port = sock.getsockname()[1]
    sock.close()
    token = work / "github-app-token"
    token.write_text(TOKEN + "\n", encoding="utf-8")
    token.chmod(0o600)
    env = os.environ.copy()
    env["SPIKE_ENV_PREFIX"] = "M7"
    env["SPIKE_FORGE_STANDALONE"] = "1"
    env["SPIKE_PR_HTML_BASE"] = "https://pr.invalid/m7/factory-m7-target/pull"
    env["M7_TOKEN_FILE"] = str(token)
    proc = subprocess.Popen(
        [
            sys.executable,
            str(ROOT / "spike" / "lib" / "forge.py"),
            "--addr",
            f"127.0.0.1:{port}",
        ],
        env=env,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    for _ in range(50):
        try:
            import urllib.request

            urllib.request.urlopen(f"http://127.0.0.1:{port}/_spike/m6", timeout=0.2)
            break
        except OSError:
            import time

            time.sleep(0.1)
    else:
        proc.kill()
        fail("forge did not start")
    return proc, f"http://127.0.0.1:{port}"


def case_ledger(work: Path) -> None:
    proc, base = start_forge(work)
    try:
        import urllib.request

        body = json.dumps(
            {
                "title": "m7",
                "head": "m7-run/local-s1/change",
                "base": "main",
                "body": "m7",
            }
        ).encode()
        req = urllib.request.Request(
            base + "/repos/polds/factory-m7-target/pulls",
            data=body,
            method="POST",
            headers={
                "Authorization": f"Bearer {TOKEN}",
                "Content-Type": "application/json",
            },
        )
        with urllib.request.urlopen(req, timeout=5) as res:
            if res.status != 201:
                fail(f"create status {res.status}")
        ledger = {
            "version": 1,
            "run_id": RUN,
            "owner": "polds",
            "repo": "factory-m7-target",
            "git_url": "https://github.com/polds/factory-m7-target.git",
            "api_base": base,
            "base_ref": f"m7-run/{RUN}/main",
            "candidate_ref": f"m7-run/{RUN}/change",
            "batch_id": "",
            "entries": [
                {"kind": "pr", "number": 1, "phase": "created"},
                {"kind": "ref", "name": f"m7-run/{RUN}/main", "phase": "created"},
            ],
        }
        path = work / "remote-ledger.json"
        path.write_text(json.dumps(ledger), encoding="utf-8")
        path.chmod(0o600)
        stub = work / "bin"
        log = work / "argv"
        log.write_text("", encoding="utf-8")
        write_git(stub, log, missing=False)
        env = base_env(work, "9")
        env["PATH"] = f"{stub}:{env['PATH']}"
        env["M7_ARGV_LOG"] = str(log)
        env["M7_GITHUB_API_BASE"] = base
        env["M7_REUSE_TOKEN"] = "1"
        env["M7_GIT_MISSING"] = "0"
        done = subprocess.run(
            [sys.executable, str(ROOT / "spike" / "lib" / "remote.py"), "cleanup"],
            cwd=ROOT,
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )
        if done.returncode != 0:
            fail("cleanup failed: " + done.stderr)
        saved = json.loads(path.read_text(encoding="utf-8"))
        phases = [entry.get("phase") for entry in saved["entries"]]
        if phases != ["cleaned", "cleaned"]:
            fail(f"ledger phases {phases}")
        argv = log.read_text(encoding="utf-8")
        if "m7-run/local-s1/main" not in argv:
            fail("cleanup did not delete the recorded ref")
        if " main" in argv or argv.strip() == "main":
            fail("cleanup deleted an unscoped ref")
        # A ref outside the run prefix is refused and is not passed to git.
        bad = json.loads(json.dumps(ledger))
        bad["entries"] = [{"kind": "ref", "name": "main", "phase": "created"}]
        path.write_text(json.dumps(bad), encoding="utf-8")
        log.write_text("", encoding="utf-8")
        refused = subprocess.run(
            [sys.executable, str(ROOT / "spike" / "lib" / "remote.py"), "cleanup"],
            cwd=ROOT,
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )
        if refused.returncode == 0:
            fail("unscoped ref was deleted")
        if "allow-list" not in refused.stderr:
            fail("refusal did not name the allow-list")
        if log.read_text(encoding="utf-8").strip():
            fail("refused ref was passed to git")
    finally:
        proc.kill()
        proc.wait(timeout=5)


def main() -> None:
    work = Path(os.environ["M7_CASE_WORK"])
    name = sys.argv[1]
    if name == "preflight":
        case_preflight(work)
    elif name == "ledger":
        case_ledger(work)
    else:
        fail(f"unknown case {name}")


if __name__ == "__main__":
    main()
