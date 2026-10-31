#!/usr/bin/env python3
"""Scrub M6 evidence before it is uploaded.

Replaces Authorization and Bearer values, the console API key, x-litellm-api-key
values, GitHub tokens (including stateless ghs_<app id>_<JWT>), and JWT-shaped
strings in the HAR, the trace zip, and every other file in the evidence
directory. Base64 HAR bodies are decoded, scrubbed, and re-encoded. A later
scan exits 1 when any of those remain, and removes the directory so an upload
cannot publish them. The script prints pattern classes and file names, never
the secret text.
"""

from __future__ import annotations

import argparse
import base64
import binascii
import json
import os
import re
import shutil
import sys
import zipfile
from pathlib import Path
from urllib.parse import quote

import remote

PLACEHOLDER = "[REDACTED]"
BEARER_PLACEHOLDER = "Bearer [REDACTED]"
HEADER_OK = {PLACEHOLDER, BEARER_PLACEHOLDER, "REDACTED"}
MIN_SECRET = 8

# GitHub's current tokens, including stateless installation tokens
# ghs_<app id>_<JWT> (dots and base64url hyphens, since 2026-10-02).
# %2E is a query-string dot, %5F an underscore, %2D a hyphen. A class that
# stops at "." leaves the JWT tail in the file after the prefix is replaced.
_US = r"(?:_|%5[Ff])"
_TOKEN_PREFIX = rf"(?:github{_US}pat{_US}|ghs{_US}|ghp{_US}|gho{_US}|ghu{_US}|ghr{_US})"
_TOKEN_BODY = r"(?:[A-Za-z0-9_\-]|%2[EeDd]|%5[Ff]|\.)"
GITHUB_TOKEN = re.compile(_TOKEN_PREFIX + _TOKEN_BODY + r"{16,}")
_B64URL = r"[A-Za-z0-9_\-]"
_DOT = r"(?:\.|%2[Ee])"
_JWT_CORE = rf"eyJ{_B64URL}{{10,}}{_DOT}{_B64URL}{{10,}}{_DOT}{_B64URL}{{10,}}"
# Optional vendor prefix so ghs_<appid>_<jwt> is one match, not a head
# that consumes "eyJ" and hides ".payload.sig" from a later pass.
JWT = re.compile(rf"(?:{_TOKEN_PREFIX}{_B64URL}*)?{_JWT_CORE}")
# A dotted tail sitting on a placeholder is the split-token failure mode.
JWT_TAIL = re.compile(rf"(?<=\[REDACTED\]){_DOT}{_B64URL}{{10,}}{_DOT}{_B64URL}{{10,}}")
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
    "INSTALL_TOKEN",
    "M6_APP_TOKEN",
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


def public_https_url(value: str) -> bool:
    """The public definition repo URL, or that repo's pull-request html_url.

    Replacing the repo URL would blank an html_url that starts with it.
    The shape is strict_owner_name's (https, owner/name[.git], no userinfo,
    query, or fragment) plus owner/name/pull/<digits>. A query token, a
    percent-encoded token, or an extra path segment stays on the scrub list.
    """
    return remote.public_definition_url(value)


def secrets_to_scrub(secrets: list[str]) -> list[str]:
    return [secret for secret in secrets if not public_https_url(secret)]


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


def apply_token_passes(text: str) -> str:
    # JWT (with its prefix) runs before the vendor-token pass, then again,
    # so a match cannot stop at the first dot and leave a payload behind.
    for _ in range(4):
        updated = JWT.sub(PLACEHOLDER, text)
        updated = GITHUB_TOKEN.sub(PLACEHOLDER, updated)
        updated = JWT_TAIL.sub(PLACEHOLDER, updated)
        if updated == text:
            return text
        text = updated
    return text


