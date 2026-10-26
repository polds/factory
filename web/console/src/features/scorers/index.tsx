import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import {
  AlertTriangle,
  BadgeCheck,
  CheckCircle2,
  CircleSlash,
  Clock,
  Sparkles,
  XCircle,
  type LucideIcon,
} from "lucide-react";
import { client } from "../../api/client";
import { useFactoryEvents } from "../../api/live";
import type { ScorerResult, ScorerResultStatus, ScorerSummary } from "../../api/types";
import { Caveat } from "../../components/Caveat";
import { EmptyState } from "../../components/EmptyState";
import { ErrorState } from "../../components/ErrorState";
import { IconLabel } from "../../components/IconLabel";
import { PageHeader } from "../../components/PageHeader";
import { Panel } from "../../components/Panel";
import { PassRateByDay } from "../../components/PassRateByDay";
import { formatScore } from "../../lib/format";
import { scorerStatusLabel } from "../../lib/run-labels";
import { unexpected } from "../../lib/unexpected";
import { problemDetail } from "../dashboard/model";

const SCORING_CAVEAT = "Infrastructure-failed and measurement runs are never scored.";

type ResultRow = {
  id: string;
  run_id: string;
  label: string;
  score: number;
  passed: boolean;
  reasoning: string;
  status: ScorerResultStatus;
  created_at: string;
};

function presentSummary(raw: ScorerSummary): ScorerSummary {
  return {
    name: raw.name,
    agents: raw.agents,
    labels: raw.labels,
    passing_score: raw.passing_score,
    sampling_rate: raw.sampling_rate,
    model: raw.model,
    self_improvement: raw.self_improvement,
    pass_rate_by_day: raw.pass_rate_by_day,
  };
}

function presentResult(raw: ScorerResult): ResultRow {
  return {
    id: raw.id,
    run_id: raw.run_id,
    label: raw.label,
    score: raw.score,
    passed: raw.passed,
    reasoning: raw.reasoning,
    status: raw.status,
    created_at: raw.created_at,
  };
}

function resultStatus(status: ScorerResultStatus): { icon: LucideIcon; label: string } {
  switch (status) {
    case "ok":
      return { icon: BadgeCheck, label: scorerStatusLabel(status) };
    case "scorer_invalid":
      return { icon: AlertTriangle, label: scorerStatusLabel(status) };
    case "pending":
      return { icon: Clock, label: scorerStatusLabel(status) };
    default:
      return unexpected(status);
  }
}

function SelfImprovement({ on }: { on: boolean }) {
  if (on) return <IconLabel icon={Sparkles} label="Self-improvement" />;
  return <IconLabel icon={CircleSlash} label="No self-improvement" />;
}

function Definition({ scorer }: { scorer: ScorerSummary }) {
  return (
    <dl className="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-4 gap-y-1">
      <dt className="text-muted-foreground">Labels</dt>
      <dd className="min-w-0 break-words">
        {scorer.labels.length === 0 ? "None" : scorer.labels.join(", ")}
      </dd>
      <dt className="text-muted-foreground">Passing score</dt>
      <dd className="mono">{formatScore(scorer.passing_score)}</dd>
      <dt className="text-muted-foreground">Sampling rate</dt>
      {/* sampling_rate is already a percent (25 means 25%), not a 0–1 fraction. */}
      <dd className="mono">{scorer.sampling_rate}%</dd>
      <dt className="text-muted-foreground">Model</dt>
      <dd className="mono break-all">{scorer.model}</dd>
      <dt className="text-muted-foreground">Agents</dt>
      <dd className="min-w-0 break-words">
        {scorer.agents.length === 0 ? "None" : scorer.agents.join(", ")}
      </dd>
      <dt className="text-muted-foreground">Self-improvement</dt>
      <dd>
        <SelfImprovement on={scorer.self_improvement} />
      </dd>
    </dl>
  );
}

