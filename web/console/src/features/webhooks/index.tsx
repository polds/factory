import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import {
  CheckCircle2,
  CircleSlash,
  Copy,
  GitPullRequest,
  Info,
  Lock,
  MessageSquare,
  SquareKanban,
  Webhook,
} from "lucide-react";
import { useState } from "react";
import { client } from "../../api/client";
import type { Webhook as WebhookRecord } from "../../api/types";
import { Button } from "../../components/Button";
import { EmptyState } from "../../components/EmptyState";
import { ErrorState } from "../../components/ErrorState";
import { listDeliveriesDetailed, type DeliveryRow } from "../deliveries/api";
import { formatWhen } from "../deliveries/format";
import { DeliveryStatus } from "../deliveries/status";

type WebhookRow = {
  uid: string;
  name: string;
  ingress_path: string;
  auth_mode: string;
  signature_scheme: string;
  enabled: boolean;
  last_delivery_at: string | null;
};

function present(raw: WebhookRecord): WebhookRow {
  return {
    uid: raw.uid,
    name: raw.name,
    ingress_path: raw.ingress_path,
    auth_mode: raw.auth_mode,
    signature_scheme: raw.signature_scheme,
    enabled: raw.enabled,
    last_delivery_at: raw.last_delivery_at,
  };
}

function providerMark(name: string) {
  const head = name.toLowerCase();
  const className = "size-4";
  if (head.includes("github")) return <GitPullRequest className={className} aria-hidden />;
  if (head.includes("slack")) return <MessageSquare className={className} aria-hidden />;
  if (head.includes("linear")) return <SquareKanban className={className} aria-hidden />;
  return <Webhook className={className} aria-hidden />;
}

function ingressURL(path: string): string {
  if (path.startsWith("http://") || path.startsWith("https://")) return path;
  return path.startsWith("/")
    ? `${window.location.origin}${path}`
    : `${window.location.origin}/${path}`;
}

function WebhooksScreen({ factoryId }: { factoryId: string }) {
  const query = useQuery({
    queryKey: ["webhooks", factoryId],
    queryFn: async () => (await client.listWebhooks(factoryId)).map(present),
  });
  const [openUid, setOpenUid] = useState<string | null>(null);
  const deliveries = useQuery({
    queryKey: ["webhook-deliveries", factoryId],
    queryFn: () => listDeliveriesDetailed(factoryId, { limit: 50 }),
    enabled: openUid !== null,
  });

  if (query.isPending) return <p>Loading webhooks</p>;
  if (query.isError) {
    return (
      <ErrorState
        title="Could not load webhooks"
        message="The webhook list could not be loaded."
        onRetry={() => void query.refetch()}
      />
    );
  }

  const rows = query.data;
  return (
    <div>
      <header className="mb-4">
        <h1 className="text-xl font-semibold">Webhooks</h1>
        <p className="text-sm text-[var(--muted-foreground)]">
          Factory <span className="font-mono">{factoryId}</span> · {rows.length} registrations
        </p>
      </header>
      <p className="mb-3 flex items-center gap-1.5 border border-dashed border-[var(--border)] bg-[var(--card)] px-3 py-2 text-sm">
        <Lock className="size-4 shrink-0" aria-hidden />
        Immutable: delete and recreate the webhook to change this
      </p>
      {rows.length === 0 ? (
        <EmptyState
          title="No webhooks registered"
          message="This factory has no webhook registrations."
        />
      ) : (
        <ul className="space-y-2">
          {rows.map((row) => (
            <li key={row.uid} className="border border-[var(--border)] bg-[var(--card)]">
              <WebhookSummary
                row={row}
                open={openUid === row.uid}
                onToggle={() => setOpenUid((current) => (current === row.uid ? null : row.uid))}
              />
              {openUid === row.uid ? (
                <WebhookDeliveries
                  factoryId={factoryId}
                  webhook={row}
                  loading={deliveries.isPending}
                  error={deliveries.isError}
                  rows={(deliveries.data ?? []).filter(
                    (delivery) => delivery.webhook_uid === row.uid,
                  )}
                  onRetry={() => void deliveries.refetch()}
                />
              ) : null}
            </li>
          ))}
        </ul>
      )}
      <p className="mt-2 flex items-start gap-1.5 text-sm text-[var(--muted-foreground)]">
        <Info className="mt-0.5 size-4 shrink-0" aria-hidden />
        <span>Signing secrets are stored as factory secrets and are never displayed here.</span>
      </p>
    </div>
  );
}

