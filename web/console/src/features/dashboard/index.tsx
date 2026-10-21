import { useQuery, useQueries, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Radio } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { client } from "../../api/client";
import { liveLabel, useFactoryEvents, type StreamStatus } from "../../api/live";
import type { Factory, FactorySummary } from "../../api/types";
import { Caveat } from "../../components/Caveat";
import { EmptyState } from "../../components/EmptyState";
import { ErrorState } from "../../components/ErrorState";
import { PageHeader } from "../../components/PageHeader";
import { Panel } from "../../components/Panel";
import { RunStatusBadge } from "../../components/RunStatusBadge";
import { RunsTable } from "../../components/RunsTable";
import { StatusBadge } from "../../components/StatusBadge";
import { formatDuration } from "../../lib/format";
import { MetricsBoard, type MetricsResult } from "./metrics";
import {
  FAILURE_CLASSES,
  RUN_STATUSES,
  isFailureClass,
  orderedCounts,
  rangeBounds,
  sumMaps,
  sumNumbers,
  type RangeKey,
  type RunRow,
} from "./model";

export default function DashboardPage() {
  const queryClient = useQueryClient();
  const factories = useQuery({ queryKey: ["factories"], queryFn: () => client.listFactories() });
  const [rangeKey, setRangeKey] = useState<RangeKey>("24h");
  const bounds = useMemo(() => rangeBounds(rangeKey), [rangeKey]);
  const factoryList = factories.data ?? [];
  const summaryQueries = useQueries({
    queries: factoryList.map((factory) => ({
      queryKey: ["summary", factory.id, bounds.from, bounds.to],
      queryFn: () => client.summary(factory.id, bounds),
    })),
  });
  const metricsQueries = useQueries({
    queries: factoryList.map((factory) => ({
      queryKey: ["metrics", factory.id, bounds.from, bounds.to],
      queryFn: () => client.metrics(factory.id, bounds),
    })),
  });
  const metricsResults: MetricsResult[] = metricsQueries.map((query) => ({
    isPending: query.isPending,
    isError: query.isError,
    data: query.data,
  }));
  const runs = useQuery({
    queryKey: ["runs", "recent"],
    queryFn: () => client.listRuns() as Promise<RunRow[]>,
  });
  const [statuses, setStatuses] = useState<Record<string, StreamStatus>>({});
  const onStatus = useCallback((factoryId: string, status: StreamStatus) => {
    setStatuses((current) =>
      current[factoryId] === status ? current : { ...current, [factoryId]: status },
    );
  }, []);
  const retryMetrics = useCallback(() => {
    void queryClient.invalidateQueries({ queryKey: ["metrics"] });
  }, [queryClient]);

  if (factories.isPending) return <p>Loading dashboard</p>;
  if (factories.isError) {
    return (
      <ErrorState
        title="Could not load the dashboard"
        message="The factory list could not be loaded."
        onRetry={() => void factories.refetch()}
      />
    );
  }

  const summaryError = summaryQueries.some((query) => query.isError);
  const summaries = summaryQueries.map((query) => query.data);
  const ready = summaries.every(isSummary);
  const streamLabel = liveLabel(factoryList.map((factory) => statuses[factory.id] ?? "connecting"));

  return (
    <div className="operator-page">
      {factoryList.map((factory) => (
        <FactoryStream key={factory.id} factoryId={factory.id} onStatus={onStatus} />
      ))}
      <PageHeader
        title="Dashboard"
        subtitle="All factories"
        actions={
          <>
            <p className="live-status" role="status" aria-label={streamLabel}>
              <Radio aria-hidden="true" size={16} />
              {streamLabel}
            </p>
            <label>
              Range{" "}
              <select
                value={rangeKey}
                onChange={(event) => {
                  const next = event.target.value;
                  if (next === "24h" || next === "7d") setRangeKey(next);
                }}
              >
                <option value="24h">Last 24h</option>
                <option value="7d">Last 7d</option>
              </select>
            </label>
          </>
        }
      />
      {summaryError ? (
        <ErrorState
          title="Could not load the dashboard"
          message="A factory summary could not be loaded."
          onRetry={() => void queryClient.invalidateQueries({ queryKey: ["summary"] })}
        />
      ) : null}
      {ready && !summaryError ? (
        <DashboardBody factories={factoryList} summaries={summaries} />
      ) : summaryError ? null : (
        <p>Loading dashboard</p>
      )}
      <MetricsBoard factories={factoryList} results={metricsResults} onRetry={retryMetrics} />
      <RecentRuns
        runs={runs.data}
        pending={runs.isPending}
        error={runs.isError}
        onRetry={() => void runs.refetch()}
      />
    </div>
  );
}

function isSummary(value: FactorySummary | undefined): value is FactorySummary {
  return value !== undefined;
}

function FactoryStream({
  factoryId,
  onStatus,
}: {
  factoryId: string;
  onStatus: (factoryId: string, status: StreamStatus) => void;
}) {
  const status = useFactoryEvents(factoryId);
  useEffect(() => {
    onStatus(factoryId, status);
  }, [factoryId, onStatus, status]);
  return null;
}

