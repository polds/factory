"""Validate defined-factory contract fixtures.

This program validates examples, not the real implementation.
It does not open a network connection, read credentials, call a model,
or start an application service.

Run from a checkout:

    python3 docs/contracts/defined-factory/validate.py
"""

from __future__ import annotations

import json
import re
import sys
from pathlib import Path

import audits
import rows

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[2]
EXAMPLES = HERE / "examples"
TRACES = HERE / "traces"
SCHEMA_DIR = HERE / "schema"

API_CODES = {
    "budget_exceeded",
    "cancelled_no_resurrect",
    "claim_fenced",
    "delegation_denied",
    "depth_exceeded",
    "grant_rejected",
    "input_invalid",
    "invocation_conflict",
    "no_work_product",
    "principal_rejected",
    "profile_rejected",
    "report_missing",
    "repository_rejected",
    "result_oversize",
    "result_path_rejected",
    "result_symlink_rejected",
    "result_unauthorized",
    "secret_publication_rejected",
    "secret_ref_rejected",
    "usage_late_over_reservation",
}

_TYPE_ORDER = ("array", "boolean", "integer", "null", "number", "object", "string")


def main() -> int:
    problems: list[str] = []
    semantic_docs = _semantic_documents()
    scenario = build_scenario_schema(semantic_docs)
    committed = _load(SCHEMA_DIR / "scenario.schema.json")
    if committed != scenario:
        problems.append("scenario.schema.json drifted from the fixture documents")
    schemas = {
        "scenario": scenario,
        "request": _load(SCHEMA_DIR / "request.schema.json"),
        "requires": _load(SCHEMA_DIR / "requires.schema.json"),
        "casefile": _load(SCHEMA_DIR / "casefile.schema.json"),
        "matrix": _load(SCHEMA_DIR / "matrix.schema.json"),
    }
    cases = _load_cases(problems, schemas["casefile"])
    matrix = _load(HERE / "matrix.json")
    schema_errors = errors_of(matrix, schemas["matrix"], schemas["matrix"])
    if schema_errors:
        problems.append("matrix schema: " + "; ".join(schema_errors[:8]))
    _check_cases(cases, schemas, problems)
    _check_matrix(cases, matrix, problems)
    _check_seams(matrix, problems)
    _check_codes(problems)
    mutations = _selftest(problems)
    doc = REPO / "docs" / "13-defined-factory-contracts.md"
    text = doc.read_text(encoding="utf-8") if doc.is_file() else ""
    for inv in audits.EXPECT:
        if inv not in text:
            problems.append(f"docs/13-defined-factory-contracts.md omits {inv}")
    if problems:
        for item in problems:
            print(item, file=sys.stderr)
        return 1
    accept = sum(1 for case in cases if case["expect"] == "accept")
    reject = sum(1 for case in cases if case["expect"] == "reject")
    print(
        f"fixtures {len(cases)} accept {accept} reject {reject} matrix {len(matrix['rows'])}"
    )
    print(f"selftest mutations {mutations} rejected")
    print("CONTRACT FIXTURES PASS")
    print("validates examples, not the real implementation")
    return 0


def build_scenario_schema(docs: list[dict]) -> dict:
    body = infer(docs)
    return {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "$id": "https://factory.internal/contracts/defined-factory/scenario.schema.json",
        "title": "Defined-factory scenario witness",
        "description": (
            "Closed witness objects for defined-factory/1. "
            "Order, identity, and budget invariants are enforced by validate.py."
        ),
        **body,
    }


def infer(values: list) -> dict:
    kinds = {_kind(value) for value in values}
    if kinds == {"object"}:
        keys: set[str] = set()
        for value in values:
            keys.update(value)
        properties = {}
        required = []
        for key in sorted(keys):
            present = [value[key] for value in values if key in value]
            properties[key] = infer(present)
            if all(key in value for value in values):
                required.append(key)
        schema: dict = {
            "additionalProperties": False,
            "properties": properties,
            "type": "object",
        }
        if required:
            schema["required"] = required
        return schema
    if kinds == {"array"}:
        items: list = []
        for value in values:
            items.extend(value)
        schema = {"type": "array"}
        if items:
            schema["items"] = infer(items)
        return schema
    ordered = [kind for kind in _TYPE_ORDER if kind in kinds]
    if len(ordered) == 1:
        return {"type": ordered[0]}
    return {"type": ordered}


