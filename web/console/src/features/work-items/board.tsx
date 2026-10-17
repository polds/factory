import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import * as Dialog from "@radix-ui/react-dialog";
import { Plus } from "lucide-react";
import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { client } from "../../api/client";
import type { Task } from "../../api/types";
import { Button } from "../../components/Button";
import { EmptyState } from "../../components/EmptyState";
import { ErrorState } from "../../components/ErrorState";
import { ensureStreamFallback } from "../dashboard/testing/ensure";
import { liveLabel, useFactoryEvents, type StreamStatus } from "../dashboard/live";
import {
  PIPELINE_STAGES,
  formatAge,
  isDoneStatus,
  isPipelineStage,
  latestByWorkItem,
  problemDetail,
  stageLabel,
  type RunRow,
} from "../dashboard/model";
import { GateBadge, PageHeader } from "../dashboard/ui";

const sources = ["cli", "github", "slack", "linear", "schedule", "issue", "manual", "api"] as const;

export function WorkItemBoard({ factoryId }: { factoryId: string }) {
  ensureStreamFallback();
  const tasks = useQuery({
    queryKey: ["tasks", factoryId],
    queryFn: () => client.listTasks(factoryId),
  });
  const runs = useQuery({
    queryKey: ["runs", "board", factoryId],
    queryFn: () => client.listRuns() as Promise<RunRow[]>,
  });
  const [streamStatus, setStreamStatus] = useState<StreamStatus>("connecting");
  const [query, setQuery] = useState("");
  const [source, setSource] = useState("");

  if (tasks.isPending || runs.isPending) return <p>Loading work items</p>;
  if (tasks.isError) {
    return (
      <ErrorState
        title="Could not load work items"
        message="The work item list could not be loaded."
        onRetry={() => void tasks.refetch()}
      />
    );
  }

  const latest = latestByWorkItem(runs.data ?? []);
  const needle = query.trim().toLowerCase();
  const visible = tasks.data.filter((task) => {
    if (source && task.source !== source) return false;
    if (!needle) return true;
    return `${task.title} ${task.source_ref ?? ""}`.toLowerCase().includes(needle);
  });
  const open = visible.filter((task) => !isDoneStatus(task.status));
  const done = visible.filter((task) => isDoneStatus(task.status));

  return (
    <div className="operator-page">
      <StreamWatch factoryId={factoryId} onStatus={setStreamStatus} />
      <PageHeader
        title="Work items"
        subtitle={
          <>
            Factory <span className="mono">{factoryId}</span>
          </>
        }
        actions={<NewWorkItem factoryId={factoryId} />}
      />
      <div className="toolbar">
        <label>
          <span className="sr-only">Search title or ref</span>
          <input
            type="search"
            placeholder="Search title or ref"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
          />
        </label>
        <label>
          Source{" "}
          <select value={source} onChange={(event) => setSource(event.target.value)}>
            <option value="">All sources</option>
            {sources.map((item) => (
              <option key={item} value={item}>
                {item}
              </option>
            ))}
          </select>
        </label>
        <p className="muted live-status">{liveLabel([streamStatus])} via event stream</p>
      </div>
      <div className="board">
        {PIPELINE_STAGES.map((stage) => {
          const cards = open.filter((task) => task.stage === stage);
          return (
            <section key={stage} className="column" aria-labelledby={`col-${stage}`}>
              <h3 id={`col-${stage}`}>
                {stageLabel(stage)} <span className="mono column-count">{cards.length}</span>
              </h3>
              {cards.length === 0 ? (
                <p className="muted">Nothing here</p>
              ) : (
                <ul>
                  {cards.map((task) => (
                    <li key={task.id}>
                      <WorkCard factoryId={factoryId} task={task} run={latest.get(task.id)} />
                    </li>
                  ))}
                </ul>
              )}
            </section>
          );
        })}
        <section className="column column-done" aria-labelledby="col-done">
          <h3 id="col-done">
            Done <span className="mono column-count">{done.length}</span>
          </h3>
          {done.length === 0 ? (
            <p className="muted">Nothing here</p>
          ) : (
            <ul>
              {done.map((task) => (
                <li key={task.id}>
                  <WorkCard factoryId={factoryId} task={task} run={latest.get(task.id)} />
                </li>
              ))}
            </ul>
          )}
        </section>
      </div>
      {visible.length === 0 ? (
        <EmptyState title="No work items match" message="Clear the search or source filter." />
      ) : null}
      <OtherStages tasks={open} factoryId={factoryId} latest={latest} />
    </div>
  );
}

function OtherStages({
  tasks,
  factoryId,
  latest,
}: {
  tasks: readonly Task[];
  factoryId: string;
  latest: Map<string, RunRow>;
}) {
  const extras = tasks.filter((task) => !isPipelineStage(task.stage));
  if (extras.length === 0) return null;
  return (
    <section className="panel" aria-labelledby="col-other">
      <h3 id="col-other">Other stages</h3>
      <ul>
        {extras.map((task) => (
          <li key={task.id}>
            <WorkCard factoryId={factoryId} task={task} run={latest.get(task.id)} />
          </li>
        ))}
      </ul>
    </section>
  );
}