function DashboardBody({
  factories,
  summaries,
}: {
  factories: readonly Factory[];
  summaries: readonly FactorySummary[];
}) {
  const active = sumNumbers(summaries.map((summary) => summary.active_runs));
  const waiting = sumNumbers(summaries.map((summary) => summary.waiting_on_human));
  const opened = sumNumbers(summaries.map((summary) => summary.prs_opened));
  const unmatched = sumNumbers(summaries.map((summary) => summary.deliveries_unmatched));
  const byStatus = orderedCounts(
    sumMaps(summaries.map((summary) => summary.runs_by_status)),
    RUN_STATUSES,
  );
  const byClass = orderedCounts(
    sumMaps(summaries.map((summary) => summary.runs_by_failure_class)),
    FAILURE_CLASSES,
  );
  const widest = Math.max(...byStatus.map((row) => row.count), 1);

  return (
    <>
      <div className="tiles">
        <section className="tile tile-hero" aria-labelledby="tile-waiting">
          <h3 id="tile-waiting">Waiting on human</h3>
          <p className="metric">{waiting}</p>
          <Caveat>approvals + agent questions</Caveat>
          <FactoryLinks factories={factories} section="inbox" label="Inbox" />
        </section>
        <section className="tile" aria-labelledby="tile-active">
          <h3 id="tile-active">Active runs</h3>
          <p className="metric">{active}</p>
          <Caveat>queued + running</Caveat>
        </section>
        <section className="tile" aria-labelledby="tile-p50">
          <h3 id="tile-p50">Queue wait p50</h3>
          <ul className="stat-list">
            {factories.map((factory, index) => (
              <li key={factory.id}>
                <span className="mono">{factory.id}</span>{" "}
                <span className="mono">
                  {formatDuration(summaries[index]?.queue_wait_p50_ms ?? 0)}
                </span>
              </li>
            ))}
          </ul>
          <Caveat>time to first start; a requeue doesn't restart the clock</Caveat>
        </section>
        <section className="tile" aria-labelledby="tile-prs">
          <h3 id="tile-prs">PRs opened</h3>
          <p className="metric">{opened}</p>
          <Caveat>work items created in this window that have a PR</Caveat>
        </section>
        <section className="tile" aria-labelledby="tile-deliveries">
          <h3 id="tile-deliveries">Unmatched deliveries</h3>
          <p className="metric">{unmatched}</p>
          <Caveat>webhook deliveries no trigger matched</Caveat>
          <FactoryLinks factories={factories} section="deliveries" label="Deliveries" />
        </section>
      </div>
      <div className="split">
        <Panel title="Runs by status">
          <ul className="stat-list">
            {byStatus.map((row) => (
              <li key={row.key} className="stat-row">
                <RunStatusBadge status={row.key} />
                <span className="bar" aria-hidden="true">
                  <span style={{ width: `${(row.count / widest) * 100}%` }} />
                </span>
                <span className="mono">{row.count}</span>
              </li>
            ))}
          </ul>
        </Panel>
        <Panel title="Runs by failure class">
          <div className="class-grid">
            {byClass.map((row) => (
              <div key={row.key} className="class-cell">
                {isFailureClass(row.key) ? (
                  <StatusBadge failureClass={row.key} />
                ) : (
                  <span>{row.key}</span>
                )}
                <span className="mono metric">{row.count}</span>
              </div>
            ))}
          </div>
          <Caveat>Infrastructure failures are not agent quality.</Caveat>
        </Panel>
      </div>
    </>
  );
}

function FactoryLinks({
  factories,
  section,
  label,
}: {
  factories: readonly Factory[];
  section: "inbox" | "deliveries";
  label: string;
}) {
  return (
    <ul className="stat-list">
      {factories.map((factory) => (
        <li key={factory.id}>
          {section === "inbox" ? (
            <Link to="/f/$factory/inbox" params={{ factory: factory.id }}>
              {label} <span className="mono">{factory.id}</span>
            </Link>
          ) : (
            <Link to="/f/$factory/deliveries" params={{ factory: factory.id }}>
              {label} <span className="mono">{factory.id}</span>
            </Link>
          )}
        </li>
      ))}
    </ul>
  );
}

function RecentRuns({
  runs,
  pending,
  error,
  onRetry,
}: {
  runs: RunRow[] | undefined;
  pending: boolean;
  error: boolean;
  onRetry: () => void;
}) {
  return (
    <Panel title="Recent runs" actions={<Link to="/runs">All runs</Link>}>
      {error ? (
        <ErrorState
          title="Could not load recent runs"
          message="The run list could not be loaded."
          onRetry={onRetry}
        />
      ) : pending ? (
        <p>Loading runs</p>
      ) : runs && runs.length > 0 ? (
        <RunsTable runs={runs.slice(0, 10)} />
      ) : (
        <EmptyState title="No runs" message="Runs appear here when a factory starts one." />
      )}
    </Panel>
  );
}
