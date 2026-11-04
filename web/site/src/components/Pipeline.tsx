// The hero illustration: one run of the example factory (examples/acme-factory)
// moving through its pipeline. Stages, agents, gates and approver roles are
// the ones in that factory.yaml. Pure CSS animation; see .run-* in styles.css.

type Step =
  | { kind: "stage"; name: string; agent: string }
  | { kind: "gate"; name: string; approvers: string }
  | { kind: "end"; name: string };

const steps: Step[] = [
  { kind: "stage", name: "triage", agent: "agents/triage" },
  { kind: "stage", name: "planning", agent: "agents/spec" },
  { kind: "gate", name: "approval: spec", approvers: "role:editor" },
  { kind: "stage", name: "building", agent: "agents/implementer" },
  { kind: "stage", name: "reviewing", agent: "agents/reviewer" },
  { kind: "gate", name: "approval: handoff", approvers: "role:user" },
  { kind: "end", name: "pull request opened" },
];

export function Pipeline() {
  return (
    <figure className="run" aria-label="A run moving through the acme-payments pipeline">
      <figcaption className="run-head">
        <span>run</span>
        <span className="run-head-id">acme-payments · issue labeled</span>
      </figcaption>
      <ol className="run-steps">
        {steps.map((s, i) => (
          <li
            key={s.name}
            className={`run-step run-${s.kind}`}
            style={{ "--i": i } as React.CSSProperties}
          >
            <span className="run-marker" aria-hidden>
              <span className="run-marker-idle" />
              <span className="run-marker-done">✓</span>
            </span>
            <span className="run-body">
              <span className="run-name">{s.name}</span>
              {s.kind === "stage" && <span className="run-meta">{s.agent}</span>}
              {s.kind === "gate" && (
                <span className="run-meta">
                  <span className="run-gate-wait">waiting for {s.approvers}</span>
                  <span className="run-gate-ok">approved by {s.approvers}</span>
                </span>
              )}
              {s.kind === "end" && <span className="run-meta">draft, ready for review</span>}
            </span>
          </li>
        ))}
      </ol>
    </figure>
  );
}
