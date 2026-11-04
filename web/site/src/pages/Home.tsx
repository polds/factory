import { FileText, GitPullRequest, Inbox, ShieldCheck } from "lucide-react";
import type { ReactNode } from "react";
import { useEffect } from "react";
import { Link } from "react-router";

import { CopyButton } from "../components/CopyButton.tsx";
import { Pipeline } from "../components/Pipeline.tsx";
import { Footer, GITHUB, SiteNav } from "../components/SiteNav.tsx";

// Excerpt of examples/acme-factory/factory.yaml. The marked keys are the ones
// this design adds to Warp's v1alpha1 schema.
const yamlLines: { text: string; mark?: number }[] = [
  { text: "schemaVersion: v1alpha1" },
  { text: "name: acme-payments" },
  { text: "repositories:" },
  { text: "  - { codeForge: GITHUB, owner: acme, name: payments-service }" },
  { text: "" },
  { text: "inference:", mark: 1 },
  { text: "  default: { provider: anthropic, credentialRef: ANTHROPIC_API_KEY }", mark: 1 },
  { text: "  providers:", mark: 1 },
  {
    text: "    local: { kind: openai-compatible, baseUrl: http://vllm.internal:8000/v1 }",
    mark: 1,
  },
  { text: "" },
  { text: "pipeline:", mark: 2 },
  { text: "  stages: [triage, planning, building, reviewing]", mark: 2 },
  {
    text: "  agents: { triage: triage, planning: spec, building: implementer, reviewing: reviewer }",
    mark: 2,
  },
  { text: "  skip: { planning: whenScopeClear }", mark: 3 },
  { text: "  approvals:", mark: 4 },
  { text: "    spec:    { required: true,  approvers: role:editor }", mark: 4 },
  { text: "    handoff: { required: true,  approvers: role:user }", mark: 4 },
  { text: "  revisionLimit: 3", mark: 2 },
];

const callouts = [
  {
    n: 1,
    title: "Bring your own inference",
    body: "A default provider plus named alternatives, including any OpenAI-compatible endpoint on your own network. The gateway lives in factoryd; keys never reach the agent container.",
  },
  {
    n: 2,
    title: "Stages map to agents",
    body: "Each stage names an agent definition in agents/. The orchestrator moves a run from stage to stage; no model is asked to remember the workflow.",
  },
  {
    n: 3,
    title: "Predicates, not prompts",
    body: "Skip rules are evaluated by the orchestrator against the run's state. They can be validated, diffed and tested like any other config.",
  },
  {
    n: 4,
    title: "Gates that actually gate",
    body: "Approvals are enforced by the control plane. A run waits at the gate until someone with the role approves; the merge gate is left to branch protection.",
  },
];

const how = [
  {
    icon: Inbox,
    title: "Work comes in",
    body: "A GitHub label, a Slack thread, a CI webhook, a schedule or a prompt from the CLI. Automations decide which factory and which agent pick it up.",
    meta: "intake",
  },
  {
    icon: FileText,
    title: "An agent writes the plan",
    body: "The planning stage runs agents/spec: scope, approach, test plan. If the intake already carries scope, the orchestrator skips straight to building.",
    meta: "planning · agents/spec",
  },
  {
    icon: ShieldCheck,
    title: "A person approves it",
    body: "The run stops at the spec gate until someone with the editor role approves. No approval, no code. The gate is enforced by factoryd, not asked of a model.",
    meta: "approval: spec · role:editor",
  },
  {
    icon: GitPullRequest,
    title: "Build, review, hand off",
    body: "agents/implementer writes the change; agents/reviewer pushes back, up to three revisions. A second gate hands the finished pull request to you.",
    meta: "building → reviewing → approval: handoff",
  },
];

const layers = [
  {
    title: "Agent",
    body: "Any image that implements Harness Contract v1: mounted at /agent, fed a task spec, emitting NDJSON events.",
    tags: ["Claude Code", "Codex", "Warp Agent CLI", "your own"],
  },
  {
    title: "Model",
    body: "Anthropic, OpenAI, or an OpenAI-compatible endpoint such as vLLM. Routed per agent, scorer or benchmark.",
    tags: ["anthropic", "openai-compatible", "local"],
  },
  {
    title: "Compute",
    body: "The data plane is the unmodified MIT oz-agent-worker with its four execution backends.",
    tags: ["docker", "kubernetes", "direct", "command"],
  },
  {
    title: "Data",
    body: "Runs, definitions and the queue in Postgres; transcripts and artifacts in S3-compatible storage. All of it stays with you.",
    tags: ["postgres", "minio / s3"],
  },
];

