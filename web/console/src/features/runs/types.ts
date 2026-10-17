import type { FailureClass, Run, RunDetail, TimelinePhase } from "../../api/types";

// The list golden is flat: failure_class, failure_summary, and cost_cents.
// api.Run has a nested failure object and no cost. T49 reconciles that.
export type RunRow = Run & {
  failure_class?: FailureClass | "";
  failure_summary?: string;
  cost_cents?: number;
  worker?: string;
  source?: string;
};

// Exit code and the kubernetes snapshot are not on RunDetail yet (T47b).
// The snapshot table renders only when a later response adds this map.
export type RunDetailWire = RunDetail & {
  cost_cents?: number;
  worker?: string;
  kubernetes_snapshot?: Record<string, string>;
};

export type RunFilters = {
  agent: string;
  status: string;
  source: string;
  model: string;
  worker: string;
  failure_class: string;
};

export const FILTER_KEYS = [
  "agent",
  "status",
  "source",
  "model",
  "worker",
  "failure_class",
] as const satisfies readonly (keyof RunFilters)[];

export type FilterKey = (typeof FILTER_KEYS)[number];

export type TranscriptKind =
  | "turn"
  | "message"
  | "tool-call"
  | "tool-result"
  | "skill"
  | "file"
  | "usage"
  | "question"
  | "terminal"
  | "run-status"
  | "run-terminal"
  | "capabilities"
  | "interrupted"
  | "followup"
  | "artifact"
  | "unknown";

export type TranscriptRow = {
  kind: TranscriptKind;
  key: string;
  seq: string;
  seqs: string[];
  label: string;
  turn?: number;
  role?: string;
  text?: string;
  tool?: string;
  redactedInput?: string;
  ok?: boolean;
  durationMs?: number;
  path?: string;
  additions?: number;
  deletions?: number;
  modelId?: string;
  inputTokens?: number;
  outputTokens?: number;
  costCents?: number;
  status?: string;
  summary?: string;
  workerId?: string;
  steer?: boolean;
  steerId?: string;
  artifactUid?: string;
  event?: string;
};

export type LocalSteer = {
  id: string;
  kind: "interrupt" | "followup";
};

export type SteerSignal = {
  id: string;
  kind: "interrupt" | "followup";
};

export type PhaseGroup = {
  executionId: string;
  phases: TimelinePhase[];
};
