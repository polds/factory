#!/usr/bin/env python3
"""Live and live-ci forge calls for spike/m6.

One function per step: preflight, seed, watch, cleanup, and the ledger.
Python holds the JSON ledger and the URL parser. MODE=offline returns
before any git or curl call. The GitHub token stays in a mode-600 file
and is handed to git through GIT_CONFIG_* and to curl on stdin
(`curl -K -`), never through argv or a config file.
"""

from __future__ import annotations

import base64
import json
import os
import re
import secrets
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import urllib.parse
from pathlib import Path

PERMISSION_NAMES = "checks contents issues metadata pull_requests"
RUN_ID_RE = re.compile(r"^[a-z0-9-]{1,80}$")
BATCH_RE = re.compile(r"^ibt_[0-9A-Za-z]+$")
SECRET_RE = re.compile(r"^[A-Za-z0-9_-]+$")
HOST_RE = re.compile(r"^[A-Za-z0-9.-]+(?::[0-9]+)?$")
SEG_RE = re.compile(r"^[A-Za-z0-9._-]+$")
LOGIN_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9-]*$")
LABELS_TO_REMOVE = ("privilege-escalation", "needs-human-design")
PRIVATE_REPO = (
    "definition repo must be readable without credentials "
    "(factoryd fetches without auth)"
)


class RemoteError(Exception):
    def __init__(
        self,
        reason: str,
        status: str = "",
        body: str = "",
        headers: list[str] | None = None,
    ) -> None:
        super().__init__(reason)
        self.reason = reason
        self.status = status
        self.body = body
        self.headers = headers or []


def live_mode() -> bool:
    return os.environ.get("MODE", "offline") in {"live", "live-ci"}


def repo_root() -> Path:
    return Path(__file__).resolve().parents[2]


def work_dir() -> Path:
    raw = os.environ.get("M6_WORK", "")
    if raw:
        return Path(raw)
    return Path(__file__).resolve().parent / ".work"


def evidence_dir() -> Path:
    raw = os.environ.get("M6_EVIDENCE", "")
    if raw:
        return Path(raw)
    return work_dir() / "evidence"


def signal_dir() -> Path:
    raw = os.environ.get("M6_SIGNAL", "")
    if raw:
        return Path(raw)
    return work_dir() / "signal"


def definition_dir() -> Path:
    raw = os.environ.get("M6_DEFINITION", "")
    if raw:
        return Path(raw)
    return work_dir() / "definition"


def token_path() -> Path:
    return work_dir() / "github-app-token"


def ledger_path() -> Path:
    return work_dir() / "remote-ledger.json"


def definition_repo(raw: str) -> str:
    """Owner/name, matching internal/server/m6_tokens.go definitionRepo."""
    text = raw.strip()
    if text == "":
        raise RemoteError("server: definition git url is empty")
    text = text.removesuffix(".git")
    path = text
    parsed = urllib.parse.urlparse(text)
    if parsed.netloc:
        path = parsed.path.strip("/")
    else:
        index = text.rfind(":")
        if index >= 0:
            path = text[index + 1 :].strip("/")
    parts = path.split("/")
    if len(parts) < 2 or parts[-1] == "" or parts[-2] == "":
        raise RemoteError("server: definition git url has no repository")
    return parts[-2] + "/" + parts[-1]


def strict_owner_name(raw: str) -> tuple[str, str]:
    parsed = urllib.parse.urlsplit(raw.strip())
    if (
        parsed.scheme != "https"
        or parsed.username
        or parsed.password
        or parsed.query
        or parsed.fragment
        or not HOST_RE.fullmatch(parsed.netloc or "")
    ):
        raise RemoteError(
            "definition git url must be https://<host>/<owner>/<name>[.git] "
            "with no userinfo, query, or fragment"
        )
    path = parsed.path
    path = path.removesuffix(".git")
    parts = [part for part in path.split("/") if part]
    if len(parts) != 2 or any(SEG_RE.fullmatch(part) is None for part in parts):
        raise RemoteError(
            "definition git url must be https://<host>/<owner>/<name>[.git] "
            "with no userinfo, query, or fragment"
        )
    repo = definition_repo(raw)
    if repo != f"{parts[0]}/{parts[1]}":
        raise RemoteError("definition git url did not parse to its owner/name")
    return parts[0], parts[1]


def validate_run_id(run: str) -> str:
    if not RUN_ID_RE.fullmatch(run):
        raise RemoteError("run id is invalid")
    return run


def new_run_id() -> str:
    chosen = os.environ.get("M6_RUN_ID", "").strip()
    if chosen:
        return validate_run_id(chosen)
    gh_run = os.environ.get("GITHUB_RUN_ID", "").strip()
    attempt = os.environ.get("GITHUB_RUN_ATTEMPT", "").strip()
    if re.fullmatch(r"[0-9]+", gh_run) and re.fullmatch(r"[0-9]+", attempt):
        return validate_run_id(f"ci-{gh_run}-{attempt}")
    return validate_run_id("local-" + secrets.token_hex(4))


