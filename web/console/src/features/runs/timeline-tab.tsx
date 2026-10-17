import { formatDuration } from "../../lib/format";
import { Caveat } from "./caveat";
import { groupPhases } from "./events";
import { QUEUE_CAVEAT } from "./format";
import type { TimelinePhase } from "../../api/types";

function phaseClass(phase: TimelinePhase["phase"]): string {
  switch (phase) {
    case "queued":
      return "phase-queued";
    case "assigned":
      return "phase-assigned";
    case "running":
      return "phase-running";
    case "idle":
      return "phase-idle";
    case "terminal":
      return "phase-terminal";
    default: {
      const neverPhase: never = phase;
      return neverPhase;
    }
  }
}

export function TimelineTab({ phases }: { phases: readonly TimelinePhase[] }) {
  const groups = groupPhases(phases);
  if (groups.length === 0) {
    return <p className="muted">No timeline yet.</p>;
  }
  return (
    <div className="timeline">
      {groups.map((group, index) => {
        const total = Math.max(
          1,
          group.phases.reduce((sum, phase) => sum + Math.max(phase.duration_ms, 0), 0),
        );
        const label = group.phases
          .map((phase) => `${phase.phase} ${formatDuration(phase.duration_ms)}`)
          .join(", ");
        return (
          <section key={group.executionId || `attempt-${index}`} className="attempt">
            <p className="section-label">
              Attempt {index + 1}
              {group.executionId ? <span className="mono"> {group.executionId}</span> : null}
            </p>
            <div
              className="phase-bar"
              role="img"
              aria-label={label}
              data-execution-id={group.executionId}
            >
              {group.phases.map((phase, phaseIndex) => (
                <div
                  key={`${phase.phase}-${phaseIndex}`}
                  className={`phase-segment ${phaseClass(phase.phase)}`}
                  style={{ width: `${(Math.max(phase.duration_ms, 0) / total) * 100}%` }}
                >
                  <span className="phase-name">{phase.phase}</span>
                </div>
              ))}
            </div>
            <ul className="phase-legend">
              {group.phases.map((phase, phaseIndex) => (
                <li key={`${phase.phase}-${phaseIndex}`}>
                  <span className={`swatch ${phaseClass(phase.phase)}`} aria-hidden="true" />
                  {phase.phase} <span className="mono">{formatDuration(phase.duration_ms)}</span>
                </li>
              ))}
            </ul>
          </section>
        );
      })}
      <table className="data-table">
        <thead>
          <tr>
            <th scope="col">Phase</th>
            <th scope="col">At</th>
            <th scope="col">Duration</th>
            <th scope="col">Execution</th>
          </tr>
        </thead>
        <tbody>
          {phases.map((phase, index) => (
            <tr key={`${phase.execution_id}-${phase.phase}-${index}`}>
              <td>{phase.phase}</td>
              <td className="mono">{phase.at}</td>
              <td className="mono">{formatDuration(phase.duration_ms)}</td>
              <td className="mono">{phase.execution_id || "—"}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <Caveat>{QUEUE_CAVEAT}</Caveat>
    </div>
  );
}
