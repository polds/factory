import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { Trophy } from "lucide-react";
import { useRef, useState, type FormEvent } from "react";
import { client } from "../../api/client";
import { useFactoryEvents } from "../../api/live";
import type { BenchmarkCell, BenchmarkSummary, BenchmarkTrials } from "../../api/types";
import { Button } from "../../components/Button";
import { ConfirmDialog } from "../../components/ConfirmDialog";
import { EmptyState } from "../../components/EmptyState";
import { ErrorState } from "../../components/ErrorState";
import { PageHeader } from "../../components/PageHeader";
import { Panel } from "../../components/Panel";
import { StatusBadge } from "../../components/StatusBadge";
import { durationDateTime, formatDuration, formatPercent } from "../../lib/format";
import { problemDetail } from "../dashboard/model";
import { formatCost, formatCount } from "../runs/format";

type TrialCell = {
  configuration: string;
  task: string;
  n: number;
  errored: number;
  pass_rate: number;
  ci_low: number;
  ci_high: number;
  median_cost_cents: number;
  median_duration_ms: number;
};

type TrialView = {
  cells: TrialCell[];
  winner: string;
  reason: string;
};

function presentCell(raw: BenchmarkCell): TrialCell {
  return {
    configuration: raw.configuration,
    task: raw.task,
    n: raw.n,
    errored: raw.errored,
    pass_rate: raw.pass_rate,
    ci_low: raw.ci_low,
    ci_high: raw.ci_high,
    median_cost_cents: raw.median_cost_cents,
    median_duration_ms: raw.median_duration_ms,
  };
}

function presentTrials(raw: BenchmarkTrials): TrialView {
  return {
    cells: raw.cells.map(presentCell),
    winner: raw.report.winner,
    reason: raw.report.reason,
  };
}

function presentSummary(raw: BenchmarkSummary): BenchmarkSummary {
  return {
    suite: raw.suite,
    agent: raw.agent,
    configurations: raw.configurations,
    tasks: raw.tasks,
    last_run: raw.last_run
      ? {
          id: raw.last_run.id,
          created_at: raw.last_run.created_at,
          repetitions: raw.last_run.repetitions,
        }
      : undefined,
  };
}

function parsedRepetitions(value: string): number | null {
  if (!/^\d+$/.test(value)) return null;
  const parsed = Number(value);
  if (!Number.isSafeInteger(parsed) || parsed < 1) return null;
  return parsed;
}

function axes(summary: BenchmarkSummary | undefined, cells: readonly TrialCell[]) {
  const configurations: string[] = [];
  const tasks: string[] = [];
  const add = (list: string[], value: string) => {
    if (!list.includes(value)) list.push(value);
  };
  for (const name of summary?.configurations ?? []) add(configurations, name);
  for (const name of summary?.tasks ?? []) add(tasks, name);
  for (const cell of cells) {
    add(configurations, cell.configuration);
    add(tasks, cell.task);
  }
  return { configurations, tasks };
}

function ErroredCount({ count }: { count: number }) {
  if (count > 0) {
    return (
      <span className="inline-flex flex-wrap items-center gap-1">
        <StatusBadge failureClass="infrastructure" />
        <span className="mono">{formatCount(count)}</span>
      </span>
    );
  }
  return <span className="mono">{formatCount(count)}</span>;
}

function TrialMetrics({ cell }: { cell: TrialCell }) {
  return (
    <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-2 gap-y-0.5 text-sm">
      <dt className="text-muted-foreground">n</dt>
      <dd className="mono">{formatCount(cell.n)}</dd>
      <dt className="text-muted-foreground">Pass rate</dt>
      <dd className="mono">{formatPercent(cell.pass_rate)}</dd>
      <dt className="text-muted-foreground">CI</dt>
      <dd className="mono whitespace-nowrap">
        {formatPercent(cell.ci_low)}–{formatPercent(cell.ci_high)}
      </dd>
      <dt className="text-muted-foreground">Errored</dt>
      <dd>
        <ErroredCount count={cell.errored} />
      </dd>
      <dt className="text-muted-foreground">Median cost</dt>
      <dd className="mono">{formatCost(cell.median_cost_cents)}</dd>
      <dt className="text-muted-foreground">Median duration</dt>
      <dd className="mono">
        <time dateTime={durationDateTime(cell.median_duration_ms)}>
          {formatDuration(cell.median_duration_ms)}
        </time>
      </dd>
    </dl>
  );
}

