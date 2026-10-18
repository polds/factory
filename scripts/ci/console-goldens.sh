#!/usr/bin/env bash
# Fail when web/console/src/api types or JSON fixtures disagree with the
# Go console goldens in internal/api/testdata/console.
#
# The check is structural: field names and JSON types. It is not a byte
# diff. A golden field must be declared on the mapped type, a required
# type field must be present, and a declared type of unknown or any does
# not count as a match. JSON fixtures under web/console/src/api, when
# present, must match the golden with the same file name.
#
# Requires web/console/node_modules (npm ci in that directory).
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${root}"

ts_lib="${root}/web/console/node_modules/typescript/lib/typescript.js"
if [[ ! -f "${ts_lib}" ]]; then
  echo "console-goldens: missing ${ts_lib}; run npm ci in web/console" >&2
  exit 1
fi

node --input-type=module - "${root}" <<'JS'
import { createRequire } from "node:module";
import fs from "node:fs";
import path from "node:path";

const root = process.argv[2];
if (!root) {
  console.error("console-goldens: missing repo root");
  process.exit(1);
}

const tsPath = path.join(root, "web/console/node_modules/typescript/lib/typescript.js");
const require = createRequire(tsPath);
const ts = require(tsPath);

const goldenDir = path.join(root, "internal/api/testdata/console");
const apiDir = path.join(root, "web/console/src/api");
const typesFile = path.join(apiDir, "types.ts");

// Golden file -> type expression in web/console/src/api/types.ts.
const mapping = [
  ["automations.json", "API.Automation[]"],
  ["definition.json", "API.DefinitionView"],
  ["deliveries.json", "API.Delivery[]"],
  ["factory-event.json", "API.FactoryEventData"],
  ["followup.json", "API.SteerAccepted"],
  ["harness-usage.json", "API.HarnessUsage"],
  ["identities.json", "API.Identity[]"],
  ["inbox.json", "API.InboxItem[]"],
  ["interrupt.json", "API.SteerAccepted"],
  ["run.json", "API.RunDetail"],
  ["runs.json", "API.Run[]"],
  ["secrets.json", "API.SecretPresence[]"],
  ["steer.json", "API.SteerMessage[]"],
  ["steers.json", "API.SteerRecord[]"],
  ["summary.json", "API.FactorySummary"],
  ["timeline.json", "API.TimelinePhase[]"],
  ["webhook-deliveries.json", "API.Delivery[]"],
  ["webhooks.json", "API.Webhook[]"],
  ["workers.json", "API.ConnectedWorkers"],
];

const errors = [];
function fail(message) {
  errors.push(message);
}

const goldenNames = fs
  .readdirSync(goldenDir)
  .filter((name) => name.endsWith(".json"))
  .sort();
const mapped = new Set(mapping.map(([name]) => name));
for (const name of goldenNames) {
  if (!mapped.has(name)) fail(`${name}: no TypeScript type mapping`);
}
for (const [name] of mapping) {
  if (!goldenNames.includes(name)) fail(`${name}: mapped golden is missing`);
}

const probeLines = mapping.map(([, expr], index) => `export type Probe_${index} = ${expr};`);
const syntheticName = path.join(apiDir, "__golden_probe.ts");
const syntheticText = `import type * as API from "./types.ts";\n${probeLines.join("\n")}\n`;

const options = {
  strict: true,
  target: ts.ScriptTarget.ES2022,
  module: ts.ModuleKind.ESNext,
  moduleResolution: ts.ModuleResolutionKind.Bundler,
  noEmit: true,
  skipLibCheck: true,
  allowImportingTsExtensions: true,
};

const host = ts.createCompilerHost(options);
const originalGetSourceFile = host.getSourceFile.bind(host);
const originalFileExists = host.fileExists.bind(host);
host.fileExists = (fileName) => {
  if (path.resolve(fileName) === path.resolve(syntheticName)) return true;
  return originalFileExists(fileName);
};
host.getSourceFile = (fileName, languageVersion, onError, shouldCreateNewSourceFile) => {
  if (path.resolve(fileName) === path.resolve(syntheticName)) {
    return ts.createSourceFile(fileName, syntheticText, languageVersion, true, ts.ScriptKind.TS);
  }
  return originalGetSourceFile(fileName, languageVersion, onError, shouldCreateNewSourceFile);
};

const program = ts.createProgram({
  rootNames: [syntheticName, typesFile],
  options,
  host,
});
const diagnostics = ts.getPreEmitDiagnostics(program).filter((diag) => {
  const file = diag.file?.fileName ?? "";
  return file.endsWith("types.ts") || file.endsWith("__golden_probe.ts");
});
if (diagnostics.length > 0) {
  for (const diag of diagnostics) {
    const text = ts.flattenDiagnosticMessageText(diag.messageText, "\n");
    const where = diag.file ? path.relative(root, diag.file.fileName) : "program";
    fail(`${where}: ${text}`);
  }
  report();
}

