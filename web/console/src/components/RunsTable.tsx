import { Link } from "@tanstack/react-router";
import type { FailureClass, RunRow } from "../api/types";
import { failureClassOf, stageLabel } from "../features/dashboard/model";
import { DurationPair } from "./DurationPair";
import { RunStatusBadge } from "./RunStatusBadge";
import { StatusBadge } from "./StatusBadge";

export function RunsTable({ runs }: { runs: readonly RunRow[] }) {
  return (
    <div className="table-scroll">
      <table>
        <caption className="sr-only">Runs</caption>
        <thead>
          <tr>
            <th scope="col">Status</th>
            <th scope="col">Title</th>
            <th scope="col">Agent</th>
            <th scope="col">Stage</th>
            <th scope="col">Failure</th>
            <th scope="col">Queue / exec</th>
            <th scope="col">Model</th>
          </tr>
        </thead>
        <tbody>
          {runs.map((run) => {
            const failureClass: FailureClass | undefined = failureClassOf(run);
            const timed = typeof run.queue_wait_ms === "number" && typeof run.exec_ms === "number";
            return (
              <tr key={run.id}>
                <td>
                  <RunStatusBadge status={run.status} />
                </td>
                <td>
                  <Link to="/runs/$runId" params={{ runId: run.id }}>
                    {run.title || run.id}
                  </Link>
                </td>
                <td>{run.agent || "—"}</td>
                <td>{run.stage ? stageLabel(run.stage) : "—"}</td>
                <td>
                  {failureClass ? (
                    <StatusBadge failureClass={failureClass} />
                  ) : (
                    <span className="muted">—</span>
                  )}
                  {run.failure_summary ? <p className="muted">{run.failure_summary}</p> : null}
                </td>
                <td>
                  {timed ? (
                    <DurationPair queueWaitMs={run.queue_wait_ms ?? 0} execMs={run.exec_ms ?? 0} />
                  ) : (
                    "—"
                  )}
                </td>
                <td className="mono">{run.model_id || "—"}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
