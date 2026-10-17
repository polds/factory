import * as Dialog from "@radix-ui/react-dialog";
import { useQuery } from "@tanstack/react-query";
import { useParams } from "@tanstack/react-router";
import {
  CalendarClock,
  GitPullRequest,
  Info,
  MessageSquare,
  Scissors,
  SquareKanban,
  Webhook,
} from "lucide-react";
import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import { client } from "../../api/client";
import { Button } from "../../components/Button";
import { EmptyState } from "../../components/EmptyState";
import { ErrorState } from "../../components/ErrorState";
import { listDeliveriesDetailed, type DeliveryRow } from "./api";
import { DeliveryDetail, Replayed } from "./detail";
import { formatWhen } from "./format";
import "./sheet.css";
import { DeliveryStatus } from "./status";

type Tab = "unmatched" | "all";

function providerMark(provider: string) {
  const head = provider.toLowerCase();
  const className = "size-4";
  if (head === "github") return <GitPullRequest className={className} aria-hidden />;
  if (head === "slack") return <MessageSquare className={className} aria-hidden />;
  if (head === "linear") return <SquareKanban className={className} aria-hidden />;
  if (head === "cron") return <CalendarClock className={className} aria-hidden />;
  return <Webhook className={className} aria-hidden />;
}

function providerLabel(provider: string): string {
  if (provider === "github") return "GitHub";
  if (provider === "slack") return "Slack";
  if (provider === "linear") return "Linear";
  if (provider === "custom") return "Custom";
  return provider;
}

function useWide(minPx: number): boolean {
  const query = `(min-width: ${minPx}px)`;
  const [wide, setWide] = useState(() => window.matchMedia?.(query).matches ?? false);
  useEffect(() => {
    const media = window.matchMedia?.(query);
    if (!media) return;
    const onChange = () => setWide(media.matches);
    onChange();
    media.addEventListener("change", onChange);
    return () => media.removeEventListener("change", onChange);
  }, [query]);
  return wide;
}

