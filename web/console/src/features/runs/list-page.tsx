import { Link, useRouter, useRouterState } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { X } from "lucide-react";
import { client } from "../../api/client";
import { Button } from "../../components/Button";
import { DurationPair } from "../../components/DurationPair";
import { EmptyState } from "../../components/EmptyState";
import { ErrorState } from "../../components/ErrorState";
import { FailureClassFilter } from "../../components/FailureClassFilter";
import { StatusBadge } from "../../components/StatusBadge";
import { useFactoryEvents } from "../dashboard/live";
import { Caveat } from "./caveat";
import { formatCost, isFailureClass, QUEUE_CAVEAT, shortId } from "./format";
import {
  activeFilterCount,
  EMPTY_FILTERS,
  filtersFromSearch,
  filtersToPath,
  filtersToQuery,
  withFilter,
} from "./search";
import { RunStatusBadge } from "./status";
import { ensureRunsStreamFallback } from "./testing/ensure";
import type { FilterKey, RunRow } from "./types";

// Factory SSE tails run_events. Inserting a run notifies run_queue and writes
// no event, so a stream alone misses the row until the harness appends one.
// The interval covers that gap. Events still refresh the list once activity exists.
export const RUNS_LIST_REFRESH_MS = 2000;

const STATUSES = ["queued", "assigned", "running", "idle", "succeeded", "failed", "cancelled"];

function unique(values: readonly string[]): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const value of values) {
    if (!value || seen.has(value)) continue;
    seen.add(value);
    out.push(value);
  }
  return out;
}

function messageOf(error: unknown): string {
  return error instanceof Error ? error.message : "The run list could not be loaded.";
}

export function RunsListPage() {
  ensureRunsStreamFallback();
  const router = useRouter();
  const searchStr = useRouterState({ select: (state) => state.location.searchStr });
  const filters = filtersFromSearch(searchStr);
  const factories = useQuery({ queryKey: ["factories"], queryFn: () => client.listFactories() });
  const runs = useQuery({
    queryKey: ["runs", filters],
    queryFn: () => client.listRuns(filtersToQuery(filters)) as Promise<RunRow[]>,
    refetchInterval: RUNS_LIST_REFRESH_MS,
  });

  function update(key: FilterKey, value: string) {
    router.history.push(filtersToPath(withFilter(filters, key, value)));
  }

  function clear() {
    router.history.push(filtersToPath(EMPTY_FILTERS));
  }

  const rows = runs.data ?? [];
  const active = activeFilterCount(filters);
  const agents = unique([filters.agent, ...rows.map((row) => row.agent ?? "")]);
  const models = unique([filters.model, ...rows.map((row) => row.model_id ?? "")]);
  const workers = unique([filters.worker, ...rows.map((row) => row.worker ?? "")]);
  const sources = unique([filters.source, ...rows.map((row) => row.source ?? "")]);
  const statuses = unique([...STATUSES, filters.status, ...rows.map((row) => row.status)]);

  return (
    <div className="runs-feature">
      {(factories.data ?? []).map((factory) => (
        <RunsFactoryStream key={factory.id} factoryId={factory.id} />
      ))}
      <header className="page-header">
        <h1>Runs</h1>
        <p className="muted">All factories</p>
      </header>
      <div className="filter-bar">
        <div className="filter-row">
          <FilterSelect
            label="Agent"
            value={filters.agent}
            options={agents}
            onChange={(value) => update("agent", value)}
          />
          <FilterSelect
            label="Status"
            value={filters.status}
            options={statuses}
            onChange={(value) => update("status", value)}
          />
          <FilterSelect
            label="Source"
            value={filters.source}
            options={sources}
            onChange={(value) => update("source", value)}
          />
          <FilterSelect
            label="Model"
            value={filters.model}
            options={models}
            onChange={(value) => update("model", value)}
          />
          <FilterSelect
            label="Worker"
            value={filters.worker}
            options={workers}
            onChange={(value) => update("worker", value)}
          />
          <div className="filter-actions">
            <span aria-live="polite">
              {active === 0
                ? "No filters"
                : `${active} ${active === 1 ? "filter" : "filters"} active`}
            </span>
            <Button type="button" onClick={clear} disabled={active === 0}>
              <X aria-hidden="true" size={16} /> Clear filters
            </Button>
          </div>
        </div>
        <FailureClassFilter
          value={isFailureClass(filters.failure_class) ? filters.failure_class : ""}
          onChange={(value) => update("failure_class", value)}
        />
      </div>
      <Caveat>{QUEUE_CAVEAT}</Caveat>
      {runs.isPending ? <p>Loading runs</p> : null}
      {runs.isError ? (
        <ErrorState
          title="Could not load runs"
          message={messageOf(runs.error)}
          onRetry={() => void runs.refetch()}
        />
      ) : null}
      {runs.isSuccess && rows.length === 0 ? (
        <EmptyState title="No runs match these filters" message="Clear filters to see every run." />
      ) : null}
      {runs.isSuccess && rows.length > 0 ? (
        <div className="table-wrap">
          <table className="data-table runs-table">
            <thead>
              <tr>
                <th scope="col">Status</th>
                <th scope="col">Run</th>
                <th scope="col">Failure</th>
                <th scope="col">Queue | Exec</th>
                <th scope="col">Cost</th>
                <th scope="col">Model</th>
                <th scope="col">Started</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <RunTableRow key={row.id} row={row} />
              ))}
            </tbody>
          </table>
          <p className="table-footer muted">Showing {rows.length}</p>
        </div>
      ) : null}
    </div>
  );
}