function ResultsTable({ rows }: { rows: readonly ResultRow[] }) {
  if (rows.length === 0) {
    return <EmptyState title="No results" message="This scorer has no results." />;
  }
  return (
    <div className="table-scroll" tabIndex={0}>
      <table>
        <caption className="sr-only">Scorer results</caption>
        <thead className="data-head">
          <tr>
            <th scope="col">Run</th>
            <th scope="col">Label</th>
            <th scope="col">Score</th>
            <th scope="col">Passed</th>
            <th scope="col">Status</th>
            <th scope="col">Reasoning</th>
            <th scope="col">Created</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => {
            const status = resultStatus(row.status);
            return (
              <tr key={row.id}>
                <td>
                  <Link to="/runs/$runId" params={{ runId: row.run_id }} className="mono">
                    {row.run_id}
                  </Link>
                </td>
                <td>{row.label}</td>
                <td className="mono">{formatScore(row.score)}</td>
                <td>
                  {row.passed ? (
                    <IconLabel icon={CheckCircle2} label="Passed" />
                  ) : (
                    <IconLabel icon={XCircle} label="Failed" />
                  )}
                </td>
                <td>
                  <IconLabel icon={status.icon} label={status.label} />
                </td>
                <td className="break-words whitespace-pre-wrap">{row.reasoning}</td>
                <td>
                  <time dateTime={row.created_at} className="mono">
                    {row.created_at}
                  </time>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function ScorersScreen({ factoryId, name }: { factoryId: string; name: string }) {
  useFactoryEvents(factoryId);
  const list = useQuery({
    queryKey: ["scorers", factoryId],
    queryFn: async () => (await client.listScorers(factoryId)).map(presentSummary),
  });
  const results = useQuery({
    queryKey: ["scorers", factoryId, "results", name],
    queryFn: async () => (await client.scorerResults(factoryId, name)).map(presentResult),
    enabled: name !== "",
  });

  if (list.isPending || (name !== "" && results.isPending)) return <p>Loading scorers</p>;
  if (list.isError) {
    return (
      <ErrorState
        title="Could not load scorers"
        message={problemDetail(list.error)}
        onRetry={() => void list.refetch()}
      />
    );
  }
  if (name !== "" && results.isError) {
    return (
      <ErrorState
        title="Could not load scorer results"
        message={problemDetail(results.error)}
        onRetry={() => void results.refetch()}
      />
    );
  }

  const scorers = name === "" ? list.data : list.data.filter((scorer) => scorer.name === name);
  const rows = name === "" ? [] : (results.data ?? []);

  return (
    <div className="operator-page">
      <PageHeader
        title="Scorers"
        subtitle={name || undefined}
        actions={
          name ? (
            <Link to="/f/$factory/scorers" params={{ factory: factoryId }}>
              All scorers
            </Link>
          ) : undefined
        }
      />
      <Caveat>{SCORING_CAVEAT}</Caveat>
      {name === "" && scorers.length === 0 ? (
        <EmptyState title="No scorers" message="This factory has no scorers." />
      ) : null}
      {name !== "" && scorers.length === 0 && rows.length === 0 ? (
        <EmptyState title="Scorer not found" message={`No scorer named ${name}.`} />
      ) : null}
      {scorers.map((scorer) => (
        <div key={scorer.name} className="min-w-0">
          <Panel
            title={scorer.name}
            actions={
              name === "" ? (
                <Link
                  to="/f/$factory/scorers/$name"
                  params={{ factory: factoryId, name: scorer.name }}
                >
                  {`Results for ${scorer.name}`}
                </Link>
              ) : undefined
            }
          >
            <Definition scorer={scorer} />
            <PassRateByDay days={scorer.pass_rate_by_day} />
          </Panel>
        </div>
      ))}
      {name !== "" && (scorers.length > 0 || rows.length > 0) ? (
        <div className="min-w-0">
          <Panel title="Results">
            <ResultsTable rows={rows} />
          </Panel>
        </div>
      ) : null}
    </div>
  );
}

export default function ScorersPage() {
  const params = useParams({ strict: false });
  const factoryId = typeof params.factory === "string" ? params.factory : "";
  const name = typeof params.name === "string" ? params.name : "";
  if (!factoryId) {
    return <ErrorState title="Missing factory" message="Open scorers from a factory." />;
  }
  return <ScorersScreen factoryId={factoryId} name={name} />;
}
