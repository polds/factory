#!/usr/bin/env python3
"""Lock the formatter and YAML lint decisions the workflow depends on."""

import pathlib
import re
import sys

root = pathlib.Path(__file__).resolve().parents[2]
errors = []

editorconfig = (root / ".editorconfig").read_text()
for line in editorconfig.splitlines():
    stripped = line.strip()
    if stripped.startswith("[") and stripped.endswith("]") and "{" in stripped:
        errors.append(f"editorconfig section uses a brace glob: {stripped}")

workflow = (root / ".github" / "workflows" / "ci.yml").read_text()
printer_flags = (
    "-i",
    "-ci",
    "-bn",
    "-sr",
    "-kp",
    "-fn",
    "--indent",
    "--case-indent",
    "--binary-next-line",
    "--space-redirects",
    "--keep-padding",
    "--func-next-line",
)
for line in workflow.splitlines():
    if "shfmt" not in line:
        continue
    for flag in printer_flags:
        if re.search(rf"(?:^|\s){re.escape(flag)}(?:\s|$)", line):
            errors.append(f"workflow passes shfmt printer flag {flag}: {line.strip()}")

yamllint = (root / ".yamllint.yaml").read_text()
required_snippets = (
    "document-start: disable",
    "check-keys: false",
    "max-spaces-inside: 1",
)
for snippet in required_snippets:
    if snippet not in yamllint:
        errors.append(f"yamllint config is missing {snippet}")

if errors:
    sys.exit("\n".join(errors))
print("lint policy ok")
