import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import {
  GitPullRequest,
  MessageCircleQuestion,
  ShieldCheck,
  XCircle,
  type LucideIcon,
} from "lucide-react";
import { useEffect, useState, type KeyboardEvent, type ReactNode } from "react";
import { client } from "../../api/client";
import { liveLabel, useFactoryEvents, type StreamStatus } from "../../api/live";
import type { FailureClass, InboxItem, InboxKind } from "../../api/types";
import { Caveat } from "../../components/Caveat";
import { EmptyState } from "../../components/EmptyState";
import { ErrorState } from "../../components/ErrorState";
import { PageHeader } from "../../components/PageHeader";
import { StatusBadge } from "../../components/StatusBadge";
import { HttpsText } from "../dashboard/https-link";
import {
  failureClassOf,
  formatAge,
  problemDetail,
  unexpected,
  type RunRow,
} from "../dashboard/model";
import { AnswerForm, ApprovalActions, submitAnswer } from "./actions";

type InboxFilter = "all" | InboxKind;

const filters = [
  "all",
  "approval",
  "question",
  "failed_run",
  "improvement_pr",
] as const satisfies readonly InboxFilter[];

export default function InboxPage() {
  const params = useParams({ strict: false });
  const factoryId = typeof params.factory === "string" ? params.factory : "";
  const queryClient = useQueryClient();
  const inbox = useQuery({
    queryKey: ["inbox", factoryId],
    queryFn: () => client.inbox(factoryId),
    enabled: factoryId.length > 0,
  });
  const approvals = useQuery({
    queryKey: ["approvals", factoryId],
    queryFn: () => client.listApprovals(factoryId, true),
    enabled: factoryId.length > 0,
  });
  const runs = useQuery({
    queryKey: ["runs", "inbox", factoryId],
    queryFn: () => client.listRuns({ factory_id: factoryId }) as Promise<RunRow[]>,
    enabled: factoryId.length > 0,
  });
  const [streamStatus, setStreamStatus] = useState<StreamStatus>("connecting");
  const [filter, setFilter] = useState<InboxFilter>("all");
  const [openId, setOpenId] = useState<string | null>(null);
  const [hidden, setHidden] = useState<ReadonlySet<string>>(new Set());
  const [notice, setNotice] = useState<string | null>(null);

  if (inbox.isPending || approvals.isPending || runs.isPending) return <p>Loading inbox</p>;
  if (inbox.isError || approvals.isError) {
    return (
      <ErrorState
        title="Could not load the inbox"
        message="The inbox could not be loaded."
        onRetry={() => {
          void inbox.refetch();
          void approvals.refetch();
        }}
      />
    );
  }

  const items = [...inbox.data].sort(byOldest).filter((item) => !hidden.has(item.id));
  const shown = filter === "all" ? items : items.filter((item) => item.kind === filter);

  async function answer(item: InboxItem, text: string) {
    setHidden((current) => new Set(current).add(item.id));
    setNotice(null);
    try {
      await submitAnswer(queryClient, factoryId, item.work_item_id, text);
    } catch (err) {
      setHidden((current) => {
        const next = new Set(current);
        next.delete(item.id);
        return next;
      });
      setNotice(problemDetail(err));
    }
  }

  return (
    <div className="operator-page">
      <StreamWatch factoryId={factoryId} onStatus={setStreamStatus} />
      <PageHeader
        title="Inbox"
        subtitle={
          <>
            Factory <span className="mono">{factoryId}</span> · Oldest first ·{" "}
            {liveLabel([streamStatus])}
          </>
        }
      />
      {notice ? <p role="alert">{notice}</p> : null}
      <div className="chips" role="group" aria-label="Filter by kind">
        {filters.map((kind) => (
          <FilterChip
            key={kind}
            kind={kind}
            pressed={filter === kind}
            count={
              kind === "all" ? items.length : items.filter((item) => item.kind === kind).length
            }
            onSelect={setFilter}
          />
        ))}
      </div>
      {shown.length === 0 ? (
        <EmptyState
          title="Nothing needs you right now"
          message="New approvals, questions, improvement PRs and failed runs show up here."
        />
      ) : (
        <ul className="inbox-list">
          {shown.map((item) => (
            <InboxRow
              key={item.id}
              item={item}
              factoryId={factoryId}
              open={openId === item.id}
              onToggle={() => setOpenId((current) => (current === item.id ? null : item.id))}
              onClose={() => setOpenId((current) => (current === item.id ? null : current))}
              failureClass={failureClassOf(runs.data?.find((run) => run.id === item.run_id))}
              onProblem={setNotice}
              onAnswer={(text) => answer(item, text)}
            />
          ))}
        </ul>
      )}
      <Caveat>Decisions are optimistic and roll back if someone else decided first.</Caveat>
    </div>
  );
}

export function byOldest(left: InboxItem, right: InboxItem): number {
  const byTime = inboxMillis(left.created_at) - inboxMillis(right.created_at);
  if (byTime !== 0) return byTime;
  return left.id.localeCompare(right.id);
}