def ref_allowed(ref: str, run_id: str, batch_id: str) -> bool:
    try:
        validate_run_id(run_id)
    except RemoteError:
        return False
    if ref in {"", "main", "master", "HEAD"} or ".." in ref.split("/"):
        return False
    prefix = f"m6-run/{run_id}/"
    if ref.startswith(prefix):
        rest = ref[len(prefix) :]
        if rest and SEG_RE.fullmatch(rest):
            return True
    return bool(
        batch_id and BATCH_RE.fullmatch(batch_id) and ref == f"improve/{batch_id}"
    )


def shown_id(value: str) -> str:
    return value if value else "(empty)"


def refuse_id(value: str) -> None:
    shown = shown_id(value)
    refuse(shown)
    raise RemoteError(f"remote ref refused: {shown}: allow-list")


def accepted_run_id(run_id: str) -> str:
    try:
        return validate_run_id(run_id)
    except RemoteError:
        refuse_id(run_id)
        raise


def accepted_batch_id(batch_id: str) -> None:
    if batch_id and not BATCH_RE.fullmatch(batch_id):
        refuse_id(batch_id)


def refuse(ref: str) -> None:
    sys.stderr.write(f"remote ref refused: {ref}: allow-list\n")


def split_csv(raw: str) -> list[str]:
    out = []
    for part in raw.split(","):
        part = part.strip()
        if part:
            out.append(part)
    return out


def load_ledger() -> dict | None:
    path = ledger_path()
    if not path.is_file():
        return None
    try:
        doc = json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        raise RemoteError("remote ledger is invalid") from exc
    if not isinstance(doc, dict):
        raise RemoteError("remote ledger is invalid")
    entries = doc.get("entries")
    if not isinstance(entries, list) or any(
        not isinstance(entry, dict) for entry in entries
    ):
        raise RemoteError("remote ledger is invalid")
    return doc


def save_ledger(doc: dict) -> None:
    path = ledger_path()
    path.parent.mkdir(parents=True, exist_ok=True)
    payload = json.dumps(doc, indent=2) + "\n"
    fd, tmp_name = tempfile.mkstemp(dir=path.parent, prefix=".ledger-")
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as fh:
            fh.write(payload)
        os.chmod(tmp_name, 0o600)
        os.replace(tmp_name, path)
    except Exception:
        if os.path.exists(tmp_name):
            os.unlink(tmp_name)
        raise
    os.chmod(path, 0o600)


def blank_ledger(run: str, owner: str, repo: str, git_url: str, api: str) -> dict:
    return {
        "version": 1,
        "run_id": run,
        "owner": owner,
        "repo": repo,
        "git_url": git_url,
        "api_base": api,
        "base_ref": f"m6-run/{run}/main",
        "candidate_ref": f"m6-run/{run}/candidate",
        "batch_id": "",
        "entries": [],
    }


def read_secret(path: Path) -> str:
    if not path.is_file():
        raise RemoteError("token file is missing")
    token = path.read_text(encoding="utf-8").strip()
    if not SECRET_RE.fullmatch(token):
        raise RemoteError("token file is invalid")
    return token


def write_secret(path: Path, token: str) -> None:
    if not SECRET_RE.fullmatch(token):
        raise RemoteError("token file is invalid")
    path.parent.mkdir(parents=True, exist_ok=True)
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as fh:
        fh.write(token)
        fh.write("\n")


def redact(text: str) -> str:
    secrets_found = []
    if token_path().is_file():
        raw = token_path().read_text(encoding="utf-8").strip()
        if len(raw) >= 4:
            secrets_found.append(raw)
            basic = base64.b64encode(f"x-access-token:{raw}".encode()).decode()
            secrets_found.append(basic)
    out = text
    for secret in sorted(set(secrets_found), key=len, reverse=True):
        out = out.replace(secret, "[REDACTED]")
    return out


def probe_report(kind: str, status: str, body: str, headers: list[str]) -> None:
    driver = repo_root() / "spike" / "lib" / "driver.sh"
    fd, name = tempfile.mkstemp(prefix="m6-probe-")
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as fh:
            fh.write(redact(body))
        os.chmod(name, 0o600)
        cmd = [
            "bash",
            "-c",
            '. "$1"; shift; probe_report "$@"',
            "probe",
            str(driver),
            kind,
            status,
            name,
            "",
            *headers,
        ]
        subprocess.run(cmd, check=False)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def record_error(err: RemoteError) -> None:
    path = work_dir() / "remote-fail"
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(err.reason + "\n")
    os.chmod(path, 0o600)
    if err.status or err.body:
        probe_report(err.reason, err.status or "error", err.body, err.headers)
    else:
        sys.stderr.write(err.reason + "\n")


def git_env(token: str, origin: str) -> dict[str, str]:
    env = os.environ.copy()
    for key in list(env):
        if key.startswith("GIT_CONFIG_"):
            del env[key]
    basic = base64.b64encode(f"x-access-token:{token}".encode()).decode()
    env["GIT_TERMINAL_PROMPT"] = "0"
    env["GIT_CONFIG_COUNT"] = "2"
    env["GIT_CONFIG_KEY_0"] = f"http.{origin}.extraheader"
    env["GIT_CONFIG_VALUE_0"] = f"Authorization: Basic {basic}"
    env["GIT_CONFIG_KEY_1"] = "safe.directory"
    env["GIT_CONFIG_VALUE_1"] = "*"
    return env


