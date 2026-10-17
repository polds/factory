import type { SSERecord } from "../../api/sse";
import type { TimelinePhase } from "../../api/types";
import type { PhaseGroup, SteerSignal, TranscriptKind, TranscriptRow } from "./types";

type Fields = {
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
  skillPath?: string;
  bundledSkillId?: string;
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
};

function readObject(data: string): Record<string, unknown> | null {
  try {
    const value: unknown = JSON.parse(data);
    if (!value || typeof value !== "object" || Array.isArray(value)) return null;
    return value as Record<string, unknown>;
  } catch {
    return null;
  }
}

function readString(obj: Record<string, unknown>, key: string): string | undefined {
  const value = obj[key];
  return typeof value === "string" ? value : undefined;
}

function readNumber(obj: Record<string, unknown>, key: string): number | undefined {
  const value = obj[key];
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function readBoolean(obj: Record<string, unknown>, key: string): boolean | undefined {
  const value = obj[key];
  return typeof value === "boolean" ? value : undefined;
}

function readFields(data: string): Fields {
  const obj = readObject(data);
  if (!obj) return {};
  return {
    turn: readNumber(obj, "turn"),
    role: readString(obj, "role"),
    text: readString(obj, "text"),
    tool: readString(obj, "tool"),
    redactedInput: readString(obj, "redacted_input"),
    ok: readBoolean(obj, "ok"),
    durationMs: readNumber(obj, "duration_ms"),
    path: readString(obj, "path"),
    additions: readNumber(obj, "additions"),
    deletions: readNumber(obj, "deletions"),
    skillPath: readString(obj, "skill_path"),
    bundledSkillId: readString(obj, "bundled_skill_id"),
    modelId: readString(obj, "model_id"),
    inputTokens: readNumber(obj, "input_tokens"),
    outputTokens: readNumber(obj, "output_tokens"),
    costCents: readNumber(obj, "cost_cents"),
    status: readString(obj, "status"),
    summary: readString(obj, "summary"),
    workerId: readString(obj, "worker_id"),
    steer: readBoolean(obj, "steer"),
    steerId: readString(obj, "steer_id"),
    artifactUid: readString(obj, "artifact_uid"),
  };
}

function roleLabel(role: string | undefined): string {
  if (role === "assistant") return "Assistant";
  if (role === "user") return "User";
  if (role === "system") return "System";
  return role && role.length > 0 ? role : "Message";
}

function labelFor(event: string, fields: Fields): { kind: TranscriptKind; label: string } {
  switch (event) {
    case "turn.started":
      return { kind: "turn", label: "Turn" };
    case "message.delta":
      return { kind: "message", label: roleLabel(fields.role) };
    case "tool.call":
      return { kind: "tool-call", label: "Tool" };
    case "tool.result":
      return { kind: "tool-result", label: "Result" };
    case "skill.read":
      return { kind: "skill", label: "Skill" };
    case "file.changed":
      return { kind: "file", label: "File" };
    case "usage":
      return { kind: "usage", label: "Usage" };
    case "question":
      return { kind: "question", label: "Question" };
    case "terminal":
      return { kind: "terminal", label: "Terminal" };
    case "run.status":
      return { kind: "run-status", label: "Status" };
    case "run.terminal":
      return { kind: "run-terminal", label: "Run ended" };
    case "run.capabilities":
      return { kind: "capabilities", label: "Capabilities" };
    case "turn.interrupted":
      return { kind: "interrupted", label: "Interrupt" };
    case "followup.received":
      return { kind: "followup", label: "Follow-up" };
    case "artifact":
    case "screenshot":
      return { kind: "artifact", label: "Artifact" };
    default:
      return { kind: "unknown", label: event || "event" };
  }
}

function rowFrom(event: SSERecord, index: number): TranscriptRow {
  const fields = readFields(event.data);
  const { kind, label } = labelFor(event.event, fields);
  const seq = event.id || String(index + 1);
  return {
    kind,
    key: event.id || `event-${index}`,
    seq,
    seqs: [seq],
    label,
    turn: fields.turn,
    role: fields.role,
    text: fields.text,
    tool: fields.tool,
    redactedInput: fields.redactedInput,
    ok: fields.ok,
    durationMs: fields.durationMs,
    path: fields.path ?? fields.skillPath ?? fields.bundledSkillId,
    additions: fields.additions,
    deletions: fields.deletions,
    modelId: fields.modelId,
    inputTokens: fields.inputTokens,
    outputTokens: fields.outputTokens,
    costCents: fields.costCents,
    status: fields.status,
    summary: fields.summary,
    workerId: fields.workerId,
    steer: fields.steer,
    steerId: fields.steerId,
    artifactUid: fields.artifactUid ?? (kind === "artifact" ? fields.text : undefined),
    event: kind === "unknown" ? event.event || "event" : undefined,
  };
}

function sameDelta(left: TranscriptRow, right: TranscriptRow): boolean {
  return (
    left.kind === "message" &&
    right.kind === "message" &&
    left.turn === right.turn &&
    (left.role ?? "") === (right.role ?? "")
  );
}

export function rowsFromEvents(events: readonly SSERecord[]): TranscriptRow[] {
  const rows: TranscriptRow[] = [];
  for (let index = 0; index < events.length; index += 1) {
    const event = events[index];
    if (!event) continue;
    const row = rowFrom(event, index);
    const prev = rows[rows.length - 1];
    if (prev && sameDelta(prev, row)) {
      prev.text = `${prev.text ?? ""}${row.text ?? ""}`;
      prev.seqs.push(row.seq);
      prev.seq =
        prev.seqs.length > 1 ? `${prev.seqs[0]}–${prev.seqs[prev.seqs.length - 1]}` : prev.seq;
      continue;
    }
    rows.push(row);
  }
  return rows;
}

export function steerSignals(events: readonly SSERecord[]): SteerSignal[] {
  const signals: SteerSignal[] = [];
  for (const event of events) {
    const fields = readFields(event.data);
    if (event.event === "followup.received" && fields.steerId) {
      signals.push({ id: fields.steerId, kind: "followup" });
    } else if (event.event === "turn.interrupted") {
      signals.push({ id: fields.steerId ?? "", kind: "interrupt" });
    }
  }
  return signals;
}

export function groupPhases(phases: readonly TimelinePhase[]): PhaseGroup[] {
  const groups = new Map<string, TimelinePhase[]>();
  for (const phase of phases) {
    const existing = groups.get(phase.execution_id);
    if (existing) existing.push(phase);
    else groups.set(phase.execution_id, [phase]);
  }
  return [...groups.entries()].map(([executionId, items]) => ({
    executionId,
    phases: items,
  }));
}

export function isTerminalStatus(status: string): boolean {
  return status === "succeeded" || status === "failed" || status === "cancelled";
}

export function canCancel(status: string): boolean {
  return status === "queued" || status === "assigned" || status === "running";
}