function DeliveriesScreen({ factoryId }: { factoryId: string }) {
  const [tab, setTab] = useState<Tab>("unmatched");
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const openerRef = useRef<HTMLElement | null>(null);
  const [replayed, setReplayed] = useState<ReadonlySet<string>>(() => new Set());
  const wide = useWide(1500);
  const unmatched = useQuery({
    queryKey: ["deliveries", factoryId, "unmatched"],
    queryFn: () => listDeliveriesDetailed(factoryId, { status: "unmatched" }),
  });
  const all = useQuery({
    queryKey: ["deliveries", factoryId, "all"],
    queryFn: () => listDeliveriesDetailed(factoryId, {}),
  });
  const names = useQuery({
    queryKey: ["webhooks", factoryId],
    queryFn: () => client.listWebhooks(factoryId),
  });

  const active = tab === "unmatched" ? unmatched : all;
  const rows = active.data ?? [];
  const selected =
    [...(unmatched.data ?? []), ...(all.data ?? [])].find((row) => row.id === selectedId) ?? null;
  const inline = Boolean(selected) && wide;
  const webhookName = new Map((names.data ?? []).map((webhook) => [webhook.uid, webhook.name]));

  function onTabKey(event: KeyboardEvent<HTMLDivElement>) {
    let resolved: Tab | null = null;
    if (event.key === "Home") resolved = "unmatched";
    else if (event.key === "End") resolved = "all";
    else if (event.key === "ArrowRight" || event.key === "ArrowLeft") {
      resolved = tab === "unmatched" ? "all" : "unmatched";
    }
    if (!resolved) return;
    event.preventDefault();
    setTab(resolved);
    queueMicrotask(() => document.getElementById(`deliveries-tab-${resolved}`)?.focus());
  }

  return (
    <div>
      <header className="mb-4">
        <h1 className="text-xl font-semibold">Deliveries</h1>
        <p className="text-sm text-[var(--muted-foreground)]">
          Factory <span className="font-mono">{factoryId}</span>
        </p>
      </header>
      <div
        role="tablist"
        aria-label="Delivery filter"
        className="mb-3 flex gap-1 border-b border-[var(--border)]"
        onKeyDown={onTabKey}
      >
        <TabButton
          id="unmatched"
          selected={tab === "unmatched"}
          label={`Unmatched (${unmatched.data?.length ?? "…"})`}
          controls="deliveries-panel"
          onSelect={() => setTab("unmatched")}
        />
        <TabButton
          id="all"
          selected={tab === "all"}
          label={`All (${all.data?.length ?? "…"})`}
          controls="deliveries-panel"
          onSelect={() => setTab("all")}
        />
      </div>
      <div
        className={inline ? "grid items-start gap-3 lg:grid-cols-[minmax(0,1fr)_26rem]" : undefined}
      >
        <div id="deliveries-panel" role="tabpanel" aria-labelledby={`deliveries-tab-${tab}`}>
          {active.isPending ? <p>Loading deliveries</p> : null}
          {active.isError ? (
            <ErrorState
              title="Could not load deliveries"
              message="The delivery list could not be loaded."
              onRetry={() => void active.refetch()}
            />
          ) : null}
          {active.isSuccess && rows.length === 0 ? (
            <EmptyState
              title={tab === "unmatched" ? "No unmatched deliveries" : "No deliveries"}
              message={
                tab === "unmatched"
                  ? "Every delivery matched an automation trigger."
                  : "No deliveries have been received."
              }
            />
          ) : null}
          {active.isSuccess && rows.length > 0 ? (
            <div className="overflow-x-auto border border-[var(--border)] bg-[var(--card)]">
              <table className="w-full border-collapse text-sm">
                <caption className="sr-only">Deliveries</caption>
                <thead className="bg-[var(--muted)] text-xs text-[var(--muted-foreground)]">
                  <tr className="border-b border-[var(--border)] text-left">
                    {[
                      "Received",
                      "Provider",
                      "Webhook",
                      "Delivery",
                      "Status",
                      "Matched automations",
                      "Truncated",
                    ].map((heading) => (
                      <th
                        key={heading}
                        scope="col"
                        className="px-3 py-2 font-medium whitespace-nowrap"
                      >
                        {heading}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {rows.map((row) => (
                    <DeliveryTableRow
                      key={row.id}
                      row={row}
                      webhook={webhookName.get(row.webhook_uid) ?? row.webhook_uid}
                      selected={selectedId === row.id}
                      replayed={replayed.has(row.id)}
                      onSelect={() => {
                        openerRef.current =
                          document.activeElement instanceof HTMLElement
                            ? document.activeElement
                            : null;
                        setSelectedId(row.id);
                      }}
                    />
                  ))}
                </tbody>
              </table>
            </div>
          ) : null}
          <p className="mt-2 flex items-start gap-1.5 text-sm text-[var(--muted-foreground)]">
            <Info className="mt-0.5 size-4 shrink-0" aria-hidden />
            <span>
              Unmatched means no automation trigger matched; replay re-runs matching against current
              automations.
            </span>
          </p>
        </div>
        {inline && selected ? (
          <aside className="sticky top-4 border border-[var(--border)] bg-[var(--card)] p-3">
            <DeliveryDetail
              delivery={selected}
              replayed={replayed.has(selected.id)}
              onClose={() => setSelectedId(null)}
              onReplay={() => setReplayed((current) => new Set(current).add(selected.id))}
            />
          </aside>
        ) : null}
      </div>
      <Dialog.Root
        open={Boolean(selected) && !wide}
        onOpenChange={(open) => {
          if (open) return;
          const back = openerRef.current;
          setSelectedId(null);
          window.setTimeout(() => back?.focus(), 0);
        }}
      >
        <Dialog.Portal>
          <Dialog.Overlay className="dialog-overlay" />
          <Dialog.Content className="delivery-sheet" aria-describedby="delivery-sheet-status">
            {selected ? (
              <>
                <div className="mb-2 flex justify-end">
                  <Dialog.Close asChild>
                    <Button type="button">Close</Button>
                  </Dialog.Close>
                </div>
                <Dialog.Title className="sr-only">{selected.id}</Dialog.Title>
                <Dialog.Description id="delivery-sheet-status" className="sr-only">
                  Headers and body for this delivery. Signature headers are redacted.
                </Dialog.Description>
                <DeliveryDetail
                  delivery={selected}
                  replayed={replayed.has(selected.id)}
                  onReplay={() => setReplayed((current) => new Set(current).add(selected.id))}
                />
              </>
            ) : null}
          </Dialog.Content>
        </Dialog.Portal>
      </Dialog.Root>
    </div>
  );
}

function TabButton({
  id,
  label,
  selected,
  controls,
  onSelect,
}: {
  id: Tab;
  label: string;
  selected: boolean;
  controls: string;
  onSelect: () => void;
}) {
  return (
    <button
      id={`deliveries-tab-${id}`}
      type="button"
      role="tab"
      aria-selected={selected}
      aria-controls={controls}
      tabIndex={selected ? 0 : -1}
      className={
        selected
          ? "-mb-px border-b-2 border-[var(--foreground)] px-3 py-1.5 text-sm font-semibold"
          : "-mb-px border-b-2 border-transparent px-3 py-1.5 text-sm text-[var(--muted-foreground)]"
      }
      onClick={onSelect}
    >
      {label}
    </button>
  );
}

function DeliveryTableRow({
  row,
  webhook,
  selected,
  replayed,
  onSelect,
}: {
  row: DeliveryRow;
  webhook: string;
  selected: boolean;
  replayed: boolean;
  onSelect: () => void;
}) {
  return (
    <tr
      className={
        selected
          ? "border-b border-[var(--border)] bg-[var(--muted)]"
          : "border-b border-[var(--border)] last:border-b-0"
      }
    >
      <td className="px-3 py-2 font-mono text-xs">
        <time dateTime={row.received_at}>{formatWhen(row.received_at)}</time>
      </td>
      <td className="px-3 py-2">
        <span className="inline-flex items-center gap-1">
          {providerMark(row.provider)}
          {providerLabel(row.provider)}
        </span>
      </td>
      <td className="px-3 py-2 font-mono text-xs">{webhook || "—"}</td>
      <td className="px-3 py-2">
        <button
          type="button"
          className="font-mono text-xs text-[var(--ring)] hover:underline"
          onClick={onSelect}
        >
          {row.delivery_id || row.id}
        </button>
      </td>
      <td className="px-3 py-2">
        {replayed ? <Replayed /> : <DeliveryStatus status={row.status} />}
      </td>
      <td className="px-3 py-2">
        {row.matched_automations.length === 0 ? (
          <span className="text-[var(--muted-foreground)]">—</span>
        ) : (
          <span className="flex flex-wrap gap-1">
            {row.matched_automations.map((name) => (
              <span
                key={name}
                className="border border-[var(--border)] bg-[var(--muted)] px-1.5 font-mono text-xs"
              >
                {name}
              </span>
            ))}
          </span>
        )}
      </td>
      <td className="px-3 py-2 text-xs">
        {row.truncated ? (
          <span className="inline-flex items-center gap-1">
            <Scissors className="size-3.5" aria-hidden />
            Truncated
          </span>
        ) : (
          <span className="text-[var(--muted-foreground)]">—</span>
        )}
      </td>
    </tr>
  );
}

export default function DeliveriesPage() {
  const params = useParams({ strict: false });
  const factoryId = typeof params.factory === "string" ? params.factory : "";
  if (!factoryId)
    return <ErrorState title="Missing factory" message="Open deliveries from a factory." />;
  return <DeliveriesScreen factoryId={factoryId} />;
}