def _num(value: float) -> str:
    rounded = round(value)
    if abs(value - rounded) < 1e-9:
        return str(int(rounded))
    text = f"{value:.3f}".rstrip("0").rstrip(".")
    return text or "0"


def call_limits() -> tuple[float, float]:
    """Return (max_time, connect_timeout). A set deadline shrinks both."""
    raw = os.environ.get("M6_CLEANUP_DEADLINE", "").strip()
    if raw == "":
        return 5.0, 2.0
    try:
        deadline = float(raw)
    except ValueError:
        return 5.0, 2.0
    left = deadline - time.time()
    if left <= 0:
        raise RemoteError("remote cleanup incomplete")
    return min(5.0, left), min(2.0, left)


def run_git(
    args: list[str],
    token: str,
    origin: str,
    cwd: Path | None = None,
    *,
    timeout: float | None = None,
    http_bound: bool = False,
    connect_timeout: float | None = None,
) -> subprocess.CompletedProcess[str]:
    cmd = ["git", "-c", "protocol.file.allow=always"]
    if http_bound:
        connect = 2.0 if connect_timeout is None else connect_timeout
        cmd.extend(
            [
                "-c",
                f"http.connectTimeout={_num(connect)}",
                "-c",
                "http.lowSpeedLimit=1",
                "-c",
                "http.lowSpeedTime=5",
            ]
        )
    if cwd is not None:
        cmd.extend(["-C", str(cwd)])
    cmd.extend(args)
    try:
        return subprocess.run(
            cmd,
            env=git_env(token, origin),
            capture_output=True,
            text=True,
            check=False,
            timeout=timeout,
        )
    except subprocess.TimeoutExpired:
        raise RemoteError("delete timed out") from None


def git_ok(proc: subprocess.CompletedProcess[str], what: str) -> None:
    if proc.returncode == 0:
        return
    detail = (proc.stderr or proc.stdout or "").strip()
    raise RemoteError(what, status=str(proc.returncode), body=detail)


def api_base() -> str:
    raw = os.environ.get("M6_GITHUB_API_BASE", "").strip().rstrip("/")
    if raw == "":
        raise RemoteError("M6_GITHUB_API_BASE is empty")
    return raw


def git_url() -> str:
    raw = os.environ.get("M6_DEFINITION_GIT_URL", "").strip()
    if raw == "":
        raise RemoteError("M6_DEFINITION_GIT_URL is empty")
    parsed = urllib.parse.urlsplit(raw)
    if parsed.username or parsed.password or parsed.query or parsed.fragment:
        raise RemoteError(
            "definition git url must be https://<host>/<owner>/<name>[.git] "
            "with no userinfo, query, or fragment"
        )
    if parsed.scheme == "https":
        strict_owner_name(raw)
        return raw
    if parsed.scheme == "file" and parsed.path:
        return raw
    raise RemoteError(
        "definition git url must be https://<host>/<owner>/<name>[.git] "
        "with no userinfo, query, or fragment"
    )


def request(
    method: str, url: str, secret: str, payload: dict | None = None
) -> tuple[int, bytes]:
    if not SECRET_RE.fullmatch(secret):
        raise RemoteError("token file is invalid")
    work_dir().mkdir(parents=True, exist_ok=True)
    body_file = work_dir() / f".curl-{secrets.token_hex(4)}.body"
    data_file: Path | None = None
    # The bearer header stays in memory and is passed on stdin. It is not
    # written to a config file.
    config = "\n".join(
        [
            f'header = "Authorization: Bearer {secret}"',
            'header = "Accept: application/vnd.github+json"',
            'header = "Content-Type: application/json"',
        ]
    )
    max_time, connect = call_limits()
    cmd = [
        "curl",
        "-q",
        "-K",
        "-",
        "-sS",
        "--connect-timeout",
        _num(connect),
        "-m",
        _num(max_time),
        "-o",
        str(body_file),
        "-w",
        "%{http_code}",
        "-X",
        method,
        "--url",
        url,
    ]
    try:
        if payload is not None:
            data_file = work_dir() / f".curl-{secrets.token_hex(4)}.json"
            data_file.write_text(json.dumps(payload), encoding="utf-8")
            os.chmod(data_file, 0o600)
            cmd.extend(["--data-binary", f"@{data_file}"])
        proc = subprocess.run(
            cmd, input=config.encode(), capture_output=True, check=False
        )
        code = proc.stdout.decode("utf-8", "replace").strip()
        body = body_file.read_bytes() if body_file.is_file() else b""
    finally:
        body_file.unlink(missing_ok=True)
        if data_file is not None:
            data_file.unlink(missing_ok=True)
    if not code.isdigit():
        detail = proc.stderr.decode("utf-8", "replace")
        raise RemoteError(
            "github request failed",
            status=f"curl-{proc.returncode}",
            body=detail,
            headers=["Authorization", "Accept", "Content-Type"],
        )
    return int(code), body


