import { Link } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, useRef, useState, type ReactNode } from "react";
import { APIError, client } from "../../api/client";
import { Button } from "../../components/Button";
import { ConfirmDialog } from "../../components/ConfirmDialog";
import { DurationPair } from "../../components/DurationPair";
import { ErrorState } from "../../components/ErrorState";
import { StatusBadge } from "../../components/StatusBadge";
import { Caveat } from "./caveat";
import { rowsFromEvents, steerSignals, canCancel, isTerminalStatus } from "./events";
import { formatCost, isFailureClass, prNumber, QUEUE_CAVEAT, shortId, workerLabel } from "./format";
import { FailurePanel } from "./failure-panel";
import { FilesTab } from "./files-tab";
import { RunStatusBadge } from "./status";
import { SteerPanel } from "./steer-panel";
import { RunTabs, type RunTab } from "./tabs";
import { TimelineTab } from "./timeline-tab";
import { Transcript } from "./transcript";
import { TreeTab } from "./tree-tab";
import type { LocalSteer, RunDetailWire, RunRow } from "./types";
import { UsageTab } from "./usage-tab";
import { useRunEvents } from "./use-run-events";

function messageOf(error: unknown): string {
  if (error instanceof APIError) return error.problem?.detail || error.message;
  return error instanceof Error ? error.message : "The run could not be loaded.";
}

function reasonOf(error: unknown): string {
  if (error instanceof APIError)
    return error.problem?.reason || error.problem?.detail || error.message;
  return error instanceof Error ? error.message : "The request failed.";
}