def errors_of(instance: object, schema: dict, root: dict) -> list[str]:
    if "$ref" in schema:
        ref = schema["$ref"]
        if not ref.startswith("#/"):
            return [f"unsupported ref {ref}"]
        return errors_of(instance, _pointer(root, ref[2:]), root)
    found: list[str] = []
    if "allOf" in schema:
        for sub in schema["allOf"]:
            found.extend(errors_of(instance, sub, root))
    if "anyOf" in schema and not any(
        not errors_of(instance, sub, root) for sub in schema["anyOf"]
    ):
        found.append("anyOf")
    if "oneOf" in schema:
        goods = [sub for sub in schema["oneOf"] if not errors_of(instance, sub, root)]
        if len(goods) != 1:
            found.append("oneOf")
    if "const" in schema and instance != schema["const"]:
        found.append("const")
    if "enum" in schema and instance not in schema["enum"]:
        found.append("enum")
    if "type" in schema:
        types = schema["type"] if isinstance(schema["type"], list) else [schema["type"]]
        if not any(_is(instance, name) for name in types):
            found.append("type")
            return found
    if isinstance(instance, dict):
        props = schema.get("properties", {})
        additional = schema.get("additionalProperties", True)
        for key in schema.get("required", []):
            if key not in instance:
                found.append(f"required {key}")
        for key, value in instance.items():
            if key in props:
                found.extend(errors_of(value, props[key], root))
            elif additional is False:
                found.append(f"additional {key}")
            elif isinstance(additional, dict):
                found.extend(errors_of(value, additional, root))
    if isinstance(instance, list) and "items" in schema:
        for value in instance:
            found.extend(errors_of(value, schema["items"], root))
        if "minItems" in schema and len(instance) < schema["minItems"]:
            found.append("minItems")
        if "maxItems" in schema and len(instance) > schema["maxItems"]:
            found.append("maxItems")
    if isinstance(instance, str):
        if "minLength" in schema and len(instance) < schema["minLength"]:
            found.append("minLength")
        if "maxLength" in schema and len(instance) > schema["maxLength"]:
            found.append("maxLength")
        if "pattern" in schema and re.search(schema["pattern"], instance) is None:
            found.append("pattern")
    if type(instance) is int:
        if "minimum" in schema and instance < schema["minimum"]:
            found.append("minimum")
        if "maximum" in schema and instance > schema["maximum"]:
            found.append("maximum")
    return found


def _check_cases(cases: list[dict], schemas: dict, problems: list[str]) -> None:
    seen: set[str] = set()
    for case in cases:
        vector = case["vectorId"]
        if vector in seen:
            problems.append(f"duplicate vector {vector}")
        seen.add(vector)
        inv = case["invariant"]
        schema = schemas[case["schema"]]
        document = case["document"]
        schema_errors = errors_of(document, schema, schema)
        if case["expect"] == "accept":
            if case["rejectBy"] != "formula":
                problems.append(f"{vector} accept must use formula")
            if schema_errors:
                problems.append(f"{vector} schema: {schema_errors[:6]}")
            if document != audits.EXPECT[inv]:
                problems.append(f"{vector} drifted from the legal world")
            if audits.formula_violations(inv, document):
                problems.append(f"{vector} legal world failed its formula")
            continue
        if case["rejectBy"] == "schema":
            if not schema_errors:
                problems.append(f"{vector} invalid fixture was accepted by the schema")
            if case.get("code") != "input_invalid":
                problems.append(f"{vector} schema rejection code")
            continue
        if schema_errors:
            problems.append(
                f"{vector} semantic rejection is not schema-valid: {schema_errors[:6]}"
            )
        if document != audits.NEGATIVES[inv]:
            problems.append(f"{vector} drifted from the illegal world")
        if not audits.formula_violations(inv, document):
            problems.append(f"{vector} illegal world was accepted")


def _check_matrix(cases: list[dict], matrix: dict, problems: list[str]) -> None:
    if matrix.get("contractVersion") != audits.CONTRACT_VERSION:
        problems.append("matrix contractVersion")
    if matrix.get("baseline") != audits.BASELINE:
        problems.append("matrix baseline")
    by_vector = {case["vectorId"]: case for case in cases}
    invariants = [row["invariant"] for row in matrix.get("rows", [])]
    if [row[0] for row in rows.ROWS] != list(audits.EXPECT):
        problems.append("rows order drifted from EXPECT")
    if invariants != list(audits.EXPECT):
        problems.append("matrix row order drifted from EXPECT")
    for row in matrix.get("rows", []):
        inv = row["invariant"]
        positive = by_vector.get(row["positive"])
        negative = by_vector.get(row["negative"])
        if (
            positive is None
            or positive["expect"] != "accept"
            or positive["invariant"] != inv
        ):
            problems.append(f"{inv} positive vector")
        if (
            negative is None
            or negative["expect"] != "reject"
            or negative["invariant"] != inv
        ):
            problems.append(f"{inv} negative vector")
    referenced = set()
    for row in matrix.get("rows", []):
        referenced.add(row["positive"])
        referenced.add(row["negative"])
    for case in cases:
        if case["vectorId"] not in referenced:
            problems.append(f"unreferenced vector {case['vectorId']}")


