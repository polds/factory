#!/usr/bin/env python3
"""Offline checks for the M7 remote preflight and ledger.

Reuses the M6 forge and the lifted remote module. Tokens here are fake.
"""

from __future__ import annotations

import json
import os
import re
import subprocess
import sys
import threading
import time
import urllib.parse
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
    env["M1_OPENAI_BASE_URL"] = ""
    env["M1_OPENAI_API_KEY"] = ""
    env["M1_OPENAI_EXTRA_HEADERS"] = ""
    env["FACTORY_INFERENCE_OPENAI_API_KEY"] = ""
    env["FACTORY_INFERENCE_OPENAI_COMPATIBLE_EXTRA_HEADERS"] = ""
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


def case_rewrite(work: Path) -> None:
    import remote

    src = ROOT / "deploy" / "m7-factory" / "agents" / "implementer" / "agent.md"
    dest = work / "agent.md"
    original = src.read_text(encoding="utf-8")
    dest.write_text(original, encoding="utf-8")
    remote.rewrite_agent_steps(dest, "m7-run/local-s1/main")
    text = dest.read_text(encoding="utf-8")
    for needle in ("m7-run/local-s1/main", "m7-run/local-s1/change", "html_url"):
        if needle not in text:
            fail(f"rewrite missed {needle}")
    if "against main" in text or "run base ref" in text:
        fail("rewrite left the pull request pointed at main")
    if src.read_text(encoding="utf-8") != original:
        fail("rewrite changed the source agent")


PLANTED_KEY = "sk-planted-m7-probe-0123456789"
PLANTED_EXTRA = "planted-extra-value-m7"


