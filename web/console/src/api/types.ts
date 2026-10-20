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

// RunRow is one flat GET /agent/runs element. queue_wait_ms and exec_ms are
// always present. failure_class, failure_code, and failure_summary sit on
// the row. The nested failure object, steer, and pr_url belong to the detail.
export type RunRow = {
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
  failure_class?: FailureClass;
  failure_code?: string;
  failure_summary?: string;
  queue_wait_ms: number;
  exec_ms: number;
  cost_cents?: number;
  exit_code?: number;
};

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

export type InboxKind = "approval" | "question" | "failed_run" | "improvement_pr";

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
  status?: string;
  stage?: string;
};

export type ScorerResultStatus = "ok" | "scorer_invalid" | "pending";

export type ScoreAccepted = {
  result_id: string;
  judge_run_id: string;
};

export type ScorerResult = {
  id: string;
  run_id: string;
  label: string;
  score: number;
  passed: boolean;
  reasoning: string;
  status: ScorerResultStatus;
  created_at: string;
  reviewed_at?: string;
};

export type BenchmarkAccepted = {
  benchmark_run_id: string;
};

export type ModelTokens = {
  model_id: string;
  input_tokens: number;
  output_tokens: number;
};

export type BenchmarkCell = {
  configuration: string;
  task: string;
  n: number;
  pass: number;
  errored: number;
  pass_rate: number;
  ci_low: number;
  ci_high: number;
  median_cost_cents: number;
  median_duration_ms: number;
  tokens_by_model: ModelTokens[];
};

export type BenchmarkReport = {
  winner: string;
  reason: string;
};

export type BenchmarkTrials = {
  cells: BenchmarkCell[];
  report: BenchmarkReport;
};

export type ImprovementBatchStatus =
  "queued" | "running" | "pr_open" | "merged" | "closed" | "failed";

export type ImprovementFailure = {
  run_id: string;
  result_id: string;
  scorer: string;
  label: string;
};

export type ImprovementBatch = {
  id: string;
  agent: string;
  status: ImprovementBatchStatus;
  failures: ImprovementFailure[];
  pr_url: string;
  created_at: string;
};

export type ImprovementRunNow = {
  checked_agents: string[];
  batches: ImprovementBatch[];
};

export type DayPassRate = {
  day: string;
  pass_rate: number;
  n: number;
};

export type ScorerSummary = {
  name: string;
  agents: string[];
  labels: string[];
  passing_score: number;
  sampling_rate: number;
  model: string;
  self_improvement: boolean;
  pass_rate_by_day: DayPassRate[];
};

export type RunScore = {
  scorer: string;
  label: string;
  score: number;
  passed: boolean;
  reasoning: string;
  status: ScorerResultStatus;
};

export type BenchmarkLastRun = {
  id: string;
  created_at: string;
  repetitions: number;
};

export type BenchmarkSummary = {
  suite: string;
  agent: string;
  configurations: string[];
  tasks: string[];
  last_run?: BenchmarkLastRun;
};

export type HeadRef = {
  host: string;
  owner: string;
  repo: string;
  sha: string;
};

export type CapturedBenchmarkTask = {
  path: string;
  content: string;
  head_refs: HeadRef[];
};

export type NamedCount = {
  name: string;
  count: number;
};

export type RunTotals = {
  by_agent: NamedCount[];
  by_status: NamedCount[];
  by_source: NamedCount[];
  by_model: NamedCount[];
  measurement: number;
};

export type AutonomyMetric = {
  share: number;
  n: number;
  forge_connected_at?: string;
};

export type CycleTimeStage = {
  name: string;
  p50_ms: number;
};

export type CycleTimeMetric = {
  p50_ms: number;
  stages: CycleTimeStage[];
};

export type PRSizeCost = {
  S: number;
  M: number;
  L: number;
  XL: number;
};

export type CostPerPRMetric = {
  median_cents: number;
  by_size: PRSizeCost;
};

export type ExpensivePR = {
  repo: string;
  number: number;
  cost_cents: number;
  changed_lines: number;
  url: string;
};

export type ScorerCard = {
  name: string;
  pass_rate_by_day: DayPassRate[];
};

export type ImprovementPR = {
  batch_id: string;
  agent: string;
  url: string;
  created_at: string;
};

export type FactoryMetrics = {
  total_runs: RunTotals;
  prs_opened: number;
  prs_merged: number;
  autonomy: AutonomyMetric;
  cycle_time: CycleTimeMetric;
  cost_per_pr: CostPerPRMetric;
  most_expensive_prs: ExpensivePR[];
  scorer_cards: ScorerCard[];
  improvement_prs: ImprovementPR[];
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
