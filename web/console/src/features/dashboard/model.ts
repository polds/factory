import { APIError } from "../../api/client";
import type { FailureClass, RunRow } from "../../api/types";
import { unexpected } from "../../lib/unexpected";

export type { RunRow } from "../../api/types";
export {
  failureClassOf,
  isFailureClass,
  isPipelineStage,
  PIPELINE_STAGES,
  stageLabel,
} from "../../lib/run-labels";
export type { PipelineStage } from "../../lib/run-labels";
export { unexpected };

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

export type RangeKey = "24h" | "7d";

export function isKnownRunStatus(value: string): value is KnownRunStatus {
  return (RUN_STATUSES as readonly string[]).includes(value);
}

export function isDoneStatus(status: string): boolean {
  return status === "complete" || status === "cancelled";
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

export function compareRunRecency(left: RunRow, right: RunRow): number {
  const byTime = createdAtMillis(left.created_at) - createdAtMillis(right.created_at);
  if (byTime !== 0) return byTime;
  return left.id.localeCompare(right.id);
}

function createdAtMillis(value: string | undefined): number {
  if (!value) return Number.NEGATIVE_INFINITY;
  const parsed = Date.parse(value);
  return Number.isNaN(parsed) ? Number.NEGATIVE_INFINITY : parsed;
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
