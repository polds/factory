import type { FailureClass } from "../../api/types";

const FAILURE_CLASSES = [
  "agent",
  "infrastructure",
  "configuration",
  "cancelled",
] as const satisfies readonly FailureClass[];

export function isFailureClass(value: string | undefined): value is FailureClass {
  return FAILURE_CLASSES.some((failureClass) => failureClass === value);
}

export function shortId(id: string): string {
  const split = id.indexOf("_");
  if (split <= 0 || id.length < 12) return id;
  return `${id.slice(0, split)}_…${id.slice(split + 1).slice(-6)}`;
}

export function formatCost(cents: number | undefined): string {
  if (cents === undefined) return "—";
  return `$${(cents / 100).toFixed(2)}`;
}

export function formatCount(value: number): string {
  return new Intl.NumberFormat("en-US").format(value);
}

export function prNumber(url: string): string | null {
  const match = /\/(\d+)\/?$/.exec(url);
  return match?.[1] ?? null;
}

export function workerLabel(worker: string | undefined): string {
  return worker && worker.length > 0 ? worker : "—";
}

export function snapshotRows(
  value: Record<string, string> | undefined,
): Array<{ key: string; value: string }> | null {
  if (!value) return null;
  const rows: Array<{ key: string; value: string }> = [];
  for (const [key, item] of Object.entries(value)) {
    if (typeof item === "string") rows.push({ key, value: item });
  }
  return rows.length > 0 ? rows : null;
}

export const QUEUE_CAVEAT =
  "Queue wait is time from creation to first start (assigned_at). Time after a requeue counts as execution.";

export const INFRA_CAVEAT = "Infrastructure failures are not agent quality.";
