import { APIError } from "../../api/client";
import type { FailureClass, Run } from "../../api/types";

// The run list row is flat on the wire. T39's TypeScript `Run` still nests
// `failure`. Goldens in internal/api/testdata/console/runs.json win.
export type RunRow = Run & {
  failure_class?: FailureClass;
  failure_summary?: string;
  cost_cents?: number;
  created_at?: string;
};

export const FAILURE_CLASSES = [
  "agent",
  "infrastructure",
  "configuration",
  "cancelled",
] as const satisfies readonly FailureClass[];

export const RUN_STATUSES = [
  "queued",
  "running",
  "idle",
  "succeeded",
  "failed",
  "cancelled",
] as const;

export type KnownRunStatus = (typeof RUN_STATUSES)[number];

export const PIPELINE_STAGES = [
  "triage",
  "planning",
  "building",
  "reviewing",
  "human_handoff",
] as const;

export type PipelineStage = (typeof PIPELINE_STAGES)[number];

export type RangeKey = "24h" | "7d";

const STAGE_LABELS = {
  triage: "Triage",
  planning: "Planning",
  building: "Building",
  reviewing: "Reviewing",
  human_handoff: "Handoff",
} as const satisfies Record<PipelineStage, string>;

export function isFailureClass(value: unknown): value is FailureClass {
  return (
    value === "agent" ||
    value === "infrastructure" ||
    value === "configuration" ||
    value === "cancelled"
  );
}

export function isKnownRunStatus(value: string): value is KnownRunStatus {
  return (RUN_STATUSES as readonly string[]).includes(value);
}

export function isPipelineStage(value: string): value is PipelineStage {
  return (PIPELINE_STAGES as readonly string[]).includes(value);
}

export function stageLabel(stage: string): string {
  return isPipelineStage(stage) ? STAGE_LABELS[stage] : stage;
}

export function isDoneStatus(status: string): boolean {
  return status === "complete" || status === "cancelled";
}

export function failureClassOf(run: RunRow | undefined): FailureClass | undefined {
  if (!run) return undefined;
  if (isFailureClass(run.failure_class)) return run.failure_class;
  // Some responses still nest the class under `failure`. Read that field
  // directly; do not derive a class from failure_reason.
  if (isFailureClass(run.failure?.class)) return run.failure.class;
  return undefined;
}

// ListRuns is newest first: created_at DESC, id DESC. Compare those fields so
// the chosen run does not depend on where it sits in the array.
export function latestByWorkItem(runs: readonly RunRow[]): Map<string, RunRow> {
  const latest = new Map<string, RunRow>();
  for (const run of runs) {
    const workItemId = run.work_item_id;
    if (!workItemId) continue;
    const current = latest.get(workItemId);
    if (!current || compareRunRecency(run, current) > 0) latest.set(workItemId, run);
  }
  return latest;
}

function compareRunRecency(left: RunRow, right: RunRow): number {
  const byTime = (left.created_at ?? "").localeCompare(right.created_at ?? "");
  if (byTime !== 0) return byTime;
  return left.id.localeCompare(right.id);
}

export function rangeBounds(range: RangeKey, now = Date.now()): { from: string; to: string } {
  const span = range === "24h" ? 86_400_000 : 7 * 86_400_000;
  return {
    from: new Date(now - span).toISOString(),
    to: new Date(now).toISOString(),
  };
}

export function sumNumbers(values: readonly number[]): number {
  return values.reduce((total, value) => total + value, 0);
}

export function sumMaps(maps: readonly Record<string, number>[]): Record<string, number> {
  const totals: Record<string, number> = {};
  for (const map of maps) {
    for (const [key, value] of Object.entries(map)) {
      totals[key] = (totals[key] ?? 0) + value;
    }
  }
  return totals;
}

export function orderedCounts(
  totals: Record<string, number>,
  order: readonly string[],
): { key: string; count: number }[] {
  const rows = order.map((key) => ({ key, count: totals[key] ?? 0 }));
  for (const [key, count] of Object.entries(totals)) {
    if (!order.includes(key)) rows.push({ key, count });
  }
  return rows;
}

export function formatAge(iso: string, now = Date.now()): string {
  const then = Date.parse(iso);
  if (!Number.isFinite(then)) return "unknown";
  const minutes = Math.round(Math.max(0, now - then) / 60_000);
  if (minutes < 1) return "just now";
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.round(minutes / 60);
  if (hours < 48) return `${hours}h`;
  return `${Math.round(hours / 24)}d`;
}

export function problemDetail(err: unknown): string {
  if (err instanceof APIError) {
    return err.problem?.detail || err.problem?.title || err.message;
  }
  return "The request failed";
}

export function unexpected(value: never): never {
  throw new Error(`Unexpected value: ${String(value)}`);
}