def redact_text(text: str, secrets: list[str]) -> str:
    for secret in secrets_to_scrub(secrets):
        for form in secret_forms(secret):
            text = text.replace(form, PLACEHOLDER)
    text = apply_token_passes(text)
    text = scrub_named_header(text, "authorization")
    text = scrub_named_header(text, "x-litellm-api-key")
    text = LITELLM_LINE.sub(rf"\1{PLACEHOLDER}", text)
    text = AUTH_LINE.sub(rf"\1Authorization\2{PLACEHOLDER}", text)
    text = BEARER.sub(rf"\1{PLACEHOLDER}", text)
    return text


def decode_base64_body(raw: str) -> bytes:
    compact = "".join(raw.split())
    pad = "=" * (-len(compact) % 4)
    return base64.b64decode(compact + pad, validate=True)


def _is_base64_text(node: dict[str, object]) -> bool:
    encoding = node.get("encoding")
    return (
        isinstance(encoding, str)
        and encoding.lower() == "base64"
        and isinstance(node.get("text"), str)
    )


def scrub_base64_tree(node: object, secrets: list[str]) -> bool:
    changed = False
    if isinstance(node, dict):
        if _is_base64_text(node):
            raw = node["text"]
            assert isinstance(raw, str)
            try:
                decoded = decode_base64_body(raw)
            except (binascii.Error, ValueError):
                node["text"] = ""
                node.pop("encoding", None)
                changed = True
            else:
                plain = decoded.decode("latin-1")
                cleaned = redact_text(plain, secrets)
                if cleaned != plain:
                    node["text"] = base64.b64encode(cleaned.encode("latin-1")).decode(
                        "ascii"
                    )
                    changed = True
        for key, child in node.items():
            if key == "text" and _is_base64_text(node):
                continue
            if scrub_base64_tree(child, secrets):
                changed = True
    elif isinstance(node, list):
        for child in node:
            if scrub_base64_tree(child, secrets):
                changed = True
    return changed


def scrub_base64_document(text: str, secrets: list[str]) -> str:
    if "base64" not in text.lower():
        return text
    try:
        doc = json.loads(text)
    except json.JSONDecodeError:
        return text
    if not scrub_base64_tree(doc, secrets):
        return text
    return json.dumps(doc, separators=(",", ":"))


def redact_bytes(data: bytes, secrets: list[str]) -> bytes:
    original = data.decode("latin-1")
    text = scrub_base64_document(original, secrets)
    cleaned = redact_text(text, secrets)
    if cleaned == original:
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
    scrub = secrets_to_scrub(secrets)
    if any(form in text for secret in scrub for form in secret_forms(secret)):
        kinds.append("known secret")
    if GITHUB_TOKEN.search(text):
        kinds.append("github token")
    if JWT.search(text) or JWT_TAIL.search(text):
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


def scan_base64_tree(node: object, secrets: list[str], kinds: list[str]) -> None:
    if isinstance(node, dict):
        if _is_base64_text(node):
            raw = node["text"]
            assert isinstance(raw, str)
            try:
                decoded = decode_base64_body(raw)
            except (binascii.Error, ValueError):
                pass
            else:
                kinds.extend(scan_text(decoded.decode("latin-1"), secrets))
        for key, child in node.items():
            if key == "text" and _is_base64_text(node):
                continue
            scan_base64_tree(child, secrets, kinds)
    elif isinstance(node, list):
        for child in node:
            scan_base64_tree(child, secrets, kinds)


def scan_bytes(data: bytes, secrets: list[str]) -> list[str]:
    text = data.decode("latin-1")
    kinds = scan_text(text, secrets)
    if "base64" in text.lower():
        try:
            doc = json.loads(text)
        except json.JSONDecodeError:
            doc = None
        if doc is not None:
            scan_base64_tree(doc, secrets, kinds)
    unique: list[str] = []
    seen: set[str] = set()
    for kind in kinds:
        if kind in seen:
            continue
        seen.add(kind)
        unique.append(kind)
    return unique


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
