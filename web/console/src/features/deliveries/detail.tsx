import { Scissors, X } from "lucide-react";
import { useRef, useState } from "react";
import { client } from "../../api/client";
import { Button } from "../../components/Button";
import { ConfirmDialog } from "../../components/ConfirmDialog";
import type { DeliveryRow } from "./api";
import { formatWhen } from "./format";
import { DeliveryStatus } from "./status";

export function DeliveryDetail({
  delivery,
  replayed,
  onReplay,
  onClose,
}: {
  delivery: DeliveryRow;
  replayed: boolean;
  onReplay: () => void;
  onClose?: () => void;
}) {
  const [confirm, setConfirm] = useState(false);
  const sent = useRef(false);

  async function replay() {
    if (sent.current || replayed) return;
    sent.current = true;
    try {
      await client.replayDelivery(delivery.id);
      onReplay();
      setConfirm(false);
    } catch (error) {
      sent.current = false;
      throw error;
    }
  }

  return (
    <div className="space-y-3">
      <div className="flex items-start justify-between gap-2">
        <div>
          <h2 className="font-mono text-sm font-semibold">{delivery.id}</h2>
          <p className="text-xs text-muted-foreground">
            <time dateTime={delivery.received_at}>{formatWhen(delivery.received_at)}</time>
            {" · "}
            {replayed ? <Replayed /> : <DeliveryStatus status={delivery.status} />}
          </p>
        </div>
        {onClose ? (
          <Button type="button" onClick={onClose}>
            <X aria-hidden />
            <span className="sr-only">Close detail</span>
          </Button>
        ) : null}
      </div>
      <section>
        <h3 className="mb-1 text-xs font-semibold tracking-wide text-muted-foreground">Headers</h3>
        {delivery.headers.length === 0 ? (
          <p className="text-sm text-muted-foreground">No headers</p>
        ) : (
          <table className="w-full border-collapse font-mono text-xs">
            <caption className="sr-only">Delivery headers</caption>
            <tbody>
              {delivery.headers.map((header) => (
                <tr key={header.name} className="border-t border-border">
                  <th scope="row" className="py-1 pr-2 text-left font-normal text-muted-foreground">
                    {header.name}
                  </th>
                  <td
                    className={
                      header.value === "[redacted]"
                        ? "py-1 italic text-muted-foreground"
                        : "py-1 break-all"
                    }
                  >
                    {header.value}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
      {delivery.events.length > 0 ? (
        <section aria-label="Normalized event">
          <h3 className="mb-1 text-xs font-semibold tracking-wide text-muted-foreground">
            Normalized event
          </h3>
          <ul className="space-y-2">
            {delivery.events.map((event, index) => (
              <li key={`${event.provider}-${event.event}-${index}`}>
                <dl className="grid grid-cols-[8rem_1fr] gap-x-2 text-sm">
                  {eventLine("provider", event.provider)}
                  {eventLine("event", event.event)}
                  {eventLine("subject kind", event.subjectKind)}
                  {eventLine("subject id", event.subjectId)}
                  {eventLine("subject number", event.subjectNumber)}
                  {eventLine("subject title", event.subjectTitle)}
                  {eventLine("actor handle", event.actorHandle)}
                  {eventLine("thread", event.thread)}
                </dl>
              </li>
            ))}
          </ul>
        </section>
      ) : null}
      <section>
        <h3 className="mb-1 flex items-center gap-2 text-xs font-semibold tracking-wide text-muted-foreground">
          Body
          {delivery.truncated ? (
            <span className="inline-flex items-center gap-1 font-normal tracking-normal text-foreground">
              <Scissors className="size-3.5" aria-hidden />
              truncated
            </span>
          ) : null}
        </h3>
        <pre
          aria-label={`Body of ${delivery.id}`}
          className="overflow-x-auto border border-border bg-card p-2 font-mono text-xs whitespace-pre-wrap"
        >
          {delivery.bodyText || " "}
        </pre>
      </section>
      <Button type="button" disabled={replayed} onClick={() => setConfirm(true)}>
        Replay delivery
      </Button>
      <ConfirmDialog
        open={confirm}
        title="Replay this delivery?"
        description="Matching re-runs against current automations. Matched automations may create new work items."
        confirmLabel="Replay"
        onConfirm={() => void replay()}
        onOpenChange={setConfirm}
      />
    </div>
  );
}

export function Replayed() {
  return <span className="inline-flex items-center gap-1">Replayed just now</span>;
}

function eventLine(label: string, value: string) {
  if (!value) return null;
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="break-all">{value}</dd>
    </>
  );
}
