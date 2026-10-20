import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useParams } from "@tanstack/react-router";
import {
  CalendarClock,
  CheckCircle2,
  CircleSlash,
  GitPullRequest,
  Info,
  MessageSquare,
  PauseCircle,
  PlayCircle,
  SquareKanban,
  Webhook,
} from "lucide-react";
import { useRef, useState } from "react";
import { client } from "../../api/client";
import type { Automation } from "../../api/types";
import { ConfirmDialog } from "../../components/ConfirmDialog";
import { EmptyState } from "../../components/EmptyState";
import { ErrorState } from "../../components/ErrorState";

type AutomationRow = {
  name: string;
  enabled: boolean;
  runtime_paused: boolean;
  triggers: string[];
  last_fired_at: string | null;
  match_count: number;
  runs_started: number;
};

type Pending = { name: string; action: "pause" | "resume" };

function present(raw: Automation): AutomationRow {
  const triggers = Array.isArray(raw.triggers)
    ? raw.triggers.filter((trigger): trigger is string => typeof trigger === "string")
    : [];
  return {
    name: raw.name,
    enabled: raw.enabled,
    runtime_paused: raw.runtime_paused,
    triggers,
    last_fired_at: raw.last_fired_at,
    match_count: raw.match_count,
    runs_started: raw.runs_started,
  };
}

function triggerMark(trigger: string) {
  const head = trigger.split(/[:.\s]/)[0]?.toLowerCase() ?? "";
  const className = "size-3.5";
  if (head === "github") return <GitPullRequest className={className} aria-hidden />;
  if (head === "slack") return <MessageSquare className={className} aria-hidden />;
  if (head === "linear") return <SquareKanban className={className} aria-hidden />;
  if (head === "cron") return <CalendarClock className={className} aria-hidden />;
  return <Webhook className={className} aria-hidden />;
}

function formatWhen(iso: string | null): string {
  if (!iso) return "—";
  const then = Date.parse(iso);
  if (Number.isNaN(then)) return iso;
  const minutes = Math.round((Date.now() - then) / 60000);
  const abs = Math.abs(minutes);
  if (abs < 1) return "just now";
  if (abs < 60) return `${minutes}m`;
  const hours = Math.round(minutes / 60);
  if (Math.abs(hours) < 48) return `${hours}h`;
  return `${Math.round(hours / 24)}d`;
}

