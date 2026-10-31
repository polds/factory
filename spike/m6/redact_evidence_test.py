"""Unit tests for the M6 evidence redactor. Secrets here are fake."""

from __future__ import annotations

import base64
import json
import os
import subprocess
import sys
import tempfile
import unittest
import zipfile
from pathlib import Path
from urllib.parse import quote

sys.path.insert(0, str(Path(__file__).resolve().parent))

import redact_evidence

KEY = "c0ffeefakeconsolekey0123456789abcdef"
JWT = "eyJhbGciOiJub25lIn0.eyJzdWIiOiJmYWtlIn0.ZmFrZXNpZ25hdHVyZQ"
GHS = "ghs_" + ("A" * 20)
GHP = "ghp_" + ("b" * 20)
PAT = "github_pat_" + ("c" * 22)
LITELLM = "sk-litellm-fake-value-0123456789"
SECRETS = (KEY, JWT, GHS, GHP, PAT, LITELLM)


def sample_tree(root: Path) -> None:
    har = {
        "log": {
            "entries": [
                {
                    "request": {
                        "headers": [
                            {"name": "Authorization", "value": f"Bearer {KEY}"},
                            {"value": LITELLM, "name": "x-litellm-api-key"},
                        ],
                        "postData": {"text": json.dumps({"apiKey": KEY, "note": JWT})},
                    }
                }
            ]
        }
    }
    (root / "loop.har").write_text(json.dumps(har), encoding="utf-8")
    (root / "driver.log").write_text(
        f"Authorization: Bearer {GHS}\nx-litellm-api-key: {LITELLM}\n{PAT}\n",
        encoding="utf-8",
    )
    trace = root / "loop-trace.zip"
    with zipfile.ZipFile(trace, "w") as zout:
        zout.writestr(
            "trace.network",
            f'{{"name":"authorization","value":"Bearer {GHP}"}}\n{JWT}\n',
        )
        zout.writestr("notes.txt", f"token {GHS}\n")


