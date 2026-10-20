import { useQuery } from "@tanstack/react-query";
import { useParams } from "@tanstack/react-router";
import { AlertTriangle, HeartOff, HeartPulse, Info } from "lucide-react";
import { client } from "../../api/client";
import type { ConnectedWorker, WorkerPool } from "../../api/types";
import { ErrorState } from "../../components/ErrorState";
import { formatDuration } from "../../lib/format";

const STALE_MS = 30_000;

type WorkerRow = {
  id: string;
  pool: string;
  platform: string;
  backend: string;
  version: string;
  in_flight: number;
  capacity: number;
  heartbeat_age_ms: number;
  reconnects: number;
  rejected_1h: number;
};

type PoolRow = {
  pool: string;
  queue_depth: number;
  p50_time_to_assign_ms: number;
  rejected_1h: number;
};

function presentWorker(raw: ConnectedWorker): WorkerRow {
  return {
    id: raw.id,
    pool: raw.pool,
    platform: raw.platform,
    backend: raw.backend,
    version: raw.version,
    in_flight: raw.in_flight,
    capacity: raw.capacity,
    heartbeat_age_ms: raw.heartbeat_age_ms,
    reconnects: raw.reconnects,
    rejected_1h: raw.rejected_1h,
  };
}

function presentPool(raw: WorkerPool): PoolRow {
  return {
    pool: raw.pool,
    queue_depth: raw.queue_depth,
    p50_time_to_assign_ms: raw.p50_time_to_assign_ms,
    rejected_1h: raw.rejected_1h,
  };
}

function known(value: string): string {
  return value.trim() === "" ? "unknown" : value;
}

function p50Label(ms: number, queueDepth: number): string {
  if (ms === 0 && queueDepth === 0) return "—";
  return formatDuration(ms);
}

