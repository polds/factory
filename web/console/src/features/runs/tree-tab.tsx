import { Link } from "@tanstack/react-router";
import { StatusBadge } from "../../components/StatusBadge";
import { isFailureClass, shortId } from "./format";
import { RunStatusBadge } from "./status";
import type { RunDetailWire, RunRow } from "./types";

export function TreeTab({ run, children }: { run: RunDetailWire; children: readonly RunRow[] }) {
  const selfClass = run.failure?.class ?? "";
  const depth = run.parent_run_id ? 1 : 0;
  return (
    <section>
      <p className="section-label">Run tree</p>
      <ul className="run-tree">
        {run.parent_run_id ? (
          <li className="tree-depth-0">
            <Link to="/runs/$runId" params={{ runId: run.parent_run_id }} className="mono">
              {shortId(run.parent_run_id)}
            </Link>
            <span className="muted">parent</span>
          </li>
        ) : null}
        <li className={`tree-depth-${depth}`}>
          <RunStatusBadge status={run.status} />
          <span>
            {run.title || shortId(run.id)} <span className="muted">(this run)</span>
          </span>
          <span className="mono">{shortId(run.id)}</span>
          {isFailureClass(selfClass) ? <StatusBadge failureClass={selfClass} /> : null}
        </li>
        {children.map((child) => (
          <li key={child.id} className={`tree-depth-${depth + 1}`}>
            <RunStatusBadge status={child.status} />
            <Link to="/runs/$runId" params={{ runId: child.id }}>
              {child.title || shortId(child.id)}
            </Link>
            <span className="mono">{shortId(child.id)}</span>
            {isFailureClass(child.failure_class) ? (
              <StatusBadge failureClass={child.failure_class} />
            ) : null}
          </li>
        ))}
      </ul>
      {!run.parent_run_id && children.length === 0 ? (
        <p className="muted">No parent or child runs.</p>
      ) : null}
    </section>
  );
}