function inboxMillis(value: string | undefined): number {
  if (!value) return Number.NEGATIVE_INFINITY;
  const parsed = Date.parse(value);
  return Number.isNaN(parsed) ? Number.NEGATIVE_INFINITY : parsed;
}

function StreamWatch({
  factoryId,
  onStatus,
}: {
  factoryId: string;
  onStatus: (status: StreamStatus) => void;
}) {
  const status = useFactoryEvents(factoryId);
  useEffect(() => {
    onStatus(status);
  }, [onStatus, status]);
  return null;
}

function FilterChip({
  kind,
  pressed,
  count,
  onSelect,
}: {
  kind: InboxFilter;
  pressed: boolean;
  count: number;
  onSelect: (kind: InboxFilter) => void;
}) {
  const label = kind === "all" ? "All" : kindMeta(kind).plural;
  const Icon = kind === "all" ? null : kindMeta(kind).icon;
  return (
    <button type="button" aria-pressed={pressed} onClick={() => onSelect(kind)}>
      {Icon ? <Icon aria-hidden="true" size={16} /> : null} {label}{" "}
      <span className="mono">{count}</span>
    </button>
  );
}

function kindMeta(kind: InboxKind): { label: string; plural: string; icon: LucideIcon } {
  switch (kind) {
    case "approval":
      return { label: "Approval", plural: "Approvals", icon: ShieldCheck };
    case "question":
      return { label: "Question", plural: "Questions", icon: MessageCircleQuestion };
    case "failed_run":
      return { label: "Failed run", plural: "Failed runs", icon: XCircle };
    case "improvement_pr":
      return { label: "Improvement", plural: "Improvements", icon: GitPullRequest };
    default:
      return unexpected(kind);
  }
}

function InboxRow({
  item,
  factoryId,
  open,
  onToggle,
  onClose,
  failureClass,
  onProblem,
  onAnswer,
}: {
  item: InboxItem;
  factoryId: string;
  open: boolean;
  onToggle: () => void;
  onClose: () => void;
  failureClass: FailureClass | undefined;
  onProblem: (detail: string | null) => void;
  onAnswer: (text: string) => Promise<void>;
}) {
  const bodyId = `inbox-body-${item.id}`;

  function onKeyDown(event: KeyboardEvent<HTMLLIElement>) {
    if (event.key !== "Escape" || !open) return;
    event.preventDefault();
    event.stopPropagation();
    onClose();
    event.currentTarget.querySelector("button")?.focus();
  }

  return (
    <li className="inbox-row" onKeyDown={onKeyDown}>
      <button
        type="button"
        className="inbox-toggle"
        aria-expanded={open}
        aria-controls={bodyId}
        onClick={onToggle}
      >
        <KindBadge kind={item.kind} />
        <span>{item.title}</span>
        <span className="muted">{item.detail}</span>
        <time dateTime={item.created_at}>{formatAge(item.created_at)}</time>
      </button>
      {open ? (
        <div className="inbox-body" id={bodyId}>
          {rowActions(item, factoryId, failureClass, onProblem, onAnswer)}
        </div>
      ) : null}
      <p className="inbox-meta">
        {item.work_item_id ? (
          <Link
            to="/f/$factory/work-items/$id"
            params={{ factory: factoryId, id: item.work_item_id }}
          >
            <span className="mono">{item.work_item_id}</span>
          </Link>
        ) : null}
        {item.run_id ? (
          <Link to="/runs/$runId" params={{ runId: item.run_id }}>
            <span className="mono">{item.run_id}</span>
          </Link>
        ) : null}
      </p>
    </li>
  );
}

function rowActions(
  item: InboxItem,
  factoryId: string,
  failureClass: FailureClass | undefined,
  onProblem: (detail: string | null) => void,
  onAnswer: (text: string) => Promise<void>,
): ReactNode {
  switch (item.kind) {
    case "approval":
      return (
        <ApprovalActions
          factoryId={factoryId}
          approvalId={item.id}
          gate={item.detail}
          onProblem={onProblem}
        />
      );
    case "question":
      return <AnswerForm inputId={`answer-${item.id}`} prompt={item.detail} onSubmit={onAnswer} />;
    case "failed_run":
      return (
        <div className="actions">
          {failureClass ? <StatusBadge failureClass={failureClass} /> : null}
          <p>{item.detail}</p>
          {item.run_id ? (
            <Link to="/runs/$runId" params={{ runId: item.run_id }}>
              Open run
            </Link>
          ) : null}
        </div>
      );
    case "improvement_pr":
      return (
        <div className="actions">
          <HttpsText url={item.detail} label="Review PR" />
          {item.run_id ? (
            <Link to="/runs/$runId" params={{ runId: item.run_id }}>
              Open run
            </Link>
          ) : null}
        </div>
      );
    default:
      return unexpected(item.kind);
  }
}

function KindBadge({ kind }: { kind: InboxKind }) {
  const meta = kindMeta(kind);
  const Icon = meta.icon;
  return (
    <span className="kind-badge" data-kind={kind}>
      <Icon aria-hidden="true" size={16} />
      <span>{meta.label}</span>
    </span>
  );
}
