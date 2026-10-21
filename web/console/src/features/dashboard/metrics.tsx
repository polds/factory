import { Link } from "@tanstack/react-router";
import type {
  CycleTimeStage,
  Factory,
  FactoryMetrics,
  ImprovementPR,
  NamedCount,
} from "../../api/types";
import { Caveat } from "../../components/Caveat";
import { ErrorState } from "../../components/ErrorState";
import { Panel } from "../../components/Panel";
import { PassRateByDay } from "../../components/PassRateByDay";
import { formatCost, formatCount, formatDuration, formatPercent } from "../../lib/format";
import { HttpsText } from "./https-link";

export const TOTAL_RUNS_CAVEAT = "Total runs includes measurement runs.";
export const MERGED_CAVEAT =
  "Opened and merged come from different sources, so merged can exceed opened.";
export const FORGE_CAVEAT =
  "Autonomy and cycle time cover only activity after the forge connected.";
export const COST_CAVEAT = "Comparison metric, not a billing figure";
export const IMPROVEMENT_CAVEAT = "Newest improvement PRs, regardless of the date filter.";
export const SIZE_CAPTION = "S/M/L/XL at 100/500/1,000 changed lines.";

const PR_SIZES = [
  { size: "S", lines: "100" },
  { size: "M", lines: "500" },
  { size: "L", lines: "1,000" },
  { size: "XL", lines: "above 1,000" },
] as const;

export type MetricsResult = {
  isPending: boolean;
  isError: boolean;
  data: FactoryMetrics | undefined;
};

export function namedTotal(rows: readonly NamedCount[]): number {
  return rows.reduce((total, row) => total + (Number.isFinite(row.count) ? row.count : 0), 0);
}

export function newestImprovementPRs(rows: readonly ImprovementPR[]): ImprovementPR[] {
  return [...rows].sort((left, right) => {
    const byTime = createdMillis(right.created_at) - createdMillis(left.created_at);
    if (byTime !== 0) return byTime;
    return right.batch_id.localeCompare(left.batch_id);
  });
}

function createdMillis(value: string): number {
  const parsed = Date.parse(value);
  return Number.isFinite(parsed) ? parsed : Number.NEGATIVE_INFINITY;
}

export function MetricsBoard({
  factories,
  results,
  onRetry,
}: {
  factories: readonly Factory[];
  results: readonly MetricsResult[];
  onRetry: () => void;
}) {
  if (factories.length === 0) return null;
  return (
    <>
      {factories.map((factory, index) => (
        <FactoryMetricsView
          key={factory.id}
          factory={factory}
          result={results[index]}
          onRetry={onRetry}
        />
      ))}
    </>
  );
}

function FactoryMetricsView({
  factory,
  result,
  onRetry,
}: {
  factory: Factory;
  result: MetricsResult | undefined;
  onRetry: () => void;
}) {
  const headingId = `metrics-${factory.id}`;
  const metrics = result?.data;
  return (
    <section className="operator-page" aria-labelledby={headingId}>
      <h2 id={headingId}>{factory.name} metrics</h2>
      {result?.isError || !result ? (
        <ErrorState
          title="Could not load metrics"
          message={`Metrics for ${factory.name} could not be loaded.`}
          onRetry={onRetry}
        />
      ) : result.isPending || !metrics ? (
        <p>Loading metrics</p>
      ) : (
        <FactoryMetricsBody factory={factory} metrics={metrics} />
      )}
    </section>
  );
}