class Probe(BaseHTTPRequestHandler):
    status_code = 402

    def log_message(self, fmt: str, *args: object) -> None:
        del fmt, args

    def do_POST(self) -> None:
        length = int(self.headers.get("Content-Length", "0") or "0")
        raw = self.rfile.read(length) if length else b""
        Probe.last = {
            "path": self.path.split("?", 1)[0],
            "body": raw.decode("utf-8", "replace"),
            "auth": self.headers.get("Authorization", ""),
            "extra": self.headers.get("x-litellm-api-key", ""),
        }
        if Probe.status_code == 200:
            body = b'{"choices":[{"message":{"content":"x"}}]}'
        else:
            body = json.dumps(
                {
                    "error": Probe.last["auth"],
                    "extra": Probe.last["extra"],
                    "message": "model not found",
                }
            ).encode()
        self.send_response(Probe.status_code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def serve_probe(status: int) -> tuple[ThreadingHTTPServer, int]:
    Probe.status_code = status
    Probe.last = {}
    server = ThreadingHTTPServer(("127.0.0.1", 0), Probe)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    return server, server.server_address[1]


def run_probe(work: Path, status: int) -> str:
    server, port = serve_probe(status)
    try:
        env = base_env(work, "9")
        env["M1_OPENAI_BASE_URL"] = f"http://127.0.0.1:{port}/v1"
        env["M1_OPENAI_API_KEY"] = PLANTED_KEY
        env["M1_OPENAI_EXTRA_HEADERS"] = f"x-litellm-api-key: {PLANTED_EXTRA}"
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
        if PLANTED_KEY in text or PLANTED_EXTRA in text:
            fail("probe output contained a planted secret")
        return text
    finally:
        server.shutdown()
        server.server_close()


def case_model_probe(work: Path) -> None:
    denied = run_probe(work, 402)
    if "payment required" not in denied or "402" not in denied:
        fail("402 did not report payment required")
    if "Authorization" not in denied or "x-litellm-api-key" not in denied:
        fail("402 did not name the request headers")
    limited = run_probe(work, 429)
    if "rate limited" not in limited or "429" not in limited:
        fail("429 did not report rate limited")
    unknown = run_probe(work, 400)
    if "unknown model" not in unknown:
        fail("400 did not report an unknown model")
    ready = run_probe(work, 200)
    if "remote preflight: model PASS" not in ready:
        fail("200 did not pass the model probe")
    seen = getattr(Probe, "last", {})
    body = seen.get("body", "")
    if '"max_tokens": 1' not in body and '"max_tokens":1' not in body:
        fail("probe did not ask for one token")
    if seen.get("auth") != f"Bearer {PLANTED_KEY}":
        fail("probe did not send the bearer")
    if seen.get("extra") != PLANTED_EXTRA:
        fail("probe did not send the extra header")


def post_pull(base: str, head: str, target: str) -> None:
    import urllib.request

    body = json.dumps(
        {"title": "m7", "head": head, "base": target, "body": "m7"}
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


def write_record_ledger(work: Path, api: str) -> None:
    ledger = {
        "version": 1,
        "run_id": RUN,
        "owner": "polds",
        "repo": "factory-m7-target",
        "git_url": "git://m7-git.factory.svc.cluster.local/target.git",
        "api_base": api,
        "base_ref": f"m7-run/{RUN}/main",
        "candidate_ref": f"m7-run/{RUN}/change",
        "batch_id": "",
        "entries": [],
    }
    path = work / "remote-ledger.json"
    path.write_text(json.dumps(ledger), encoding="utf-8")
    path.chmod(0o600)


def write_slow_git(stub: Path) -> None:
    stub.mkdir(parents=True, exist_ok=True)
    path = stub / "git"
    path.write_text(
        """#!/bin/bash
printf '%s\\n' "$*" >> "$M7_ARGV_LOG"
if [[ "$*" == *ls-remote* ]]; then
  if [[ "${M7_GIT_HEADS_LEFT:-}" == 1 ]]; then
    printf '%s\\t%s\\n' \\
      1111111111111111111111111111111111111111 refs/heads/m7-run/local-s1/main \\
      2222222222222222222222222222222222222222 refs/heads/m7-run/local-s1/candidate \\
      3333333333333333333333333333333333333333 refs/heads/m7-run/local-s1/change
  fi
  exit 0
fi
if [[ "$*" == *push* ]]; then
  sleep "${M7_GIT_PUSH_SLEEP:-2}"
  exit 0
fi
exit 0
""",
        encoding="utf-8",
    )
    path.chmod(0o755)


def serve_empty() -> tuple[ThreadingHTTPServer, str]:
    """Answer the prefix sweep with no refs and no pulls."""

    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, fmt: str, *args: object) -> None:
            del fmt, args

        def do_GET(self) -> None:
            path = self.path.split("?", 1)[0]
            if path.endswith("/pulls"):
                body, status = b"[]", 200
            else:
                body, status = b'{"message":"not found"}', 404
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server, f"http://127.0.0.1:{server.server_address[1]}"


def case_cleanup_low_speed(work: Path) -> None:
    hub, api = serve_empty()
    try:
        _cleanup_low_speed(work, api)
    finally:
        hub.shutdown()


def _cleanup_low_speed(work: Path, api: str) -> None:
    token = work / "github-app-token"
    token.write_text(TOKEN + "\n", encoding="utf-8")
    token.chmod(0o600)
    name = f"m7-run/{RUN}/main"
    ledger = {
        "version": 1,
        "run_id": RUN,
        "owner": "polds",
        "repo": "factory-m7-target",
        "git_url": "https://github.com/polds/factory-m7-target.git",
        "api_base": api,
        "base_ref": name,
        "candidate_ref": name,
        "batch_id": "",
        "entries": [{"kind": "ref", "name": name, "phase": "created"}],
    }
    path = work / "remote-ledger.json"
    path.write_text(json.dumps(ledger), encoding="utf-8")
    path.chmod(0o600)
    log = work / "argv"
    log.write_text("", encoding="utf-8")
    stub = work / "bin"
    stub.mkdir(parents=True, exist_ok=True)
    git = stub / "git"
    git.write_text(
        '#!/bin/bash\nprintf \'%s\\n\' "$*" >> "$M7_ARGV_LOG"\nexit 0\n',
        encoding="utf-8",
    )
    git.chmod(0o755)
    env = base_env(work, "9")
    env["PATH"] = f"{stub}:{env['PATH']}"
    env["M7_ARGV_LOG"] = str(log)
    env["M7_CLEANUP_DEADLINE"] = f"{time.time() + 4.938:.3f}"
    proc = subprocess.run(
        [sys.executable, str(ROOT / "spike" / "lib" / "remote.py"), "cleanup"],
        cwd=ROOT,
        env=env,
        capture_output=True,
        text=True,
        check=False,
    )
    if proc.returncode != 0:
        fail("budgeted delete failed: " + proc.stderr + proc.stdout)
    key = "http.lowSpeedTime="
    values = [
        part[len(key) :]
        for part in log.read_text(encoding="utf-8").split()
        if part.startswith(key)
    ]
    if values != ["4"]:
        fail(f"http.lowSpeedTime for a 4.938s budget was {values}")


def case_cleanup_timeout(work: Path) -> None:
    hub, api = serve_empty()
    try:
        _cleanup_timeout(work, api)
    finally:
        hub.shutdown()


def _cleanup_timeout(work: Path, api: str) -> None:
    token = work / "github-app-token"
    token.write_text(TOKEN + "\n", encoding="utf-8")
    token.chmod(0o600)
    names = [
        f"m7-run/{RUN}/main",
        f"m7-run/{RUN}/candidate",
        f"m7-run/{RUN}/change",
    ]
    ledger = {
        "version": 1,
        "run_id": RUN,
        "owner": "polds",
        "repo": "factory-m7-target",
        "git_url": "https://github.com/polds/factory-m7-target.git",
        "api_base": api,
        "base_ref": names[0],
        "candidate_ref": names[2],
        "batch_id": "",
        "entries": [{"kind": "pr", "number": 1, "phase": "cleaned"}]
        + [{"kind": "ref", "name": name, "phase": "created"} for name in names],
    }
    path = work / "remote-ledger.json"
    path.write_text(json.dumps(ledger), encoding="utf-8")
    path.chmod(0o600)
    log = work / "argv"
    log.write_text("", encoding="utf-8")
    write_slow_git(work / "bin")
    env = base_env(work, "9")
    env["PATH"] = f"{work / 'bin'}:{env['PATH']}"
    env["M7_ARGV_LOG"] = str(log)
    env["M7_REUSE_TOKEN"] = "1"
    env["M7_DELETE_TIMEOUT"] = "0.3"

    def run_cleanup() -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [sys.executable, str(ROOT / "spike" / "lib" / "remote.py"), "cleanup"],
            cwd=ROOT,
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )

    env["M7_GIT_HEADS_LEFT"] = "1"
    held = run_cleanup()
    if held.returncode == 0 or "delete timed out" not in held.stderr:
        fail("still-present refs did not time out: " + held.stderr + held.stdout)
    phases = [
        entry.get("phase")
        for entry in json.loads(path.read_text(encoding="utf-8"))["entries"]
    ]
    if phases != ["cleaned", "created", "created", "created"]:
        fail(f"timed-out ledger phases {phases}")
    env.pop("M7_GIT_HEADS_LEFT", None)
    log.write_text("", encoding="utf-8")
    gone = run_cleanup()
    if gone.returncode != 0:
        fail("cleanup after refs were gone: " + gone.stderr + gone.stdout)
    phases = [
        entry.get("phase")
        for entry in json.loads(path.read_text(encoding="utf-8"))["entries"]
    ]
    if phases != ["cleaned", "cleaned", "cleaned", "cleaned"]:
        fail(f"reconciled ledger phases {phases}")
    argv = log.read_text(encoding="utf-8")
    if "ls-remote" not in argv or "push" not in argv:
        fail("reconcile did not push and then list heads")


