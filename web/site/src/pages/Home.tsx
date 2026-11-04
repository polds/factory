import type { ReactNode } from "react";
import { useEffect } from "react";
import { Link } from "react-router";

import { Footer, GITHUB, SiteNav } from "../components/SiteNav.tsx";

const modes = [
  ["Laptop / single node", "docker compose up: control plane, Postgres, MinIO, one worker"],
  ["Self-hosted cloud", "Helm chart: control plane + operator + worker pools"],
  ["Cloud-native", "Kubernetes CRDs (factory.dev/v1alpha1) reconciled by an operator"],
];

const binaries = [
  ["factoryd", "control plane and console"],
  ["factory-worker", "data plane; the unmodified upstream worker"],
  ["factory-operator", "CRD reconciler"],
  ["factoryctl", "CLI"],
];

// Verbatim from README.md.
const spike = `  PASS  worker authenticated         m0-spike
  PASS  task assignment accepted     task_assignment written to socket
  PASS  task claimed                 m0-spike
  PASS  heartbeat answered           1 ping/pong exchange(s)
  PASS  terminal state               task_completed: Task completed successfully
VERDICT: confirmed — an unmodified worker ran our harness against our server.`;

const validator = `5 problem(s) in examples/acme-factory:
  factory.yaml:33: FF_UNKNOWN_FIELD: unknown field "pool"
  factory.yaml:36: FF_UNKNOWN_FIELD: unknown field "inference"
  factory.yaml:48: FF_UNKNOWN_FIELD: unknown field "pipeline"
  factory.yaml:66: FF_UNKNOWN_FIELD: unknown field "staleFailureDays"
  factory.yaml:71: FF_UNKNOWN_FIELD: unknown field "model"      # scorerDefaults.model
Validated with the warp-server parser for v1alpha1`;

const componentMap = `                 ┌─────────────────────────────────────────────┐
  Slack/Linear   │                 factoryd                    │
  GitHub/GitLab ─┤  ingress · automations · orchestrator        │
  Webhooks       │  definition sync · inference gw · console    │
  Schedules      └───┬──────────────┬─────────────────┬─────────┘
                     │ WS           │ SQL             │ S3
              ┌──────▼──────┐  ┌────▼─────┐   ┌───────▼────────┐
              │   workers   │  │ Postgres │   │ object store   │
              │ docker/k8s/ │  │ runs·defs│   │ transcripts·   │
              │ direct/cmd  │  │ queue    │   │ artifacts      │
              └──────┬──────┘  └──────────┘   └────────────────┘
                     │ spawns
              ┌──────▼───────────────────────┐
              │ task container               │
              │  repo checkout + /agent      │ ← Harness Contract v1
              │  (claude-code | codex | …)   │
              └──────────────────────────────┘`;

const quickstart = `cp deploy/.env.example deploy/.env
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o harness/generic-openai/factory-harness ./cmd/factory-harness
docker compose -f deploy/docker-compose.yml up -d --build --wait
go build -o bin/factoryctl ./cmd/factoryctl
FACTORY_API_KEY=m2-dev-api-key ./bin/factoryctl run --prompt "…" --factory m2 --watch`;

const workerCmd = `oz-agent-worker --worker-id local-1 \\
  --web-socket-url ws://factoryd:8080/api/v1/selfhosted/worker/ws \\
  --server-root-url http://factoryd:8080`;

function Pre({ caption, children }: { caption?: string; children: ReactNode }) {
  return (
    <figure>
      {caption && (
        <figcaption className="mb-1 font-mono text-xs text-muted-foreground">{caption}</figcaption>
      )}
      <pre>{children}</pre>
    </figure>
  );
}

function Doc({ slug, children }: { slug: string; children: ReactNode }) {
  return <Link to={`/docs/${slug}`}>{children}</Link>;
}

