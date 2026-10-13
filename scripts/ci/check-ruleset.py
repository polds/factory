#!/usr/bin/env python3
"""Fail when branch protection does not require the CI gate check."""

import json
import os
import pathlib
import subprocess
import sys

root = pathlib.Path(__file__).resolve().parents[2]
workflow = (root / ".github" / "workflows" / "ci.yml").read_text().splitlines()

gate_name = None
seen_gate_job = False
for line in workflow:
    if line.startswith("  required:"):
        seen_gate_job = True
        continue
    if seen_gate_job and line.startswith("    name:"):
        gate_name = line.split(":", 1)[1].strip().strip('"').strip("'")
        break
    if seen_gate_job and line.startswith("  ") and not line.startswith("    "):
        break

if gate_name is None:
    sys.exit("workflow has no required gate job name")

repo = os.environ.get("GITHUB_REPOSITORY", "polds/factory")
result = subprocess.run(
    ["gh", "api", f"repos/{repo}/rulesets"],
    check=False,
    capture_output=True,
    text=True,
)
if result.returncode != 0:
    sys.exit(result.stderr.strip() or "gh api rulesets failed")

contexts = []
for summary in json.loads(result.stdout):
    detail = subprocess.run(
        ["gh", "api", f"repos/{repo}/rulesets/{summary['id']}"],
        check=True,
        capture_output=True,
        text=True,
    )
    ruleset = json.loads(detail.stdout)
    include = ruleset.get("conditions", {}).get("ref_name", {}).get("include", [])
    if "~DEFAULT_BRANCH" not in include and "refs/heads/main" not in include:
        continue
    for rule in ruleset.get("rules", []):
        if rule.get("type") != "required_status_checks":
            continue
        for check in rule.get("parameters", {}).get("required_status_checks", []):
            contexts.append(check["context"])

if gate_name not in contexts:
    sys.exit(
        f"default-branch ruleset does not require {gate_name!r}; it requires {contexts}"
    )
print(f"ruleset requires {gate_name}")
