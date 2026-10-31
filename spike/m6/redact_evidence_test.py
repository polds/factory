"""Unit tests for the M6 evidence redactor. Secrets here are fake."""

from __future__ import annotations

import json
import subprocess
import sys
import tempfile
import unittest
import zipfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import redact_evidence  # noqa: E402

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
        zout.writestr("trace.network", f'{{"name":"authorization","value":"Bearer {GHP}"}}\n{JWT}\n')
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
            for kind in ("bearer", "jwt", "github token", "x-litellm-api-key", "known secret"):
                self.assertIn(kind, err)
            for secret in SECRETS:
                self.assertNotIn(secret, err)
                self.assertNotIn(secret, proc.stdout)

    def test_missing_directory_is_success(self) -> None:
        missing = Path(tempfile.mkdtemp()) / "absent"
        self.assertEqual(redact_evidence.main([str(missing)]), 0)


if __name__ == "__main__":
    unittest.main()
