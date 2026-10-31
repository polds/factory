#!/usr/bin/env python3
"""Cases for spike/m6/run_test.sh. No live network and no real App."""

from __future__ import annotations

import base64
import json
import os
import shutil
import socket
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "spike" / "m6"))

import remote

TOKEN = "ghs_planted_m6_token_value"
BATCH = "ibt_new"
OLD_BATCH = "ibt_old"
PARITY_INPUTS = [
    "https://github.com/acme/widgets.git",
    "https://github.com/acme/widgets",
    "https://github.com/acme/widgets/",
    "https://example.invalid/m6/factory.git",
    "https://github.com/org/acme/widgets.git",
    "git@github.com:acme/widgets.git",
    "ssh://git@github.com/acme/widgets.git",
    "https://user:token@github.com/acme/widgets.git",
    "https://github.com/acme/widgets.git?foo=1",
    "https://github.com/acme/widgets#frag",
    "http://github.com/acme/widgets.git",
    "https://github.com/only",
    "",
    "not a url",
    "https://github.com/acme/my.repo.git",
    "git://host/m6/factory.git",
    "file:///tmp/m6/acme/widgets.git",
]


def fail(text: str) -> None:
    sys.stderr.write(text + "\n")
    raise SystemExit(1)


def require_mode(mode: str) -> None:
    if mode not in {"live", "live-ci"}:
        fail(f"mode {mode}")


def free_port() -> int:
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.bind(("127.0.0.1", 0))
    port = sock.getsockname()[1]
    sock.close()
    return port


def wait_http(url: str) -> None:
    for _ in range(50):
        try:
            with urllib.request.urlopen(url, timeout=1) as res:
                if res.status == 200:
                    return
        except (OSError, urllib.error.URLError):
            time.sleep(0.1)
    fail(f"listener did not start: {url}")


def real_binary(name: str) -> str:
    path = shutil.which(name)
    if not path:
        fail(f"{name} is not on PATH")
    return path


def write_stub(
    directory: Path, name: str, forwards: str, *, ls_remote_ok: bool = False
) -> None:
    skip = ""
    if ls_remote_ok:
        skip = 'if [[ "$*" == *ls-remote* ]]; then\n  exit 0\nfi\n'
    path = directory / name
    path.write_text(
        "#!/bin/bash\n"
        'printf \'%s\\n\' "$*" >> "$M6_ARGV_LOG"\n'
        f"{skip}"
        f'exec {forwards} "$@"\n',
        encoding="utf-8",
    )
    path.chmod(0o755)