def http_error(what: str, status: int, body: bytes) -> RemoteError:
    return RemoteError(
        what,
        status=str(status),
        body=body.decode("utf-8", "replace"),
        headers=["Authorization", "Accept", "Content-Type"],
    )


def anonymous_ls_remote(url: str) -> None:
    env = os.environ.copy()
    for key in list(env):
        if key.startswith("GIT_CONFIG_"):
            del env[key]
    env["GIT_TERMINAL_PROMPT"] = "0"
    env["GIT_ASKPASS"] = ""
    env["GIT_CONFIG_COUNT"] = "1"
    env["GIT_CONFIG_KEY_0"] = "credential.helper"
    env["GIT_CONFIG_VALUE_0"] = ""
    cmd = [
        "git",
        "-c",
        "credential.helper=",
        "-c",
        "http.extraHeader=",
        "-c",
        "http.connectTimeout=2",
        "-c",
        "protocol.file.allow=always",
        "ls-remote",
        url,
        "HEAD",
    ]
    try:
        proc = subprocess.run(
            cmd,
            env=env,
            capture_output=True,
            text=True,
            timeout=8,
            check=False,
        )
    except subprocess.TimeoutExpired:
        raise RemoteError(
            PRIVATE_REPO, status="timeout", body="ls-remote timed out"
        ) from None
    if proc.returncode != 0:
        detail = (proc.stderr or proc.stdout or "").strip()
        raise RemoteError(PRIVATE_REPO, status=str(proc.returncode), body=detail)


def build_apptoken() -> Path:
    override = os.environ.get("M6_APPTOKEN_BIN", "").strip()
    if override:
        return Path(override)
    dest = work_dir() / "apptoken"
    dest.parent.mkdir(parents=True, exist_ok=True)
    goos = subprocess.run(
        ["go", "env", "GOOS"], capture_output=True, text=True, check=False
    )
    goarch = subprocess.run(
        ["go", "env", "GOARCH"], capture_output=True, text=True, check=False
    )
    if goos.returncode != 0 or goarch.returncode != 0:
        raise RemoteError(
            "go env failed", status="go", body=(goos.stderr or goarch.stderr)
        )
    env = os.environ.copy()
    env["CGO_ENABLED"] = "0"
    env["GOOS"] = goos.stdout.strip()
    env["GOARCH"] = goarch.stdout.strip()
    proc = subprocess.run(
        ["go", "build", "-o", str(dest), "./spike/m4/apptoken"],
        cwd=repo_root(),
        env=env,
        capture_output=True,
        text=True,
        check=False,
    )
    if proc.returncode != 0:
        raise RemoteError(
            "apptoken build failed", status=str(proc.returncode), body=proc.stderr
        )
    return dest


def status_from_text(text: str) -> str:
    match = re.search(r"HTTP (\d+)", text)
    if match:
        return match.group(1)
    match = re.search(r"\b(\d{3})\b", text)
    if match:
        return match.group(1)
    return "mint"


def mint_token(
    owner: str, repo: str, dest: Path, *, timeout: float | None = None
) -> None:
    binary = build_apptoken()
    env = os.environ.copy()
    env["FACTORY_GITHUB_API_BASE"] = api_base()
    try:
        proc = subprocess.run(
            [str(binary), "-repo", f"{owner}/{repo}"],
            env=env,
            capture_output=True,
            check=False,
            timeout=timeout,
        )
    except subprocess.TimeoutExpired:
        raise RemoteError("mint timed out") from None
    stderr = proc.stderr.decode("utf-8", "replace")
    if proc.returncode != 0:
        raise RemoteError(
            "mint failed",
            status=status_from_text(stderr),
            body=stderr,
            headers=[
                "Authorization",
                "Accept",
                "Content-Type",
                "User-Agent",
                "X-GitHub-Api-Version",
            ],
        )
    token = proc.stdout.decode("utf-8", "replace").strip()
    write_secret(dest, token)
    print(f"app token: ok repo {owner}/{repo} permissions {PERMISSION_NAMES}")


def reviewer_logins() -> list[str]:
    logins = split_csv(os.environ.get("FACTORY_REVIEWERS_ADMINS", ""))
    if not logins:
        raise RemoteError("FACTORY_REVIEWERS_ADMINS is empty")
    for login in logins:
        if not LOGIN_RE.fullmatch(login):
            raise RemoteError(f"collaborator {login} was not accepted")
    return logins


def preflight() -> None:
    if not live_mode():
        return
    raw = os.environ.get("M6_DEFINITION_GIT_URL", "")
    owner, repo = strict_owner_name(raw)
    anonymous_ls_remote(raw.strip())
    mint_token(owner, repo, token_path())
    secret = read_secret(token_path())
    base = api_base()
    for login in reviewer_logins():
        status, body = request(
            "GET",
            f"{base}/repos/{owner}/{repo}/collaborators/{login}",
            secret,
        )
        if status != 204:
            raise http_error(f"collaborator {login} was not accepted", status, body)


