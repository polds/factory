import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { CheckCircle2, Circle, CircleDot, type LucideIcon } from "lucide-react";
import { useState } from "react";
import { client } from "../../api/client";
import type { Approval, Task } from "../../api/types";
import { Button } from "../../components/Button";
import { ConfirmDialog } from "../../components/ConfirmDialog";
import { ErrorState } from "../../components/ErrorState";
import { useFactoryEvents } from "../dashboard/live";
import {
  PIPELINE_STAGES,
  latestByWorkItem,
  problemDetail,
  stageLabel,
  unexpected,
  type RunRow,
} from "../dashboard/model";
import { GateBadge, PageHeader, Panel, RunsTable } from "../dashboard/ui";
import { ApprovalActions, AnswerForm, submitAnswer } from "../inbox/actions";

export function WorkItemDetail({ factoryId, id }: { factoryId: string; id: string }) {
  const queryClient = useQueryClient();
  const task = useQuery({
    queryKey: ["task", factoryId, id],
    queryFn: () => client.getTask(factoryId, id),
  });
  const runs = useQuery({
    queryKey: ["runs", "work-item", id],
    queryFn: () => client.listRuns({ work_item_id: id }) as Promise<RunRow[]>,
  });
  const approvals = useQuery({
    queryKey: ["approvals", factoryId],
    queryFn: () => client.listApprovals(factoryId, true),
  });
  const inbox = useQuery({
    queryKey: ["inbox", factoryId],
    queryFn: () => client.inbox(factoryId),
  });
  const latest = latestByWorkItem(runs.data ?? []).get(id);
  const detail = useQuery({
    queryKey: ["run", latest?.id ?? "none"],
    enabled: Boolean(latest?.id),
    queryFn: () => client.getRun(latest?.id ?? ""),
  });
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [cancelError, setCancelError] = useState<string | null>(null);
  const [cancelling, setCancelling] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);

  if (task.isPending || runs.isPending || approvals.isPending || inbox.isPending) {
    return <p>Loading work item</p>;
  }
  if (task.isError) {
    return (
      <ErrorState
        title="Could not load the work item"
        message="The work item could not be loaded."
        onRetry={() => void task.refetch()}
      />
    );
  }

  const inboxItems = inbox.data ?? [];
  const runRows = runs.data ?? [];
  const approval = matchingApproval(approvals.data ?? [], task.data);
  const question = inboxItems.find(
    (item) => item.kind === "question" && item.work_item_id === task.data.id,
  );
  const waiting =
    task.data.status === "awaiting_approval" || task.data.status === "awaiting_input"
      ? task.data.status
      : null;

  async function onCancel() {
    if (cancelling) return;
    setCancelling(true);
    setCancelError(null);
    try {
      const updated = await client.cancelTask(factoryId, id);
      queryClient.setQueryData(["task", factoryId, id], updated);
      setConfirmOpen(false);
    } catch (err) {
      setCancelError(problemDetail(err));
    } finally {
      setCancelling(false);
    }
  }

  return (
    <div className="operator-page">
      <StreamWatch factoryId={factoryId} />
      <nav aria-label="Breadcrumb">
        <Link to="/f/$factory/work-items" params={{ factory: factoryId }}>
          Work items
        </Link>
        <span className="muted"> / </span>
        <span className="mono">{task.data.id}</span>
      </nav>
      {notice ? <p role="alert">{notice}</p> : null}
      <PageHeader
        title={task.data.title}
        subtitle={
          <span>
            <span className="mono">{task.data.id}</span>
            {" · "}
            {task.data.source}
            {task.data.source_ref ? <span className="mono"> {task.data.source_ref}</span> : null}
            {" · Stage: "}
            <strong>{stageLabel(task.data.stage)}</strong>
          </span>
        }
        actions={
          <Button type="button" tone="danger" onClick={() => setConfirmOpen(true)}>
            Cancel work item
          </Button>
        }
      />
      {waiting ? <GateBadge status={waiting} /> : null}
      <Panel title="Stage history">
        <ol className="stepper">
          {PIPELINE_STAGES.map((stage, index) => {
            const state = stepState(task.data.stage, task.data.status, index);
            const Icon = stepIcon(state);
            return (
              <li
                key={stage}
                className="step"
                data-state={state}
                aria-current={state === "current" ? "step" : undefined}
              >
                <span className="step-label">
                  <Icon aria-hidden="true" size={16} />
                  {stageLabel(stage)}
                </span>
                <span className="muted">{stepText(state)}</span>
              </li>
            );
          })}
        </ol>
      </Panel>
      {waiting === "awaiting_approval" && approval ? (
        <section
          className="callout"
          aria-labelledby="gate-heading"
          style={{ borderStyle: "solid" }}
        >
          <h3 id="gate-heading">Approval</h3>
          <ApprovalActions
            factoryId={factoryId}
            approvalId={approval.id}
            gate={approval.gate || task.data.pending_gate || ""}
            onProblem={setNotice}
          />
          <Link to="/f/$factory/inbox" params={{ factory: factoryId }}>
            Open in Inbox
          </Link>
        </section>
      ) : null}
      {waiting === "awaiting_input" ? (
        <AnswerCallout
          factoryId={factoryId}
          workItemId={task.data.id}
          prompt={
            question?.detail || task.data.pending_gate || "The agent is waiting for an answer."
          }
          onProblem={setNotice}
        />
      ) : null}
      {task.data.body ? <p>{task.data.body}</p> : null}
      <div className="detail-grid">
        <Panel title={`Runs (${runRows.length})`}>
          {runRows.length === 0 ? (
            <p className="muted">No runs yet.</p>
          ) : (
            <RunsTable runs={runRows} />
          )}
        </Panel>
        <Panel title="Pull request">
          {detail.data?.pr_url ? (
            <a href={detail.data.pr_url}>{detail.data.pr_url}</a>
          ) : (
            <p className="muted">No PR recorded yet. Builders open one during Building.</p>
          )}
          <p>
            {latest?.agent ? <span>{latest.agent}</span> : null}
            {latest?.model_id ? <span className="mono"> · {latest.model_id}</span> : null}
          </p>
          <p className="mono">rev {task.data.revision_count}</p>
        </Panel>
      </div>
      {cancelError ? <p role="alert">{cancelError}</p> : null}
      <ConfirmDialog
        open={confirmOpen}
        title="Cancel this work item?"
        description="Active runs stop and the work item leaves the pipeline."
        confirmLabel="Cancel work item"
        cancelLabel="Keep work item"
        tone="danger"
        onConfirm={() => void onCancel()}
        onOpenChange={setConfirmOpen}
      />
    </div>
  );
}

