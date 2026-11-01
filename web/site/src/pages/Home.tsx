import { ArrowRight } from "lucide-react";
import type { ReactNode } from "react";
import { useEffect } from "react";
import { Link } from "react-router";

import { Footer, GITHUB, GithubMark, SiteNav } from "../components/SiteNav.tsx";

const modes = [
  {
    tag: "01 / laptop",
    title: "Single node",
    cmd: "docker compose up",
    body: "Control plane, Postgres, MinIO and one worker on your machine.",
  },
  {
    tag: "02 / self-hosted",
    title: "Self-hosted cloud",
    cmd: "helm install factory",
    body: "Helm chart: control plane, operator and worker pools.",
  },
  {
    tag: "03 / cloud-native",
    title: "Kubernetes CRDs",
    cmd: "factory.dev/v1alpha1",
    body: "Declarative resources reconciled by factory-operator.",
  },
];

const decisions = [
  {
    title: "Reimplement the control plane; reuse the data plane.",
    body: "The MIT-licensed oz-agent-worker runs unmodified. factoryd is a wire-compatible control plane, verified by a spike in which an upstream worker authenticated, claimed a task, heartbeated and reached terminal state against our server.",
  },
  {
    title: "The harness is a contract, not a binary.",
    body: "Harness Contract v1: a container image mounted at /agent exposing /agent/entrypoint.sh, fed a task spec, emitting NDJSON events. Adapters for Claude Code, Codex and Warp Agent CLI.",
  },
  {
    title: "Pipeline policy is declarative, not prompt-resident.",
    body: "Stage routing and human approval gates live in factory.yaml as a pipeline: block with explicit approvals: gates, enforced by the orchestrator rather than requested of a model.",
  },
];

const binaries = [
  ["factoryd", "control plane + console"],
  ["factory-worker", "data plane"],
  ["factory-operator", "CRD reconciler"],
  ["factoryctl", "CLI"],
];

const quickstart = `cp deploy/.env.example deploy/.env
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o harness/generic-openai/factory-harness ./cmd/factory-harness
docker compose -f deploy/docker-compose.yml up -d --build --wait
go build -o bin/factoryctl ./cmd/factoryctl
FACTORY_API_KEY=m2-dev-api-key ./bin/factoryctl run --prompt "…" --factory m2 --watch`;

// Verbatim from README.md: Warp's own validator run over examples/acme-factory.
const validator = [
  ["5 problem(s) in examples/acme-factory:", "muted"],
  ['  factory.yaml:33: FF_UNKNOWN_FIELD: unknown field "pool"', "err"],
  ['  factory.yaml:36: FF_UNKNOWN_FIELD: unknown field "inference"', "err"],
  ['  factory.yaml:48: FF_UNKNOWN_FIELD: unknown field "pipeline"', "err"],
  ['  factory.yaml:66: FF_UNKNOWN_FIELD: unknown field "staleFailureDays"', "err"],
  ['  factory.yaml:71: FF_UNKNOWN_FIELD: unknown field "model"      # scorerDefaults.model', "err"],
  ["Validated with the warp-server parser for v1alpha1", "ok"],
] as const;

const spike = [
  ["  PASS  worker authenticated         m0-spike", "ok"],
  ["  PASS  task assignment accepted     task_assignment written to socket", "ok"],
  ["  PASS  task claimed                 m0-spike", "ok"],
  ["  PASS  heartbeat answered           1 ping/pong exchange(s)", "ok"],
  ["  PASS  terminal state               task_completed: Task completed successfully", "ok"],
  ["VERDICT: confirmed — an unmodified worker ran our harness against our server.", "muted"],
] as const;

const tone = { muted: "text-muted-foreground", err: "text-destructive", ok: "text-success" };

function Section({
  label,
  title,
  children,
}: {
  label: string;
  title: string;
  children: ReactNode;
}) {
  return (
    <section className="border-b border-border">
      <div className="mx-auto max-w-7xl px-4 py-16 sm:px-6 sm:py-24">
        <p className="label-mono">{label}</p>
        <h2 className="mt-3 max-w-2xl text-2xl font-semibold tracking-tight sm:text-3xl">
          {title}
        </h2>
        <div className="mt-10">{children}</div>
      </div>
    </section>
  );
}