def case_record(work: Path) -> None:
    proc, base = start_forge(work)
    try:
        post_pull(base, f"m7-run/{RUN}/change", f"m7-run/{RUN}/main")
        write_record_ledger(work, base)
        env = base_env(work, "9")
        env["MODE"] = "offline"
        opened = subprocess.run(
            [sys.executable, str(ROOT / "spike" / "lib" / "remote.py"), "record-prs"],
            cwd=ROOT,
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )
        if opened.returncode != 0:
            fail("offline record-prs failed")
        url = (work / "opened-pr-url").read_text(encoding="utf-8").strip()
        if url != "https://pr.invalid/m7/factory-m7-target/pull/1":
            fail("offline record-prs did not write the forge url")
        (work / "opened-pr-url").unlink()
        env["SPIKE_ENV_PREFIX"] = "M6"
        skipped = subprocess.run(
            [sys.executable, str(ROOT / "spike" / "lib" / "remote.py"), "record-prs"],
            cwd=ROOT,
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )
        if skipped.returncode != 0 or (work / "opened-pr-url").exists():
            fail("m6 offline record-prs was not a no-op")
    finally:
        proc.kill()
        proc.wait(timeout=5)

    proc, base = start_forge(work)
    try:
        post_pull(base, f"m7-run/{RUN}/change", "main")
        write_record_ledger(work, base)
        env = base_env(work, "9")
        env["MODE"] = "offline"
        refused = subprocess.run(
            [sys.executable, str(ROOT / "spike" / "lib" / "remote.py"), "record-prs"],
            cwd=ROOT,
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )
        if refused.returncode == 0:
            fail("a main-base pull was recorded")
        if "not the single run-base pull" not in refused.stderr:
            fail("main-base pull was not rejected")
    finally:
        proc.kill()
        proc.wait(timeout=5)