function AnswerCallout({
  factoryId,
  workItemId,
  prompt,
  onProblem,
}: {
  factoryId: string;
  workItemId: string;
  prompt: string;
  onProblem: (detail: string | null) => void;
}) {
  const queryClient = useQueryClient();
  const [sent, setSent] = useState(false);

  async function onSubmit(text: string) {
    setSent(true);
    onProblem(null);
    try {
      await submitAnswer(queryClient, factoryId, workItemId, text);
    } catch (err) {
      setSent(false);
      onProblem(problemDetail(err));
    }
  }

  return (
    <section className="callout" aria-labelledby="answer-heading" style={{ borderStyle: "dashed" }}>
      <h3 id="answer-heading">Question</h3>
      {sent ? <p role="status">Answer sent</p> : null}
      {sent ? null : (
        <AnswerForm inputId={`detail-answer-${workItemId}`} prompt={prompt} onSubmit={onSubmit} />
      )}
      <Link to="/f/$factory/inbox" params={{ factory: factoryId }}>
        Open in Inbox
      </Link>
    </section>
  );
}

function matchingApproval(approvals: readonly Approval[], task: Task): Approval | undefined {
  const pending = approvals.filter((item) => item.work_item_id === task.id && !item.decision);
  return pending.find((item) => item.gate === task.pending_gate) ?? pending[0];
}

function stepState(stage: string, status: string, index: number): "done" | "current" | "pending" {
  if (status === "complete" || status === "cancelled") return "done";
  const current = PIPELINE_STAGES.indexOf(stage as (typeof PIPELINE_STAGES)[number]);
  if (current < 0) return "pending";
  if (index < current) return "done";
  if (index === current) return "current";
  return "pending";
}

function stepIcon(state: "done" | "current" | "pending"): LucideIcon {
  switch (state) {
    case "done":
      return CheckCircle2;
    case "current":
      return CircleDot;
    case "pending":
      return Circle;
    default:
      return unexpected(state);
  }
}

function stepText(state: "done" | "current" | "pending"): string {
  switch (state) {
    case "done":
      return "Done";
    case "current":
      return "Current";
    case "pending":
      return "Pending";
    default:
      return unexpected(state);
  }
}

function StreamWatch({ factoryId }: { factoryId: string }) {
  useFactoryEvents(factoryId);
  return null;
}