export function Home() {
  useEffect(() => {
    document.title = "factory";
  }, []);

  return (
    <div className="min-h-screen">
      <SiteNav />
      <main className="mx-auto max-w-6xl px-4 py-12 sm:px-6 sm:py-16">
        <div className="prose-doc max-w-3xl">
          <h1>factory</h1>
          <p className="text-lg">
            A self-hostable software factory orchestrator: an open equivalent of Warp&rsquo;s Oz
            (cloud agent platform) and Factories (multi-agent SDLC pipelines), with no dependency on
            a vendor control plane.
          </p>
          <p>
            It runs coding agents such as Claude Code, Codex and the Warp Agent CLI as pipelines. A
            prompt or an issue comes in, a spec is written, a human approves it, and a pull request
            comes out. The approval gates are enforced by the orchestrator, not requested of a
            model.
          </p>
          <p className="font-mono text-sm">
            <Link to="/docs">read the docs</Link>
            {" · "}
            <a href={GITHUB}>github.com/polds/factory</a>
          </p>

          <h2 id="modes">Operating modes</h2>
          <p>All three come from one definition.</p>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Mode</th>
                  <th>Shape</th>
                </tr>
              </thead>
              <tbody>
                {modes.map(([mode, shape]) => (
                  <tr key={mode}>
                    <td className="whitespace-nowrap">{mode}</td>
                    <td>{shape}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          <h2 id="decisions">Three decisions</h2>
          <ol>
            <li>
              <strong>Reimplement the control plane; reuse the data plane.</strong> The data plane,{" "}
              <code>oz-agent-worker</code>, is MIT-licensed and good. The control plane is the
              closed part, so <code>factoryd</code> is a new, wire-compatible control plane and the
              worker is kept as shipped. An unmodified upstream worker points at it with one hidden
              flag.
            </li>
            <li>
              <strong>The harness is a contract, not a binary.</strong> Harness Contract v1 is a
              container image mounted at <code>/agent</code>, exposing{" "}
              <code>/agent/entrypoint.sh</code>, fed a task spec, emitting NDJSON events. Adapters
              exist for Claude Code, Codex and the Warp Agent CLI. See{" "}
              <Doc slug="05-harness-contract">05</Doc>.
            </li>
            <li>
              <strong>Pipeline policy is declarative, not prompt-resident.</strong> Stage routing
              and the human gates live in <code>factory.yaml</code> as a <code>pipeline:</code>{" "}
              block with explicit <code>approvals:</code>, validated and enforced by the
              orchestrator. This is the design&rsquo;s main intentional divergence from Warp; the
              rationale is in <Doc slug="03-definition-schema">03</Doc>.
            </li>
          </ol>

          <h2 id="verified">Verified, not asserted</h2>
          <p>
            <code>spike/m0</code> runs the unmodified upstream worker against a 400-line control
            plane and checks the whole handshake end to end:
          </p>
          <Pre caption="oz-agent-worker, unmodified">{workerCmd}</Pre>
          <Pre caption="spike/m0 output">{spike}</Pre>

          <h2 id="compatibility">Compatibility</h2>
          <p>
            The definition schema is checkable against Warp&rsquo;s published v1alpha1. Running{" "}
            <code>examples/acme-factory</code> through Warp&rsquo;s own validator reports
            unknown-field errors for exactly the keys this design documents as extensions, and
            nothing else. A Warp definition ports by addition, and ours degrades to a valid Warp
            definition by deleting those keys.
          </p>
          <Pre caption="validate_factory_files.py examples/acme-factory">{validator}</Pre>

          <h2 id="components">Component map</h2>
          <Pre>{componentMap}</Pre>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Binary</th>
                  <th>Role</th>
                </tr>
              </thead>
              <tbody>
                {binaries.map(([name, role]) => (
                  <tr key={name}>
                    <td>
                      <code>{name}</code>
                    </td>
                    <td>{role}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          <h2 id="quickstart">Quickstart</h2>
          <p>
            Laptop profile. Build the harness for the Docker daemon&rsquo;s architecture (arm64 or
            amd64), not the host OS. <code>deploy/.env</code> needs a few values set first; the{" "}
            <Doc slug="readme#quickstart">README</Doc> lists which ones and what an empty one does.
          </p>
          <Pre caption="bash">{quickstart}</Pre>

          <h2 id="status">Status</h2>
          <p>
            Milestones M0 through M7 are in the tree. The build sequence, with the evidence for each
            exit, is <Doc slug="12-build-sequence">12</Doc>. Start with{" "}
            <Doc slug="01-evidence-and-provenance">01</Doc> if you intend to rely on any claim here:
            it separates what is observed from what is inferred.
          </p>
        </div>
      </main>
      <Footer />
    </div>
  );
}
