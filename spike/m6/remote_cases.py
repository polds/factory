#!/usr/bin/env python3
"""Cases for spike/m6/run_test.sh. No live network and no real App."""

from __future__ import annotations

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
        if not minted.startswith("ghs_"):
            fail("mint did not stay in the token file")
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
        ledger_path.write_text(json.dumps(ledger), encoding="utf-8")
        env["M6_GITHUB_API_BASE"] = base
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
        if pulls[2]["state"] != "open" or "skill" not in pulls[2]["labels"]:
            fail("the other run's pull request was changed")
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
    elif name == "offline":
        case_offline()
    elif name == "forge-lookup":
        case_forge_lookup()
    else:
        fail(f"unknown case {name}")


if __name__ == "__main__":
    main()