function FactoryMetricsBody({ factory, metrics }: { factory: Factory; metrics: FactoryMetrics }) {
  const totalId = `tile-total-${factory.id}`;
  const mergedId = `tile-merged-${factory.id}`;
  const autonomyId = `tile-autonomy-${factory.id}`;
  const cycleId = `tile-cycle-${factory.id}`;
  const costId = `tile-cost-${factory.id}`;
  const forgeAt = metrics.autonomy.forge_connected_at;

  return (
    <>
      <div className="tiles">
        <div className="tile">
          <h3 id={totalId}>Total runs</h3>
          <p className="metric">{formatCount(namedTotal(metrics.total_runs.by_agent))}</p>
          <p>
            Measurement <span className="mono">{formatCount(metrics.total_runs.measurement)}</span>
          </p>
          <Caveat>{TOTAL_RUNS_CAVEAT}</Caveat>
        </div>
        <div className="tile">
          <h3 id={mergedId}>PRs merged</h3>
          <p className="metric">{formatCount(metrics.prs_merged)}</p>
        </div>
        <div className="tile">
          <h3 id={autonomyId}>Autonomy</h3>
          <p className="metric">{formatPercent(metrics.autonomy.share)}</p>
          <p>
            n <span className="mono">{formatCount(metrics.autonomy.n)}</span>
          </p>
          <ForgeConnected at={forgeAt} />
          <Caveat>{FORGE_CAVEAT}</Caveat>
        </div>
        <div className="tile">
          <h3 id={cycleId}>PR cycle time</h3>
          <p className="metric">{formatDuration(metrics.cycle_time.p50_ms)}</p>
          <ForgeConnected at={forgeAt} />
          <Caveat>{FORGE_CAVEAT}</Caveat>
        </div>
        <div className="tile">
          <h3 id={costId}>Cost per PR</h3>
          <p className="metric">{formatCost(metrics.cost_per_pr.median_cents)}</p>
          <Caveat>{COST_CAVEAT}</Caveat>
        </div>
      </div>
      <Panel title={`${factory.name} · run breakdown`}>
        <CountTable caption="Runs by agent" rows={metrics.total_runs.by_agent} />
        <CountTable caption="Runs by status" rows={metrics.total_runs.by_status} />
        <CountTable caption="Runs by source" rows={metrics.total_runs.by_source} />
        <CountTable caption="Runs by model" rows={metrics.total_runs.by_model} />
      </Panel>
      <Panel title={`${factory.name} · PR outcomes`}>
        <CountTable
          caption="Opened and merged counts"
          rows={[
            { name: "Opened", count: metrics.prs_opened },
            { name: "Merged", count: metrics.prs_merged },
          ]}
        />
        <Caveat>{MERGED_CAVEAT}</Caveat>
      </Panel>
      <Panel title={`${factory.name} · cycle time`}>
        <StageTable stages={metrics.cycle_time.stages} />
      </Panel>
      <Panel title={`${factory.name} · cost by size`}>
        <p>{SIZE_CAPTION}</p>
        <div className="table-scroll" tabIndex={0}>
          <table>
            <caption className="sr-only">Cost per PR by size</caption>
            <thead className="data-head">
              <tr>
                <th scope="col">Size</th>
                <th scope="col">Changed lines</th>
                <th scope="col">Median</th>
              </tr>
            </thead>
            <tbody>
              {PR_SIZES.map((row) => (
                <tr key={row.size}>
                  <th scope="row">{row.size}</th>
                  <td>{row.lines}</td>
                  <td className="mono">{formatCost(metrics.cost_per_pr.by_size[row.size])}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Panel>
      <Panel title={`${factory.name} · expensive PRs`}>
        {metrics.most_expensive_prs.length === 0 ? (
          <p>No expensive PRs</p>
        ) : (
          <ul className="stat-list">
            {metrics.most_expensive_prs.map((pr) => (
              <li key={`${pr.repo}#${pr.number}`}>
                <HttpsText url={pr.url} label={`${pr.repo}#${pr.number}`} />{" "}
                <span className="mono">{formatCost(pr.cost_cents)}</span>{" "}
                <span className="mono">{formatCount(pr.changed_lines)} lines</span>
              </li>
            ))}
          </ul>
        )}
      </Panel>
      <Panel title={`${factory.name} · scorer cards`}>
        {metrics.scorer_cards.length === 0 ? (
          <p>No scorer cards</p>
        ) : (
          metrics.scorer_cards.map((card) => (
            <div key={card.name}>
              <p>
                <Link
                  to="/f/$factory/scorers/$name"
                  params={{ factory: factory.id, name: card.name }}
                >
                  {card.name}
                </Link>
              </p>
              <PassRateByDay days={card.pass_rate_by_day} />
            </div>
          ))
        )}
      </Panel>
      <Panel title={`${factory.name} · improvement PRs`}>
        <Caveat>{IMPROVEMENT_CAVEAT}</Caveat>
        {metrics.improvement_prs.length === 0 ? (
          <p>No improvement PRs</p>
        ) : (
          <ul className="stat-list">
            {newestImprovementPRs(metrics.improvement_prs).map((pr) => (
              <li key={pr.batch_id}>
                <span>{pr.agent}</span> <time dateTime={pr.created_at}>{pr.created_at}</time>{" "}
                <HttpsText url={pr.url} label="Review PR" />
              </li>
            ))}
          </ul>
        )}
      </Panel>
    </>
  );
}

function ForgeConnected({ at }: { at: string | undefined }) {
  if (!at) return <p>forge not connected</p>;
  return (
    <p>
      Forge connected <time dateTime={at}>{at}</time>
    </p>
  );
}

function CountTable({ caption, rows }: { caption: string; rows: readonly NamedCount[] }) {
  if (rows.length === 0) return <p>None</p>;
  return (
    <div className="table-scroll" tabIndex={0}>
      <table>
        <caption className="sr-only">{caption}</caption>
        <thead className="data-head">
          <tr>
            <th scope="col">Name</th>
            <th scope="col">Count</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row, index) => (
            <tr key={`${row.name}-${index}`}>
              <th scope="row">{row.name}</th>
              <td className="mono">{formatCount(row.count)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function StageTable({ stages }: { stages: readonly CycleTimeStage[] }) {
  if (stages.length === 0) return <p>No stages</p>;
  return (
    <div className="table-scroll" tabIndex={0}>
      <table>
        <caption className="sr-only">Per-stage median cycle time</caption>
        <thead className="data-head">
          <tr>
            <th scope="col">Stage</th>
            <th scope="col">Median</th>
          </tr>
        </thead>
        <tbody>
          {stages.map((stage, index) => (
            <tr key={`${stage.name}-${index}`}>
              <th scope="row" className="mono">
                {stage.name}
              </th>
              <td className="mono">{formatDuration(stage.p50_ms)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