class SweepState:
    def __init__(self) -> None:
        self.paths: list[str] = []
        self.refs: list[dict] = []
        self.pulls: list[dict] = []
        self.delay = 0.0
        self.stop = False


def serve_sweep(state: SweepState) -> ThreadingHTTPServer:
    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, fmt: str, *args: object) -> None:
            del fmt, args

        def _send(self, status: int, body: bytes) -> None:
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            try:
                self.wfile.write(body)
            except BrokenPipeError:
                return

        def do_GET(self) -> None:
            self._route("GET")

        def do_PATCH(self) -> None:
            length = int(self.headers.get("Content-Length", "0") or "0")
            if length:
                self.rfile.read(length)
            self._route("PATCH")

        def _route(self, method: str) -> None:
            path = self.path.split("?", 1)[0]
            state.paths.append(f"{method} {self.path}")
            if state.delay:
                end = time.time() + state.delay
                while time.time() < end and not state.stop:
                    time.sleep(0.05)
            if "/git/matching-refs/" in path:
                self._send(200, json.dumps(state.refs).encode())
                return
            if method == "GET" and path.endswith("/pulls"):
                rows = [
                    {
                        "number": pull["number"],
                        "html_url": pull["html_url"],
                        "state": pull["state"],
                        "head": {"ref": pull["head"]},
                        "base": {"ref": pull["base"]},
                    }
                    for pull in state.pulls
                    if pull["state"] == "open"
                ]
                self._send(200, json.dumps(rows).encode())
                return
            match = re.fullmatch(r"/repos/[^/]+/[^/]+/pulls/(\d+)", path)
            if match is None:
                self._send(404, b'{"message":"not found"}')
                return
            number = int(match.group(1))
            for pull in state.pulls:
                if pull["number"] != number:
                    continue
                if method == "PATCH":
                    pull["state"] = "closed"
                    self._send(200, b"{}")
                    return
                self._send(
                    200,
                    json.dumps(
                        {
                            "number": number,
                            "state": pull["state"],
                            "head": {"ref": pull["head"]},
                            "base": {"ref": pull["base"]},
                            "requested_reviewers": [],
                            "labels": [],
                        }
                    ).encode(),
                )
                return
            self._send(404, b'{"message":"not found"}')

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server


def write_ok_git(stub: Path) -> None:
    stub.mkdir(parents=True, exist_ok=True)
    path = stub / "git"
    path.write_text(
        '#!/bin/bash\nprintf \'%s\\n\' "$*" >> "$M7_ARGV_LOG"\nexit 0\n',
        encoding="utf-8",
    )
    path.chmod(0o755)


def sweep_ledger(work: Path, api: str, ref_prefix: str, run: str) -> None:
    ledger = {
        "version": 1,
        "run_id": run,
        "owner": "polds",
        "repo": "factory-m7-target",
        "git_url": "https://github.com/polds/factory-m7-target.git",
        "api_base": api,
        "base_ref": f"{ref_prefix}/{run}/main",
        "candidate_ref": f"{ref_prefix}/{run}/candidate",
        "batch_id": "",
        "entries": [],
    }
    path = work / "remote-ledger.json"
    path.write_text(json.dumps(ledger), encoding="utf-8")
    path.chmod(0o600)


