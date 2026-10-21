import type { FailureClass, RunRow } from "../api/types";

export const PIPELINE_STAGES = [
  "triage",
  "planning",
  "building",
  "reviewing",
  "human_handoff",
] as const;

export type PipelineStage = (typeof PIPELINE_STAGES)[number];

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

export function isPipelineStage(value: string): value is PipelineStage {
  return (PIPELINE_STAGES as readonly string[]).includes(value);
}

export function stageLabel(stage: string): string {
  return isPipelineStage(stage) ? STAGE_LABELS[stage] : stage;
}

export function failureClassOf(
  run: (RunRow & { failure?: { class?: unknown } }) | undefined,
): FailureClass | undefined {
  if (!run) return undefined;
  if (isFailureClass(run.failure_class)) return run.failure_class;
  // Some responses still nest the class under `failure`. Read that field
  // directly; do not derive a class from failure_reason.
  if (isFailureClass(run.failure?.class)) return run.failure.class;
  return undefined;
}