def rewrite_pushed_copy(dest: Path, base_ref: str, candidate_ref: str) -> None:
    suite = dest / "benchmarks" / "convention" / "suite.yaml"
    agent = dest / "agents" / "implementer" / "agent.md"
    suite_text = suite.read_text(encoding="utf-8")
    old_ref = "definitionRef: candidate\n"
    new_ref = f"definitionRef: {candidate_ref}\n"
    if old_ref not in suite_text:
        raise RemoteError("suite definitionRef was not the candidate ref")
    suite.write_text(suite_text.replace(old_ref, new_ref, 1), encoding="utf-8")
    agent_text = agent.read_text(encoding="utf-8")
    old_clone = "1. Clone the repository in DEFINITION_GIT_URL.\n"
    new_clone = (
        f"1. Clone the repository in DEFINITION_GIT_URL and check out {base_ref}.\n"
    )
    old_pr = "5. Open a pull request against main at GITHUB_API_BASE using GH_TOKEN.\n"
    new_pr = f"5. Open a pull request against {base_ref} at GITHUB_API_BASE using GH_TOKEN.\n"
    if old_clone not in agent_text or old_pr not in agent_text:
        raise RemoteError("implementer steps did not name main")
    agent_text = agent_text.replace(old_clone, new_clone, 1).replace(old_pr, new_pr, 1)
    if "against main" in agent_text:
        raise RemoteError("implementer steps still open a pull request against main")
    agent.write_text(agent_text, encoding="utf-8")


def note_ref(ledger: dict, name: str, phase: str) -> None:
    if not ref_allowed(name, ledger["run_id"], ledger.get("batch_id") or ""):
        refuse(name)
        raise RemoteError(f"remote ref refused: {name}: allow-list")
    for entry in ledger["entries"]:
        if entry.get("kind") == "ref" and entry.get("name") == name:
            entry["phase"] = phase
            save_ledger(ledger)
            return
    ledger["entries"].append({"kind": "ref", "name": name, "phase": phase})
    save_ledger(ledger)


def push_ref(
    repo: Path, url: str, token: str, src: str, dest_ref: str, ledger: dict
) -> None:
    note_ref(ledger, dest_ref, "pending")
    proc = run_git(["push", url, f"{src}:refs/heads/{dest_ref}"], token, url, cwd=repo)
    git_ok(proc, f"push {dest_ref} failed")
    note_ref(ledger, dest_ref, "created")


def seed() -> None:
    if not live_mode():
        return
    url = git_url()
    owner, repo = definition_repo(url).split("/", 1)
    run = new_run_id()
    existing = load_ledger()
    if existing and existing.get("run_id"):
        run = validate_run_id(str(existing["run_id"]))
    base_ref = f"m6-run/{run}/main"
    candidate_ref = f"m6-run/{run}/candidate"
    if not ref_allowed(base_ref, run, "") or not ref_allowed(candidate_ref, run, ""):
        raise RemoteError("run ref is not in the allow-list")
    src = definition_dir()
    if not src.is_dir():
        raise RemoteError("definition copy is missing")
    dest = work_dir() / "remote-seed"
    if dest.exists():
        shutil.rmtree(dest)
    shutil.copytree(src, dest, ignore=shutil.ignore_patterns(".git"))
    rewrite_pushed_copy(dest, base_ref, candidate_ref)
    token = read_secret(token_path())
    for args in (
        ["init", "-b", "main"],
        ["add", "."],
    ):
        git_ok(run_git(args, token, url, cwd=dest), "seed git failed")
    git_ok(
        run_git(
            [
                "-c",
                "user.email=factory@example.com",
                "-c",
                "user.name=factory",
                "-c",
                "commit.gpgsign=false",
                "commit",
                "-m",
                "m6 factory",
            ],
            token,
            url,
            cwd=dest,
        ),
        "seed commit failed",
    )
    ledger = blank_ledger(run, owner, repo, url, api_base())
    save_ledger(ledger)
    push_ref(dest, url, token, "HEAD", base_ref, ledger)
    push_ref(dest, url, token, "HEAD", candidate_ref, ledger)
    ref_file = work_dir() / "m6-definition-ref"
    ref_file.write_text(base_ref + "\n", encoding="utf-8")
    os.chmod(ref_file, 0o600)


def factory_base() -> str:
    raw = os.environ.get("M6_FACTORY_URL", "").strip().rstrip("/")
    if raw == "":
        raise RemoteError("M6_FACTORY_URL is empty")
    return raw


def factory_get(path: str) -> object:
    key = os.environ.get("FACTORY_API_KEY", "").strip()
    status, body = request("GET", factory_base() + path, key)
    if status != 200:
        raise http_error("factory request failed", status, body)
    return json.loads(body.decode("utf-8"))


def pr_number(url: str) -> int:
    path = urllib.parse.urlparse(url).path.rstrip("/")
    part = path.rsplit("/", 1)[-1]
    if not part.isdigit():
        raise RemoteError("pull request url has no number")
    return int(part)