def sweep_env(work: Path, api: str, env_prefix: str, ref_prefix: str) -> dict[str, str]:
    env = base_env(work, "9")
    env["SPIKE_ENV_PREFIX"] = env_prefix
    env["SPIKE_RUN_REF_PREFIX"] = ref_prefix
    env[f"{env_prefix}_WORK"] = str(work)
    env[f"{env_prefix}_REUSE_TOKEN"] = "1"
    env[f"{env_prefix}_RUN_ID"] = RUN
    env[f"{env_prefix}_GITHUB_API_BASE"] = api
    env["M7_ARGV_LOG"] = str(work / "argv")
    env["PATH"] = f"{work / 'bin'}:{env['PATH']}"
    return env


def seed_leaks(state: SweepState, ref_prefix: str, run: str) -> None:
    other = "ci-999-1"
    state.refs = [
        {"ref": f"refs/heads/{ref_prefix}/{run}/change"},
        {"ref": f"refs/heads/{ref_prefix}/{other}/change"},
        {"ref": "refs/heads/main"},
        {"ref": "refs/heads/improve/ibt_other"},
    ]
    state.pulls = [
        {
            "number": 4,
            "html_url": "https://pr.invalid/m7/pull/4",
            "state": "open",
            "head": f"{ref_prefix}/{run}/change",
            "base": f"{ref_prefix}/{run}/main",
        },
        {
            "number": 5,
            "html_url": "https://pr.invalid/m7/pull/5",
            "state": "open",
            "head": f"{ref_prefix}/{other}/change",
            "base": f"{ref_prefix}/{other}/main",
        },
        {
            "number": 6,
            "html_url": "https://pr.invalid/m7/pull/6",
            "state": "open",
            "head": f"{ref_prefix}/{run}/change",
            "base": "main",
        },
        {
            "number": 7,
            "html_url": "https://pr.invalid/m7/pull/7",
            "state": "open",
            "head": "main",
            "base": f"{ref_prefix}/{run}/main",
        },
        {
            "number": 8,
            "html_url": "https://pr.invalid/m7/pull/8",
            "state": "open",
            "head": "improve/ibt_other",
            "base": f"{ref_prefix}/{run}/main",
        },
    ]


def assert_sweep(work: Path, ref_prefix: str, state: SweepState) -> None:
    change = f"{ref_prefix}/{RUN}/change"
    other = f"{ref_prefix}/ci-999-1/change"
    argv = (work / "argv").read_text(encoding="utf-8")
    if f":refs/heads/{change}" not in argv:
        fail(f"{ref_prefix} did not delete {change}")
    for banned in (other, ":refs/heads/main", "improve/ibt_other"):
        if banned in argv:
            fail(f"{ref_prefix} deleted {banned}")
    pulls = {pull["number"]: pull["state"] for pull in state.pulls}
    if pulls[4] != "closed":
        fail(f"{ref_prefix} left the run pull open")
    for number in (5, 6, 7, 8):
        if pulls[number] != "open":
            fail(f"{ref_prefix} closed pull {number}")
    saved = json.loads((work / "remote-ledger.json").read_text(encoding="utf-8"))
    cleaned = {"ref": 0, "pr": 0}
    left = {"ref": 0, "pr": 0}
    for entry in saved["entries"]:
        bucket = cleaned if entry.get("phase") == "cleaned" else left
        bucket[str(entry.get("kind"))] += 1
    if cleaned != {"ref": 1, "pr": 1} or left != {"ref": 0, "pr": 0}:
        fail(f"{ref_prefix} ledger counts cleaned={cleaned} left={left}")
    needle = f"/git/matching-refs/heads/{ref_prefix}/{RUN}/"
    if not any(needle in path for path in state.paths):
        fail(f"{ref_prefix} did not list matching refs")
    quoted = urllib.parse.quote(f"{ref_prefix}/{RUN}/main", safe="")
    if not any(f"base={quoted}" in path for path in state.paths):
        fail(f"{ref_prefix} did not list pulls for the run base")


