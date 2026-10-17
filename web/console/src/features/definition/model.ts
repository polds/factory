import type { DefinitionFile, Diagnostic } from "../../api/types";

export function cloneFiles(files: readonly DefinitionFile[]): DefinitionFile[] {
  return files.map((file) => ({ path: file.path, content: file.content }));
}

export function filesKey(files: readonly DefinitionFile[]): string {
  return JSON.stringify(files.map((file) => [file.path, file.content]));
}

export function filesEqual(
  left: readonly DefinitionFile[],
  right: readonly DefinitionFile[],
): boolean {
  if (left.length !== right.length) return false;
  const contents = new Map(left.map((file) => [file.path, file.content]));
  return right.every((file) => contents.get(file.path) === file.content);
}

export type FileDiff = {
  created: string[];
  updated: string[];
  deleted: string[];
};

export function diffFiles(
  baseline: readonly DefinitionFile[],
  draft: readonly DefinitionFile[],
): FileDiff {
  const before = new Map(baseline.map((file) => [file.path, file.content]));
  const after = new Map(draft.map((file) => [file.path, file.content]));
  const created: string[] = [];
  const updated: string[] = [];
  const deleted: string[] = [];
  for (const [path, content] of after) {
    if (!before.has(path)) created.push(path);
    else if (before.get(path) !== content) updated.push(path);
  }
  for (const path of before.keys()) {
    if (!after.has(path)) deleted.push(path);
  }
  return { created, updated, deleted };
}

export function schemaVersion(files: readonly DefinitionFile[]): string | null {
  const factory = files.find((file) => file.path === "factory.yaml") ?? files[0];
  if (!factory) return null;
  const match = /^(?:schemaVersion|apiVersion):\s*([^\s#]+)\s*$/m.exec(factory.content);
  return match?.[1] ?? null;
}

export function shortSha(sha: string): string {
  if (!sha) return "—";
  return sha.slice(0, 7);
}

export function errorCount(diagnostics: readonly Diagnostic[]): number {
  return diagnostics.filter((diagnostic) => diagnostic.severity.toLowerCase() === "error").length;
}

export function lineCount(value: string): number {
  if (value === "") return 1;
  return value.split("\n").length;
}

export function lineOffset(value: string, line: number): number {
  const lines = value.split("\n");
  const index = Math.max(0, Math.min(line, lines.length) - 1);
  let offset = 0;
  for (let i = 0; i < index; i += 1) offset += (lines[i]?.length ?? 0) + 1;
  return offset;
}
