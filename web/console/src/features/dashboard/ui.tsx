import {
  Ban,
  CheckCircle2,
  Circle,
  Clock,
  Info,
  Loader,
  MessageCircleQuestion,
  PauseCircle,
  ShieldQuestion,
  XCircle,
  type LucideIcon,
} from "lucide-react";
import type { ReactNode } from "react";
import { Link } from "@tanstack/react-router";
import { StatusBadge } from "../../components/StatusBadge";
import { DurationPair } from "../../components/DurationPair";
import type { FailureClass } from "../../api/types";
import {
  failureClassOf,
  isKnownRunStatus,
  stageLabel,
  type KnownRunStatus,
  type RunRow,
} from "./model";
import "./surfaces.css";

const statusIcons = {
  queued: Clock,
  running: Loader,
  idle: PauseCircle,
  succeeded: CheckCircle2,
  failed: XCircle,
  cancelled: Ban,
} as const satisfies Record<KnownRunStatus, LucideIcon>;

const statusLabels = {
  queued: "Queued",
  running: "Running",
  idle: "Idle",
  succeeded: "Succeeded",
  failed: "Failed",
  cancelled: "Cancelled",
} as const satisfies Record<KnownRunStatus, string>;

export function Caveat({ children }: { children: string }) {
  return (
    <p className="caveat">
      <Info aria-hidden="true" size={16} />
      <span>{children}</span>
    </p>
  );
}

export function PageHeader({
  title,
  subtitle,
  actions,
}: {
  title: string;
  subtitle?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <header className="page-header">
      <div>
        <h2>{title}</h2>
        {subtitle ? <p className="muted">{subtitle}</p> : null}
      </div>
      {actions ? <div className="page-actions">{actions}</div> : null}
    </header>
  );
}

export function Panel({
  title,
  actions,
  children,
}: {
  title: string;
  actions?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section className="panel">
      <div className="panel-head">
        <h3>{title}</h3>
        {actions}
      </div>
      {children}
    </section>
  );
}

export function GateBadge({ status }: { status: "awaiting_approval" | "awaiting_input" }) {
  const approval = status === "awaiting_approval";
  const Icon = approval ? ShieldQuestion : MessageCircleQuestion;
  return (
    <span
      className="gate-badge"
      data-waiting={status}
      data-icon={approval ? "shield-question" : "message-circle-question"}
      style={{ borderStyle: approval ? "solid" : "dashed" }}
    >
      <Icon aria-hidden="true" size={16} />
      <span>{approval ? "Awaiting approval" : "Awaiting input"}</span>
    </span>
  );
}

export function RunStatusBadge({ status }: { status: string }) {
  if (!isKnownRunStatus(status)) {
    return (
      <span className="stat-label">
        <Circle aria-hidden="true" size={16} />
        <span>{status}</span>
      </span>
    );
  }
  const Icon = statusIcons[status];
  return (
    <span className="stat-label">
      <Icon aria-hidden="true" size={16} />
      <span>{statusLabels[status]}</span>
    </span>
  );
}

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
