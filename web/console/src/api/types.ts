// Shapes from the M5 Contracts section, plus the existing public JSON
// already returned by internal/api. Fields the contract leaves as an
// ellipsis stay optional so a later golden check can tighten them.

export type FailureClass = "agent" | "infrastructure" | "configuration" | "cancelled";

export type Failure = {
  class: FailureClass;
  code: string;
  summary: string;
  retryable: boolean;
  evidence: string[];
};

export type SteerState = {
  enabled: boolean;
  reason: string;
  idle_until?: string;
};

export type Factory = {
  id: string;
  name: string;
  alias?: string;
};

export type Agent = {
  id: string;
  name: string;
  factory_id: string;
};

export type Diagnostic = {
  file: string;
  line: number;
  severity: string;
  message: string;
};

export type ValidateResponse = {
  diagnostics: Diagnostic[];
};

export type ApplyResponse = {
  id: string;
  diagnostics: Diagnostic[];
};

export type Run = {
  id: string;
  status: string;
  factory_id?: string;
  agent?: string;
  title?: string;
  priority?: string;
  repo?: string;
  work_item_id?: string;
  parent_run_id?: string;
  conversation_id?: string;
  stage?: string;
  model_id?: string;
  failure_reason?: string;
  created_at?: string;
  started_at?: string;
  worker?: string;
  source?: string;
  // List rows are flat. Detail rows nest the same facts under failure.
  failure_class?: FailureClass;
  failure_code?: string;
  failure_summary?: string;
  exit_code?: number;
  cost_cents?: number;
  kubernetes_snapshot?: Record<string, string>;
  failure?: Failure;
  queue_wait_ms?: number;
  exec_ms?: number;
  steer?: SteerState;
  pr_url?: string;
};

export type RunDetail = Run;

export type RunListQuery = {
  factory_id?: string;
  failure_class?: FailureClass;
  source?: string;
  parent_run_id?: string;
  work_item_id?: string;
  agent?: string;
  status?: string;
  worker?: string;
  model?: string;
};

export type TimelinePhaseName = "queued" | "assigned" | "running" | "idle" | "terminal";

export type TimelinePhase = {
  phase: TimelinePhaseName;
  at: string;
  duration_ms: number;
  execution_id: string;
};

export type UsageTotal = {
  input_tokens: number;
  output_tokens: number;
  cost_cents: number;
};

export type UsageByModel = UsageTotal & {
  model_id: string;
};

export type UsageCall = UsageTotal & {
  seq: number;
  turn: number;
  model_id: string;
};

export type HarnessUsage = {
  total: UsageTotal;
  by_model: UsageByModel[];
  calls: UsageCall[];
};

export type SteerAccepted = {
  steer_id: string;
};

export type SteerMessage = {
  id: string;
  kind: "interrupt" | "followup";
  text: string;
};

export type SteerRecord = {
  id: string;
  kind: "interrupt" | "followup";
  text: string;
  actor: string;
  created_at: string;
  delivered_at?: string;
};

export type DefinitionFile = {
  path: string;
  content: string;
};

export type DefinitionView = {
  source: "git" | "local";
  git_sha: string;
  production_ref: string;
  editable: boolean;
  forge_url?: string;
  files: DefinitionFile[];
};

export type InboxKind = "approval" | "question" | "failed_run";

export type InboxItem = {
  kind: InboxKind;
  id: string;
  work_item_id: string;
  run_id: string;
  created_at: string;
  title: string;
  detail: string;
  action: string;
};

export type Automation = {
  name: string;
  enabled: boolean;
  runtime_paused: boolean;
  triggers: string[];
  last_fired_at: string | null;
  match_count: number;
  runs_started: number;
};

export type Webhook = {
  uid: string;
  name: string;
  ingress_path: string;
  auth_mode: string;
  signature_scheme: string;
  enabled: boolean;
  last_delivery_at: string | null;
};

export type ConnectedWorker = {
  id: string;
  pool: string;
  platform: string;
  backend: string;
  version: string;
  in_flight: number;
  capacity: number;
  heartbeat_age_ms: number;
  reconnects: number;
  rejected_1h: number;
};

export type WorkerPool = {
  pool: string;
  queue_depth: number;
  p50_time_to_assign_ms: number;
  rejected_1h: number;
};

export type ConnectedWorkers = {
  workers: ConnectedWorker[];
  pools: WorkerPool[];
};

export type SecretUsedBy = {
  kind: "agent" | "webhook" | "harness" | "integration";
  name: string;
};

export type SecretPresence = {
  name: string;
  provider: string;
  present: boolean;
  used_by: SecretUsedBy[];
  last_used_run_id?: string;
};

export type IdentityKind = "github_app" | "github_pat" | "slack_bot" | "linear_key";

export type Identity = {
  subject: string;
  kind: IdentityKind;
  scope: string;
  last_used_at: string | null;
};

export type FactorySummary = {
  runs_by_status: Record<string, number>;
  runs_by_failure_class: Record<string, number>;
  queue_wait_p50_ms: number;
  active_runs: number;
  waiting_on_human: number;
  prs_opened: number;
  deliveries_unmatched: number;
};

export type Task = {
  id: string;
  factory_id: string;
  source: string;
  source_ref?: string;
  title: string;
  body?: string;
  stage: string;
  status: string;
  created_by?: string;
  conversation_id?: string;
  revision_count: number;
  pending_gate?: string;
  created_at: string;
};

export type TaskCreate = {
  title: string;
  body?: string;
  source?: string;
  source_ref?: string;
};

export type Approval = {
  id: string;
  work_item_id: string;
  gate: string;
  requested_at: string;
  decided_at?: string;
  decided_by?: string;
  decision?: string;
  note?: string;
};

export type ApprovalDecision = "approve" | "reject";

export type Delivery = {
  id: string;
  factory_id?: string;
  provider: string;
  webhook_uid?: string;
  delivery_id: string;
  received_at: string;
  headers?: Record<string, string>;
  body?: unknown;
  truncated: boolean;
  status: string;
  matched_automations: string[];
  normalized_events?: NormalizedEvent[];
};

export type NormalizedActor = {
  provider_id?: string;
  handle?: string;
  factory_user_id?: string | null;
};

export type NormalizedRepo = {
  code_forge: string;
  owner: string;
  name: string;
};

export type NormalizedSubject = {
  kind?: string;
  id?: string;
  number?: number;
  title?: string;
  url?: string;
  repo?: NormalizedRepo;
  labels?: string[];
  branch?: string | null;
  base_branch?: string | null;
};

export type NormalizedThread = {
  kind?: string;
  external_ref?: string;
};

export type NormalizedEvent = {
  provider?: string;
  event?: string;
  delivery_id?: string;
  occurred_at?: string;
  actor?: NormalizedActor;
  subject?: NormalizedSubject;
  thread?: NormalizedThread;
};

// Data object on the factory SSE stream. The writer always adds gid and
// run_id. The other fields come from the per-run event and vary by type.
export type FactoryEventData = {
  gid: number;
  run_id: string;
  seq?: number;
  turn?: number;
  role?: string;
  text?: string;
};

export type InputAnswer = {
  id: string;
  work_item_id: string;
  answer: string;
  answered_by: string;
};

export type Problem = {
  type?: string;
  title?: string;
  status?: number;
  detail?: string;
  reason?: string;
};
