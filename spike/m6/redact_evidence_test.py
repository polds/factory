#!/usr/bin/env python3
"""M6 entrypoint. The implementation lives in spike/lib/redact_evidence_test.py."""

from __future__ import annotations

import runpy
from pathlib import Path

_SHIM = Path(__file__).resolve().parents[1] / "lib" / "shim.py"
_mod = runpy.run_path(str(_SHIM))["install"]("redact_evidence_test", globals())

if __name__ == "__main__":
    raise SystemExit("run via unittest")
