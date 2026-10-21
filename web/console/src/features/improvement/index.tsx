import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import {
  CircleSlash,
  Clock,
  GitMerge,
  GitPullRequest,
  Loader,
  XCircle,
  type LucideIcon,
} from "lucide-react";
import { useRef, useState } from "react";
import { client } from "../../api/client";
import { useFactoryEvents } from "../../api/live";
import type { ImprovementBatch, ImprovementBatchStatus, ImprovementFailure } from "../../api/types";
import { Button } from "../../components/Button";
import { ConfirmDialog } from "../../components/ConfirmDialog";
import { EmptyState } from "../../components/EmptyState";
import { ErrorState } from "../../components/ErrorState";
import { PageHeader } from "../../components/PageHeader";
import { Panel } from "../../components/Panel";
import { unexpected } from "../../lib/unexpected";
import { problemDetail } from "../dashboard/model";

function presentFailure(raw: ImprovementFailure): ImprovementFailure {
  return {
    run_id: raw.run_id,
    result_id: raw.result_id,
    scorer: raw.scorer,
    label: raw.label,
  };
}

function presentBatch(raw: ImprovementBatch): ImprovementBatch {
  return {
    id: raw.id,
    agent: raw.agent,
    status: raw.status,
    failures: raw.failures.map(presentFailure),
    pr_url: raw.pr_url,
    created_at: raw.created_at,
  };
}

function batchStatus(status: ImprovementBatchStatus): { icon: LucideIcon; label: string } {
  switch (status) {
    case "queued":
      return { icon: Clock, label: "Queued" };
    case "running":
      return { icon: Loader, label: "Running" };
    case "pr_open":
      return { icon: GitPullRequest, label: "PR open" };
    case "merged":
      return { icon: GitMerge, label: "Merged" };
    case "closed":
      return { icon: CircleSlash, label: "Closed" };
    case "failed":
      return { icon: XCircle, label: "Failed" };
    default:
      return unexpected(status);
  }
}

function Mark({ icon: Icon, label }: { icon: LucideIcon; label: string }) {
  return (
    <span className="inline-flex items-center gap-1 whitespace-nowrap">
      <Icon aria-hidden="true" size={16} />
      <span>{label}</span>
    </span>
  );
}

function httpsURL(value: string): { href: string; text: string } | null {
  const text = value.trim();
  if (text === "") return null;
  try {
    const url = new URL(text);
    if (url.protocol !== "https:") return null;
    return { href: url.href, text };
  } catch {
    return null;
  }
}

function PullRequest({ url }: { url: string }) {
  const https = httpsURL(url);
  if (https) {
    return (
      <a href={https.href} className="mono break-all">
        {https.text}
      </a>
    );
  }
  if (url.trim() === "") return <span className="muted">No pull request</span>;
  return <span className="mono break-all">{url}</span>;
}

