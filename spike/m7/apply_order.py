#!/usr/bin/env python3
"""Write factoryctl convert output in webhook admission order.

convert emits Factory before Runners. The admission webhook fails closed
unless the Runner exists, and oneForeman rejects every FactoryAgent until
the factory has exactly one FOREMAN. This splits documents and numbers
the files so kubectl apply can follow that order. internal/convert is
unchanged.
"""

from __future__ import annotations

import sys
from pathlib import Path


def documents(text: str) -> list[str]:
    parts = text.split("\n---\n")
    found: list[str] = []
    for part in parts:
        body = part.strip()
        if body.startswith("---"):
            body = body[3:].strip()
        if body:
            found.append(body)
    return found


def kind_of(doc: str) -> str:
    for line in doc.splitlines():
        if line.startswith("kind:"):
            return line.split(":", 1)[1].strip()
    return ""


def foreman(doc: str) -> bool:
    return any(line.strip() == "agentType: FOREMAN" for line in doc.splitlines())


def main(argv: list[str]) -> int:
    if len(argv) != 2:
        sys.stderr.write("usage: apply_order.py DEST_DIR\n")
        return 2
    dest = Path(argv[1])
    dest.mkdir(parents=True, exist_ok=True)
    groups: dict[str, list[str]] = {
        "00-configmaps": [],
        "01-runners": [],
        "02-routers": [],
        "03-scorers": [],
        "04-skills": [],
        "05-factory": [],
        "06-foreman": [],
        "07-agents": [],
        "08-automations": [],
        "09-other": [],
    }
    kind_group = {
        "ConfigMap": "00-configmaps",
        "Runner": "01-runners",
        "ModelRouter": "02-routers",
        "Scorer": "03-scorers",
        "Skill": "04-skills",
        "Factory": "05-factory",
        "Automation": "08-automations",
    }
    for doc in documents(sys.stdin.read()):
        kind = kind_of(doc)
        if kind == "FactoryAgent":
            key = "06-foreman" if foreman(doc) else "07-agents"
        else:
            key = kind_group.get(kind, "09-other")
        groups[key].append(doc)
    for name, docs in groups.items():
        if not docs:
            continue
        body = "---\n" + "\n---\n".join(docs) + "\n"
        (dest / f"{name}.yaml").write_text(body, encoding="utf-8")
        print(name)
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
