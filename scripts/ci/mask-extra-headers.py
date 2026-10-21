#!/usr/bin/env python3
"""Mask M1_OPENAI_EXTRA_HEADERS for GitHub Actions logs.

Callers: .github/workflows/m5-live.yml and .github/workflows/m6-live.yml.
A value is registered with ::add-mask:: and is never printed. Empty values,
and values that contain a newline or carriage return, are skipped.
"""

import os
import sys


def mask(value: str) -> None:
    if value == "" or "\r" in value or "\n" in value:
        return
    escaped = value.replace("%", "%25")
    sys.stdout.write("::add-mask::" + escaped + "\n")


raw = os.environ.get("M1_OPENAI_EXTRA_HEADERS", "")
for line in raw.splitlines():
    mask(line)
    if ":" not in line:
        continue
    value = line.split(":", 1)[1].strip()
    mask(value)
    if value.lower().startswith("bearer "):
        mask(value[7:].strip())