def call_captured(work: Path, fn):
    """Run fn while this process's stdout and stderr go to files."""
    out_path = work / "captured.out"
    err_path = work / "captured.err"
    sys.stdout.flush()
    sys.stderr.flush()
    saved_out = os.dup(1)
    saved_err = os.dup(2)
    out_fd = os.open(out_path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    err_fd = os.open(err_path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    os.dup2(out_fd, 1)
    os.dup2(err_fd, 2)
    os.close(out_fd)
    os.close(err_fd)
    try:
        return fn()
    finally:
        sys.stdout.flush()
        sys.stderr.flush()
        os.dup2(saved_out, 1)
        os.dup2(saved_err, 2)
        os.close(saved_out)
        os.close(saved_err)


def secret_absent(text: str, secret: str) -> None:
    if secret and secret in text:
        fail("output contained the token")
    if "eyJ" in text or "PRIVATE KEY" in text:
        fail("output contained secret material")


def assert_token_absent(text: str) -> None:
    if TOKEN in text or "eyJ" in text:
        fail("output contained the token")


def git_identity(repo: Path) -> None:
    subprocess.run(
        ["git", "-C", str(repo), "config", "user.email", "factory@example.com"],
        check=True,
    )
    subprocess.run(
        ["git", "-C", str(repo), "config", "user.name", "factory"],
        check=True,
    )


def base_env(mode: str, work: Path) -> dict[str, str]:
    env = os.environ.copy()
    env["MODE"] = mode
    env["M6_WORK"] = str(work)
    env["M6_EVIDENCE"] = str(work / "evidence")
    env["M6_SIGNAL"] = str(work / "signal")
    env["M6_RUN_ID"] = "local-s1"
    env["FACTORY_REVIEWERS_ADMINS"] = "m6-reviewer"
    env["FACTORY_API_KEY"] = "abcdef0123456789"
    env.pop("GITHUB_RUN_ID", None)
    env.pop("GITHUB_RUN_ATTEMPT", None)
    return env


def apply_env(env: dict[str, str]) -> None:
    os.environ.clear()
    os.environ.update(env)


def case_url_shape(mode: str) -> None:
    require_mode(mode)
    accepted = [
        "https://github.com/acme/widgets.git",
        "https://github.com/acme/widgets",
        "https://example.invalid:443/m6/factory.git",
    ]
    rejected = [
        "http://github.com/acme/widgets.git",
        "https://user:token@github.com/acme/widgets.git",
        "https://github.com/acme/widgets.git?x=1",
        "https://github.com/acme/widgets.git#f",
        "git@github.com:acme/widgets.git",
        "https://github.com/acme/widgets/extra.git",
        "https://github.com/acme/../widgets.git",
        "",
        "https://github.com/only",
    ]
    for raw in accepted:
        owner, repo = remote.strict_owner_name(raw)
        if f"{owner}/{repo}" != remote.definition_repo(raw):
            fail(f"strict parse disagreed for {raw}")
    for raw in rejected:
        try:
            remote.strict_owner_name(raw)
        except remote.RemoteError:
            continue
        fail(f"accepted {raw}")
    proc = subprocess.run(
        ["go", "run", "./spike/m6/repoparse"],
        cwd=ROOT,
        input=json.dumps(PARITY_INPUTS).encode(),
        capture_output=True,
        check=False,
    )
    if proc.returncode != 0:
        fail("repoparse failed")
    go_rows = json.loads(proc.stdout.decode())
    if len(go_rows) != len(PARITY_INPUTS):
        fail("repoparse row count")
    for row in go_rows:
        try:
            got = remote.definition_repo(row["in"])
            err = ""
        except remote.RemoteError as exc:
            got = ""
            err = exc.reason
        if got != row.get("repo", "") or bool(err) != bool(row.get("err")):
            fail(f"parser mismatch for {row['in']!r}: go {row} py {got!r} {err!r}")
    work = Path(os.environ["M6_CASE_WORK"])
    stub = work / "bin"
    stub.mkdir(parents=True)
    log = work / "argv"
    env = base_env(mode, work)
    env["M6_ARGV_LOG"] = str(log)
    env["PATH"] = f"{stub}:{env['PATH']}"
    env["M6_DEFINITION_GIT_URL"] = "https://user:token@github.com/acme/widgets.git"
    env["M6_GITHUB_API_BASE"] = "https://api.example.invalid"
    write_stub(stub, "git", real_binary("git"))
    write_stub(stub, "curl", real_binary("curl"))
    apply_env(env)
    code = remote.main(["remote.py", "preflight"])
    if code == 0:
        fail("userinfo url passed preflight")
    if log.exists() and log.read_text(encoding="utf-8").strip():
        fail("preflight called git or curl for a bad url")
    reason = (work / "remote-fail").read_text(encoding="utf-8")
    if "userinfo" not in reason and "https://" not in reason:
        fail("preflight reason did not name the url shape")


def case_ls_remote(mode: str) -> None:
    require_mode(mode)
    work = Path(os.environ["M6_CASE_WORK"])
    bare = work / "repo.git"
    bare.parent.mkdir(parents=True, exist_ok=True)
    subprocess.run(
        ["git", "init", "--bare", "-b", "main", str(bare)],
        check=True,
        capture_output=True,
    )
    src = work / "src"
    src.mkdir()
    (src / "file").write_text("one\n", encoding="utf-8")
    subprocess.run(
        ["git", "-C", str(src), "init", "-b", "main"], check=True, capture_output=True
    )
    git_identity(src)
    subprocess.run(["git", "-C", str(src), "add", "file"], check=True)
    subprocess.run(
        ["git", "-C", str(src), "-c", "commit.gpgsign=false", "commit", "-m", "one"],
        check=True,
        capture_output=True,
    )
    subprocess.run(
        ["git", "-C", str(src), "push", str(bare), "HEAD:refs/heads/main"], check=True
    )
    env = base_env(mode, work)
    apply_env(env)
    remote.anonymous_ls_remote(bare.as_uri())
    try:
        remote.anonymous_ls_remote((work / "missing.git").as_uri())
    except remote.RemoteError as exc:
        if exc.reason != remote.PRIVATE_REPO:
            fail("private repo message was not the required sentence")
        return
    fail("missing repo was readable")


def start_forge(
    work: Path, env: dict[str, str]
) -> tuple[subprocess.Popen[bytes], subprocess.Popen[bytes], str]:
    fake_bin = work / "fake-github"
    subprocess.run(
        ["go", "build", "-o", str(fake_bin), "./spike/m4/fakes/github"],
        cwd=ROOT,
        check=True,
        stdout=subprocess.DEVNULL,
    )
    fake_port = free_port()
    forge_port = free_port()
    fake = subprocess.Popen(
        [str(fake_bin), "-addr", f"127.0.0.1:{fake_port}"],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    child = env.copy()
    child["M6_UPSTREAM_GITHUB"] = f"http://127.0.0.1:{fake_port}"
    child["M6_TOKEN_FILE"] = str(work / "captured-token")
    forge = subprocess.Popen(
        [
            sys.executable,
            str(ROOT / "spike" / "m6" / "forge.py"),
            "--addr",
            f"127.0.0.1:{forge_port}",
        ],
        env=child,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    try:
        wait_http(f"http://127.0.0.1:{fake_port}/healthz")
        wait_http(f"http://127.0.0.1:{forge_port}/_spike/m6")
    except SystemExit:
        forge.kill()
        fake.kill()
        raise
    return fake, forge, f"http://127.0.0.1:{forge_port}"


def stop_proc(proc: subprocess.Popen[bytes]) -> None:
    proc.kill()
    proc.wait(timeout=5)


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


def case_mint(mode: str, deny: str) -> None:
    require_mode(mode)
    work = Path(os.environ["M6_CASE_WORK"])
    key = ensure_key(work)
    binary = ensure_apptoken(work)
    env = base_env(mode, work)
    env["M6_FORGE_COLLABORATORS"] = "m6-reviewer"
    if deny:
        env["M6_FORGE_DENY_PERMISSION"] = deny
    fake, forge, base = start_forge(work, env)
    try:
        env["M6_APPTOKEN_BIN"] = str(binary)
        env["M6_GITHUB_API_BASE"] = base
        env["M6_DEFINITION_GIT_URL"] = "https://example.invalid/acme/widgets.git"
        env["FACTORY_GITHUB_APP_ID"] = "17"
        env["FACTORY_GITHUB_APP_PRIVATE_KEY_FILE"] = str(key)
        stub = work / "bin"
        stub.mkdir()
        log = work / "argv"
        env["M6_ARGV_LOG"] = str(log)
        env["PATH"] = f"{stub}:{env['PATH']}"
        write_stub(stub, "git", real_binary("git"), ls_remote_ok=True)
        write_stub(stub, "curl", real_binary("curl"))
        apply_env(env)
        code = call_captured(work, lambda: remote.main(["remote.py", "preflight"]))
        stdout = (work / "captured.out").read_text(encoding="utf-8")
        stderr = (work / "captured.err").read_text(encoding="utf-8")
        argv = log.read_text(encoding="utf-8") if log.exists() else ""
        token_file = work / "github-app-token"
        minted = (
            token_file.read_text(encoding="utf-8").strip()
            if token_file.is_file()
            else ""
        )
        blob = stdout + stderr + argv
        secret_absent(blob, minted)
        if deny:
            if code == 0:
                fail("denied mint succeeded")
            if deny not in stderr or "422" not in stderr:
                fail("denied mint did not name the permission")
            if minted:
                fail("denied mint wrote a token file")
            return
        if code != 0:
            reason = ""
            fail_path = work / "remote-fail"
            if fail_path.exists():
                reason = fail_path.read_text(encoding="utf-8").strip()
            fail(f"mint preflight failed: {reason}")
        names = (
            "app token: ok repo acme/widgets permissions "
            "checks contents issues metadata pull_requests"
        )
        if stdout.strip() != names:
            fail("mint success line was not the permission names")
        if not token_file.is_file() or token_file.stat().st_mode & 0o077:
            fail("token file was not mode 600")
        if not stateless_shape(minted):
            fail(f"minted token was not stateless-shaped (length {len(minted)})")
        for label in (
            "url",
            "ls-remote",
            "mint",
            "token",
            "reviewers",
            "collaborator m6-reviewer",
        ):
            if f"remote preflight: {label} PASS" not in stderr:
                fail(f"preflight missing PASS for {label}")
    finally:
        stop_proc(forge)
        stop_proc(fake)


def case_collaborators(mode: str) -> None:
    require_mode(mode)
    work = Path(os.environ["M6_CASE_WORK"])
    key = ensure_key(work)
    binary = ensure_apptoken(work)
    env = base_env(mode, work)
    env["M6_FORGE_COLLABORATORS"] = "m6-reviewer"
    fake, forge, base = start_forge(work, env)
    try:
        env.update(
            {
                "M6_APPTOKEN_BIN": str(binary),
                "M6_GITHUB_API_BASE": base,
                "M6_DEFINITION_GIT_URL": "https://example.invalid/acme/widgets.git",
                "FACTORY_GITHUB_APP_ID": "17",
                "FACTORY_GITHUB_APP_PRIVATE_KEY_FILE": str(key),
                "FACTORY_REVIEWERS_ADMINS": "m6-reviewer",
            }
        )
        stub = work / "bin"
        stub.mkdir()
        env["PATH"] = f"{stub}:{env['PATH']}"
        env["M6_ARGV_LOG"] = str(work / "argv")
        write_stub(stub, "git", real_binary("git"), ls_remote_ok=True)
        write_stub(stub, "curl", real_binary("curl"))
        apply_env(env)
        code = call_captured(work, lambda: remote.main(["remote.py", "preflight"]))
        if code != 0:
            fail("collaborator 204 was rejected")
        minted = (work / "github-app-token").read_text(encoding="utf-8").strip()
        argv = (
            (work / "argv").read_text(encoding="utf-8")
            if (work / "argv").exists()
            else ""
        )
        captured = (work / "captured.out").read_text(encoding="utf-8") + (
            work / "captured.err"
        ).read_text(encoding="utf-8")
        secret_absent(captured + argv, minted)
        env["M6_FORGE_COLLABORATORS"] = "other-login"
        env["FACTORY_REVIEWERS_ADMINS"] = "m6-reviewer"
        # The forge process already started with the allow list. Restart it.
    finally:
        stop_proc(forge)
        stop_proc(fake)
    env["M6_FORGE_COLLABORATORS"] = "other-login"
    fake, forge, base = start_forge(work, env)
    try:
        env["M6_GITHUB_API_BASE"] = base
        apply_env(env)
        code = remote.main(["remote.py", "preflight"])
        if code == 0:
            fail("collaborator 404 was accepted")
        reason = (work / "remote-fail").read_text(encoding="utf-8")
        if "m6-reviewer" not in reason:
            fail("collaborator failure did not name the login")
    finally:
        stop_proc(forge)
        stop_proc(fake)


class Factory(BaseHTTPRequestHandler):
    batch_id = BATCH
    pr_url = "https://pr.invalid/acme/widgets/pull/1"

    def do_GET(self) -> None:
        if self.path == "/api/v1/factory":
            body = json.dumps([{"id": "fac_m6", "name": "m6"}]).encode()
        elif self.path == "/api/v1/factory/fac_m6/improvement-batches":
            body = json.dumps(
                [
                    {
                        "id": self.batch_id,
                        "status": "pr_open",
                        "pr_url": self.pr_url,
                        "created_at": "2026-10-21T00:00:00Z",
                    }
                ]
            ).encode()
        else:
            self.send_response(404)
            self.end_headers()
            return
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt: str, *args: object) -> None:
        del fmt, args


def serve_factory() -> tuple[ThreadingHTTPServer, int]:
    port = free_port()
    server = ThreadingHTTPServer(("127.0.0.1", port), Factory)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    return server, port


def prepare_definition(work: Path) -> None:
    dest = work / "definition"
    if dest.exists():
        shutil.rmtree(dest)
    shutil.copytree(ROOT / "deploy" / "m6-factory", dest)


def bare_repo(work: Path) -> Path:
    bare = work / "acme" / "widgets.git"
    bare.parent.mkdir(parents=True, exist_ok=True)
    subprocess.run(
        ["git", "init", "--bare", "-b", "main", str(bare)],
        check=True,
        capture_output=True,
    )
    return bare


def seed_env(mode: str, work: Path, api: str) -> dict[str, str]:
    prepare_definition(work)
    bare = bare_repo(work)
    env = base_env(mode, work)
    env["M6_DEFINITION"] = str(work / "definition")
    env["M6_DEFINITION_GIT_URL"] = bare.as_uri()
    env["M6_GITHUB_API_BASE"] = api
    token = work / "github-app-token"
    token.write_text(TOKEN + "\n", encoding="utf-8")
    token.chmod(0o600)
    env["M6_REUSE_TOKEN"] = "1"
    return env


def assert_seed(work: Path, mode: str) -> None:
    require_mode(mode)
    env = seed_env(mode, work, "http://127.0.0.1:9")
    stub = work / "bin"
    stub.mkdir()
    log = work / "argv"
    env["M6_ARGV_LOG"] = str(log)
    env["PATH"] = f"{stub}:{env['PATH']}"
    write_stub(stub, "git", real_binary("git"))
    write_stub(stub, "curl", real_binary("curl"))
    apply_env(env)
    if remote.main(["remote.py", "seed"]) != 0:
        reason = (
            (work / "remote-fail").read_text(encoding="utf-8")
            if (work / "remote-fail").exists()
            else ""
        )
        fail(f"seed failed: {reason}")
    argv = log.read_text(encoding="utf-8")
    assert_token_absent(argv)
    bare = work / "acme" / "widgets.git"
    base = "refs/heads/m6-run/local-s1/main"
    candidate = "refs/heads/m6-run/local-s1/candidate"
    base_sha = subprocess.check_output(
        ["git", "--git-dir", str(bare), "rev-parse", base], text=True
    ).strip()
    candidate_sha = subprocess.check_output(
        ["git", "--git-dir", str(bare), "rev-parse", candidate], text=True
    ).strip()
    if base_sha != candidate_sha:
        fail("candidate was not seeded at the base")
    suite = subprocess.check_output(
        [
            "git",
            "--git-dir",
            str(bare),
            "show",
            f"{base}:benchmarks/convention/suite.yaml",
        ],
        text=True,
    )
    if "definitionRef: m6-run/local-s1/candidate" not in suite:
        fail("pushed suite kept the candidate ref name")
    agent = subprocess.check_output(
        ["git", "--git-dir", str(bare), "show", f"{base}:agents/implementer/agent.md"],
        text=True,
    )
    if "against main" in agent or "m6-run/local-s1/main" not in agent:
        fail("pushed implementer still targets main")
    source = (
        ROOT / "deploy" / "m6-factory" / "benchmarks" / "convention" / "suite.yaml"
    ).read_text(encoding="utf-8")
    if "definitionRef: candidate\n" not in source:
        fail("seed rewrote the source tree")
    ledger = json.loads((work / "remote-ledger.json").read_text(encoding="utf-8"))
    names = {entry["name"] for entry in ledger["entries"]}
    if (
        base.removeprefix("refs/heads/") not in names
        or candidate.removeprefix("refs/heads/") not in names
    ):
        fail("ledger missed a seeded ref")
    if TOKEN in json.dumps(ledger):
        fail("ledger contained the token")


def push_improve(work: Path, branch: str, path: str, text: str) -> str:
    clone = work / f"clone-{branch.replace('/', '-')}"
    bare = work / "acme" / "widgets.git"
    subprocess.run(
        ["git", "clone", "--branch", "m6-run/local-s1/main", bare.as_uri(), str(clone)],
        check=True,
        capture_output=True,
    )
    git_identity(clone)
    target = clone / path
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(text, encoding="utf-8")
    subprocess.run(
        ["git", "-C", str(clone), "checkout", "-b", branch],
        check=True,
        capture_output=True,
    )
    subprocess.run(["git", "-C", str(clone), "add", path], check=True)
    subprocess.run(
        ["git", "-C", str(clone), "-c", "commit.gpgsign=false", "commit", "-m", branch],
        check=True,
        capture_output=True,
    )
    subprocess.run(
        ["git", "-C", str(clone), "push", "origin", f"HEAD:refs/heads/{branch}"],
        check=True,
        capture_output=True,
    )
    return subprocess.check_output(
        ["git", "-C", str(clone), "rev-parse", "HEAD"], text=True
    ).strip()


def forge_call(
    base: str, method: str, path: str, payload: dict | None, token: str
) -> None:
    data = None if payload is None else json.dumps(payload).encode()
    req = urllib.request.Request(
        base + path,
        data=data,
        headers={
            "Authorization": f"Bearer {token}",
            "Content-Type": "application/json",
        },
        method=method,
    )
    with urllib.request.urlopen(req, timeout=5) as res:
        res.read()


def capture_token(base: str, work: Path) -> str:
    req = urllib.request.Request(
        base + "/app/installations/1/access_tokens",
        data=b'{"repositories":["widgets"],"permissions":{"checks":"write"}}',
        headers={"Authorization": "Bearer app", "Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=5) as res:
        token = json.load(res)["token"]
    path = work / "github-app-token"
    path.write_text(token + "\n", encoding="utf-8")
    path.chmod(0o600)
    return token


def case_watch(mode: str, stale: bool) -> None:
    require_mode(mode)
    work = Path(os.environ["M6_CASE_WORK"])
    env = seed_env(mode, work, "http://127.0.0.1:9")
    apply_env(env)
    ledger_missing = not (work / "remote-ledger.json").is_file()
    if ledger_missing and remote.main(["remote.py", "seed"]) != 0:
        reason = ""
        fail_path = work / "remote-fail"
        if fail_path.exists():
            reason = fail_path.read_text(encoding="utf-8").strip()
        fail(f"seed failed: {reason}")
    new_sha = push_improve(
        work,
        f"improve/{BATCH}",
        "skills/cite-sources/SKILL.md",
        "CONVENTION: cite-sources\n",
    )
    old_sha = ""
    if stale:
        old_sha = push_improve(work, f"improve/{OLD_BATCH}", "README.md", "stale\n")
    forge_env = env.copy()
    forge_env["M6_FORGE_COLLABORATORS"] = "m6-reviewer"
    fake, forge, base = start_forge(work, forge_env)
    factory, factory_port = serve_factory()
    try:
        token = capture_token(base, work)
        forge_call(
            base,
            "POST",
            "/repos/acme/widgets/pulls",
            {
                "title": "improve",
                "head": f"improve/{BATCH}",
                "base": "m6-run/local-s1/main",
                "body": "body",
            },
            token,
        )
        forge_call(
            base,
            "POST",
            "/repos/acme/widgets/pulls/1/requested_reviewers",
            {"reviewers": ["m6-reviewer"]},
            token,
        )
        env["M6_GITHUB_API_BASE"] = base
        env["M6_FACTORY_URL"] = f"http://127.0.0.1:{factory_port}"
        ledger_path = work / "remote-ledger.json"
        ledger = json.loads(ledger_path.read_text(encoding="utf-8"))
        ledger["api_base"] = base
        ledger_path.write_text(json.dumps(ledger), encoding="utf-8")
        apply_env(env)
        if remote.main(["remote.py", "watch-once"]) != 0:
            fail("watch failed")
        watch_log = work / "watch.log"
        if watch_log.exists():
            fail("watch failed: " + watch_log.read_text(encoding="utf-8").strip())
        bare = work / "acme" / "widgets.git"
        moved = subprocess.check_output(
            [
                "git",
                "--git-dir",
                str(bare),
                "rev-parse",
                "refs/heads/m6-run/local-s1/candidate",
            ],
            text=True,
        ).strip()
        if moved != new_sha:
            fail("candidate did not move to this run's improve head")
        if stale and moved == old_sha:
            fail("candidate moved to the stale improve ref")
        diff = (work / "evidence" / "skill.diff").read_text(encoding="utf-8")
        if "skills/cite-sources/SKILL.md" not in diff:
            fail("skill diff missed the skill path")
        if stale and "README.md" in diff:
            fail("skill diff included the stale ref")
        view = json.loads(
            (work / "evidence" / "remote-pr.json").read_text(encoding="utf-8")
        )
        if view["state"] != "open" or view["head"] != f"improve/{BATCH}":
            fail("remote-pr head or state")
        if view["base"] != "m6-run/local-s1/main" or not str(
            view["html_url"]
        ).startswith("https://"):
            fail("remote-pr base or url")
        if "m6-reviewer" not in view["reviewers"]:
            fail("remote-pr dropped the reviewer")
        minted = (work / "github-app-token").read_text(encoding="utf-8").strip()
        secret_absent(json.dumps(view) + diff, minted)
        secret_absent(json.dumps(view), TOKEN)
        ready = (
            (work / "signal" / "candidate-ready").read_text(encoding="utf-8").strip()
        )
        if ready != new_sha:
            fail("candidate-ready was not the improve sha")
    finally:
        factory.shutdown()
        stop_proc(forge)
        stop_proc(fake)


def case_cleanup(mode: str) -> None:
    require_mode(mode)
    work = Path(os.environ["M6_CASE_WORK"])
    env = seed_env(mode, work, "http://127.0.0.1:9")
    apply_env(env)
    if remote.main(["remote.py", "seed"]) != 0:
        fail("seed failed")
    push_improve(work, f"improve/{BATCH}", "skills/cite-sources/SKILL.md", "line\n")
    push_improve(work, f"improve/{OLD_BATCH}", "README.md", "stale\n")
    forge_env = env.copy()
    fake, forge, base = start_forge(work, forge_env)
    try:
        token = capture_token(base, work)
        forge_call(
            base,
            "POST",
            "/repos/acme/widgets/pulls",
            {
                "title": "this",
                "head": f"improve/{BATCH}",
                "base": "m6-run/local-s1/main",
                "body": "a",
            },
            token,
        )
        forge_call(
            base,
            "POST",
            "/repos/acme/widgets/pulls",
            {
                "title": "other",
                "head": f"improve/{OLD_BATCH}",
                "base": "main",
                "body": "b",
            },
            token,
        )
        forge_call(
            base,
            "POST",
            "/repos/acme/widgets/pulls/1/requested_reviewers",
            {"reviewers": ["m6-reviewer"]},
            token,
        )
        forge_call(
            base,
            "POST",
            "/repos/acme/widgets/issues/1/labels",
            {"labels": ["privilege-escalation", "needs-human-design", "skill"]},
            token,
        )
        forge_call(
            base,
            "POST",
            "/repos/acme/widgets/issues/2/labels",
            {"labels": ["skill"]},
            token,
        )
        forge_call(
            base,
            "POST",
            "/repos/acme/widgets/pulls",
            {
                "title": "base-main",
                "head": f"improve/{BATCH}",
                "base": "main",
                "body": "c",
            },
            token,
        )
        forge_call(
            base,
            "POST",
            "/repos/acme/widgets/pulls/3/requested_reviewers",
            {"reviewers": ["m6-reviewer"]},
            token,
        )
        forge_call(
            base,
            "POST",
            "/repos/acme/widgets/issues/3/labels",
            {"labels": ["privilege-escalation", "needs-human-design", "skill"]},
            token,
        )
        ledger_path = work / "remote-ledger.json"
        ledger = json.loads(ledger_path.read_text(encoding="utf-8"))
        ledger["api_base"] = base
        ledger["batch_id"] = BATCH
        ledger["entries"].append(
            {"kind": "ref", "name": f"improve/{BATCH}", "phase": "observed"}
        )
        ledger["entries"].append(
            {
                "kind": "pr",
                "number": 1,
                "head": f"improve/{BATCH}",
                "base": "m6-run/local-s1/main",
                "phase": "observed",
            }
        )
        ledger["entries"].append(
            {
                "kind": "pr",
                "number": 3,
                "head": f"improve/{BATCH}",
                "base": "main",
                "phase": "observed",
            }
        )
        ledger_path.write_text(json.dumps(ledger), encoding="utf-8")
        env["M6_GITHUB_API_BASE"] = base
        stub = work / "bin"
        stub.mkdir(exist_ok=True)
        argv_log = work / "argv"
        env["M6_ARGV_LOG"] = str(argv_log)
        env["PATH"] = f"{stub}:{env['PATH']}"
        write_stub(stub, "git", real_binary("git"))
        write_stub(stub, "curl", real_binary("curl"))
        apply_env(env)
        if remote.main(["remote.py", "cleanup"]) != 0:
            reason = (
                (work / "remote-fail").read_text(encoding="utf-8")
                if (work / "remote-fail").exists()
                else ""
            )
            fail(f"cleanup failed: {reason}")
        with urllib.request.urlopen(base + "/_spike/m6", timeout=5) as res:
            state = json.load(res)
        pulls = {pull["number"]: pull for pull in state["pulls"]}
        if (
            pulls[1]["state"] != "closed"
            or pulls[1]["reviewers"]
            or "privilege-escalation" in pulls[1]["labels"]
        ):
            fail("this run's pull request was left open, requested, or labeled")
        if "needs-human-design" in pulls[1]["labels"]:
            fail("needs-human-design remained")
        if "skill" not in pulls[1]["labels"]:
            fail("skill label was removed")
        if pulls[2]["state"] != "open" or "skill" not in pulls[2]["labels"]:
            fail("the other run's pull request was changed")
        if (
            pulls[3]["state"] != "closed"
            or pulls[3]["reviewers"]
            or "privilege-escalation" in pulls[3]["labels"]
            or "needs-human-design" in pulls[3]["labels"]
            or "skill" not in pulls[3]["labels"]
        ):
            fail("matching head on main was not closed cleanly")
        argv = argv_log.read_text(encoding="utf-8") if argv_log.is_file() else ""
        secret_absent(argv, token)
        if "--config" in argv or ".cfg" in argv:
            fail("curl config was a file")
        if "-K" not in argv:
            fail("curl did not read config from stdin")
        bare = work / "acme" / "widgets.git"

        def has(ref: str) -> bool:
            proc = subprocess.run(
                ["git", "--git-dir", str(bare), "rev-parse", "--verify", ref],
                capture_output=True,
                check=False,
            )
            return proc.returncode == 0

        if has("refs/heads/m6-run/local-s1/main") or has(
            "refs/heads/m6-run/local-s1/candidate"
        ):
            fail("run refs were not deleted")
        if has(f"refs/heads/improve/{BATCH}"):
            fail("this run's improve ref was not deleted")
        if not has(f"refs/heads/improve/{OLD_BATCH}"):
            fail("the stale improve ref was deleted")
        mutations = list(state["mutations"])
        stub = work / "bin"
        stub.mkdir(exist_ok=True)
        log = work / "argv-again"
        if log.exists():
            log.unlink()
        env["M6_ARGV_LOG"] = str(log)
        env["PATH"] = f"{stub}:{env['PATH']}"
        blocked = stub / "git"
        blocked.write_text("#!/bin/bash\nexit 99\n", encoding="utf-8")
        blocked.chmod(0o755)
        curl_block = stub / "curl"
        curl_block.write_text("#!/bin/bash\nexit 99\n", encoding="utf-8")
        curl_block.chmod(0o755)
        apply_env(env)
        if remote.main(["remote.py", "cleanup"]) != 0:
            fail("second cleanup failed")
        if log.exists() and log.read_text(encoding="utf-8").strip():
            fail("second cleanup invoked git or curl")
        with urllib.request.urlopen(base + "/_spike/m6", timeout=5) as res:
            again = json.load(res)
        if again["mutations"] != mutations:
            fail("second cleanup made a changing call")
    finally:
        stop_proc(forge)
        stop_proc(fake)


def case_allow(mode: str) -> None:
    require_mode(mode)
    run = "local-s1"
    if not remote.ref_allowed(f"m6-run/{run}/main", run, BATCH):
        fail("run base was refused")
    if not remote.ref_allowed(f"improve/{BATCH}", run, BATCH):
        fail("this run's improve ref was refused")
    for ref in (
        "main",
        "master",
        "m6-run/other/main",
        f"m6-run/{run}/../main",
        f"improve/{OLD_BATCH}",
        "..",
    ):
        if remote.ref_allowed(ref, run, BATCH):
            fail(f"allow-list accepted {ref}")
    for bad_run in ("a/b", "*", "?", " ", "", "..", "/", "a*"):
        if remote.ref_allowed(f"m6-run/{bad_run}/main", bad_run, BATCH):
            fail(f"allow-list accepted run id {bad_run!r}")
    work = Path(os.environ["M6_CASE_WORK"])
    env = base_env(mode, work)
    env["M6_REUSE_TOKEN"] = "1"
    env["M6_GITHUB_API_BASE"] = "http://127.0.0.1:9"
    env["M6_DEFINITION_GIT_URL"] = "file:///tmp/acme/widgets.git"
    token = work / "github-app-token"
    token.write_text(TOKEN + "\n", encoding="utf-8")
    token.chmod(0o600)
    ledger = {
        "version": 1,
        "run_id": run,
        "owner": "acme",
        "repo": "widgets",
        "git_url": "file:///tmp/acme/widgets.git",
        "api_base": "http://127.0.0.1:9",
        "base_ref": f"m6-run/{run}/main",
        "candidate_ref": f"m6-run/{run}/candidate",
        "batch_id": BATCH,
        "entries": [
            {"kind": "ref", "name": name, "phase": "created"}
            for name in ("main", "m6-run/other/x", "..")
        ],
    }
    (work / "remote-ledger.json").write_text(json.dumps(ledger), encoding="utf-8")
    stub = work / "bin"
    stub.mkdir()
    log = work / "argv"
    env["M6_ARGV_LOG"] = str(log)
    env["PATH"] = f"{stub}:{env['PATH']}"
    blocked = stub / "git"
    blocked.write_text(
        '#!/bin/bash\nprintf \'%s\\n\' "$*" >> "$M6_ARGV_LOG"\nexit 99\n',
        encoding="utf-8",
    )
    blocked.chmod(0o755)
    apply_env(env)
    code = remote.main(["remote.py", "cleanup"])
    if code == 0:
        fail("allow-list refusal succeeded")
    err = (work / "remote-fail").read_text(encoding="utf-8")
    if "allow-list" not in err:
        fail("refusal did not name the allow-list")
    if log.exists() and log.read_text(encoding="utf-8").strip():
        fail("refused ref was passed to git")


def case_offline() -> None:
    work = Path(os.environ["M6_CASE_WORK"])
    stub = work / "bin"
    stub.mkdir(parents=True)
    log = work / "argv"
    env = os.environ.copy()
    env["MODE"] = "offline"
    env["M6_WORK"] = str(work)
    env["M6_ARGV_LOG"] = str(log)
    env["PATH"] = f"{stub}:{env['PATH']}"
    for name in ("git", "curl"):
        path = stub / name
        path.write_text(
            '#!/bin/bash\nprintf \'%s\\n\' "$*" >> "$M6_ARGV_LOG"\nexit 99\n',
            encoding="utf-8",
        )
        path.chmod(0o755)
    apply_env(env)
    for command in ("preflight", "seed", "watch-once", "cleanup", "ls-remote"):
        argv = (
            ["remote.py", command]
            if command != "ls-remote"
            else ["remote.py", command, "file:///tmp/nope"]
        )
        if remote.main(argv) != 0:
            fail(f"offline {command} was not a no-op")
    if log.exists() and log.read_text(encoding="utf-8").strip():
        fail("offline called git or curl")


def case_forge_lookup() -> None:
    work = Path(os.environ["M6_CASE_WORK"])
    env = os.environ.copy()
    fake, forge, base = start_forge(work, env)
    try:
        token = capture_token(base, work)

        def call(
            method: str, path: str, payload: dict | None = None
        ) -> tuple[int, object]:
            data = None if payload is None else json.dumps(payload).encode()
            req = urllib.request.Request(
                base + path,
                data=data,
                headers={
                    "Authorization": f"Bearer {token}",
                    "Content-Type": "application/json",
                },
                method=method,
            )
            try:
                with urllib.request.urlopen(req, timeout=5) as res:
                    raw = res.read()
                    return res.status, json.loads(raw) if raw else {}
            except urllib.error.HTTPError as err:
                raw = err.read()
                return err.code, json.loads(raw) if raw else {}

        status, _ = call(
            "POST",
            "/repos/m6/factory/pulls",
            {
                "title": "improver",
                "head": "improve/ibt_batch",
                "base": "main",
                "body": "Opened by the offline improver.",
            },
        )
        if status != 201:
            fail("improver create was not 201")
        product = "## Regressions addressed\n\n- run run_1 \n\nLayer changed: skill\n"
        status, _ = call(
            "POST",
            "/repos/m6/factory/pulls",
            {
                "title": "factoryd",
                "head": "improve/ibt_batch",
                "base": "main",
                "body": product,
            },
        )
        if status != 422:
            fail("factoryd create was not 422")
        status, _ = call(
            "GET", "/repos/m6/factory/pulls?state=open&head=m6:improve/ibt_batch"
        )
        if status != 200:
            fail("lookup failed")
        call(
            "POST",
            "/repos/m6/factory/pulls/1/requested_reviewers",
            {"reviewers": ["m6-reviewer"]},
        )
        with urllib.request.urlopen(base + "/_spike/m6", timeout=5) as res:
            state = json.load(res)
        pulls = state["pulls"]
        if len(pulls) != 1 or pulls[0]["head"] != "improve/ibt_batch":
            fail("lookup path did not keep one pull request")
        creates = pulls[0]["creates"]
        if not any(
            row["status"] == 201 and "Opened by the offline improver." in row["body"]
            for row in creates
        ):
            fail("201 create was not the improver")
        if not any(
            row["status"] == 422
            and "## Regressions addressed" in row["body"]
            and "Layer changed: skill" in row["body"]
            for row in creates
        ):
            fail("422 create dropped the product body")
        heads = [row.get("head") for row in state["lookups"]]
        if "m6:improve/ibt_batch" not in heads:
            fail("lookup was not recorded")
        if "m6-reviewer" not in pulls[0]["reviewers"]:
            fail("reviewer was not requested")
    finally:
        stop_proc(forge)
        stop_proc(fake)


def spike_state(base: str) -> dict:
    with urllib.request.urlopen(base + "/_spike/m6", timeout=5) as res:
        return json.load(res)


def install_tools(work: Path, env: dict[str, str], *, forward_curl: bool) -> Path:
    stub = work / "bin"
    stub.mkdir(exist_ok=True)
    log = work / "argv"
    log.write_text("", encoding="utf-8")
    env["M6_ARGV_LOG"] = str(log)
    base_path = env.get("PATH", "")
    if not base_path.startswith(f"{stub}:"):
        env["PATH"] = f"{stub}:{base_path}"
    git = stub / "git"
    git.write_text(
        '#!/bin/bash\nprintf \'git %s\\n\' "$*" >> "$M6_ARGV_LOG"\nexit 99\n',
        encoding="utf-8",
    )
    git.chmod(0o755)
    curl = stub / "curl"
    if forward_curl:
        real = real_binary("curl")
        curl.write_text(
            "#!/bin/bash\n"
            'printf \'curl %s\\n\' "$*" >> "$M6_ARGV_LOG"\n'
            f'exec {real} "$@"\n',
            encoding="utf-8",
        )
    else:
        curl.write_text(
            '#!/bin/bash\nprintf \'curl %s\\n\' "$*" >> "$M6_ARGV_LOG"\nexit 99\n',
            encoding="utf-8",
        )
    curl.chmod(0o755)
    return log


def argv_text(log: Path) -> str:
    return log.read_text(encoding="utf-8") if log.is_file() else ""


def assert_no_calls(log: Path) -> None:
    if argv_text(log).strip():
        fail("cleanup invoked git or curl")


def open_labeled(base: str, token: str, head: str, base_ref: str) -> None:
    forge_call(
        base,
        "POST",
        "/repos/acme/widgets/pulls",
        {"title": head, "head": head, "base": base_ref, "body": "body"},
        token,
    )


def decorate(base: str, token: str, number: int) -> None:
    forge_call(
        base,
        "POST",
        f"/repos/acme/widgets/pulls/{number}/requested_reviewers",
        {"reviewers": ["m6-reviewer"]},
        token,
    )
    forge_call(
        base,
        "POST",
        f"/repos/acme/widgets/issues/{number}/labels",
        {"labels": ["privilege-escalation", "needs-human-design", "skill"]},
        token,
    )


def assert_untouched(pull: dict, number: int) -> None:
    if pull["state"] != "open" or pull["reviewers"] != ["m6-reviewer"]:
        fail(f"pull {number} was mutated")
    for label in ("privilege-escalation", "needs-human-design", "skill"):
        if label not in pull["labels"]:
            fail(f"pull {number} lost {label}")


def pr_ledger(
    work: Path,
    *,
    run_id: str,
    batch_id: str,
    number: int,
    api: str,
    recorded_head: str,
) -> None:
    ledger = {
        "version": 1,
        "run_id": run_id,
        "owner": "acme",
        "repo": "widgets",
        "git_url": "file:///tmp/acme/widgets.git",
        "api_base": api,
        "base_ref": "m6-run/local-s1/main",
        "candidate_ref": "m6-run/local-s1/candidate",
        "batch_id": batch_id,
        "entries": [
            {
                "kind": "pr",
                "number": number,
                "head": recorded_head,
                "base": "m6-run/local-s1/main",
                "phase": "observed",
            }
        ],
    }
    path = work / "remote-ledger.json"
    path.write_text(json.dumps(ledger), encoding="utf-8")
    path.chmod(0o600)


def assert_get_only(text: str, name: str) -> None:
    if "\ngit " in f"\n{text}" or text.startswith("git "):
        fail(f"{name} invoked git")
    if (
        "DELETE" in text
        or "PATCH" in text
        or "requested_reviewers" in text
        or "/labels/" in text
    ):
        fail(f"{name} mutated the pull request")
    if "--config" in text or ".cfg" in text:
        fail(f"{name} passed a curl config file")
    if "-K" not in text:
        fail(f"{name} did not pass the curl config on stdin")


def case_close_guard(mode: str) -> None:
    require_mode(mode)
    work = Path(os.environ["M6_CASE_WORK"])
    env = base_env(mode, work)
    fake, forge, base = start_forge(work, env)
    try:
        token = capture_token(base, work)
        specs = [
            (f"improve/{OLD_BATCH}", "main"),
            ("feature/unrelated", "main"),
            (f"improve/{BATCH}", "m6-run/local-s1/main"),
            (f"improve/{BATCH}", "release"),
            (f"improve/{BATCH}", "legacy"),
        ]
        for head, base_ref in specs:
            open_labeled(base, token, head, base_ref)
        for number in range(1, 6):
            decorate(base, token, number)
        forge_call(
            base,
            "PATCH",
            "/repos/acme/widgets/pulls/5",
            {"state": "closed"},
            token,
        )
        env["M6_REUSE_TOKEN"] = "1"
        env["M6_GITHUB_API_BASE"] = base
        env["M6_DEFINITION_GIT_URL"] = "file:///tmp/acme/widgets.git"
        log = install_tools(work, env, forward_curl=True)
        apply_env(env)
        recorded = f"improve/{BATCH}"

        def run_cleanup(
            name: str, run_id: str, batch_id: str, number: int, *, code_ok: int
        ) -> str:
            log.write_text("", encoding="utf-8")
            pr_ledger(
                work,
                run_id=run_id,
                batch_id=batch_id,
                number=number,
                api=base,
                recorded_head=recorded,
            )
            fail_path = work / "remote-fail"
            if fail_path.exists():
                fail_path.unlink()
            before = list(spike_state(base)["mutations"])
            code = call_captured(work, lambda: remote.main(["remote.py", "cleanup"]))
            err = (work / "captured.err").read_text(encoding="utf-8")
            secret_absent(err, token)
            if "Traceback" in err:
                fail(f"{name} traceback")
            if code != code_ok:
                reason = (
                    fail_path.read_text(encoding="utf-8") if fail_path.exists() else ""
                )
                fail(f"{name} exited {code}: {reason.strip()}")
            text = argv_text(log)
            secret_absent(text, token)
            after = spike_state(base)
            if after["mutations"] != before:
                fail(f"{name} made a mutating call")
            return err

        err = run_cleanup("old-head", "local-s1", BATCH, 1, code_ok=0)
        if "allow-list" not in err:
            fail("old-head did not log the allow-list")
        assert_get_only(argv_text(log), "old-head")
        err = run_cleanup("unrelated", "local-s1", BATCH, 2, code_ok=0)
        if "allow-list" not in err:
            fail("unrelated did not log the allow-list")
        assert_get_only(argv_text(log), "unrelated")
        err = run_cleanup("empty-batch", "local-s1", "", 3, code_ok=0)
        if "allow-list" not in err:
            fail("empty-batch did not log the allow-list")
        assert_no_calls(log)
        err = run_cleanup("empty-run", "", BATCH, 4, code_ok=1)
        if "allow-list" not in err:
            fail("empty-run did not log the allow-list")
        assert_no_calls(log)
        run_cleanup("missing-pr", "local-s1", BATCH, 99, code_ok=0)
        assert_get_only(argv_text(log), "missing-pr")
        run_cleanup("already-closed", "local-s1", BATCH, 5, code_ok=0)
        assert_get_only(argv_text(log), "already-closed")
        state = spike_state(base)
        pulls = {pull["number"]: pull for pull in state["pulls"]}
        assert_untouched(pulls[1], 1)
        assert_untouched(pulls[2], 2)
        assert_untouched(pulls[3], 3)
        assert_untouched(pulls[4], 4)
        if pulls[5]["state"] != "closed" or pulls[5]["reviewers"] != ["m6-reviewer"]:
            fail("already-closed pull request was mutated")
        if "skill" not in pulls[5]["labels"]:
            fail("already-closed pull request lost skill")
    finally:
        stop_proc(forge)
        stop_proc(fake)


def case_watch_mismatch(mode: str) -> None:
    require_mode(mode)
    work = Path(os.environ["M6_CASE_WORK"])
    env = seed_env(mode, work, "http://127.0.0.1:9")
    apply_env(env)
    if remote.main(["remote.py", "seed"]) != 0:
        reason = ""
        fail_path = work / "remote-fail"
        if fail_path.exists():
            reason = fail_path.read_text(encoding="utf-8").strip()
        fail(f"seed failed: {reason}")
    fake, forge, base = start_forge(work, env)
    Factory.batch_id = BATCH
    Factory.pr_url = "https://pr.invalid/acme/widgets/pull/1"
    factory, factory_port = serve_factory()
    try:
        token = capture_token(base, work)
        open_labeled(base, token, "feature/unrelated", "main")
        decorate(base, token, 1)
        open_labeled(base, token, f"improve/{OLD_BATCH}", "main")
        decorate(base, token, 2)
        ledger_path = work / "remote-ledger.json"
        ledger = json.loads(ledger_path.read_text(encoding="utf-8"))
        ledger["api_base"] = base
        ledger_path.write_text(json.dumps(ledger), encoding="utf-8")
        env["M6_GITHUB_API_BASE"] = base
        env["M6_FACTORY_URL"] = f"http://127.0.0.1:{factory_port}"
        apply_env(env)
        code = call_captured(work, lambda: remote.main(["remote.py", "watch-once"]))
        err = (work / "captured.err").read_text(encoding="utf-8")
        secret_absent(err, token)
        if code != 0:
            fail("watch-mismatch exited")
        watch_log = work / "watch.log"
        logged = watch_log.read_text(encoding="utf-8") if watch_log.is_file() else ""
        if "pull request does not match this run" not in logged:
            fail("watch did not log the mismatch")
        ledger = json.loads(ledger_path.read_text(encoding="utf-8"))
        for entry in ledger["entries"]:
            if entry.get("kind") == "pr" or "number" in entry:
                fail("watch recorded a pull request that does not match")
            if str(entry.get("name") or "").startswith("improve/"):
                fail("watch recorded an improve ref")
        before = list(spike_state(base)["mutations"])
        if remote.main(["remote.py", "cleanup"]) != 0:
            reason = ""
            fail_path = work / "remote-fail"
            if fail_path.exists():
                reason = fail_path.read_text(encoding="utf-8").strip()
            fail(f"cleanup after mismatch failed: {reason}")
        after = spike_state(base)
        if after["mutations"] != before:
            fail("cleanup closed a pull request the watch rejected")
        pulls = {pull["number"]: pull for pull in after["pulls"]}
        assert_untouched(pulls[1], 1)
        assert_untouched(pulls[2], 2)
    finally:
        factory.shutdown()
        stop_proc(forge)
        stop_proc(fake)


def commit_ref(work: Path, bare: Path, ref: str) -> str:
    git = real_binary("git")
    src = work / "src"
    if src.exists():
        shutil.rmtree(src)
    src.mkdir()
    (src / "file").write_text("one\n", encoding="utf-8")
    subprocess.run(
        [git, "-C", str(src), "init", "-b", "main"], check=True, capture_output=True
    )
    git_identity(src)
    subprocess.run([git, "-C", str(src), "add", "file"], check=True)
    subprocess.run(
        [git, "-C", str(src), "-c", "commit.gpgsign=false", "commit", "-m", "one"],
        check=True,
        capture_output=True,
    )
    subprocess.run(
        [git, "-C", str(src), "push", str(bare), f"HEAD:refs/heads/{ref}"],
        check=True,
        capture_output=True,
    )
    return git


def case_ledger_run(mode: str) -> None:
    require_mode(mode)
    work = Path(os.environ["M6_CASE_WORK"])
    bare = bare_repo(work)
    git = commit_ref(work, bare, "m6-run/a/b/main")
    env = base_env(mode, work)
    env["M6_GITHUB_API_BASE"] = "http://127.0.0.1:9"
    log = install_tools(work, env, forward_curl=False)
    apply_env(env)

    def once(run_id: str, batch_id: str, name: str) -> None:
        ledger = {
            "version": 1,
            "run_id": run_id,
            "owner": "acme",
            "repo": "widgets",
            "git_url": bare.as_uri(),
            "api_base": "http://127.0.0.1:9",
            "base_ref": "m6-run/local-s1/main",
            "candidate_ref": "m6-run/local-s1/candidate",
            "batch_id": batch_id,
            "entries": [{"kind": "ref", "name": name, "phase": "created"}],
        }
        (work / "remote-ledger.json").write_text(json.dumps(ledger), encoding="utf-8")
        log.write_text("", encoding="utf-8")
        fail_path = work / "remote-fail"
        if fail_path.exists():
            fail_path.unlink()
        try:
            code = call_captured(work, lambda: remote.main(["remote.py", "cleanup"]))
        except (json.JSONDecodeError, TypeError) as exc:
            fail(f"ledger id {run_id!r} {batch_id!r} raised {type(exc).__name__}")
        err = (work / "captured.err").read_text(encoding="utf-8")
        if "Traceback" in err or "JSONDecodeError" in err or "TypeError" in err:
            fail(f"ledger id {run_id!r} {batch_id!r} traceback")
        if code == 0:
            fail(f"ledger id {run_id!r} {batch_id!r} was accepted")
        reason = fail_path.read_text(encoding="utf-8") if fail_path.exists() else ""
        if "allow-list" not in reason:
            fail(f"ledger id {run_id!r} {batch_id!r} did not name the allow-list")
        assert_no_calls(log)
        secret_absent(err + reason, TOKEN)

    for run_id in ("a/b", "*", "?", " ", "", "..", "/", "a*"):
        once(run_id, BATCH, f"m6-run/{run_id}/main")
    once("local-s1", "*", "m6-run/local-s1/main")
    once("local-s1", "ibt_a/b", "m6-run/local-s1/main")
    proc = subprocess.run(
        [
            git,
            "--git-dir",
            str(bare),
            "rev-parse",
            "--verify",
            "refs/heads/m6-run/a/b/main",
        ],
        capture_output=True,
        check=False,
    )
    if proc.returncode != 0:
        fail("run id a/b deleted m6-run/a/b/main")


def case_ledger_corrupt(mode: str) -> None:
    require_mode(mode)
    work = Path(os.environ["M6_CASE_WORK"])
    env = base_env(mode, work)
    env["M6_GITHUB_API_BASE"] = "http://127.0.0.1:9"
    log = install_tools(work, env, forward_curl=False)
    apply_env(env)
    bodies = [
        "{",
        "[]",
        json.dumps({"version": 1, "run_id": "local-s1", "entries": None}),
    ]
    for body in bodies:
        (work / "remote-ledger.json").write_text(body, encoding="utf-8")
        log.write_text("", encoding="utf-8")
        fail_path = work / "remote-fail"
        if fail_path.exists():
            fail_path.unlink()
        try:
            code = call_captured(work, lambda: remote.main(["remote.py", "cleanup"]))
        except (json.JSONDecodeError, TypeError) as exc:
            fail(f"corrupt ledger raised {type(exc).__name__}")
        err = (work / "captured.err").read_text(encoding="utf-8")
        if "Traceback" in err or "JSONDecodeError" in err or "TypeError" in err:
            fail("corrupt ledger traceback")
        if code == 0:
            fail("corrupt ledger was accepted")
        reason = fail_path.read_text(encoding="utf-8") if fail_path.exists() else ""
        if "remote ledger is invalid" not in reason:
            fail("corrupt ledger message was not remote ledger is invalid")
        assert_no_calls(log)


class Stall:
    def __init__(self) -> None:
        self.sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        self.sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        self.sock.bind(("127.0.0.1", 0))
        self.sock.listen(32)
        self.port = int(self.sock.getsockname()[1])
        self.held: list[socket.socket] = []
        self.closed = False
        self.thread = threading.Thread(target=self._accept, daemon=True)
        self.thread.start()

    def _accept(self) -> None:
        self.sock.settimeout(0.2)
        while not self.closed:
            try:
                conn, _addr = self.sock.accept()
            except TimeoutError:
                continue
            except OSError:
                return
            self.held.append(conn)

    def url(self) -> str:
        return f"http://127.0.0.1:{self.port}"

    def stop(self) -> None:
        self.closed = True
        self.sock.close()
        for conn in self.held:
            conn.close()


def within(limit: float, fn):
    outcome: dict[str, object] = {}

    def target() -> None:
        outcome["value"] = fn()

    thread = threading.Thread(target=target, daemon=True)
    started = time.monotonic()
    thread.start()
    thread.join(limit)
    elapsed = time.monotonic() - started
    if thread.is_alive():
        fail(f"still running after {elapsed:.2f}s")
    if "value" not in outcome:
        fail("cleanup raised")
    return outcome["value"], elapsed


def bound_ledger(work: Path, git_url: str) -> None:
    ledger = {
        "version": 1,
        "run_id": "local-s1",
        "owner": "acme",
        "repo": "widgets",
        "git_url": git_url,
        "api_base": "http://127.0.0.1:9",
        "base_ref": "m6-run/local-s1/main",
        "candidate_ref": "m6-run/local-s1/candidate",
        "batch_id": BATCH,
        "entries": [
            {"kind": "ref", "name": "m6-run/local-s1/main", "phase": "created"}
        ],
    }
    path = work / "remote-ledger.json"
    path.write_text(json.dumps(ledger), encoding="utf-8")
    path.chmod(0o600)


def assert_bounded(elapsed: float, what: str) -> None:
    if elapsed < 3 or elapsed >= 6.5:
        fail(f"{what} finished in {elapsed:.2f}s")


def case_cleanup_bound_git(mode: str) -> None:
    require_mode(mode)
    work = Path(os.environ["M6_CASE_WORK"])
    stall = Stall()
    try:
        bare = bare_repo(work)
        git = commit_ref(work, bare, "m6-run/local-s1/main")
        env = base_env(mode, work)
        env["M6_REUSE_TOKEN"] = "1"
        env["M6_GITHUB_API_BASE"] = "http://127.0.0.1:9"
        token = work / "github-app-token"
        token.write_text(TOKEN + "\n", encoding="utf-8")
        token.chmod(0o600)
        bound_ledger(work, f"{stall.url()}/acme/widgets.git")
        apply_env(env)
        code, elapsed = within(6.5, lambda: remote.main(["remote.py", "cleanup"]))
        print(f"bounded git in {elapsed:.2f}s")
        assert_bounded(elapsed, "stalled delete")
        if code == 0:
            fail("stalled delete succeeded")
        reason = ""
        fail_path = work / "remote-fail"
        if fail_path.exists():
            reason = fail_path.read_text(encoding="utf-8")
        if "timed out" not in reason and "delete " not in reason:
            fail("stalled delete did not time out")
        secret_absent(reason, TOKEN)
        proc = subprocess.run(
            [
                git,
                "--git-dir",
                str(bare),
                "rev-parse",
                "--verify",
                "refs/heads/m6-run/local-s1/main",
            ],
            capture_output=True,
            check=False,
        )
        if proc.returncode != 0:
            fail("stalled delete removed the ref")
    finally:
        stall.stop()


def case_cleanup_bound_mint(mode: str) -> None:
    require_mode(mode)
    work = Path(os.environ["M6_CASE_WORK"])
    key = ensure_key(work)
    binary = ensure_apptoken(work)
    stall = Stall()
    try:
        env = base_env(mode, work)
        env["M6_APPTOKEN_BIN"] = str(binary)
        env["M6_GITHUB_API_BASE"] = stall.url()
        env["FACTORY_GITHUB_APP_ID"] = "17"
        env["FACTORY_GITHUB_APP_PRIVATE_KEY_FILE"] = str(key)
        log = install_tools(work, env, forward_curl=False)
        bound_ledger(work, "file:///tmp/acme/widgets.git")
        apply_env(env)
        code, elapsed = within(6.5, lambda: remote.main(["remote.py", "cleanup"]))
        print(f"bounded mint in {elapsed:.2f}s")
        assert_bounded(elapsed, "stalled mint")
        if code == 0:
            fail("stalled mint succeeded")
        reason = ""
        fail_path = work / "remote-fail"
        if fail_path.exists():
            reason = fail_path.read_text(encoding="utf-8")
        if reason.strip() != "mint timed out":
            fail("stalled mint did not time out")
        assert_no_calls(log)
        if "PRIVATE KEY" in reason or "ghs_" in reason:
            fail("mint timeout leaked secret material")
    finally:
        stall.stop()


def stateless_shape(token: str) -> bool:
    if len(token) < 480 or token.count(".") != 2:
        return False
    if not token.startswith("ghs_") or "-" not in token:
        return False
    return not any(ch.isspace() for ch in token)


def sample_stateless() -> str:
    header = "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9"
    alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-"
    payload = "".join(alphabet[i % len(alphabet)] for i in range(200))
    sig = ("A" * 100) + "_" + ("B" * 100) + "-" + ("C" * 70)
    return f"ghs_1234567_{header}.{payload}.{sig}"


LEGACY_TOKEN = "ghs_" + ("Ab" * 18)


class EchoBearer(BaseHTTPRequestHandler):
    got = ""

    def do_GET(self) -> None:
        if self.path == "/healthz":
            body = b"ok\n"
            self.send_response(200)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        EchoBearer.got = self.headers.get("Authorization", "")
        self.send_response(204)
        self.end_headers()

    def log_message(self, fmt: str, *args: object) -> None:
        del fmt, args


def assert_round_trip(path: Path, token: str) -> None:
    remote.write_secret(path, token)
    if remote.read_secret(path) != token:
        fail("token did not round-trip")
    if path.stat().st_mode & 0o777 != 0o600:
        fail("token file was not mode 600")
    raw = path.read_bytes()
    if raw != (token + "\n").encode() or raw.count(b"\n") != 1 or b" " in raw:
        fail("token file was not the token plus one newline")


def assert_rejected(path: Path, token: str) -> None:
    if path.exists():
        path.unlink()
    try:
        remote.write_secret(path, token)
    except remote.RemoteError as exc:
        if exc.reason != "token file is invalid":
            fail("rejected token used the wrong reason")
    else:
        fail("invalid token was accepted")
    if path.exists():
        fail("rejected token was written")


def case_opaque_token(mode: str) -> None:
    require_mode(mode)
    work = Path(os.environ["M6_CASE_WORK"])
    env = base_env(mode, work)
    apply_env(env)
    stateless = sample_stateless()
    if not stateless_shape(stateless) or len(stateless) < 500:
        fail(f"stateless fixture length {len(stateless)}")
    if len(LEGACY_TOKEN) != 40:
        fail(f"legacy fixture length {len(LEGACY_TOKEN)}")
    assert_round_trip(work / "stateless", stateless)
    assert_round_trip(work / "legacy", LEGACY_TOKEN)
    wide = "b" * 2048
    assert_round_trip(work / "wide", wide)
    for bad in (
        "has space",
        "has\nline",
        "has\r",
        "has\t",
        "",
        "\n",
        "abc\x00def",
        "abc\x7fdef",
        "a" * 8193,
    ):
        assert_rejected(work / "rejected", bad)
    embedded = work / "embedded"
    embedded.write_text("ab\ncd\n", encoding="utf-8")
    embedded.chmod(0o600)
    try:
        remote.read_secret(embedded)
    except remote.RemoteError as exc:
        if exc.reason != "token file is invalid":
            fail("newline token used the wrong reason")
    else:
        fail("newline token was read")
    remote.write_secret(remote.token_path(), stateless)
    basic = base64.b64encode(f"x-access-token:{stateless}".encode()).decode()
    covered = remote.redact(f"see {stateless} and {basic} end")
    if stateless in covered or basic in covered or "[REDACTED]" not in covered:
        fail("redact left the token or its basic-auth form")
    secret_absent(covered, stateless)
    port = free_port()
    server = ThreadingHTTPServer(("127.0.0.1", port), EchoBearer)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        wait_http(f"http://127.0.0.1:{port}/healthz")
        quoted = 'ghs_quo"te\\and.dot_-'
        for token in (stateless, LEGACY_TOKEN, wide, quoted):
            EchoBearer.got = ""
            status, _body = remote.request("GET", f"http://127.0.0.1:{port}/use", token)
            if status != 204 or EchoBearer.got != f"Bearer {token}":
                fail("request did not present the token")
        EchoBearer.got = ""
        try:
            remote.request("GET", f"http://127.0.0.1:{port}/use", "has\nline")
        except remote.RemoteError as exc:
            if exc.reason != "token file is invalid":
                fail("request accepted a newline token")
        else:
            fail("request accepted a newline token")
        if EchoBearer.got:
            fail("rejected request reached the server")
    finally:
        server.shutdown()
        server.server_close()


def case_preflight_all(mode: str) -> None:
    require_mode(mode)
    work = Path(os.environ["M6_CASE_WORK"])
    stub = work / "bin"
    stub.mkdir()
    log = work / "argv"
    env = base_env(mode, work)
    env["M6_ARGV_LOG"] = str(log)
    env["PATH"] = f"{stub}:{env['PATH']}"
    env["M6_DEFINITION_GIT_URL"] = "https://user:token@github.com/acme/widgets.git"
    env["M6_GITHUB_API_BASE"] = "https://api.example.invalid"
    env["FACTORY_REVIEWERS_ADMINS"] = ""
    write_stub(stub, "git", real_binary("git"))
    write_stub(stub, "curl", real_binary("curl"))
    apply_env(env)
    code = call_captured(work, lambda: remote.main(["remote.py", "preflight"]))
    stdout = (work / "captured.out").read_text(encoding="utf-8")
    stderr = (work / "captured.err").read_text(encoding="utf-8")
    secret_absent(stdout + stderr, "")
    assert_token_absent(stdout + stderr)
    fails = [
        line
        for line in stderr.splitlines()
        if line.startswith("remote preflight:") and " FAIL " in line
    ]
    if code == 0 or len(fails) != 2:
        fail(f"expected 2 FAIL lines and a non-zero exit, got {len(fails)} and {code}")
    if not any(line.startswith("remote preflight: url FAIL") for line in fails):
        fail("url check did not FAIL")
    if not any(line.startswith("remote preflight: reviewers FAIL") for line in fails):
        fail("reviewers check did not FAIL")
    for skip in (
        "remote preflight: ls-remote SKIP (needs url)",
        "remote preflight: mint SKIP (needs url)",
        "remote preflight: token SKIP (needs mint)",
        "remote preflight: collaborators SKIP (needs token)",
    ):
        if skip not in stderr:
            fail(f"missing {skip}")
    if log.exists() and log.read_text(encoding="utf-8").strip():
        fail("preflight called git or curl")
    if stdout.strip():
        fail("preflight wrote stdout")
    text = (work / "remote-fail").read_text(encoding="utf-8")
    if "userinfo" not in text and "https://" not in text:
        fail("remote-fail missed the url")
    if "FACTORY_REVIEWERS_ADMINS" not in text:
        fail("remote-fail missed reviewers")
    secret_absent(text, "")


def main() -> None:
    name = sys.argv[1]
    mode = sys.argv[2] if len(sys.argv) > 2 else ""
    work = Path(os.environ["M6_CASE_WORK"])
    work.mkdir(parents=True, exist_ok=True)
    if name == "url-shape":
        case_url_shape(mode)
    elif name == "ls-remote":
        case_ls_remote(mode)
    elif name == "mint-ok":
        case_mint(mode, "")
    elif name == "mint-deny":
        case_mint(mode, "checks")
    elif name == "collaborators":
        case_collaborators(mode)
    elif name == "seed-move":
        assert_seed(work, mode)
        case_watch(mode, False)
    elif name == "watch-stale":
        case_watch(mode, True)
    elif name == "cleanup":
        case_cleanup(mode)
    elif name == "allow":
        case_allow(mode)
    elif name == "token-argv":
        assert_seed(work, mode)
    elif name == "close-guard":
        case_close_guard(mode)
    elif name == "watch-mismatch":
        case_watch_mismatch(mode)
    elif name == "ledger-run":
        case_ledger_run(mode)
    elif name == "ledger-corrupt":
        case_ledger_corrupt(mode)
    elif name == "cleanup-bound-git":
        case_cleanup_bound_git(mode)
    elif name == "cleanup-bound-mint":
        case_cleanup_bound_mint(mode)
    elif name == "opaque-token":
        case_opaque_token(mode)
    elif name == "preflight-all":
        case_preflight_all(mode)
    elif name == "offline":
        case_offline()
    elif name == "forge-lookup":
        case_forge_lookup()
    else:
        fail(f"unknown case {name}")


if __name__ == "__main__":
    main()