def choose_batch(rows: object, want: str) -> dict | None:
    if not isinstance(rows, list):
        raise RemoteError("improvement batches were not a list")
    chosen: dict | None = None
    for row in rows:
        if not isinstance(row, dict):
            continue
        if row.get("status") != "pr_open":
            continue
        batch_id = str(row.get("id") or "")
        pr_url = str(row.get("pr_url") or "")
        if not BATCH_RE.fullmatch(batch_id) or pr_url == "":
            continue
        if want and batch_id != want:
            continue
        if chosen is None or str(row.get("created_at") or "") >= str(
            chosen.get("created_at") or ""
        ):
            chosen = row
    return chosen


def observe_batch(ledger: dict, batch_id: str, number: int, base_ref: str) -> None:
    if ledger.get("batch_id") not in {"", batch_id}:
        raise RemoteError("batch id changed")
    ledger["batch_id"] = batch_id
    improve = f"improve/{batch_id}"
    if not ref_allowed(improve, ledger["run_id"], batch_id):
        refuse(improve)
        raise RemoteError(f"remote ref refused: {improve}: allow-list")
    numbers = {
        entry.get("number") for entry in ledger["entries"] if entry.get("kind") == "pr"
    }
    if number not in numbers:
        ledger["entries"].append(
            {
                "kind": "pr",
                "number": number,
                "head": improve,
                "base": base_ref,
                "phase": "observed",
            }
        )
    names = {
        entry.get("name") for entry in ledger["entries"] if entry.get("kind") == "ref"
    }
    if improve not in names:
        ledger["entries"].append({"kind": "ref", "name": improve, "phase": "observed"})
    save_ledger(ledger)


def publish(state: dict) -> None:
    evidence = evidence_dir()
    signal = signal_dir()
    evidence.mkdir(parents=True, exist_ok=True)
    signal.mkdir(parents=True, exist_ok=True)
    (evidence / "skill.diff").write_text(state["diff"], encoding="utf-8")
    remote_pr = evidence / "remote-pr.json"
    remote_pr.write_text(json.dumps(state["pr"], indent=2) + "\n", encoding="utf-8")
    os.chmod(remote_pr, 0o600)
    (signal / "candidate-ready").write_text(state["sha"] + "\n", encoding="utf-8")


def pull_view(doc: object) -> dict:
    if not isinstance(doc, dict):
        raise RemoteError("pull request was not an object")
    head = doc.get("head")
    base = doc.get("base")
    head_ref = head.get("ref") if isinstance(head, dict) else ""
    base_ref = base.get("ref") if isinstance(base, dict) else ""
    reviewers = []
    for row in doc.get("requested_reviewers") or []:
        if isinstance(row, dict) and isinstance(row.get("login"), str):
            reviewers.append(row["login"])
    labels = []
    for row in doc.get("labels") or []:
        if isinstance(row, dict) and isinstance(row.get("name"), str):
            labels.append(row["name"])
    html = str(doc.get("html_url") or "")
    if not html.startswith("https://"):
        raise RemoteError("pull request url is not https")
    return {
        "number": doc.get("number"),
        "html_url": html,
        "head": head_ref,
        "base": base_ref,
        "state": doc.get("state"),
        "reviewers": reviewers,
        "labels": labels,
    }


def watch_once() -> None:
    if not live_mode():
        return
    if (signal_dir() / "stop").is_file():
        return
    saved = work_dir() / "watch-state.json"
    if saved.is_file():
        publish(json.loads(saved.read_text(encoding="utf-8")))
        return
    ledger = load_ledger()
    if ledger is None:
        raise RemoteError("remote ledger is missing")
    factories = factory_get("/api/v1/factory")
    factory_id = ""
    if isinstance(factories, list):
        for row in factories:
            if isinstance(row, dict) and row.get("name") == "m6":
                factory_id = str(row.get("id") or "")
    if factory_id == "":
        return
    rows = factory_get(f"/api/v1/factory/{factory_id}/improvement-batches")
    batch = choose_batch(rows, str(ledger.get("batch_id") or ""))
    if batch is None:
        return
    batch_id = str(batch["id"])
    number = pr_number(str(batch["pr_url"]))
    improve = f"improve/{batch_id}"
    token = read_secret(token_path())
    status, body = request(
        "GET",
        f"{ledger['api_base']}/repos/{ledger['owner']}/{ledger['repo']}/pulls/{number}",
        token,
    )
    if status != 200:
        raise http_error("pull request lookup failed", status, body)
    try:
        parsed = json.loads(body.decode("utf-8"))
    except json.JSONDecodeError as exc:
        raise RemoteError("pull request lookup failed") from exc
    view = pull_view(parsed)
    if (
        view["head"] != improve
        or view["base"] != ledger["base_ref"]
        or view["state"] != "open"
    ):
        raise RemoteError("pull request does not match this run")
    observe_batch(ledger, batch_id, number, ledger["base_ref"])
    url = str(ledger["git_url"])
    scratch = work_dir() / "fetch"
    if scratch.exists():
        shutil.rmtree(scratch)
    scratch.mkdir(parents=True)
    git_ok(
        run_git(["init", "-b", "main"], token, url, cwd=scratch), "fetch init failed"
    )
    fetch = run_git(
        [
            "fetch",
            url,
            f"refs/heads/{ledger['base_ref']}:refs/heads/m6base",
            f"refs/heads/{improve}:refs/heads/m6improve",
        ],
        token,
        url,
        cwd=scratch,
    )
    git_ok(fetch, "fetch failed")
    diff = run_git(["diff", "m6base...m6improve"], token, url, cwd=scratch)
    git_ok(diff, "diff failed")
    sha_proc = run_git(["rev-parse", "m6improve"], token, url, cwd=scratch)
    git_ok(sha_proc, "rev-parse failed")
    sha = sha_proc.stdout.strip()
    push_ref(scratch, url, token, sha, ledger["candidate_ref"], ledger)
    state = {"diff": diff.stdout, "sha": sha, "pr": view}
    saved.write_text(json.dumps(state), encoding="utf-8")
    os.chmod(saved, 0o600)
    publish(state)