function BatchTable({ batches }: { batches: readonly ImprovementBatch[] }) {
  return (
    <div className="min-w-0">
      <Panel title="Batches">
        <div className="table-scroll" tabIndex={0}>
          <table>
            <caption className="sr-only">Improvement batches</caption>
            <thead className="data-head">
              <tr>
                <th scope="col">Batch</th>
                <th scope="col">Status</th>
                <th scope="col">Agent</th>
                <th scope="col">Created</th>
                <th scope="col">Pull request</th>
              </tr>
            </thead>
            <tbody>
              {batches.map((batch) => {
                const status = batchStatus(batch.status);
                return (
                  <tr key={batch.id}>
                    <td className="mono">{batch.id}</td>
                    <td>
                      <Mark icon={status.icon} label={status.label} />
                    </td>
                    <td>{batch.agent}</td>
                    <td>
                      <time dateTime={batch.created_at} className="mono">
                        {batch.created_at}
                      </time>
                    </td>
                    <td>
                      <PullRequest url={batch.pr_url} />
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </Panel>
    </div>
  );
}

function Regressions({ batches }: { batches: readonly ImprovementBatch[] }) {
  return (
    <div className="min-w-0">
      <Panel title="Regressions addressed">
        {batches.map((batch) => (
          <div key={batch.id}>
            <h4 className="mono">{batch.id}</h4>
            {batch.failures.length === 0 ? (
              <p className="muted">No failures recorded</p>
            ) : (
              <div className="table-scroll" tabIndex={0}>
                <table>
                  <caption className="sr-only">Regressions for {batch.id}</caption>
                  <thead className="data-head">
                    <tr>
                      <th scope="col">Run</th>
                      <th scope="col">Scorer</th>
                      <th scope="col">Label</th>
                    </tr>
                  </thead>
                  <tbody>
                    {batch.failures.map((failure) => (
                      <tr key={failure.result_id}>
                        <td>
                          <Link
                            to="/runs/$runId"
                            params={{ runId: failure.run_id }}
                            className="mono"
                          >
                            {failure.run_id}
                          </Link>
                        </td>
                        <td>{failure.scorer}</td>
                        <td>{failure.label}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        ))}
      </Panel>
    </div>
  );
}

function RunNow({ factoryId }: { factoryId: string }) {
  const queryClient = useQueryClient();
  const lock = useRef(false);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [runError, setRunError] = useState<string | null>(null);
  const [checked, setChecked] = useState<string[] | null>(null);

  async function confirm() {
    if (lock.current) return;
    lock.current = true;
    setRunError(null);
    setChecked(null);
    try {
      const accepted = await client.runImprovementNow(factoryId);
      setChecked(accepted.checked_agents);
      setConfirmOpen(false);
      await queryClient.invalidateQueries({ queryKey: ["batches", factoryId] });
    } catch (error) {
      setRunError(problemDetail(error));
      setConfirmOpen(false);
    } finally {
      lock.current = false;
    }
  }

  return (
    <>
      <Button type="button" onClick={() => setConfirmOpen(true)}>
        Run now
      </Button>
      {runError ? (
        <p role="alert" className="basis-full text-danger">
          {runError}
        </p>
      ) : null}
      {checked ? (
        <div role="status" className="basis-full">
          <p>Checked agents</p>
          {checked.length === 0 ? (
            <p className="muted">No agents checked</p>
          ) : (
            <ul>
              {checked.map((agent, index) => (
                <li key={`${agent}-${index}`}>{agent}</li>
              ))}
            </ul>
          )}
        </div>
      ) : null}
      <ConfirmDialog
        open={confirmOpen}
        title="Run now?"
        description="Run the improvement check for this factory now."
        confirmLabel="Run now"
        onConfirm={() => void confirm()}
        onOpenChange={(open) => {
          if (!open) setConfirmOpen(false);
        }}
      />
    </>
  );
}

function ImprovementScreen({ factoryId }: { factoryId: string }) {
  useFactoryEvents(factoryId);
  const batches = useQuery({
    queryKey: ["batches", factoryId],
    queryFn: async () => (await client.improvementBatches(factoryId)).map(presentBatch),
  });

  if (batches.isPending) return <p>Loading improvement</p>;
  if (batches.isError) {
    return (
      <ErrorState
        title="Could not load improvement batches"
        message={problemDetail(batches.error)}
        onRetry={() => void batches.refetch()}
      />
    );
  }

  return (
    <div className="operator-page">
      <PageHeader title="Improvement" actions={<RunNow factoryId={factoryId} />} />
      {batches.data.length === 0 ? (
        <EmptyState title="No batches" message="This factory has no improvement batches." />
      ) : (
        <>
          <BatchTable batches={batches.data} />
          <Regressions batches={batches.data} />
        </>
      )}
    </div>
  );
}

export default function ImprovementPage() {
  const params = useParams({ strict: false });
  const factoryId = typeof params.factory === "string" ? params.factory : "";
  if (!factoryId) {
    return <ErrorState title="Missing factory" message="Open improvement from a factory." />;
  }
  return <ImprovementScreen factoryId={factoryId} />;
}
