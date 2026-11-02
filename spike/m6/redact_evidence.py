#!/usr/bin/env python3
"""M6 entrypoint. The implementation lives in spike/lib/redact_evidence.py."""

from __future__ import annotations

import importlib.util
import sys
from pathlib import Path

_LIB = Path(__file__).resolve().parents[1] / "lib" / "redact_evidence.py"
_spec = importlib.util.spec_from_file_location("spike_lib_redact_evidence", _LIB)
if _spec is None or _spec.loader is None:
    raise ImportError(f"cannot load {_LIB}")
_mod = importlib.util.module_from_spec(_spec)
sys.modules["spike_lib_redact_evidence"] = _mod
_spec.loader.exec_module(_mod)
globals().update({k: getattr(_mod, k) for k in dir(_mod) if not k.startswith("__")})

if __name__ == "__main__":
    raise SystemExit(_mod.main())