function Terminal({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="border border-border bg-code">
      <div className="flex items-center gap-2 border-b border-border px-3 py-2">
        <span className="h-2 w-2 bg-muted-foreground/40" />
        <span className="h-2 w-2 bg-muted-foreground/40" />
        <span className="h-2 w-2 bg-primary" />
        <span className="ml-2 font-mono text-[11px] text-muted-foreground">{title}</span>
      </div>
      <pre className="overflow-x-auto p-4 font-mono text-xs leading-relaxed sm:text-[13px]">
        {children}
      </pre>
    </div>
  );
}

function Lines({ lines }: { lines: readonly (readonly [string, keyof typeof tone])[] }) {
  return (
    <>
      {lines.map(([text, t], i) => (
        <span key={i} className={tone[t]}>
          {text}
          {"\n"}
        </span>
      ))}
    </>
  );
}

function Box({ title, items, accent }: { title: string; items: string[]; accent?: boolean }) {
  return (
    <div className={`border bg-card p-4 ${accent ? "border-primary" : "border-border"}`}>
      <p className={`font-mono text-xs font-semibold ${accent ? "text-primary" : ""}`}>{title}</p>
      <ul className="mt-2 space-y-1 font-mono text-[11px] text-muted-foreground">
        {items.map((i) => (
          <li key={i}>· {i}</li>
        ))}
      </ul>
    </div>
  );
}

const Arrow = () => (
  <div
    className="flex items-center justify-center py-2 font-mono text-primary lg:px-2 lg:py-0"
    aria-hidden
  >
    <span className="lg:hidden">↓</span>
    <span className="hidden lg:inline">→</span>
  </div>
);

function ComponentMap() {
  return (
    <div className="bg-grid border border-border p-4 sm:p-8">
      <div className="grid items-center lg:grid-cols-[1fr_auto_1.4fr_auto_1fr]">
        <Box
          title="ingress sources"
          items={["Slack", "Linear", "GitHub / GitLab", "Webhooks", "Schedules"]}
        />
        <Arrow />
        <Box
          accent
          title="factoryd"
          items={[
            "ingress",
            "automations",
            "orchestrator",
            "definition sync",
            "inference gateway",
            "console",
          ]}
        />
        <Arrow />
        <div className="grid gap-3">
          <Box title="workers  ⇄ WebSocket" items={["docker", "k8s", "direct", "cmd"]} />
          <Box title="Postgres" items={["runs · defs · queue"]} />
          <Box title="object store" items={["transcripts · artifacts"]} />
        </div>
      </div>
      <div className="mt-6 border border-dashed border-border bg-background/60 p-4 font-mono text-[11px] text-muted-foreground">
        <span className="text-foreground">worker</span> spawns task container ─▶ repo checkout +{" "}
        <span className="text-primary">/agent</span> (Harness Contract v1) ─▶ NDJSON events
      </div>
    </div>
  );
}