class RedactorTest(unittest.TestCase):
    def test_redact_removes_fake_secrets(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            sample_tree(root)
            redact_evidence.redact_tree(root, [KEY, LITELLM])
            blob = b""
            for path in root.rglob("*"):
                if path.is_file() and path.suffix != ".zip":
                    blob += path.read_bytes()
            with zipfile.ZipFile(root / "loop-trace.zip") as zin:
                for info in zin.infolist():
                    blob += zin.read(info.filename)
            text = blob.decode("utf-8")
            for secret in SECRETS:
                self.assertNotIn(secret, text)
            self.assertIn("[REDACTED]", text)
            self.assertEqual(redact_evidence.scan_tree(root, [KEY, LITELLM]), [])
            har = json.loads((root / "loop.har").read_text(encoding="utf-8"))
            headers = har["log"]["entries"][0]["request"]["headers"]
            self.assertEqual(headers[0]["value"], "[REDACTED]")
            self.assertEqual(headers[1]["value"], "[REDACTED]")

    def test_placeholders_are_not_findings(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "clean.log").write_text(
                "Authorization: [REDACTED]\nBearer [REDACTED]\n",
                encoding="utf-8",
            )
            self.assertEqual(redact_evidence.scan_tree(root, [KEY]), [])

    def test_scan_only_fails_and_removes_the_tree(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "evidence"
            root.mkdir()
            sample_tree(root)
            key_file = Path(tmp) / "api-key"
            key_file.write_text(KEY + "\n", encoding="utf-8")
            proc = subprocess.run(
                [
                    sys.executable,
                    str(Path(redact_evidence.__file__)),
                    "--scan-only",
                    "--key-file",
                    str(key_file),
                    str(root),
                ],
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 1)
            self.assertFalse(root.exists())
            err = proc.stderr
            for kind in (
                "bearer",
                "jwt",
                "github token",
                "x-litellm-api-key",
                "known secret",
            ):
                self.assertIn(kind, err)
            for secret in SECRETS:
                self.assertNotIn(secret, err)
                self.assertNotIn(secret, proc.stdout)

    def test_missing_directory_is_success(self) -> None:
        missing = Path(tempfile.mkdtemp()) / "absent"
        self.assertEqual(redact_evidence.main([str(missing)]), 0)

    def test_stateless_installation_token_is_removed(self) -> None:
        token = stateless_token()
        encoded = quote(token, safe="")
        marker = PAYLOAD_MARKER
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "evidence"
            plant_stateless_tree(root)
            untouched = Path(tmp) / "unredacted"
            untouched.mkdir()
            plant_stateless_tree(untouched)
            proc = subprocess.run(
                [
                    sys.executable,
                    str(Path(redact_evidence.__file__)),
                    "--scan-only",
                    str(untouched),
                ],
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 1)
            self.assertFalse(untouched.exists())
            self.assertIn("github token", proc.stderr)
            self.assertIn("jwt", proc.stderr)
            self.assertNotIn(token, proc.stderr)
            self.assertNotIn(marker, proc.stderr)
            self.assertNotIn(token, proc.stdout)
            redact_evidence.redact_tree(root, [])
            blob = evidence_text(root)
            self.assertNotIn(token, blob)
            self.assertNotIn(encoded, blob)
            self.assertNotIn(marker, blob)
            self.assertNotIn(PAYLOAD, blob)
            self.assertNotIn(SIG, blob)
            for other in OTHER_TOKENS:
                self.assertNotIn(other, blob)
            self.assertEqual(redact_evidence.scan_tree(root, []), [])
            har = json.loads((root / "loop.har").read_text(encoding="utf-8"))
            header = har["log"]["entries"][0]["request"]["headers"][0]
            self.assertEqual(header["value"], "[REDACTED]")

    def test_base64_har_body_is_decoded(self) -> None:
        token = stateless_token()
        plain = json.dumps({"apiKey": KEY, "note": JWT, "installation": token})
        encoded = base64.b64encode(plain.encode("utf-8")).decode("ascii")
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            har = {
                "log": {
                    "entries": [
                        {
                            "request": {
                                "postData": {
                                    "mimeType": "application/octet-stream",
                                    "encoding": "base64",
                                    "text": encoded,
                                }
                            },
                            "response": {
                                "content": {
                                    "mimeType": "application/octet-stream",
                                    "encoding": "base64",
                                    "text": encoded,
                                }
                            },
                        },
                        {
                            "request": {
                                "postData": {
                                    "mimeType": "application/octet-stream",
                                    "encoding": "base64",
                                    "text": "@@@@",
                                }
                            }
                        },
                    ]
                }
            }
            (root / "loop.har").write_text(json.dumps(har), encoding="utf-8")
            self.assertNotEqual(redact_evidence.scan_tree(root, [KEY]), [])
            redact_evidence.redact_tree(root, [KEY])
            doc = json.loads((root / "loop.har").read_text(encoding="utf-8"))
            bodies = [
                doc["log"]["entries"][0]["request"]["postData"]["text"],
                doc["log"]["entries"][0]["response"]["content"]["text"],
            ]
            for body in bodies:
                decoded = base64.b64decode(body).decode("utf-8")
                self.assertNotIn(KEY, decoded)
                self.assertNotIn(JWT, decoded)
                self.assertNotIn(token, decoded)
                self.assertNotIn(PAYLOAD_MARKER, decoded)
                self.assertIn("[REDACTED]", decoded)
            dropped = doc["log"]["entries"][1]["request"]["postData"]
            self.assertEqual(dropped.get("text", ""), "")
            self.assertNotIn("encoding", dropped)
            self.assertEqual(redact_evidence.scan_tree(root, [KEY]), [])


HEADER = "eyJhbGciOiJub25lIn0"
PAYLOAD_MARKER = "QQVlMT0FETUFSS0VS"
PAYLOAD = f"eyJzdWIiOiJ{PAYLOAD_MARKER}In0"
SIG = "c2lnbmF0dXJlTUFSSy0xMjM0NTY"


def stateless_token() -> str:
    return f"ghs_12345_{HEADER}.{PAYLOAD}.{SIG}"


OTHER_TOKENS = (
    "gho_" + ("A" * 20) + "." + ("B" * 20),
    "ghu_" + ("C" * 20) + "." + ("D" * 20),
    "ghr_" + ("E" * 20) + "-" + ("F" * 20),
    "github_pat_" + ("G" * 20) + "." + ("H" * 20),
    "ghs_1234567_" + ("A" * 40) + "." + ("B" * 40) + "." + ("C" * 40),
)


def plant_stateless_tree(root: Path) -> None:
    root.mkdir(parents=True, exist_ok=True)
    token = stateless_token()
    encoded = quote(token, safe="")
    har = {
        "log": {
            "entries": [
                {
                    "request": {
                        "url": f"https://api.github.com/installation?access_token={token}&x=1",
                        "queryString": [{"name": "access_token", "value": token}],
                        "headers": [
                            {"name": "X-Installation-Token", "value": token},
                        ],
                        "postData": {
                            "mimeType": "application/json",
                            "text": json.dumps({"token": token}),
                        },
                    }
                }
            ]
        }
    }
    (root / "loop.har").write_text(json.dumps(har), encoding="utf-8")
    (root / "driver.log").write_text(
        f"installed {token}\nurl https://example.test/cb?token={encoded}\n"
        + "\n".join(OTHER_TOKENS)
        + "\n",
        encoding="utf-8",
    )
    trace = root / "loop-trace.zip"
    with zipfile.ZipFile(trace, "w", compression=zipfile.ZIP_DEFLATED) as zout:
        zout.writestr("trace.network", f"member {token}\n")


class PublicURLTest(unittest.TestCase):
    def test_pull_request_url_survives_and_tokens_do_not(self) -> None:
        repo = "https://github.com/polds/factory-m6-definition"
        html = repo + "/pull/1"
        token = "ghs_" + ("a" * 20)
        credentialed = (
            f"https://x-access-token:{token}@github.com/polds/factory-m6-definition.git"
        )
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "evidence"
            root.mkdir()
            (root / "remote-pr.json").write_text(
                json.dumps(
                    {
                        "html_url": html,
                        "clone": credentialed,
                        "note": f"Authorization: Bearer {token}",
                    }
                )
                + "\n",
                encoding="utf-8",
            )
            saved = {
                "M6_TASK_GIT_URL": os.environ.get("M6_TASK_GIT_URL"),
                "M6_CLONED_URL": os.environ.get("M6_CLONED_URL"),
                "EVIDENCE_EXTRA_SECRETS": os.environ.get("EVIDENCE_EXTRA_SECRETS"),
            }
            os.environ["M6_TASK_GIT_URL"] = repo
            os.environ["M6_CLONED_URL"] = credentialed
            os.environ["EVIDENCE_EXTRA_SECRETS"] = "M6_TASK_GIT_URL M6_CLONED_URL"
            try:
                self.assertNotEqual(
                    redact_evidence.scan_tree(root, redact_evidence.load_secrets("")),
                    [],
                )
                code = redact_evidence.main([str(root)])
            finally:
                for name, value in saved.items():
                    if value is None:
                        os.environ.pop(name, None)
                    else:
                        os.environ[name] = value
            self.assertEqual(code, 0)
            self.assertTrue(root.is_dir())
            text = (root / "remote-pr.json").read_text(encoding="utf-8")
            self.assertIn(html, text)
            self.assertNotIn("[REDACTED]/pull/", text)
            self.assertNotIn(token, text)
            self.assertNotIn(credentialed, text)
            self.assertIn("[REDACTED]", text)
            self.assertEqual(
                redact_evidence.scan_tree(root, [repo, credentialed, token]), []
            )
            self.assertTrue(redact_evidence.public_https_url(repo))
            self.assertTrue(redact_evidence.public_https_url(repo + ".git"))
            self.assertTrue(redact_evidence.public_https_url(html))

    def test_percent_encoded_ghs_in_query_is_redacted(self) -> None:
        # Underscore and hyphen are percent-encoded. quote() leaves both
        # raw characters alone, so the token regex has to see %5F and %2D.
        token = "ghs%5F" + ("a" * 8) + "%2D" + ("b" * 8)
        url = "https://github.com/polds/factory-m6-definition?token=" + token
        encoded = quote(url, safe="")
        self.assertFalse(redact_evidence.public_https_url(url))
        # The token regex catches the encoding even when the URL is not a
        # known secret. The quoted URL is a secret_forms copy of that secret.
        self.assertNotEqual(redact_evidence.scan_text(url, []), [])
        self.assertNotEqual(redact_evidence.scan_text(encoded, [url]), [])
        bare = redact_evidence.redact_text(url, [])
        self.assertNotIn(token, bare)
        self.assertNotIn("%5F", bare)
        self.assertNotIn("%2D", bare)
        self.assertEqual(redact_evidence.scan_text(bare, []), [])
        quoted = redact_evidence.redact_text(encoded, [url])
        self.assertNotIn(encoded, quoted)
        self.assertEqual(redact_evidence.scan_text(quoted, [url]), [])

    def test_opaque_query_token_is_redacted(self) -> None:
        opaque = "q" * 24
        url = "https://github.com/polds/factory-m6-definition?access=" + opaque
        encoded = quote(url, safe="")
        self.assertFalse(redact_evidence.public_https_url(url))
        self.assertNotEqual(redact_evidence.scan_text(url, [url]), [])
        self.assertNotEqual(redact_evidence.scan_text(encoded, [url]), [])
        for blob in (url, encoded):
            redacted = redact_evidence.redact_text(blob, [url])
            self.assertNotIn(opaque, redacted)
            self.assertNotIn(url, redacted)
            self.assertEqual(redact_evidence.scan_text(redacted, [url]), [])

    def test_opaque_path_segment_is_redacted(self) -> None:
        segment = "p" * 24
        url = "https://github.com/polds/factory-m6-definition/" + segment
        encoded = quote(url, safe="")
        self.assertFalse(redact_evidence.public_https_url(url))
        self.assertNotEqual(redact_evidence.scan_text(url, [url]), [])
        self.assertNotEqual(redact_evidence.scan_text(encoded, [url]), [])
        for blob in (url, encoded):
            redacted = redact_evidence.redact_text(blob, [url])
            self.assertNotIn(segment, redacted)
            self.assertNotIn(url, redacted)
            self.assertEqual(redact_evidence.scan_text(redacted, [url]), [])

    def test_public_api_origin_is_not_a_secret(self) -> None:
        origin = "https://api.github.com"
        repo = "https://github.com/polds/factory-m6-definition"
        credentialed = (
            "https://x-access-token:ghs_"
            + ("a" * 20)
            + "@github.com/polds/factory-m6-definition.git"
        )
        self.assertTrue(redact_evidence.public_https_url(origin))
        self.assertTrue(redact_evidence.public_https_url(origin + "/"))
        self.assertFalse(redact_evidence.public_https_url(origin + "/repos/o/n"))
        self.assertFalse(redact_evidence.public_https_url(origin + "?access=" + ("q" * 24)))
        self.assertFalse(redact_evidence.public_https_url(credentialed))
        self.assertEqual(
            redact_evidence.secrets_to_scrub([origin, repo, repo + "/pull/7", credentialed]),
            [credentialed],
        )


def leaked_stateless(text: str) -> bool:
    token = stateless_token()
    return token in text or PAYLOAD_MARKER in text or quote(token, safe="") in text


def evidence_text(root: Path) -> str:
    blob = b""
    for path in root.rglob("*"):
        if not path.is_file() or path.suffix == ".zip":
            continue
        blob += path.read_bytes()
    zip_path = root / "loop-trace.zip"
    if zip_path.is_file():
        with zipfile.ZipFile(zip_path) as zin:
            for info in zin.infolist():
                blob += zin.read(info.filename)
    return blob.decode("utf-8")


if __name__ == "__main__":
    unittest.main()