function TrialMatrix({
  summary,
  trials,
}: {
  summary: BenchmarkSummary | undefined;
  trials: TrialView;
}) {
  const { configurations, tasks } = axes(summary, trials.cells);
  const byKey = new Map(trials.cells.map((cell) => [`${cell.configuration}\0${cell.task}`, cell]));
  if (configurations.length === 0 || tasks.length === 0) {
    return <EmptyState title="No trials" message="This suite has no trial cells." />;
  }
  return (
    <div className="table-scroll" tabIndex={0}>
      <table>
        <caption className="sr-only">Trial matrix</caption>
        <thead className="data-head">
          <tr>
            <th scope="col">Configuration</th>
            {tasks.map((task) => (
              <th key={task} scope="col">
                {task}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {configurations.map((configuration) => {
            const winner = trials.winner !== "" && configuration === trials.winner;
            return (
              <tr key={configuration}>
                <th scope="row">
                  <span className="mono">{configuration}</span>
                  {winner ? (
                    <span className="mt-1 flex items-center gap-1">
                      <Trophy aria-hidden="true" size={16} />
                      <span>Winner</span>
                    </span>
                  ) : null}
                </th>
                {tasks.map((task) => {
                  const cell = byKey.get(`${configuration}\0${task}`);
                  return <td key={task}>{cell ? <TrialMetrics cell={cell} /> : "—"}</td>;
                })}
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function SuiteList({
  factoryId,
  suites,
}: {
  factoryId: string;
  suites: readonly BenchmarkSummary[];
}) {
  if (suites.length === 0) {
    return <EmptyState title="No benchmarks" message="This factory has no benchmark suites." />;
  }
  return (
    <div className="min-w-0">
      <Panel title="Suites">
        <div className="table-scroll" tabIndex={0}>
          <table>
            <caption className="sr-only">Benchmark suites</caption>
            <thead className="data-head">
              <tr>
                <th scope="col">Suite</th>
                <th scope="col">Agent</th>
                <th scope="col">Configurations</th>
                <th scope="col">Tasks</th>
                <th scope="col">Last run</th>
              </tr>
            </thead>
            <tbody>
              {suites.map((suite) => (
                <tr key={suite.suite}>
                  <td>
                    <Link
                      to="/f/$factory/benchmarks/$suite"
                      params={{ factory: factoryId, suite: suite.suite }}
                      className="mono"
                    >
                      {suite.suite}
                    </Link>
                  </td>
                  <td>{suite.agent}</td>
                  <td className="break-words">{suite.configurations.join(", ") || "None"}</td>
                  <td className="break-words">{suite.tasks.join(", ") || "None"}</td>
                  <td>
                    {suite.last_run ? (
                      <span className="mono">
                        {suite.last_run.id} · {suite.last_run.repetitions} repetitions ·{" "}
                        <time dateTime={suite.last_run.created_at}>
                          {suite.last_run.created_at}
                        </time>
                      </span>
                    ) : (
                      "No runs yet"
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Panel>
    </div>
  );
}

function RunBenchmark({ factoryId, suite }: { factoryId: string; suite: string }) {
  const queryClient = useQueryClient();
  const lock = useRef(false);
  const [repetitions, setRepetitions] = useState("5");
  const [chosen, setChosen] = useState(5);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [runError, setRunError] = useState<string | null>(null);
  const [started, setStarted] = useState<string | null>(null);

  function openConfirm(event?: FormEvent) {
    event?.preventDefault();
    const parsed = parsedRepetitions(repetitions);
    if (parsed === null) {
      setRunError("Repetitions must be at least 1.");
      setStarted(null);
      return;
    }
    setChosen(parsed);
    setRunError(null);
    setConfirmOpen(true);
  }

  async function confirm() {
    if (lock.current) return;
    lock.current = true;
    setRunError(null);
    setStarted(null);
    try {
      const accepted = await client.startBenchmark(factoryId, suite, { repetitions: chosen });
      setStarted(accepted.benchmark_run_id);
      setConfirmOpen(false);
      await queryClient.invalidateQueries({ queryKey: ["benchmarks", factoryId] });
    } catch (error) {
      setRunError(problemDetail(error));
      setConfirmOpen(false);
    } finally {
      lock.current = false;
    }
  }

  return (
    <>
      <form className="flex min-w-0 basis-full flex-wrap items-center gap-2" onSubmit={openConfirm}>
        <label htmlFor="benchmark-repetitions">Repetitions</label>
        <input
          id="benchmark-repetitions"
          type="number"
          min={1}
          step={1}
          inputMode="numeric"
          value={repetitions}
          onChange={(event) => {
            setRepetitions(event.target.value);
            setRunError(null);
          }}
        />
        <Button type="submit">Run benchmark</Button>
        {runError ? (
          <p role="alert" className="basis-full text-danger">
            {runError}
          </p>
        ) : null}
      </form>
      {started ? (
        <p role="status" className="basis-full">
          Benchmark run {started} started.
        </p>
      ) : null}
      <ConfirmDialog
        open={confirmOpen}
        title="Run benchmark?"
        description={`Run ${suite} with ${chosen} repetitions.`}
        confirmLabel="Run benchmark"
        onConfirm={() => void confirm()}
        onOpenChange={(open) => {
          if (!open) setConfirmOpen(false);
        }}
      />
    </>
  );
}

function BenchmarksScreen({ factoryId, suite }: { factoryId: string; suite: string }) {
  useFactoryEvents(factoryId);
  const list = useQuery({
    queryKey: ["benchmarks", factoryId],
    queryFn: async () => (await client.listBenchmarks(factoryId)).map(presentSummary),
  });
  const trials = useQuery({
    queryKey: ["benchmarks", factoryId, "trials", suite],
    queryFn: async () => presentTrials(await client.benchmarkTrials(factoryId, suite)),
    enabled: suite !== "",
  });

  if (list.isPending || (suite !== "" && trials.isPending)) return <p>Loading benchmarks</p>;
  if (list.isError) {
    return (
      <ErrorState
        title="Could not load benchmarks"
        message={problemDetail(list.error)}
        onRetry={() => void list.refetch()}
      />
    );
  }
  if (suite !== "" && trials.isError) {
    return (
      <ErrorState
        title="Could not load trials"
        message={problemDetail(trials.error)}
        onRetry={() => void trials.refetch()}
      />
    );
  }

  const summary = suite === "" ? undefined : list.data.find((item) => item.suite === suite);
  const view = suite === "" ? undefined : trials.data;

  return (
    <div className="operator-page">
      <PageHeader
        title="Benchmarks"
        subtitle={suite || undefined}
        actions={
          suite ? (
            <>
              <Link to="/f/$factory/benchmarks" params={{ factory: factoryId }}>
                All suites
              </Link>
              <RunBenchmark factoryId={factoryId} suite={suite} />
            </>
          ) : undefined
        }
      />
      {suite === "" ? <SuiteList factoryId={factoryId} suites={list.data} /> : null}
      {view ? (
        <>
          {view.winner === "" ? (
            <div className="callout" role="status">
              <p>{view.reason}</p>
            </div>
          ) : view.reason ? (
            <p className="muted">{view.reason}</p>
          ) : null}
          <div className="min-w-0">
            <Panel title={summary?.suite ?? suite}>
              <TrialMatrix summary={summary} trials={view} />
            </Panel>
          </div>
        </>
      ) : null}
    </div>
  );
}

export default function BenchmarksPage() {
  const params = useParams({ strict: false });
  const factoryId = typeof params.factory === "string" ? params.factory : "";
  const suite = typeof params.suite === "string" ? params.suite : "";
  if (!factoryId) {
    return <ErrorState title="Missing factory" message="Open benchmarks from a factory." />;
  }
  return <BenchmarksScreen factoryId={factoryId} suite={suite} />;
}