function RunsFactoryStream({ factoryId }: { factoryId: string }) {
  useFactoryEvents(factoryId);
  return null;
}

function FilterSelect({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: string;
  options: readonly string[];
  onChange: (value: string) => void;
}) {
  const id = `filter-${label.toLowerCase()}`;
  return (
    <label className="filter-select" htmlFor={id}>
      {label}
      <select id={id} value={value} onChange={(event) => onChange(event.target.value)}>
        <option value="">Any</option>
        {options.map((option) => (
          <option key={option} value={option}>
            {option}
          </option>
        ))}
      </select>
    </label>
  );
}

function RunTableRow({ row }: { row: RunRow }) {
  const failureClass = isFailureClass(row.failure_class) ? row.failure_class : "";
  return (
    <tr data-run={row.id}>
      <td>
        <RunStatusBadge status={row.status} />
      </td>
      <td className="run-cell">
        <Link to="/runs/$runId" params={{ runId: row.id }} className="run-link">
          {row.title || shortId(row.id)}
        </Link>
        <p className="run-sub mono">
          <span title={row.id}>{shortId(row.id)}</span>
          {row.agent ? ` · ${row.agent}` : ""}
          {row.stage ? ` · ${row.stage}` : ""}
        </p>
      </td>
      <td className="failure-cell">
        {failureClass ? (
          <>
            <StatusBadge failureClass={failureClass} />
            {row.failure_code || row.exit_code != null ? (
              <p>
                {row.failure_code ? <span className="mono">{row.failure_code}</span> : null}
                {row.exit_code != null ? <span> exit {row.exit_code}</span> : null}
              </p>
            ) : null}
            {row.failure_summary ? <p>{row.failure_summary}</p> : null}
          </>
        ) : (
          <span>—</span>
        )}
      </td>
      <td>
        <DurationPair queueWaitMs={row.queue_wait_ms ?? 0} execMs={row.exec_ms ?? 0} />
      </td>
      <td className="mono">{formatCost(row.cost_cents)}</td>
      <td className="mono">{row.model_id || "—"}</td>
      <td>{row.started_at ? <time dateTime={row.started_at}>{row.started_at}</time> : "—"}</td>
    </tr>
  );
}
