import type { RunDetail, TimelinePhase } from "../../api/types";

export type { RunRow } from "../../api/types";

export type RunDetailWire = RunDetail;

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
