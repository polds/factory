#!/usr/bin/env python3
"""Scrub M6 evidence before it is uploaded.

Replaces Authorization and Bearer values, the console API key, x-litellm-api-key
values, GitHub tokens, and JWT-shaped strings in the HAR, the trace zip, and
every other file in the evidence directory. A later scan exits 1 when any of
those remain, and removes the directory so an upload cannot publish them.
The script prints pattern classes and file names, never the secret text.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import sys
import zipfile
from pathlib import Path
from urllib.parse import quote

PLACEHOLDER = "[REDACTED]"
BEARER_PLACEHOLDER = "Bearer [REDACTED]"
HEADER_OK = {PLACEHOLDER, BEARER_PLACEHOLDER, "REDACTED"}
MIN_SECRET = 8

GITHUB_TOKEN = re.compile(r"(?:ghs_|ghp_|github_pat_)[A-Za-z0-9_]{16,}")
JWT = re.compile(r"eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}")
BEARER = re.compile(r"(?i)(Bearer\s+)(?!\[REDACTED\])([^\"'\s\\]{4,})")
AUTH_LINE = re.compile(
    r"(?i)((?:^|[^A-Za-z0-9_\"]))authorization(\s*:\s*)(?!\[REDACTED\])([^\r\n]+)"
)
LITELLM_LINE = re.compile(r"(?i)(x-litellm-api-key\s*:\s*)(?!\[REDACTED\])(\S+)")
ENV_SECRET_NAMES = (
    "FACTORY_API_KEY",
    "M6_API_KEY",
    "M1_OPENAI_API_KEY",
    "FACTORY_INFERENCE_OPENAI_API_KEY",
    "FACTORY_WORKER_API_KEY",
    "S3_SECRET_KEY",
)
EXTRA_HEADER_NAMES = (
    "FACTORY_INFERENCE_OPENAI_COMPATIBLE_EXTRA_HEADERS",
    "M1_OPENAI_EXTRA_HEADERS",
)


def load_secrets(key_file: str) -> list[str]:
    found: list[str] = []

    def add(value: str) -> None:
        text = value.strip()
        if len(text) >= MIN_SECRET:
            found.append(text)

    add(read_key_file(key_file))
    for name in ENV_SECRET_NAMES:
        add(os.environ.get(name, ""))
    for name in os.environ.get("EVIDENCE_EXTRA_SECRETS", "").split():
        add(os.environ.get(name, ""))
    for name in EXTRA_HEADER_NAMES:
        raw = os.environ.get(name, "")
        if "\r" in raw:
            continue
        for line in raw.split("\n"):
            if ":" not in line:
                continue
            value = line.split(":", 1)[1].strip()
            add(value)
            if value.lower().startswith("bearer "):
                add(value[7:])
    unique = sorted(set(found), key=len, reverse=True)
    return unique


def read_key_file(path: str) -> str:
    if path == "":
        return ""
    file = Path(path)
    if not file.is_file():
        return ""
    return file.read_text(encoding="utf-8").strip()


def secret_forms(secret: str) -> list[str]:
    forms = [secret]
    quoted = quote(secret, safe="")
    if quoted != secret:
        forms.append(quoted)
    escaped = json.dumps(secret)[1:-1]
    if escaped != secret:
        forms.append(escaped)
    return forms


def scrub_named_header(text: str, header: str) -> str:
    name = re.escape(header)
    text = re.sub(
        rf'(?i)("name"\s*:\s*"{name}"\s*,\s*"value"\s*:\s*")([^"]*)(")',
        rf"\1{PLACEHOLDER}\3",
        text,
    )
    text = re.sub(
        rf'(?i)("value"\s*:\s*")([^"]*)("\s*,\s*"name"\s*:\s*"{name}")',
        rf"\1{PLACEHOLDER}\3",
        text,
    )
    return text


def redact_text(text: str, secrets: list[str]) -> str:
    for secret in secrets:
        for form in secret_forms(secret):
            text = text.replace(form, PLACEHOLDER)
    text = GITHUB_TOKEN.sub(PLACEHOLDER, text)
    text = JWT.sub(PLACEHOLDER, text)
    text = scrub_named_header(text, "authorization")
    text = scrub_named_header(text, "x-litellm-api-key")
    text = LITELLM_LINE.sub(rf"\1{PLACEHOLDER}", text)
    text = AUTH_LINE.sub(rf"\1Authorization\2{PLACEHOLDER}", text)
    text = BEARER.sub(rf"\1{PLACEHOLDER}", text)
    return text


def redact_bytes(data: bytes, secrets: list[str]) -> bytes:
    text = data.decode("latin-1")
    cleaned = redact_text(text, secrets)
    if cleaned == text:
        return data
    return cleaned.encode("latin-1")


def is_zip(path: Path) -> bool:
    if path.suffix.lower() == ".zip":
        return True
    try:
        with path.open("rb") as fh:
            return fh.read(4) == b"PK\x03\x04"
    except OSError:
        return False


def rewrite_zip(path: Path, secrets: list[str]) -> None:
    tmp = path.with_name(path.name + ".next")
    changed = False
    with zipfile.ZipFile(path) as zin, zipfile.ZipFile(tmp, "w") as zout:
        for info in zin.infolist():
            data = zin.read(info.filename)
            new = redact_bytes(data, secrets)
            if new != data:
                changed = True
            out = zipfile.ZipInfo(filename=info.filename, date_time=info.date_time)
            out.compress_type = zipfile.ZIP_DEFLATED
            out.external_attr = info.external_attr
            zout.writestr(out, new)
    if changed:
        os.replace(tmp, path)
    else:
        tmp.unlink()


def redact_tree(root: Path, secrets: list[str]) -> None:
    for path in sorted(root.rglob("*")):
        if not path.is_file() or path.name.endswith(".next"):
            continue
        if is_zip(path):
            try:
                rewrite_zip(path, secrets)
            except zipfile.BadZipFile:
                pass
            continue
        data = path.read_bytes()
        new = redact_bytes(data, secrets)
        if new == data:
            continue
        tmp = path.with_name(path.name + ".next")
        tmp.write_bytes(new)
        os.replace(tmp, path)


def header_values(text: str, header: str) -> list[str]:
    name = re.escape(header)
    found: list[str] = []
    for pattern in (
        rf'(?i)"name"\s*:\s*"{name}"\s*,\s*"value"\s*:\s*"([^"]*)"',
        rf'(?i)"value"\s*:\s*"([^"]*)"\s*,\s*"name"\s*:\s*"{name}"',
    ):
        found.extend(re.findall(pattern, text))
    return found


def scan_text(text: str, secrets: list[str]) -> list[str]:
    kinds: list[str] = []
    if any(form in text for secret in secrets for form in secret_forms(secret)):
        kinds.append("known secret")
    if GITHUB_TOKEN.search(text):
        kinds.append("github token")
    if JWT.search(text):
        kinds.append("jwt")
    if BEARER.search(text):
        kinds.append("bearer")
    for header, kind in (
        ("authorization", "authorization"),
        ("x-litellm-api-key", "x-litellm-api-key"),
    ):
        for value in header_values(text, header):
            if value not in HEADER_OK:
                kinds.append(kind)
                break
    for match in LITELLM_LINE.finditer(text):
        if match.group(2) != PLACEHOLDER:
            kinds.append("x-litellm-api-key")
            break
    for match in AUTH_LINE.finditer(text):
        value = match.group(3).strip()
        if value not in HEADER_OK and value != BEARER_PLACEHOLDER:
            kinds.append("authorization")
            break
    return kinds


def scan_bytes(data: bytes, secrets: list[str]) -> list[str]:
    return scan_text(data.decode("latin-1"), secrets)


def scan_tree(root: Path, secrets: list[str]) -> list[str]:
    findings: list[str] = []
    for path in sorted(root.rglob("*")):
        if not path.is_file():
            continue
        rel = path.relative_to(root).as_posix()
        # Scan zip entries, not the compressed container. Deflate bytes can
        # accidentally match a token pattern after the entries are clean.
        if is_zip(path):
            try:
                with zipfile.ZipFile(path) as zin:
                    for info in zin.infolist():
                        for kind in scan_bytes(zin.read(info.filename), secrets):
                            findings.append(f"{kind} in {rel}")
            except zipfile.BadZipFile:
                findings.append(f"unreadable zip {rel}")
            else:
                continue
        data = path.read_bytes()
        for kind in scan_bytes(data, secrets):
            findings.append(f"{kind} in {rel}")
    # A deflated zip hides plaintext from the raw scan. Unique the lines.
    unique: list[str] = []
    seen: set[str] = set()
    for line in findings:
        if line in seen:
            continue
        seen.add(line)
        unique.append(line)
    return unique


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("root")
    parser.add_argument("--key-file", default="")
    parser.add_argument("--scan-only", action="store_true")
    args = parser.parse_args(argv)
    root = Path(args.root)
    if not root.is_dir():
        sys.stderr.write("no evidence directory\n")
        return 0
    secrets = load_secrets(args.key_file)
    if not args.scan_only:
        redact_tree(root, secrets)
    findings = scan_tree(root, secrets)
    if findings:
        for line in findings:
            sys.stderr.write(line + "\n")
        shutil.rmtree(root)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
