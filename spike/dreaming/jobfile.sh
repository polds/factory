#!/usr/bin/env bash
# Turn go test -json output and the P13 probe into the preflight job file.
set -euo pipefail

jsonl="${1:?usage: spike/dreaming/jobfile.sh <tests.jsonl> <probe.json> <out.json>}"
probe="${2:?}"
out="${3:?}"

python3 - "${jsonl}" "${probe}" "${out}" <<'PY'
import json, sys

jsonl, probe_path, out = sys.argv[1:]
wanted = {
    "TestProducedHeadReviewerPipeline",
    "TestDraftPublishRequiresReviewerPass",
    "TestRunSpendHardStop",
    "TestHarnessSecretsNotInProc",
}
states = {name: "fail" for name in wanted}
allow = "fail"
with open(jsonl, encoding="utf-8") as handle:
    for line in handle:
        line = line.strip()
        if not line:
            continue
        try:
            ev = json.loads(line)
        except json.JSONDecodeError:
            continue
        name = ev.get("Test") or ""
        action = ev.get("Action") or ""
        if name == "TestPublisherRouteAllowlist" and action in ("pass", "skip"):
            allow = action
        if name in states and action in ("pass", "skip"):
            states[name] = action
with open(probe_path, encoding="utf-8") as handle:
    probe = json.load(handle)
doc = {
    "tests": states,
    "allowlist": allow,
    "reexec": bool(probe.get("reexec")),
    "hits": int(probe.get("hits") or 0),
}
with open(out, "w", encoding="utf-8") as handle:
    json.dump(doc, handle)
    handle.write("\n")
PY