const checker = program.getTypeChecker();
const source = program.getSourceFile(syntheticName);
if (!source) {
  fail("console-goldens: probe file was not loaded");
  report();
}

const probes = new Map();
for (const statement of source.statements) {
  if (!ts.isTypeAliasDeclaration(statement)) continue;
  probes.set(statement.name.text, checker.getTypeFromTypeNode(statement.type));
}

for (const [index, [name]] of mapping.entries()) {
  const type = probes.get(`Probe_${index}`);
  if (!type) {
    fail(`${name}: probe type Probe_${index} was not created`);
    continue;
  }
  let value;
  try {
    value = JSON.parse(fs.readFileSync(path.join(goldenDir, name), "utf8"));
  } catch (err) {
    fail(`${name}: ${err.message}`);
    continue;
  }
  checkValue(type, value, name);
}

checkFixtures();
report();

function report() {
  if (errors.length === 0) {
    console.log(`console-goldens: ok (${mapping.length} goldens)`);
    process.exit(0);
  }
  console.error(`console-goldens: ${errors.length} difference(s)`);
  for (const message of errors) console.error(`  ${message}`);
  process.exit(1);
}

function checkFixtures() {
  const fixtures = [];
  walk(apiDir, fixtures);
  if (fixtures.length === 0) {
    console.log("console-goldens: no JSON fixtures under web/console/src/api");
    return;
  }
  for (const file of fixtures) {
    const name = path.basename(file);
    const goldenPath = path.join(goldenDir, name);
    const rel = path.relative(root, file);
    if (!fs.existsSync(goldenPath)) {
      fail(`${rel}: no Go golden named ${name}`);
      continue;
    }
    const fixture = JSON.parse(fs.readFileSync(file, "utf8"));
    const golden = JSON.parse(fs.readFileSync(goldenPath, "utf8"));
    compareJson(golden, fixture, rel);
  }
}

function walk(dir, out) {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    if (entry.name === "node_modules") continue;
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) walk(full, out);
    else if (entry.name.endsWith(".json")) out.push(full);
  }
}

function compareJson(golden, fixture, loc) {
  if (fixture === null || golden === null) {
    if (fixture !== golden) fail(`${loc}: JSON type ${jsonKind(fixture)} != golden ${jsonKind(golden)}`);
    return;
  }
  if (Array.isArray(fixture) || Array.isArray(golden)) {
    if (!Array.isArray(fixture) || !Array.isArray(golden)) {
      fail(`${loc}: JSON type ${jsonKind(fixture)} != golden ${jsonKind(golden)}`);
      return;
    }
    if (golden.length === 0) return;
    fixture.forEach((item, index) => compareJson(golden[0], item, `${loc}[${index}]`));
    return;
  }
  if (typeof fixture !== "object" || typeof golden !== "object") {
    if (typeof fixture !== typeof golden) {
      fail(`${loc}: JSON type ${jsonKind(fixture)} != golden ${jsonKind(golden)}`);
    }
    return;
  }
  const goldenKeys = Object.keys(golden).sort();
  const fixtureKeys = Object.keys(fixture).sort();
  for (const key of fixtureKeys) {
    if (!Object.prototype.hasOwnProperty.call(golden, key)) {
      fail(`${loc}.${key}: fixture field is absent from the golden`);
    }
  }
  for (const key of goldenKeys) {
    if (!Object.prototype.hasOwnProperty.call(fixture, key)) {
      fail(`${loc}.${key}: golden field is absent from the fixture`);
      continue;
    }
    compareJson(golden[key], fixture[key], `${loc}.${key}`);
  }
}

function checkValue(type, value, loc) {
  const current = structured(type);
  if (current.isUnion()) {
    const parts = current.types.filter((part) => (part.flags & ts.TypeFlags.Undefined) === 0);
    const attempts = [];
    for (const part of parts) {
      const before = errors.length;
      checkValue(part, value, loc);
      const added = errors.splice(before);
      if (added.length === 0) return;
      attempts.push(added);
    }
    if (attempts.length === 0) {
      fail(`${loc}: no union member accepts ${jsonKind(value)}`);
      return;
    }
    attempts.sort((left, right) => left.length - right.length);
    for (const message of attempts[0]) fail(message);
    return;
  }
  if (isWide(current)) {
    fail(`${loc}: declared type ${checker.typeToString(current)} does not name a JSON type`);
    return;
  }
  if (value === null) {
    if ((current.flags & ts.TypeFlags.Null) === 0) {
      fail(`${loc}: golden is null but type is ${checker.typeToString(current)}`);
    }
    return;
  }
  if (Array.isArray(value)) {
    checkArray(current, value, loc);
    return;
  }
  switch (typeof value) {
    case "string":
      if (!stringOk(current, value)) {
        fail(`${loc}: golden string ${JSON.stringify(value)} is not ${checker.typeToString(current)}`);
      }
      return;
    case "number":
      if (!numberOk(current, value)) {
        fail(`${loc}: golden number ${value} is not ${checker.typeToString(current)}`);
      }
      return;
    case "boolean":
      if (!boolOk(current, value)) {
        fail(`${loc}: golden boolean ${value} is not ${checker.typeToString(current)}`);
      }
      return;
    case "object":
      checkObject(current, value, loc);
      return;
    default:
      fail(`${loc}: unsupported JSON value`);
  }
}