function WebhookSummary({
  row,
  open,
  onToggle,
}: {
  row: WebhookRow;
  open: boolean;
  onToggle: () => void;
}) {
  const url = ingressURL(row.ingress_path);
  const [copied, setCopied] = useState(false);
  return (
    <div className="flex flex-wrap items-center gap-3 px-3 py-2">
      <button
        type="button"
        className="inline-flex items-center gap-2 text-left font-semibold"
        aria-expanded={open}
        aria-controls={`webhook-${row.uid}`}
        onClick={onToggle}
      >
        {providerMark(row.name)}
        {row.name}
      </button>
      <span className="inline-flex min-w-0 items-center gap-1 font-mono text-xs">
        <span className="truncate">{url}</span>
        <Button
          type="button"
          aria-label={`Copy ingress URL for ${row.name}`}
          onClick={() => {
            void navigator.clipboard?.writeText(url).then(
              () => setCopied(true),
              () => setCopied(false),
            );
          }}
        >
          <Copy className="size-3.5" aria-hidden />
          <span className="sr-only">Copy</span>
        </Button>
      </span>
      {copied ? (
        <span role="status" className="text-xs">
          Copied
        </span>
      ) : null}
      <span className="inline-flex items-center gap-1 text-sm">
        <Lock className="size-3.5" aria-hidden />
        <span className="font-mono text-xs">{row.auth_mode}</span>
        <span className="font-mono text-xs">{row.signature_scheme}</span>
      </span>
      {row.enabled ? (
        <span className="inline-flex items-center gap-1 text-sm">
          <CheckCircle2 className="size-4" aria-hidden /> Enabled
        </span>
      ) : (
        <span className="inline-flex items-center gap-1 text-sm text-[var(--muted-foreground)]">
          <CircleSlash className="size-4" aria-hidden /> Disabled
        </span>
      )}
      <span className="ml-auto font-mono text-xs text-[var(--muted-foreground)]">
        {row.last_delivery_at ? (
          <time dateTime={row.last_delivery_at}>{formatWhen(row.last_delivery_at)}</time>
        ) : (
          "—"
        )}
      </span>
    </div>
  );
}

function WebhookDeliveries({
  factoryId,
  webhook,
  rows,
  loading,
  error,
  onRetry,
}: {
  factoryId: string;
  webhook: WebhookRow;
  rows: readonly DeliveryRow[];
  loading: boolean;
  error: boolean;
  onRetry: () => void;
}) {
  return (
    <div id={`webhook-${webhook.uid}`} className="border-t border-[var(--border)] px-3 py-2">
      <div className="mb-2 flex items-center justify-between gap-2">
        <h2 className="text-sm font-semibold">Recent deliveries</h2>
        <Link
          to="/f/$factory/deliveries"
          params={{ factory: factoryId }}
          className="text-sm text-[var(--ring)] hover:underline"
        >
          All deliveries
        </Link>
      </div>
      {loading ? <p>Loading deliveries</p> : null}
      {error ? (
        <ErrorState
          title="Could not load deliveries"
          message="Recent deliveries could not be loaded."
          onRetry={onRetry}
        />
      ) : null}
      {!loading && !error && rows.length === 0 ? (
        <p className="text-sm text-[var(--muted-foreground)]">No deliveries for this webhook.</p>
      ) : null}
      {rows.length > 0 ? (
        <ul className="space-y-1">
          {rows.map((row) => (
            <li key={row.id} className="flex items-center gap-3 text-sm">
              <DeliveryStatus status={row.status} />
              <span className="font-mono text-xs">{row.delivery_id || row.id}</span>
              <time className="font-mono text-xs" dateTime={row.received_at}>
                {formatWhen(row.received_at)}
              </time>
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}

export default function WebhooksPage() {
  const params = useParams({ strict: false });
  const factoryId = typeof params.factory === "string" ? params.factory : "";
  if (!factoryId)
    return <ErrorState title="Missing factory" message="Open webhooks from a factory." />;
  return <WebhooksScreen factoryId={factoryId} />;
}