function WorkersScreen() {
  const query = useQuery({
    queryKey: ["workers"],
    queryFn: async () => {
      const view = await client.listWorkers();
      return {
        workers: (view.workers ?? []).map(presentWorker),
        pools: (view.pools ?? []).map(presentPool),
      };
    },
  });

  if (query.isPending) return <p>Loading workers</p>;
  if (query.isError) {
    return (
      <ErrorState
        title="Could not load workers"
        message="The worker list could not be loaded."
        onRetry={() => void query.refetch()}
      />
    );
  }

  const { workers, pools } = query.data;
  return (
    <div>
      <header className="page-header mb-5">
        <div>
          <h1>Workers</h1>
          <p className="muted">All factories · workers connect to factoryd and pull runs by pool</p>
        </div>
      </header>
      <div className="mb-4 grid gap-3 lg:grid-cols-3">
        {pools.map((pool) => {
          const count = workers.filter((worker) => worker.pool === pool.pool).length;
          return (
            <section
              key={pool.pool}
              aria-labelledby={`pool-${pool.pool}`}
              className={pool.rejected_1h > 0 ? "pool-card pool-card-alert" : "pool-card"}
            >
              <div className="flex items-baseline justify-between">
                <h2 id={`pool-${pool.pool}`} className="font-mono text-sm font-semibold">
                  {pool.pool}
                </h2>
                <p className="font-sans text-xs text-muted-foreground">
                  {count} {count === 1 ? "worker" : "workers"}
                </p>
              </div>
              <dl className="mt-2 grid grid-cols-3 gap-2">
                <div>
                  <dt className="text-xs text-muted-foreground">Queue depth</dt>
                  <dd className="font-mono text-xl font-semibold">{pool.queue_depth}</dd>
                </div>
                <div>
                  <dt className="text-xs text-muted-foreground">p50 time to first start</dt>
                  <dd className="font-mono text-xl font-semibold">
                    {p50Label(pool.p50_time_to_assign_ms, pool.queue_depth)}
                  </dd>
                </div>
                <div>
                  <dt className="text-xs text-muted-foreground">Rejections (1h)</dt>
                  <dd className="font-mono text-xl font-semibold">{pool.rejected_1h}</dd>
                </div>
              </dl>
              <p className="mt-2 flex items-start gap-1.5 text-xs text-muted-foreground">
                <Info className="mt-0.5 size-3.5 shrink-0" aria-hidden />
                <span>measured to the first start; a requeue doesn&apos;t restart it</span>
              </p>
              {pool.rejected_1h > 0 ? (
                <p
                  role="status"
                  className="mt-2 flex items-start gap-1.5 border border-dashed border-border bg-muted p-2 text-sm"
                >
                  <AlertTriangle className="mt-0.5 size-4 shrink-0 text-danger" aria-hidden />
                  Sustained rejections usually mean the pool is too small.
                </p>
              ) : null}
            </section>
          );
        })}
      </div>
      <div className="panel table-scroll">
        <table className="w-full border-collapse text-sm">
          <caption className="sr-only">Connected workers</caption>
          <thead className="data-head">
            <tr>
              {[
                "Worker",
                "Pool",
                "Platform",
                "Backend",
                "Version",
                "In-flight / capacity",
                "Heartbeat",
                "Reconnects",
                "Rejected (1h)",
              ].map((heading) => (
                <th key={heading} scope="col" className="px-3 py-2 whitespace-nowrap">
                  {heading}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {workers.map((worker) => (
              <WorkerRowView key={worker.id} worker={worker} />
            ))}
          </tbody>
        </table>
      </div>
      <div className="mt-2 space-y-1 text-sm text-muted-foreground">
        <p className="flex items-start gap-1.5">
          <Info className="mt-0.5 size-4 shrink-0" aria-hidden />
          <span>Stale = no heartbeat for 30s+.</span>
        </p>
        <p className="flex items-start gap-1.5">
          <Info className="mt-0.5 size-4 shrink-0" aria-hidden />
          <span>Rejected (1h) = task offers this worker rejected in the last hour.</span>
        </p>
      </div>
    </div>
  );
}

function WorkerRowView({ worker }: { worker: WorkerRow }) {
  const never = worker.heartbeat_age_ms < 0;
  const stale = !never && worker.heartbeat_age_ms >= STALE_MS;
  const width = worker.capacity > 0 ? Math.min(100, (worker.in_flight / worker.capacity) * 100) : 0;
  return (
    <tr
      className={
        stale
          ? "border-b border-border bg-muted last:border-b-0"
          : "border-b border-border last:border-b-0"
      }
    >
      <td className="px-3 py-2 font-mono text-xs font-semibold">{worker.id}</td>
      <td className="px-3 py-2 font-mono text-xs">{worker.pool}</td>
      <td className="px-3 py-2 font-mono text-xs">{worker.platform}</td>
      <td className="px-3 py-2">{known(worker.backend)}</td>
      <td className="px-3 py-2 font-mono text-xs">{known(worker.version)}</td>
      <td className="px-3 py-2">
        <span className="flex items-center gap-2">
          <span className="font-mono text-xs">
            {worker.in_flight} / {worker.capacity}
          </span>
          <span className="h-2 w-16 border border-border bg-muted" aria-hidden>
            <span className="block h-full bg-foreground" style={{ width: `${width}%` }} />
          </span>
        </span>
      </td>
      <td className="px-3 py-2 whitespace-nowrap">
        {never ? (
          <span className="inline-flex items-center gap-1">
            <HeartOff className="size-4" aria-hidden />
            never
          </span>
        ) : stale ? (
          <span className="inline-flex items-center gap-1">
            <AlertTriangle className="size-4 text-danger" aria-hidden />
            <span>Stale</span>
            <span className="font-mono text-xs">{formatDuration(worker.heartbeat_age_ms)}</span>
          </span>
        ) : (
          <span className="inline-flex items-center gap-1">
            <HeartPulse className="size-4" aria-hidden />
            <span className="font-mono text-xs">{formatDuration(worker.heartbeat_age_ms)}</span>
          </span>
        )}
      </td>
      <td className="px-3 py-2 font-mono text-xs">{worker.reconnects}</td>
      <td className="px-3 py-2 font-mono text-xs">{worker.rejected_1h}</td>
    </tr>
  );
}

export default function WorkersPage() {
  useParams({ strict: false });
  return <WorkersScreen />;
}
