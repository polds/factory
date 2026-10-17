import { clearAPIKey, getAPIKey } from "./session";
import type {
  Agent,
  ApplyResponse,
  Approval,
  ApprovalDecision,
  Automation,
  ConnectedWorkers,
  DefinitionView,
  Delivery,
  Factory,
  FactorySummary,
  HarnessUsage,
  Identity,
  InboxItem,
  InputAnswer,
  Problem,
  Run,
  RunDetail,
  RunListQuery,
  SecretPresence,
  SteerAccepted,
  SteerMessage,
  SteerRecord,
  Task,
  TaskCreate,
  TimelinePhase,
  ValidateResponse,
  Webhook,
} from "./types";

export class APIError extends Error {
  readonly status: number;
  readonly problem: Problem | null;

  constructor(status: number, problem: Problem | null) {
    super(problem?.detail || problem?.title || `HTTP ${status}`);
    this.name = "APIError";
    this.status = status;
    this.problem = problem;
  }

  toJSON(): { name: string; status: number; title: string } {
    return { name: this.name, status: this.status, title: this.problem?.title ?? "" };
  }
}

export class UnauthorizedError extends APIError {
  constructor() {
    super(401, { title: "unauthorized", status: 401, detail: "unauthorized" });
    this.name = "UnauthorizedError";
  }
}

type Auth = "session" | { bearer: string };

function segment(value: string): string {
  return encodeURIComponent(value);
}

function withQuery(path: string, query: Record<string, string | number | undefined>): string {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined || value === "") continue;
    params.set(key, String(value));
  }
  const qs = params.toString();
  return qs ? `${path}?${qs}` : path;
}

async function readProblem(text: string): Promise<Problem | null> {
  if (!text) return null;
  try {
    return JSON.parse(text) as Problem;
  } catch {
    return null;
  }
}

export class ConsoleClient {
  constructor(private readonly fetchImpl?: typeof fetch) {}

  listFactories(): Promise<Factory[]> {
    return this.request("/api/v1/factory");
  }

  getFactory(id: string): Promise<Factory> {
    return this.request(`/api/v1/factory/${segment(id)}`);
  }

  getDefinition(factoryId: string): Promise<DefinitionView> {
    return this.request(`/api/v1/factory/${segment(factoryId)}/definition`);
  }

  validateDefinition(factoryId: string, body: BodyInit): Promise<ValidateResponse> {
    return this.request(`/api/v1/factory/${segment(factoryId)}/definition:validate`, {
      method: "POST",
      body,
      headers: { "Content-Type": "application/octet-stream" },
    });
  }

  applyDefinition(factoryId: string): Promise<ApplyResponse> {
    return this.request(`/api/v1/factory/${segment(factoryId)}/definition:apply`, {
      method: "POST",
    });
  }

  listAgents(factoryId: string): Promise<Agent[]> {
    return this.request(`/api/v1/factory/${segment(factoryId)}/agents`);
  }

  listRuns(query: RunListQuery = {}): Promise<Run[]> {
    return this.request(
      withQuery("/api/v1/agent/runs", {
        factory_id: query.factory_id,
        failure_class: query.failure_class,
        source: query.source,
        parent_run_id: query.parent_run_id,
        work_item_id: query.work_item_id,
        agent: query.agent,
        status: query.status,
        worker: query.worker,
        model: query.model,
      }),
    );
  }

  listRunSteers(id: string): Promise<SteerRecord[]> {
    return this.request(`/api/v1/agent/runs/${segment(id)}/steers`);
  }

  getRun(id: string): Promise<RunDetail> {
    return this.request(`/api/v1/agent/runs/${segment(id)}`);
  }

  cancelRun(id: string): Promise<Run> {
    return this.request(`/api/v1/agent/runs/${segment(id)}/cancel`, { method: "POST" });
  }

  runTimeline(id: string): Promise<TimelinePhase[]> {
    return this.request(`/api/v1/agent/runs/${segment(id)}/timeline`);
  }

  runUsage(id: string): Promise<HarnessUsage> {
    return this.request(`/api/v1/agent/runs/${segment(id)}/harness-usage`);
  }

  interruptRun(id: string): Promise<SteerAccepted> {
    return this.request(`/api/v1/agent/runs/${segment(id)}/interrupt`, { method: "POST" });
  }

  followUp(id: string, text: string): Promise<SteerAccepted> {
    return this.request(`/api/v1/agent/runs/${segment(id)}/followups`, {
      method: "POST",
      body: JSON.stringify({ text }),
    });
  }

  nextSteers(runId: string, after: string, runToken: string): Promise<SteerMessage[]> {
    return this.request(
      withQuery(`/api/v1/agent/runs/${segment(runId)}/steer`, { after }),
      { method: "GET" },
      { bearer: runToken },
    );
  }

  runEventsURL(runId: string): string {
    return `/api/v1/agent/runs/${segment(runId)}/events`;
  }

  factoryEventsURL(factoryId: string): string {
    return `/api/v1/factory/${segment(factoryId)}/events`;
  }