def delete_missing(stderr: str) -> bool:
    text = stderr.lower()
    return (
        "remote ref does not exist" in text
        or "unable to delete" in text
        or "does not match any" in text
        or "couldn't find remote ref" in text
    )


def _named(rows: object, key: str) -> set[str]:
    found: set[str] = set()
    if not isinstance(rows, list):
        return found
    for row in rows:
        if not isinstance(row, dict):
            continue
        value = row.get(key)
        if isinstance(value, str) and value:
            found.add(value)
    return found


def porcelain_deleted(stdout: str) -> set[str]:
    done: set[str] = set()
    for line in stdout.splitlines():
        if "\t" not in line:
            continue
        parts = line.split("\t")
        flag = parts[0]
        if flag == "!":
            continue
        refspec = parts[1] if len(parts) > 1 else ""
        target = refspec.split(":", 1)[1] if ":" in refspec else refspec
        if target.startswith("refs/heads/"):
            done.add(target[len("refs/heads/") :])
    return done


def delete_refs(ledger: dict, names: list[str]) -> set[str]:
    run_id = accepted_run_id(str(ledger.get("run_id") or ""))
    batch_id = str(ledger.get("batch_id") or "")
    accepted_batch_id(batch_id)
    for name in names:
        if not ref_allowed(name, run_id, batch_id):
            refuse(name)
            raise RemoteError(f"remote ref refused: {name}: allow-list")
    if not names:
        return set()
    token = read_secret(token_path())
    url = str(ledger["git_url"])
    specs = [f":refs/heads/{name}" for name in names]
    budgeted = os.environ.get("M6_CLEANUP_DEADLINE", "").strip() != ""
    timeout = 5.0
    connect: float | None = None
    if budgeted:
        timeout, connect = call_limits()
    proc = run_git(
        ["push", "--porcelain", url, *specs],
        token,
        url,
        timeout=timeout,
        http_bound=True,
        connect_timeout=connect,
    )
    done = porcelain_deleted(proc.stdout or "")
    if proc.returncode == 0:
        return done or set(names)
    if done:
        return done
    if delete_missing(proc.stderr or ""):
        return set(names)
    git_ok(proc, "delete refs failed")
    return done


def ensure_cleanup_token(ledger: dict) -> None:
    path = token_path()
    if path.is_file() and (time.time() - path.stat().st_mtime) < 50 * 60:
        read_secret(path)
        return
    binary = work_dir() / "apptoken"
    if binary.is_file():
        os.environ["M6_APPTOKEN_BIN"] = str(binary)
        limit, _connect = call_limits()
        mint_token(str(ledger["owner"]), str(ledger["repo"]), path, timeout=limit)
        return
    raise RemoteError("remote cleanup incomplete")


def pull_document(body: bytes) -> dict:
    try:
        doc = json.loads(body.decode("utf-8"))
    except json.JSONDecodeError as exc:
        raise RemoteError("pull request read failed") from exc
    if not isinstance(doc, dict):
        raise RemoteError("pull request read failed")
    return doc


def live_identity(doc: dict, owner: str) -> str:
    # head.ref is the branch. head.label is owner:branch when the forge sends it.
    head = doc.get("head")
    if not isinstance(head, dict):
        return ""
    label = head.get("label")
    if isinstance(label, str) and label:
        return label
    ref = head.get("ref")
    if not isinstance(ref, str) or ref == "":
        return ""
    prefix = f"{owner}:"
    if ref.startswith(prefix):
        return ref
    return prefix + ref


def head_matches(doc: dict, owner: str, batch_id: str) -> bool:
    expected = f"{owner}:improve/{batch_id}"
    branch = f"improve/{batch_id}"
    head = doc.get("head")
    if not isinstance(head, dict):
        return False
    label = head.get("label")
    ref = head.get("ref")
    ref_s = ref if isinstance(ref, str) else ""
    if isinstance(label, str) and label:
        return label == expected and ref_s in {"", branch, expected}
    return ref_s in {branch, expected}


def accept_status(status: int, ok: set[int], what: str, body: bytes) -> None:
    if status not in ok:
        raise http_error(what, status, body)