function AutomationsScreen({ factoryId }: { factoryId: string }) {
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: ["automations", factoryId],
    queryFn: async () => (await client.listAutomations(factoryId)).map(present),
  });
  const [pending, setPending] = useState<Pending | null>(null);
  const [error, setError] = useState<string | null>(null);
  const lock = useRef(false);

  async function confirm() {
    if (!pending || lock.current) return;
    lock.current = true;
    setError(null);
    try {
      if (pending.action === "pause") await client.pauseAutomation(factoryId, pending.name);
      else await client.resumeAutomation(factoryId, pending.name);
      await queryClient.invalidateQueries({ queryKey: ["automations", factoryId] });
      setPending(null);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "The runtime override failed");
    } finally {
      lock.current = false;
    }
  }

  if (query.isPending) return <p>Loading automations</p>;
  if (query.isError) {
    return (
      <ErrorState
        title="Could not load automations"
        message="The automation list could not be loaded."
        onRetry={() => void query.refetch()}
      />
    );
  }

  const rows = query.data;
  const paused = rows.filter((row) => row.runtime_paused).length;
  const matches = rows.reduce((sum, row) => sum + row.match_count, 0);
  const started = rows.reduce((sum, row) => sum + row.runs_started, 0);

  return (
    <div>
      <header className="page-header mb-5">
        <div>
          <h1>Automations</h1>
          <p className="muted">
            Factory <span className="font-mono">{factoryId}</span> · defined in the factory
            definition
          </p>
        </div>
      </header>
      <div className="mb-4 grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Stat label="Automations" value={rows.length} />
        <Stat label="Paused" value={paused} />
        <Stat label="Matches" value={matches} />
        <Stat label="Runs started" value={started} />
      </div>
      {error ? (
        <p role="alert" className="mb-3 text-sm text-danger">
          {error}
        </p>
      ) : null}
      {rows.length === 0 ? (
        <EmptyState
          title="No automations defined"
          message="This factory definition has no automations."
        />
      ) : (
        <div className="panel table-scroll">
          <table className="w-full border-collapse text-sm">
            <caption className="sr-only">Automations</caption>
            <thead className="data-head">
              <tr>
                {[
                  "Name",
                  "Triggers",
                  "Enabled",
                  "Runtime",
                  "Last fired",
                  "Matches",
                  "Runs started",
                ].map((heading) => (
                  <th key={heading} scope="col" className="px-3 py-2 whitespace-nowrap">
                    {heading}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr key={row.name} className="border-b border-border align-top last:border-b-0">
                  <td className="px-3 py-2 font-mono text-xs font-semibold">{row.name}</td>
                  <td className="px-3 py-2">
                    <ul className="flex flex-wrap gap-1">
                      {row.triggers.map((trigger) => (
                        <li
                          key={trigger}
                          className="inline-flex items-center gap-1 border border-border bg-muted px-1.5 font-mono text-xs"
                        >
                          {triggerMark(trigger)}
                          {trigger}
                        </li>
                      ))}
                    </ul>
                  </td>
                  <td className="px-3 py-2 whitespace-nowrap">
                    {row.enabled ? (
                      <span className="inline-flex items-center gap-1">
                        <CheckCircle2 className="size-4" aria-hidden />
                        Enabled
                      </span>
                    ) : (
                      <span className="inline-flex items-center gap-1 text-muted-foreground">
                        <CircleSlash className="size-4" aria-hidden />
                        Disabled
                      </span>
                    )}
                  </td>
                  <td className="px-3 py-2">
                    <button
                      type="button"
                      role="switch"
                      aria-checked={row.runtime_paused}
                      aria-label={row.runtime_paused ? `Resume ${row.name}` : `Pause ${row.name}`}
                      className="inline-flex items-center gap-1 border border-border bg-muted px-2 py-1 text-sm"
                      onClick={() =>
                        setPending({
                          name: row.name,
                          action: row.runtime_paused ? "resume" : "pause",
                        })
                      }
                    >
                      {row.runtime_paused ? (
                        <>
                          <PauseCircle className="size-4" aria-hidden /> Paused
                        </>
                      ) : (
                        <>
                          <PlayCircle className="size-4" aria-hidden /> Active
                        </>
                      )}
                    </button>
                    {row.runtime_paused ? (
                      <p className="mt-1 flex items-center gap-1 text-xs">
                        <PauseCircle className="size-3.5" aria-hidden />
                        Paused at runtime
                      </p>
                    ) : null}
                  </td>
                  <td className="px-3 py-2 font-mono text-xs">
                    {row.last_fired_at ? (
                      <time dateTime={row.last_fired_at}>{formatWhen(row.last_fired_at)}</time>
                    ) : (
                      "—"
                    )}
                  </td>
                  <td className="px-3 py-2 font-mono text-xs">{row.match_count}</td>
                  <td className="px-3 py-2 font-mono text-xs">{row.runs_started}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <div className="mt-2 space-y-1 text-sm text-muted-foreground">
        <p className="flex items-start gap-1.5">
          <Info className="mt-0.5 size-4 shrink-0" aria-hidden />
          <span>
            Runtime override: survives definition applies and doesn&apos;t edit the definition.
          </span>
        </p>
        <p className="flex items-start gap-1.5">
          <Info className="mt-0.5 size-4 shrink-0" aria-hidden />
          <span>Enabled comes from the definition and is read-only here.</span>
        </p>
      </div>
      <ConfirmDialog
        open={pending !== null}
        title={
          pending?.action === "resume"
            ? `Resume ${pending.name}?`
            : `Pause ${pending?.name ?? "automation"}?`
        }
        description="This is a runtime override. It survives definition applies and doesn't edit the definition."
        confirmLabel={pending?.action === "resume" ? "Resume" : "Pause"}
        onConfirm={() => void confirm()}
        onOpenChange={(open) => {
          if (!open) setPending(null);
        }}
      />
    </div>
  );
}

function Stat({ label, value }: { label: string; value: number }) {
  return (
    <section className="stat-card">
      <p className="stat-card-label">{label}</p>
      <p className="metric">{value}</p>
    </section>
  );
}

export default function AutomationsPage() {
  const params = useParams({ strict: false });
  const factoryId = typeof params.factory === "string" ? params.factory : "";
  if (!factoryId)
    return <ErrorState title="Missing factory" message="Open automations from a factory." />;
  return <AutomationsScreen factoryId={factoryId} />;
}