  inbox(factoryId: string): Promise<InboxItem[]> {
    return this.request(`/api/v1/factory/${segment(factoryId)}/inbox`);
  }

  listAutomations(factoryId: string): Promise<Automation[]> {
    return this.request(`/api/v1/factory/${segment(factoryId)}/automations`);
  }

  pauseAutomation(factoryId: string, name: string): Promise<void> {
    return this.request(
      `/api/v1/factory/${segment(factoryId)}/automations/${segment(name)}/pause`,
      {
        method: "POST",
      },
    );
  }

  resumeAutomation(factoryId: string, name: string): Promise<void> {
    return this.request(
      `/api/v1/factory/${segment(factoryId)}/automations/${segment(name)}/resume`,
      {
        method: "POST",
      },
    );
  }

  listWebhooks(factoryId: string): Promise<Webhook[]> {
    return this.request(`/api/v1/factory/${segment(factoryId)}/webhooks`);
  }

  listWebhookDeliveries(
    factoryId: string,
    webhookUid: string,
    query: { status?: string; limit?: number; include?: string } = {},
  ): Promise<Delivery[]> {
    return this.request(
      withQuery(
        `/api/v1/factory/${segment(factoryId)}/webhooks/${segment(webhookUid)}/deliveries`,
        {
          status: query.status,
          limit: query.limit === undefined ? undefined : String(query.limit),
          include: query.include,
        },
      ),
    );
  }

  listWorkers(): Promise<ConnectedWorkers> {
    return this.request("/api/v1/agent/connected-self-hosted-workers");
  }

  listSecrets(factoryId: string): Promise<SecretPresence[]> {
    return this.request(`/api/v1/factory/${segment(factoryId)}/secrets`);
  }

  listIdentities(): Promise<Identity[]> {
    return this.request("/api/v1/agent/identities");
  }

  summary(factoryId: string, range: { from?: string; to?: string } = {}): Promise<FactorySummary> {
    return this.request(withQuery(`/api/v1/factory/${segment(factoryId)}/summary`, range));
  }

  listTasks(
    factoryId: string,
    query: { stage?: string; status?: string; source?: string } = {},
  ): Promise<Task[]> {
    return this.request(withQuery(`/api/v1/factory/${segment(factoryId)}/tasks`, query));
  }

  getTask(factoryId: string, id: string): Promise<Task> {
    return this.request(`/api/v1/factory/${segment(factoryId)}/tasks/${segment(id)}`);
  }

  createTask(factoryId: string, body: TaskCreate): Promise<Task> {
    return this.request(`/api/v1/factory/${segment(factoryId)}/tasks`, {
      method: "POST",
      body: JSON.stringify(body),
    });
  }

  cancelTask(factoryId: string, id: string): Promise<Task> {
    return this.request(`/api/v1/factory/${segment(factoryId)}/tasks/${segment(id)}/cancel`, {
      method: "POST",
    });
  }

  listApprovals(factoryId: string, pending = true): Promise<Approval[]> {
    return this.request(
      withQuery(`/api/v1/factory/${segment(factoryId)}/approvals`, {
        pending: pending ? "true" : "false",
      }),
    );
  }

  decideApproval(id: string, decision: ApprovalDecision, note?: string): Promise<Approval> {
    return this.request(`/api/v1/approvals/${segment(id)}:decide`, {
      method: "POST",
      body: JSON.stringify({ decision, note }),
    });
  }

  listDeliveries(
    factoryId: string,
    query: { status?: string; limit?: number } = {},
  ): Promise<Delivery[]> {
    return this.request(withQuery(`/api/v1/factory/${segment(factoryId)}/deliveries`, query));
  }

  replayDelivery(id: string): Promise<Delivery> {
    return this.request(`/api/v1/deliveries/${segment(id)}:replay`, { method: "POST" });
  }

  answerInput(workItemId: string, text: string): Promise<InputAnswer> {
    return this.request(`/api/v1/work-items/${segment(workItemId)}/input`, {
      method: "POST",
      body: JSON.stringify({ text }),
    });
  }

  private async request<T>(
    path: string,
    init: RequestInit = {},
    auth: Auth = "session",
  ): Promise<T> {
    const headers = new Headers(init.headers);
    const bearer = auth === "session" ? getAPIKey() : auth.bearer;
    if (bearer) headers.set("Authorization", `Bearer ${bearer}`);
    if (!headers.has("Accept")) headers.set("Accept", "application/json");
    if (typeof init.body === "string" && !headers.has("Content-Type")) {
      headers.set("Content-Type", "application/json");
    }
    const fetchImpl = this.fetchImpl ?? globalThis.fetch;
    const response = await fetchImpl(path, { ...init, headers });
    const text = await response.text();
    if (response.status === 401) {
      if (auth === "session") clearAPIKey();
      throw new UnauthorizedError();
    }
    if (!response.ok) {
      throw new APIError(response.status, await readProblem(text));
    }
    if (!text) return undefined as T;
    try {
      return JSON.parse(text) as T;
    } catch {
      throw new APIError(response.status, null);
    }
  }
}

export const client = new ConsoleClient();