def close_pr(ledger: dict, entry: dict) -> None:
    run_id = str(ledger.get("run_id") or "")
    batch_id = str(ledger.get("batch_id") or "")
    owner = str(ledger.get("owner") or "")
    if not RUN_ID_RE.fullmatch(run_id) or not BATCH_RE.fullmatch(batch_id):
        shown = run_id if not RUN_ID_RE.fullmatch(run_id) else batch_id
        refuse(shown_id(shown))
        return
    token = read_secret(token_path())
    number = int(entry["number"])
    base = f"{ledger['api_base']}/repos/{owner}/{ledger['repo']}"
    status, body = request("GET", f"{base}/pulls/{number}", token)
    if status == 404:
        return
    if status != 200:
        raise http_error("pull request read failed", status, body)
    doc = pull_document(body)
    if doc.get("state") != "open":
        return
    if not head_matches(doc, owner, batch_id):
        refuse(live_identity(doc, owner) or f"{owner}:improve/{batch_id}")
        return
    present_logins = _named(doc.get("requested_reviewers"), "login")
    needed = [login for login in reviewer_logins() if login in present_logins]
    if needed:
        status, body = request(
            "DELETE",
            f"{base}/pulls/{number}/requested_reviewers",
            token,
            {"reviewers": needed},
        )
        accept_status(status, {200, 404, 422}, "reviewer removal failed", body)
    present_labels = _named(doc.get("labels"), "name")
    for label in LABELS_TO_REMOVE:
        if label not in present_labels:
            continue
        status, body = request(
            "DELETE",
            f"{base}/issues/{number}/labels/{urllib.parse.quote(label)}",
            token,
        )
        accept_status(status, {200, 404, 422}, "label removal failed", body)
    status, body = request(
        "PATCH", f"{base}/pulls/{number}", token, {"state": "closed"}
    )
    accept_status(status, {200, 422}, "pull request close failed", body)


def cleanup_key(entry: dict) -> tuple[int, str]:
    if entry.get("kind") == "pr":
        return (0, "")
    name = str(entry.get("name") or "")
    if name.startswith("improve/"):
        return (1, name)
    return (2, name)


def remint(ledger: dict) -> None:
    if os.environ.get("M6_REUSE_TOKEN") == "1" and token_path().is_file():
        read_secret(token_path())
        return
    mint_token(str(ledger["owner"]), str(ledger["repo"]), token_path(), timeout=5)


def cleanup() -> None:
    # The hook has already ignored INT and TERM in the shell. A second
    # Ctrl-C still reaches this process group; don't raise from it.
    signal.signal(signal.SIGINT, signal.SIG_IGN)
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
    if not live_mode():
        return
    ledger = load_ledger()
    if ledger is None:
        raise RemoteError("remote ledger is missing")
    pending = [entry for entry in ledger["entries"] if entry.get("phase") != "cleaned"]
    if not pending:
        return
    accepted_run_id(str(ledger.get("run_id") or ""))
    accepted_batch_id(str(ledger.get("batch_id") or ""))
    budgeted = os.environ.get("M6_CLEANUP_DEADLINE", "").strip() != ""
    if budgeted:
        ensure_cleanup_token(ledger)
    else:
        remint(ledger)
    refs: list[dict] = []
    for entry in sorted(pending, key=cleanup_key):
        if budgeted:
            call_limits()
        if entry.get("kind") == "pr":
            close_pr(ledger, entry)
            entry["phase"] = "cleaned"
            save_ledger(ledger)
        elif entry.get("kind") == "ref":
            refs.append(entry)
        else:
            raise RemoteError("remote ledger entry is invalid")
    if not refs:
        return
    if budgeted:
        call_limits()
    deleted = delete_refs(ledger, [str(entry.get("name") or "") for entry in refs])
    for entry in refs:
        if str(entry.get("name") or "") in deleted:
            entry["phase"] = "cleaned"
    save_ledger(ledger)
    if any(entry.get("phase") != "cleaned" for entry in refs):
        raise RemoteError("remote cleanup incomplete")


def remember_watch_error(err: RemoteError) -> None:
    log = work_dir() / "watch.log"
    line = err.reason + "\n"
    prev = log.read_text(encoding="utf-8") if log.is_file() else ""
    if line not in prev:
        sys.stderr.write(line)
        log.parent.mkdir(parents=True, exist_ok=True)
        log.write_text(prev + line, encoding="utf-8")


def main(argv: list[str]) -> int:
    if len(argv) < 2:
        sys.stderr.write("usage: remote.py preflight|seed|watch-once|cleanup\n")
        return 2
    command = argv[1]
    if not live_mode():
        return 0
    try:
        if command == "preflight":
            preflight()
        elif command == "seed":
            seed()
        elif command == "watch-once":
            try:
                watch_once()
            except RemoteError as err:
                remember_watch_error(err)
        elif command == "cleanup":
            cleanup()
        elif command == "ls-remote":
            anonymous_ls_remote(argv[2])
        else:
            sys.stderr.write(f"unknown command {command}\n")
            return 2
    except RemoteError as err:
        record_error(err)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