const shapes = [
  {
    title: "Laptop",
    body: "Control plane, Postgres, MinIO and one worker on one machine.",
    cmd: "docker compose -f deploy/docker-compose.yml up -d --build --wait",
  },
  {
    title: "Self-hosted cloud",
    body: "Helm chart with the control plane, the operator and worker pools, plus migration and preflight hooks.",
    cmd: "helm install factory deploy/helm/factory",
  },
  {
    title: "Kubernetes-native",
    body: "factory.dev/v1alpha1 CRDs reconciled by factory-operator. Generate them from the same definition tree.",
    cmd: "factoryctl convert crds examples/acme-factory",
  },
];

const milestones = [
  ["M0", "Protocol spike", "done"],
  ["M1", "Harness Contract v1", "done"],
  ["M2", "Control plane", "done"],
  ["M3", "Factory semantics", "done"],
  ["M4", "Integrations", "done"],
  ["M5", "Console", "done"],
  ["M6", "Measure and improve", "now"],
  ["M7", "Kubernetes", "now"],
  ["M8", "Hardening", "later"],
] as const;

const spike = [
  ["  PASS  worker authenticated         m0-spike", "ok"],
  ["  PASS  task assignment accepted     task_assignment written to socket", "ok"],
  ["  PASS  task claimed                 m0-spike", "ok"],
  ["  PASS  heartbeat answered           1 ping/pong exchange(s)", "ok"],
  ["  PASS  terminal state               task_completed: Task completed successfully", "ok"],
  ["VERDICT: confirmed — an unmodified worker ran our harness against our server.", "dim"],
] as const;

const validator = [
  ["5 problem(s) in examples/acme-factory:", "dim"],
  ['  factory.yaml:33: FF_UNKNOWN_FIELD: unknown field "pool"', "err"],
  ['  factory.yaml:36: FF_UNKNOWN_FIELD: unknown field "inference"', "err"],
  ['  factory.yaml:48: FF_UNKNOWN_FIELD: unknown field "pipeline"', "err"],
  ['  factory.yaml:66: FF_UNKNOWN_FIELD: unknown field "staleFailureDays"', "err"],
  ['  factory.yaml:71: FF_UNKNOWN_FIELD: unknown field "model"      # scorerDefaults.model', "err"],
  ["Validated with the warp-server parser for v1alpha1", "ok"],
] as const;

const quickstart = `cp deploy/.env.example deploy/.env
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o harness/generic-openai/factory-harness ./cmd/factory-harness
docker compose -f deploy/docker-compose.yml up -d --build --wait
go build -o bin/factoryctl ./cmd/factoryctl
FACTORY_API_KEY=m2-dev-api-key ./bin/factoryctl run --prompt "…" --factory m2 --watch`;

function Container({ children, className = "" }: { children: ReactNode; className?: string }) {
  return <div className={`mx-auto max-w-6xl px-4 sm:px-6 ${className}`}>{children}</div>;
}

function Lines({ lines }: { lines: readonly (readonly [string, "ok" | "err" | "dim"])[] }) {
  return (
    <>
      {lines.map(([t, c], i) => (
        <span key={i} className={c}>
          {t}
          {"\n"}
        </span>
      ))}
    </>
  );
}