function WorkCard({
  factoryId,
  task,
  run,
}: {
  factoryId: string;
  task: Task;
  run: RunRow | undefined;
}) {
  const waiting =
    task.status === "awaiting_approval" || task.status === "awaiting_input" ? task.status : null;
  const who = [run?.agent, run?.model_id].filter(Boolean).join(" · ");
  return (
    <Link
      className="work-card"
      to="/f/$factory/work-items/$id"
      params={{ factory: factoryId, id: task.id }}
      data-waiting={waiting ?? undefined}
      data-icon={
        waiting === "awaiting_approval"
          ? "shield-question"
          : waiting === "awaiting_input"
            ? "message-circle-question"
            : undefined
      }
      style={
        waiting
          ? { borderStyle: waiting === "awaiting_input" ? "dashed" : "solid", borderWidth: "2px" }
          : undefined
      }
    >
      <span>{task.title}</span>
      <span className="muted">
        {task.source}
        {task.source_ref ? <span className="mono"> {task.source_ref}</span> : null}
      </span>
      <time dateTime={task.created_at}>{formatAge(task.created_at)}</time>
      {who ? <span className="muted">{who}</span> : null}
      {task.revision_count > 0 ? <span className="mono">rev {task.revision_count}</span> : null}
      {waiting ? <GateBadge status={waiting} /> : null}
    </Link>
  );
}

function NewWorkItem({ factoryId }: { factoryId: string }) {
  const queryClient = useQueryClient();
  const titleRef = useRef<HTMLInputElement>(null);
  const [open, setOpen] = useState(false);
  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const [source, setSource] = useState<(typeof sources)[number]>("cli");
  const [sourceRef, setSourceRef] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);
  const knownSources = useMemo(() => sources, []);

  function reset() {
    setTitle("");
    setBody("");
    setSource("cli");
    setSourceRef("");
    setError(null);
  }

  async function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!title.trim() || pending) {
      setError("Title is required");
      titleRef.current?.focus();
      return;
    }
    setPending(true);
    setError(null);
    try {
      const created = await client.createTask(factoryId, {
        title,
        body,
        source,
        source_ref: sourceRef,
      });
      queryClient.setQueryData<Task[]>(["tasks", factoryId], (current) => {
        const list = current ?? [];
        if (list.some((task) => task.id === created.id)) return list;
        return [...list, created];
      });
      setOpen(false);
      reset();
    } catch (err) {
      setError(problemDetail(err));
    } finally {
      setPending(false);
    }
  }

  return (
    <Dialog.Root
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) reset();
      }}
    >
      <Dialog.Trigger asChild>
        <Button type="button" className="button-primary">
          <Plus aria-hidden="true" size={16} /> New work item
        </Button>
      </Dialog.Trigger>
      <Dialog.Portal>
        <Dialog.Overlay className="dialog-overlay" />
        <Dialog.Content
          className="dialog-content"
          onOpenAutoFocus={(event) => {
            event.preventDefault();
            titleRef.current?.focus();
          }}
        >
          <Dialog.Title>New work item</Dialog.Title>
          <Dialog.Description>Enters the pipeline at Triage.</Dialog.Description>
          <form className="dialog-form" onSubmit={(event) => void onSubmit(event)}>
            <label className="field" htmlFor="work-item-title">
              Title
              <input
                id="work-item-title"
                ref={titleRef}
                value={title}
                required
                onChange={(event) => setTitle(event.target.value)}
              />
            </label>
            <label className="field" htmlFor="work-item-body">
              Body
              <textarea
                id="work-item-body"
                value={body}
                onChange={(event) => setBody(event.target.value)}
              />
            </label>
            <div className="form-grid">
              <label className="field" htmlFor="work-item-source">
                Source
                <select
                  id="work-item-source"
                  value={source}
                  onChange={(event) => {
                    const next = event.target.value;
                    if (isSource(next)) setSource(next);
                  }}
                >
                  {knownSources.map((item) => (
                    <option key={item} value={item}>
                      {item}
                    </option>
                  ))}
                </select>
              </label>
              <label className="field" htmlFor="work-item-ref">
                Source ref
                <input
                  id="work-item-ref"
                  value={sourceRef}
                  onChange={(event) => setSourceRef(event.target.value)}
                />
              </label>
            </div>
            {error ? <p role="alert">{error}</p> : null}
            <div className="dialog-actions">
              <Dialog.Close asChild>
                <Button type="button">Cancel</Button>
              </Dialog.Close>
              <Button type="submit" className="button-primary" disabled={pending}>
                Create
              </Button>
            </div>
          </form>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

function isSource(value: string): value is (typeof sources)[number] {
  return (sources as readonly string[]).includes(value);
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