def _check_seams(matrix: dict, problems: list[str]) -> None:
    for row in matrix.get("rows", []):
        baseline = row["baseline"]
        path = REPO / baseline["file"]
        if not path.is_file():
            problems.append(f"{row['invariant']} missing seam file {baseline['file']}")
            continue
        lines = path.read_text(encoding="utf-8").splitlines()
        start = baseline["start"]
        end = baseline["end"]
        if start < 1 or end > len(lines) or start > end:
            problems.append(f"{row['invariant']} seam range {start}-{end}")
            continue
        chunk = "\n".join(lines[start - 1 : end])
        if baseline["anchor"] not in chunk:
            problems.append(
                f"{row['invariant']} anchor {baseline['anchor']!r} not in {baseline['file']}:{start}-{end}"
            )


def _check_codes(problems: list[str]) -> None:
    found: set[str] = set()

    def walk(value: object) -> None:
        if isinstance(value, dict):
            for key, item in value.items():
                if key == "code" and isinstance(item, str):
                    found.add(item)
                walk(item)
        elif isinstance(value, list):
            for item in value:
                walk(item)

    for document in audits.EXPECT.values():
        walk(document)
    unknown = found - API_CODES
    if unknown:
        problems.append("undeclared error codes: " + ", ".join(sorted(unknown)))


def _selftest(problems: list[str]) -> int:
    """Corrupt legal worlds in memory and require the formulas to reject them."""
    count = 0
    for inv, document in audits.EXPECT.items():
        for mutated in _deliberate_corruptions(inv, document):
            count += 1
            if not audits.formula_violations(inv, mutated):
                problems.append(f"selftest accepted a corrupted {inv}")
    return count


def _deliberate_corruptions(inv: str, document: dict) -> list[dict]:
    corruptions = []
    if inv in audits.SCHEMA_REJECT:
        return corruptions
    illegal = json.loads(json.dumps(audits.NEGATIVES[inv]))
    corruptions.append(illegal)
    extra = json.loads(json.dumps(document))
    if inv == "INV-REPLAY":
        extra["canonicalDigest"] = "0" * 64
        corruptions.append(extra)
    elif inv == "INV-LATE-COMMIT":
        extra["events"][3]["ledgerSeq"] = 5
        extra["events"][4]["ledgerSeq"] = 4
        corruptions.append(extra)
    elif inv == "INV-ROLLUP":
        extra["rollups"]["specialist-b"] = 500
        corruptions.append(extra)
    elif inv == "INV-SECURITY-GATE":
        extra["g14ActualBackend"] = True
        extra["paidProbeAllowed"] = True
        corruptions.append(extra)
    elif inv == "INV-CAPACITY":
        extra["capacity"] = 2
        corruptions.append(extra)
    return corruptions


def _load_cases(problems: list[str], case_schema: dict) -> list[dict]:
    paths = sorted(EXAMPLES.glob("*.json")) + sorted(TRACES.glob("*.json"))
    cases: list[dict] = []
    if not paths:
        problems.append("no fixture files")
    for path in paths:
        payload = _load(path)
        schema_errors = errors_of(payload, case_schema, case_schema)
        if schema_errors:
            problems.append(f"{path.name} casefile: {schema_errors[:6]}")
            continue
        if payload["contractVersion"] != audits.CONTRACT_VERSION:
            problems.append(f"{path.name} contractVersion")
        cases.extend(payload["cases"])
    return cases


def _semantic_documents() -> list[dict]:
    docs: list[dict] = []
    for inv, document in audits.EXPECT.items():
        if inv != "INV-REQUEST-SHAPE":
            docs.append(document)
    for inv, document in audits.NEGATIVES.items():
        if inv not in audits.SCHEMA_REJECT:
            docs.append(document)
    return docs


def _load(path: Path) -> dict:
    return json.loads(path.read_text(encoding="utf-8"))


def _kind(value: object) -> str:
    if value is None:
        return "null"
    if type(value) is bool:
        return "boolean"
    if type(value) is int:
        return "integer"
    if isinstance(value, str):
        return "string"
    if isinstance(value, list):
        return "array"
    if isinstance(value, dict):
        return "object"
    raise TypeError(type(value))


def _is(value: object, name: str) -> bool:
    return _kind(value) == name


def _pointer(root: dict, pointer: str) -> dict:
    cursor = root
    for part in pointer.split("/"):
        part = part.replace("~1", "/").replace("~0", "~")
        cursor = cursor[part]
    return cursor


if __name__ == "__main__":
    sys.exit(main())