export function RunDetailPage({ runId }: { runId: string }) {
  const queryClient = useQueryClient();
  const runQuery = useQuery({
    queryKey: ["run", runId],
    queryFn: () => client.getRun(runId) as Promise<RunDetailWire>,
  });
  const stream = useRunEvents(runId, isTerminalStatus(runQuery.data?.status ?? ""));
  const timeline = useQuery({
    queryKey: ["run-timeline", runId],
    queryFn: () => client.runTimeline(runId),
  });
  const usage = useQuery({
    queryKey: ["run-usage", runId],
    queryFn: () => client.runUsage(runId),
  });
  const children = useQuery({
    queryKey: ["run-children", runId],
    queryFn: async () => {
      const rows = (await client.listRuns({ parent_run_id: runId })) as RunRow[];
      return rows.filter((row) => row.parent_run_id === runId && row.id !== runId);
    },
  });
  const [text, setText] = useState("");
  const [sendError, setSendError] = useState("");
  const [localSteers, setLocalSteers] = useState<LocalSteer[]>([]);
  const [cancelOpen, setCancelOpen] = useState(false);
  const [steerPending, setSteerPending] = useState(false);
  const followInFlight = useRef(false);
  const interruptInFlight = useRef(false);

  const rows = useMemo(() => rowsFromEvents(stream.events), [stream.events]);
  const signals = useMemo(() => steerSignals(stream.events), [stream.events]);
  const latestSeq =
    stream.events.length > 0 ? stream.events[stream.events.length - 1]?.id || null : null;

  const followUp = useMutation({
    mutationFn: (body: string) => client.followUp(runId, body),
    onSuccess: (accepted) => {
      setLocalSteers((prev) => [...prev, { id: accepted.steer_id, kind: "followup" }]);
      setText("");
      setSendError("");
    },
    onError: (error: unknown) => setSendError(reasonOf(error)),
  });
  const interrupt = useMutation({
    mutationFn: () => client.interruptRun(runId),
    onSuccess: (accepted) => {
      setLocalSteers((prev) => [...prev, { id: accepted.steer_id, kind: "interrupt" }]);
      setSendError("");
    },
    onError: (error: unknown) => setSendError(reasonOf(error)),
  });
  const cancel = useMutation({
    mutationFn: () => client.cancelRun(runId),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["run", runId] });
    },
  });

  function sendFollowUp() {
    if (followInFlight.current || interruptInFlight.current) return;
    if (text.trim() === "") return;
    followInFlight.current = true;
    setSteerPending(true);
    followUp.mutate(text, {
      onSettled: () => {
        followInFlight.current = false;
        setSteerPending(false);
      },
    });
  }

  function sendInterrupt() {
    if (followInFlight.current || interruptInFlight.current) return;
    interruptInFlight.current = true;
    setSteerPending(true);
    interrupt.mutate(undefined, {
      onSettled: () => {
        interruptInFlight.current = false;
        setSteerPending(false);
      },
    });
  }

  if (runQuery.isPending) return <p>Loading run</p>;
  if (runQuery.isError || !runQuery.data) {
    return (
      <ErrorState
        title="Could not load run"
        message={messageOf(runQuery.error)}
        onRetry={() => void runQuery.refetch()}
      />
    );
  }

  const run = runQuery.data;
  const failureClass = run.failure?.class ?? "";
  const number = run.pr_url ? prNumber(run.pr_url) : null;
  const panels: Record<RunTab, ReactNode> = {
    transcript: (
      <Transcript
        rows={rows}
        eventCount={stream.events.length}
        status={stream.status}
        latestSeq={latestSeq}
        resumedFrom={stream.resumedFrom}
      />
    ),
    timeline: timeline.isError ? (
      <ErrorState
        title="Could not load the timeline"
        message={messageOf(timeline.error)}
        onRetry={() => void timeline.refetch()}
      />
    ) : timeline.data ? (
      <TimelineTab phases={timeline.data} />
    ) : (
      <p>Loading timeline</p>
    ),
    usage: usage.isError ? (
      <ErrorState
        title="Could not load usage"
        message={messageOf(usage.error)}
        onRetry={() => void usage.refetch()}
      />
    ) : usage.data ? (
      <UsageTab usage={usage.data} />
    ) : (
      <p>Loading usage</p>
    ),
    files: <FilesTab rows={rows} prUrl={run.pr_url} />,
    tree: children.isError ? (
      <ErrorState
        title="Could not load child runs"
        message={messageOf(children.error)}
        onRetry={() => void children.refetch()}
      />
    ) : (
      <TreeTab run={run} children={children.data ?? []} />
    ),
  };

  return (
    <div className="runs-feature">
      <nav className="breadcrumb" aria-label="Breadcrumb">
        <Link to="/runs">Runs</Link>
        <span aria-hidden="true"> / </span>
        <span className="mono" title={run.id}>
          {shortId(run.id)}
        </span>
      </nav>
      <header className="run-header">
        <div className="title-row">
          <div>
            <h1>{run.title || shortId(run.id)}</h1>
            <div className="title-meta">
              <RunStatusBadge status={run.status} />
              {isFailureClass(failureClass) ? <StatusBadge failureClass={failureClass} /> : null}
              <span className="mono">{run.id}</span>
            </div>
          </div>
          <div className="header-actions">
            {run.pr_url ? (
              <Button asChild>
                <a href={run.pr_url} target="_blank" rel="noreferrer">
                  {number ? `Open PR #${number}` : "Open PR"}
                </a>
              </Button>
            ) : null}
            {canCancel(run.status) ? (
              <Button
                id="cancel-run"
                type="button"
                tone="danger"
                onClick={() => setCancelOpen(true)}
              >
                Cancel run
              </Button>
            ) : null}
          </div>
        </div>
        <Caveat>{QUEUE_CAVEAT}</Caveat>
        <dl className="meta-row">
          <div>
            <dt>Agent</dt>
            <dd>{run.agent || "—"}</dd>
          </div>
          <div>
            <dt>Model</dt>
            <dd className="mono">{run.model_id || "—"}</dd>
          </div>
          <div>
            <dt>Stage</dt>
            <dd>{run.stage || "—"}</dd>
          </div>
          <div>
            <dt>Worker</dt>
            <dd className="mono">{workerLabel(run.worker)}</dd>
          </div>
          <div>
            <dt>Work item</dt>
            <dd>
              {run.factory_id && run.work_item_id ? (
                <Link
                  to="/f/$factory/work-items/$id"
                  params={{ factory: run.factory_id, id: run.work_item_id }}
                >
                  {shortId(run.work_item_id)}
                </Link>
              ) : (
                "—"
              )}
            </dd>
          </div>
          <div>
            <dt>Parent</dt>
            <dd>
              {run.parent_run_id ? (
                <Link to="/runs/$runId" params={{ runId: run.parent_run_id }} className="mono">
                  {shortId(run.parent_run_id)}
                </Link>
              ) : (
                "—"
              )}
            </dd>
          </div>
          <div>
            <dt>Children</dt>
            <dd className="mono">{children.data ? children.data.length : "—"}</dd>
          </div>
          <div>
            <dt>Time</dt>
            <dd>
              <DurationPair queueWaitMs={run.queue_wait_ms ?? 0} execMs={run.exec_ms ?? 0} />
            </dd>
          </div>
          <div>
            <dt>Cost</dt>
            <dd className="mono">{formatCost(run.cost_cents)}</dd>
          </div>
        </dl>
      </header>
      <FailurePanel run={run} />
      <div className="detail-layout">
        <RunTabs panels={panels} />
        <SteerPanel
          steer={run.steer ?? { enabled: false, reason: "" }}
          local={localSteers}
          signals={signals}
          text={text}
          sendError={sendError}
          onText={setText}
          pending={steerPending}
          onFollowUp={sendFollowUp}
          onInterrupt={sendInterrupt}
        />
      </div>
      <ConfirmDialog
        open={cancelOpen}
        title="Cancel this run?"
        description="The run stops and ends as cancelled."
        confirmLabel="Cancel run"
        tone="danger"
        onConfirm={() => {
          setCancelOpen(false);
          cancel.mutate();
        }}
        onOpenChange={(open) => {
          setCancelOpen(open);
          if (!open) window.setTimeout(() => document.getElementById("cancel-run")?.focus(), 0);
        }}
      />
    </div>
  );
}