def case_prefix_sweep(work: Path) -> None:
    token = work / "github-app-token"
    token.write_text(TOKEN + "\n", encoding="utf-8")
    token.chmod(0o600)
    write_ok_git(work / "bin")
    for env_prefix, ref_prefix in (("M7", "m7-run"), ("M6", "m6-run")):
        state = SweepState()
        seed_leaks(state, ref_prefix, RUN)
        server = serve_sweep(state)
        try:
            api = f"http://127.0.0.1:{server.server_address[1]}"
            sweep_ledger(work, api, ref_prefix, RUN)
            (work / "argv").write_text("", encoding="utf-8")
            fail_path = work / "remote-fail"
            if fail_path.exists():
                fail_path.unlink()
            proc = subprocess.run(
                [sys.executable, str(ROOT / "spike" / "lib" / "remote.py"), "cleanup"],
                cwd=ROOT,
                env=sweep_env(work, api, env_prefix, ref_prefix),
                capture_output=True,
                text=True,
                check=False,
            )
            if proc.returncode != 0:
                fail(f"{ref_prefix} cleanup failed: {proc.stderr}")
            for refused in (
                f"{ref_prefix}/ci-999-1/change",
                "main",
                "improve/ibt_other",
            ):
                if f"remote ref refused: {refused}: allow-list" not in proc.stderr:
                    fail(f"{ref_prefix} did not refuse {refused}")
            if "prefix sweep: found 4 refs, 1 pulls" not in proc.stderr:
                fail(f"{ref_prefix} summary was {proc.stderr}")
            assert_sweep(work, ref_prefix, state)
        finally:
            state.stop = True
            server.shutdown()
    case_sweep_deadline(work)


def case_sweep_deadline(work: Path) -> None:
    state = SweepState()
    state.delay = 3
    state.refs = [{"ref": f"refs/heads/m7-run/{RUN}/change"}]
    server = serve_sweep(state)
    try:
        api = f"http://127.0.0.1:{server.server_address[1]}"
        sweep_ledger(work, api, "m7-run", RUN)
        write_ok_git(work / "bin")
        env = sweep_env(work, api, "M7", "m7-run")
        env["M7_CLEANUP_DEADLINE"] = f"{time.time() + 0.6:.3f}"
        (work / "argv").write_text("", encoding="utf-8")
        started = time.monotonic()
        proc = subprocess.run(
            [sys.executable, str(ROOT / "spike" / "lib" / "remote.py"), "cleanup"],
            cwd=ROOT,
            env=env,
            capture_output=True,
            text=True,
            check=False,
            timeout=8,
        )
        elapsed = time.monotonic() - started
        if elapsed >= 2.5:
            fail(f"cleanup ignored the deadline ({elapsed:.2f}s)")
        if proc.returncode == 0:
            fail("a slow sweep inside the deadline succeeded")
        reason = (work / "remote-fail").read_text(encoding="utf-8")
        # curl -m reports HTTP 000, which the sweep surfaces as a failed list.
        if reason.strip() not in {
            "github request failed",
            "remote cleanup incomplete",
            "matching refs failed",
        }:
            fail(f"deadline failure was {reason.strip()}")
        if "push" in (work / "argv").read_text(encoding="utf-8"):
            fail("deadline still deleted refs")
        state.paths.clear()
        state.delay = 0
        env["M7_CLEANUP_DEADLINE"] = f"{time.time() - 1:.3f}"
        (work / "argv").write_text("", encoding="utf-8")
        proc = subprocess.run(
            [sys.executable, str(ROOT / "spike" / "lib" / "remote.py"), "cleanup"],
            cwd=ROOT,
            env=env,
            capture_output=True,
            text=True,
            check=False,
            timeout=5,
        )
        if state.paths:
            fail(f"past deadline still called GitHub: {state.paths}")
        if proc.returncode == 0:
            fail("past deadline succeeded")
        reason = (work / "remote-fail").read_text(encoding="utf-8")
        if "remote cleanup incomplete" not in reason:
            fail(f"past deadline said {reason.strip()}")
        if (work / "argv").read_text(encoding="utf-8").strip():
            fail("past deadline invoked git")
    finally:
        state.stop = True
        server.shutdown()


def main() -> None:
    work = Path(os.environ["M7_CASE_WORK"])
    name = sys.argv[1]
    if name == "preflight":
        case_preflight(work)
    elif name == "ledger":
        case_ledger(work)
    elif name == "rewrite":
        case_rewrite(work)
    elif name == "probe":
        case_model_probe(work)
    elif name == "record":
        case_record(work)
    elif name == "cleanup-low-speed":
        case_cleanup_low_speed(work)
    elif name == "cleanup-timeout":
        case_cleanup_timeout(work)
    elif name == "sweep":
        case_prefix_sweep(work)
    else:
        fail(f"unknown case {name}")


if __name__ == "__main__":
    main()