// Intersections (UsageByModel is UsageTotal & { model_id }) collapse to one
// object. getApparentType on a primitive string returns the String
// interface, so primitives stay as they are.
function structured(type) {
  if ((type.flags & ts.TypeFlags.Intersection) !== 0) return checker.getApparentType(type);
  return type;
}

function checkArray(type, value, loc) {
  if (!checker.isArrayType(type) && !checker.isTupleType(type)) {
    fail(`${loc}: golden is an array but type is ${checker.typeToString(type)}`);
    return;
  }
  const args = checker.getTypeArguments(type);
  if (checker.isTupleType(type)) {
    if (args.length !== value.length) {
      fail(`${loc}: golden has ${value.length} entries but tuple has ${args.length}`);
      return;
    }
    value.forEach((item, index) => checkValue(args[index], item, `${loc}[${index}]`));
    return;
  }
  const element = args[0];
  if (!element) {
    fail(`${loc}: array type has no element type`);
    return;
  }
  value.forEach((item, index) => checkValue(element, item, `${loc}[${index}]`));
}

function checkObject(type, value, loc) {
  const objectLike = (type.flags & (ts.TypeFlags.Object | ts.TypeFlags.Intersection)) !== 0;
  if (!objectLike || checker.isArrayType(type) || checker.isTupleType(type)) {
    fail(`${loc}: golden is an object but type is ${checker.typeToString(type)}`);
    return;
  }
  const props = checker.getPropertiesOfType(type).filter(declaredInTypes);
  const byName = new Map(props.map((sym) => [sym.name, sym]));
  const stringIndex = stringIndexInfo(type);
  for (const key of Object.keys(value)) {
    const sym = byName.get(key);
    if (sym) {
      checkValue(propertyType(sym), value[key], `${loc}.${key}`);
      continue;
    }
    if (stringIndex) {
      checkValue(stringIndex.type, value[key], `${loc}.${key}`);
      continue;
    }
    fail(`${loc}.${key}: golden field is not declared on ${checker.typeToString(type)}`);
  }
  for (const sym of props) {
    if (isOptional(sym)) continue;
    if (!Object.prototype.hasOwnProperty.call(value, sym.name)) {
      fail(`${loc}.${sym.name}: required by the type but absent from the golden`);
    }
  }
}

function declaredInTypes(sym) {
  return (sym.declarations ?? []).some((decl) => decl.getSourceFile().fileName.endsWith(`${path.sep}types.ts`));
}

function propertyType(sym) {
  const decl = sym.valueDeclaration ?? sym.declarations?.[0];
  if (!decl) return checker.getTypeOfSymbol(sym);
  return checker.getTypeOfSymbolAtLocation(sym, decl);
}

function isOptional(sym) {
  return (sym.flags & ts.SymbolFlags.Optional) !== 0;
}

function stringIndexInfo(type) {
  if (typeof checker.getIndexInfosOfType === "function") {
    return (
      checker
        .getIndexInfosOfType(type)
        .find((info) => (info.keyType.flags & ts.TypeFlags.String) !== 0) ?? null
    );
  }
  if (typeof checker.getIndexInfoOfType === "function") {
    return checker.getIndexInfoOfType(type, ts.IndexKind.String) ?? null;
  }
  return null;
}

function isWide(type) {
  return (type.flags & (ts.TypeFlags.Any | ts.TypeFlags.Unknown)) !== 0;
}

function stringOk(type, value) {
  if ((type.flags & ts.TypeFlags.String) !== 0) return true;
  if ((type.flags & ts.TypeFlags.StringLiteral) !== 0) return type.value === value;
  return false;
}

function numberOk(type, value) {
  if (!Number.isFinite(value)) return false;
  if ((type.flags & ts.TypeFlags.Number) !== 0) return true;
  if ((type.flags & ts.TypeFlags.NumberLiteral) !== 0) return type.value === value;
  return false;
}

function boolOk(type, value) {
  if ((type.flags & ts.TypeFlags.Boolean) !== 0) return true;
  if ((type.flags & ts.TypeFlags.BooleanLiteral) !== 0) {
    return (type.intrinsicName === "true") === value;
  }
  return false;
}

function jsonKind(value) {
  if (value === null) return "null";
  if (Array.isArray(value)) return "array";
  return typeof value;
}
JS