export function Home() {
  useEffect(() => {
    document.title = "factory";
  }, []);

  return (
    <div className="min-h-screen">
      <SiteNav />

      {/* hero */}
      <section className="hero-glow overflow-hidden">
        <Container className="grid items-center gap-12 py-16 sm:py-24 lg:grid-cols-[1.1fr_0.9fr] lg:gap-16">
          <div>
            <p className="eyebrow">open source · self-hosted · MIT data plane</p>
            <h1 className="mt-4 text-4xl font-semibold leading-[1.05] tracking-[-0.02em] sm:text-5xl lg:text-6xl">
              A software factory you can run yourself.
            </h1>
            <p className="lede mt-6 max-w-xl">
              factory turns a prompt or an issue into a spec, a human approval and a pull request,
              using Claude Code, Codex or the Warp Agent CLI. An open equivalent of Warp&rsquo;s Oz
              and Factories, with no vendor control plane: a laptop, a Helm chart or Kubernetes
              CRDs, all from one definition.
            </p>
            <div className="mt-8 flex flex-col gap-3 sm:flex-row">
              <Link to="/docs" className="btn btn-primary">
                Read the docs
              </Link>
              <a href={GITHUB} className="btn">
                github.com/polds/factory
              </a>
            </div>
            <p className="mt-6 font-mono text-xs text-muted-foreground">
              bring your agent: Claude Code · Codex · Warp Agent CLI · anything that speaks Harness
              Contract v1
            </p>
          </div>
          <Pipeline />
        </Container>
      </section>

      {/* how it works */}
      <section className="section">
        <Container>
          <p className="eyebrow">how a run works</p>
          <h2 className="h2 mt-3 max-w-2xl">
            Intent in. A reviewed pull request out. A human in between.
          </h2>
          <div className="mt-10 grid gap-px border border-border bg-border sm:grid-cols-2 lg:grid-cols-4">
            {how.map((h, i) => (
              <div key={h.title} className="bg-card p-5">
                <div className="flex items-center justify-between">
                  <h.icon
                    size={20}
                    strokeWidth={1.5}
                    className="text-muted-foreground"
                    aria-hidden
                  />
                  <span className="font-mono text-xs text-muted-foreground">0{i + 1}</span>
                </div>
                <p className="card-title mt-5">{h.title}</p>
                <p className="mt-2 text-sm text-muted-foreground">{h.body}</p>
                <p className="mt-4 font-mono text-[11px] text-muted-foreground">{h.meta}</p>
              </div>
            ))}
          </div>
        </Container>
      </section>

      {/* defined in code */}
      <section className="section">
        <Container>
          <p className="eyebrow">agents as code</p>
          <h2 className="h2 mt-3 max-w-2xl">Your factory is a directory of YAML and Markdown.</h2>
          <p className="lede mt-4 max-w-2xl">
            One tree describes agents, automations, runners, skills, scorers, benchmarks and
            webhooks. It is Warp&rsquo;s v1alpha1 format plus a handful of documented keys, so an
            existing definition ports by addition.
          </p>
          <div className="mt-8 flex flex-wrap gap-2">
            {[
              "factory.yaml",
              "agents/",
              "automations/",
              "runners/",
              "skills/",
              "scorers/",
              "benchmarks/",
              "webhooks/",
            ].map((t) => (
              <span key={t} className="tag">
                {t}
              </span>
            ))}
          </div>
          <div className="mt-8 grid gap-8 lg:grid-cols-[1.3fr_1fr]">
            <div className="codeblock min-w-0">
              <div className="codeblock-head">
                <span>examples/acme-factory/factory.yaml</span>
                <span>excerpt</span>
              </div>
              <pre>
                {yamlLines.map((l, i) => (
                  <span key={i} className={l.mark ? "yaml-mark" : undefined}>
                    {l.text || " "}
                    {"\n"}
                  </span>
                ))}
              </pre>
            </div>
            <ol className="grid gap-5 self-center">
              {callouts.map((c) => (
                <li key={c.n} className="flex gap-3">
                  <span className="callout-n">{c.n}</span>
                  <div>
                    <p className="font-semibold">{c.title}</p>
                    <p className="mt-1 text-sm text-muted-foreground">{c.body}</p>
                  </div>
                </li>
              ))}
            </ol>
          </div>
        </Container>
      </section>

      {/* open at every layer */}
      <section className="section">
        <Container>
          <p className="eyebrow">open at every layer</p>
          <h2 className="h2 mt-3 max-w-2xl">
            Swap the agent, the model, the compute or the store.
          </h2>
          <div className="mt-10 grid gap-px border border-border bg-border sm:grid-cols-2 lg:grid-cols-4">
            {layers.map((l) => (
              <div key={l.title} className="bg-card p-5">
                <p className="card-title">{l.title}</p>
                <p className="mt-2 text-sm text-muted-foreground">{l.body}</p>
                <div className="mt-4 flex flex-wrap gap-1.5">
                  {l.tags.map((t) => (
                    <span key={t} className="tag">
                      {t}
                    </span>
                  ))}
                </div>
              </div>
            ))}
          </div>
        </Container>
      </section>

      {/* runs anywhere */}
      <section className="section">
        <Container>
          <p className="eyebrow">three shapes, one definition</p>
          <h2 className="h2 mt-3 max-w-2xl">
            From a laptop to a cluster without rewriting anything.
          </h2>
          <div className="mt-10 grid gap-4 md:grid-cols-3">
            {shapes.map((s) => (
              <div key={s.title} className="card flex flex-col">
                <p className="card-title">{s.title}</p>
                <p className="mt-2 flex-1 text-sm text-muted-foreground">{s.body}</p>
                <code className="mt-5 block overflow-x-auto whitespace-nowrap border border-border bg-code px-3 py-2 font-mono text-xs">
                  $ {s.cmd}
                </code>
              </div>
            ))}
          </div>
        </Container>
      </section>

      {/* proof */}
      <section className="section">
        <Container>
          <p className="eyebrow">verified, not asserted</p>
          <h2 className="h2 mt-3 max-w-2xl">
            Checked against the real worker and the real schema.
          </h2>
          <div className="mt-10 grid gap-6 lg:grid-cols-2">
            <div>
              <p className="text-sm text-muted-foreground">
                The M0 spike points the stock oz-agent-worker at a 400-line control plane and walks
                the whole handshake: auth, assignment, claim, heartbeat, terminal state. The worker
                is reused byte for byte; only the server is ours.
              </p>
              <div className="codeblock mt-4">
                <div className="codeblock-head">
                  <span>spike/m0</span>
                  <span className="ok">5 passed</span>
                </div>
                <pre>
                  <Lines lines={spike} />
                </pre>
              </div>
            </div>
            <div>
              <p className="text-sm text-muted-foreground">
                The example factory run through Warp&rsquo;s own validator reports unknown-field
                errors for exactly the keys this design documents as extensions, and nothing else.
                Delete them and it is a valid Warp definition again.
              </p>
              <div className="codeblock mt-4">
                <div className="codeblock-head">
                  <span>validate_factory_files.py examples/acme-factory</span>
                  <span className="dim">5 expected</span>
                </div>
                <pre>
                  <Lines lines={validator} />
                </pre>
              </div>
            </div>
          </div>
        </Container>
      </section>

      {/* status */}
      <section className="section">
        <Container>
          <div className="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
            <div>
              <p className="eyebrow">where it stands</p>
              <h2 className="h2 mt-3">Built in milestones, each with an exit that was run.</h2>
            </div>
            <Link
              to="/docs/12-build-sequence"
              className="font-mono text-sm text-link underline underline-offset-4"
            >
              build sequence and evidence
            </Link>
          </div>
          <div className="rail mt-10">
            {milestones.map(([id, name, state]) => (
              <div
                key={id}
                className={`rail-item ${state === "done" ? "done" : state === "now" ? "now" : ""}`}
              >
                <span className="dim">
                  {id} · {state === "done" ? "done" : state === "now" ? "in progress" : "ongoing"}
                </span>
                <span className="rail-name">{name}</span>
              </div>
            ))}
          </div>
        </Container>
      </section>

      {/* quickstart */}
      <section className="section">
        <Container>
          <div className="grid gap-8 lg:grid-cols-[1fr_1.4fr] lg:items-start">
            <div>
              <p className="eyebrow">quickstart</p>
              <h2 className="h2 mt-3">Laptop profile in five commands.</h2>
              <p className="lede mt-4">
                Build the harness for the Docker daemon&rsquo;s architecture, not the host OS. Set
                the required values in deploy/.env first; the README says which ones and what an
                empty one does.
              </p>
              <p className="mt-6 font-mono text-sm">
                <Link
                  to="/docs/readme#quickstart"
                  className="text-link underline underline-offset-4"
                >
                  full quickstart
                </Link>
                {" · "}
                <Link to="/docs/02-architecture" className="text-link underline underline-offset-4">
                  architecture
                </Link>
              </p>
            </div>
            <div className="codeblock min-w-0">
              <div className="codeblock-head">
                <span>bash</span>
                <CopyButton text={quickstart} />
              </div>
              <pre>{quickstart}</pre>
            </div>
          </div>
        </Container>
      </section>

      <Footer />
    </div>
  );
}
