"""Load a spike/lib module into an M6 entrypoint."""

from __future__ import annotations

import importlib.util
import sys
from pathlib import Path


def install(name: str, namespace: dict[str, object]) -> object:
    """Execute spike/lib/<name>.py and copy its public names into namespace."""
    path = Path(__file__).resolve().parent / f"{name}.py"
    mod_name = f"spike_lib_{name}"
    spec = importlib.util.spec_from_file_location(mod_name, path)
    if spec is None or spec.loader is None:
        raise ImportError(f"cannot load {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[mod_name] = module
    spec.loader.exec_module(module)
    namespace.update(
        {key: getattr(module, key) for key in dir(module) if not key.startswith("__")}
    )
    return module