export function Home() {
  useEffect(() => {
    document.title = "factory — self-hostable software factory";
  }, []);

  return (
    <div className="min-h-screen">
      <SiteNav />
      <section className="bg-grid border-b border-border">
        <div className="mx-auto max-w-7xl px-4 pb-20 pt-16 sm:px-6 sm:pb-28 sm:pt-28">
          <p className="label-mono">
            <span className="text-primary">●</span> M0–M7 landed · open source
          </p>
          <h1 className="mt-5 max-w-4xl text-4xl font-semibold leading-[1.05] tracking-tight sm:text-6xl">
            A self-hostable software factory for AI coding agents.
          </h1>
          <p className="mt-6 max-w-2xl text-base text-muted-foreground sm:text-lg">
            factory runs Claude Code, Codex and Warp Agent CLI as pipelines: prompt or issue → spec
            → human approval → pull request. An open equivalent of Warp&rsquo;s Oz and Factories,
            with no vendor control plane.
          </p>
          <div className="mt-10 flex flex-col gap-3 sm:flex-row">
            <Link
              to="/docs"
              className="inline-flex items-center justify-center gap-2 bg-primary px-5 py-3 font-mono text-sm font-medium text-primary-foreground hover:opacity-90"
            >
              Get started <ArrowRight size={16} />
            </Link>
            <a
              href={GITHUB}
              className="inline-flex items-center justify-center gap-2 border border-border bg-background px-5 py-3 font-mono text-sm hover:border-foreground"
            >
              <GithubMark size={16} /> polds/factory
            </a>
          </div>
          <div className="mt-14 grid grid-cols-2 border border-border sm:grid-cols-4">
            {binaries.map(([b, d]) => (
              <div
                key={b}
                className="border-border p-4 [&:not(:last-child)]:border-r max-sm:[&:nth-child(2)]:border-r-0 max-sm:[&:nth-child(-n+2)]:border-b"
              >
                <p className="font-mono text-sm text-foreground">{b}</p>
                <p className="mt-1 font-mono text-[11px] text-muted-foreground">{d}</p>
              </div>
            ))}
          </div>
        </div>
      </section>

      <Section label="operating modes" title="Three ways to run it, from one definition.">
        <div className="grid gap-px border border-border bg-border md:grid-cols-3">
          {modes.map((m) => (
            <div key={m.tag} className="bg-card p-6">
              <p className="label-mono">{m.tag}</p>
              <h3 className="mt-4 text-lg font-semibold">{m.title}</h3>
              <p className="mt-2 text-sm text-muted-foreground">{m.body}</p>
              <code className="mt-6 block border border-border bg-code px-3 py-2 font-mono text-xs text-primary">
                $ {m.cmd}
              </code>
            </div>
          ))}
        </div>
      </Section>

      <Section label="design" title="Three decisions that shape everything.">
        <div className="divide-y divide-border border-y border-border">
          {decisions.map((d, i) => (
            <div key={d.title} className="grid gap-3 py-8 md:grid-cols-[6rem_1fr_1.4fr] md:gap-8">
              <span className="font-mono text-3xl text-primary">0{i + 1}</span>
              <h3 className="text-lg font-semibold">{d.title}</h3>
              <p className="text-sm leading-relaxed text-muted-foreground">{d.body}</p>
            </div>
          ))}
        </div>
      </Section>

      <Section label="architecture" title="Component map.">
        <ComponentMap />
      </Section>

      <Section
        label="verified, not asserted"
        title="An unmodified upstream worker ran against factoryd."
      >
        <div className="grid gap-8 lg:grid-cols-[1fr_1.4fr]">
          <p className="text-sm leading-relaxed text-muted-foreground">
            The M0 spike points the stock oz-agent-worker at a 400-line control plane and checks the
            whole handshake end to end. The worker is reused byte for byte; only the server is ours.
          </p>
          <Terminal title="spike/m0">
            <Lines lines={spike} />
          </Terminal>
        </div>
      </Section>

      <Section
        label="compatibility, verified"
        title="A Warp definition ports by addition, not translation."
      >
        <div className="grid gap-8 lg:grid-cols-[1fr_1.4fr]">
          <p className="text-sm leading-relaxed text-muted-foreground">
            The definition schema is checked against Warp&rsquo;s own v1alpha1 validator. Over
            examples/acme-factory it reports unknown-field errors for exactly the keys this design
            documents as extensions, and nothing else. Delete those keys and the tree is a valid
            Warp definition again.
          </p>
          <Terminal title="validate_factory_files.py examples/acme-factory">
            <Lines lines={validator} />
          </Terminal>
        </div>
      </Section>

      <Section label="quickstart" title="Laptop mode in five commands.">
        <Terminal title="bash">{quickstart}</Terminal>
        <p className="mt-4 max-w-2xl text-sm text-muted-foreground">
          Build the harness for the Docker daemon&rsquo;s architecture (arm64 or amd64), not the
          host OS. Set the variables in deploy/.env first; the README lists which ones are required.
        </p>
        <Link
          to="/docs"
          className="mt-6 inline-flex items-center gap-2 font-mono text-sm text-primary hover:underline"
        >
          Read the docs <ArrowRight size={14} />
        </Link>
      </Section>

      <Footer />
    </div>
  );
}
