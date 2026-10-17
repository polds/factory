import { Clock, Lock } from "lucide-react";
import { useEffect, useState } from "react";
import type { SteerState } from "../../api/types";
import { Button } from "../../components/Button";
import { ConfirmDialog } from "../../components/ConfirmDialog";
import { formatDuration } from "../../lib/format";
import type { LocalSteer, SteerSignal } from "./types";

type ShownSteer = {
  id: string;
  kind: "interrupt" | "followup";
  delivered: boolean;
};

function shownSteers(local: readonly LocalSteer[], signals: readonly SteerSignal[]): ShownSteer[] {
  const rows: ShownSteer[] = local.map((steer) => ({ ...steer, delivered: false }));
  for (const signal of signals) {
    if (signal.id) {
      const match = rows.find((row) => row.id === signal.id);
      if (match) {
        match.delivered = true;
        continue;
      }
      rows.push({ id: signal.id, kind: signal.kind, delivered: true });
      continue;
    }
    const pending = [...rows].reverse().find((row) => row.kind === signal.kind && !row.delivered);
    if (pending) pending.delivered = true;
  }
  return rows;
}

export function SteerPanel({
  steer,
  local,
  signals,
  text,
  sendError,
  onText,
  onFollowUp,
  onInterrupt,
}: {
  steer: SteerState;
  local: readonly LocalSteer[];
  signals: readonly SteerSignal[];
  text: string;
  sendError: string;
  onText: (value: string) => void;
  onFollowUp: () => void;
  onInterrupt: () => void;
}) {
  const [now, setNow] = useState(() => Date.now());
  const [interruptOpen, setInterruptOpen] = useState(false);
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(id);
  }, []);

  const parsed = steer.idle_until ? Date.parse(steer.idle_until) : Number.NaN;
  const remaining = Number.isFinite(parsed) ? parsed - now : Number.NaN;
  const counting = steer.enabled && remaining > 0;
  const expired = steer.enabled && Number.isFinite(remaining) && remaining <= 0;
  const enabled = steer.enabled && !expired;
  const reason = !steer.enabled
    ? steer.reason
    : expired && steer.idle_until
      ? `run finished; idle window ended at ${steer.idle_until}`
      : "";
  const describedBy = [reason ? "steer-reason" : "", sendError ? "steer-error" : ""]
    .filter((id) => id !== "")
    .join(" ");
  const described = describedBy || undefined;
  const history = shownSteers(local, signals);

  return (
    <aside className="steer-panel" aria-labelledby="steer-heading">
      <h2 id="steer-heading">Attach / steer</h2>
      <div className="steer-body">
        {counting ? (
          <p className="idle-countdown">
            <Clock aria-hidden="true" size={16} /> Idle: accepting follow-ups for{" "}
            {formatDuration(remaining)}
          </p>
        ) : null}
        {reason ? (
          <p id="steer-reason" className="steer-reason">
            <Lock aria-hidden="true" size={16} />
            <span>{reason}</span>
          </p>
        ) : null}
        {sendError ? (
          <p id="steer-error" className="steer-error" role="alert">
            {sendError}
          </p>
        ) : null}
        <form
          onSubmit={(event) => {
            event.preventDefault();
            if (!enabled || text.trim() === "") return;
            onFollowUp();
          }}
        >
          <label htmlFor="follow-up">Send a follow-up to the agent</label>
          <textarea
            id="follow-up"
            rows={4}
            value={text}
            disabled={!enabled}
            aria-describedby={described}
            onChange={(event) => onText(event.target.value)}
          />
          <Button
            type="submit"
            disabled={!enabled || text.trim() === ""}
            aria-describedby={described}
          >
            Send follow-up
          </Button>
        </form>
        <Button
          id="interrupt-run"
          type="button"
          tone="danger"
          className="button-outline-danger"
          disabled={!enabled}
          aria-describedby={described}
          onClick={() => setInterruptOpen(true)}
        >
          Interrupt
        </Button>
        <ConfirmDialog
          open={interruptOpen}
          title="Interrupt this turn?"
          description="The agent keeps its conversation and waits for a follow-up."
          confirmLabel="Interrupt"
          tone="danger"
          onConfirm={() => {
            setInterruptOpen(false);
            onInterrupt();
          }}
          onOpenChange={(open) => {
            setInterruptOpen(open);
            if (!open)
              window.setTimeout(() => document.getElementById("interrupt-run")?.focus(), 0);
          }}
        />
        <p className="section-label">Recent steers</p>
        {history.length === 0 ? (
          <p className="muted">None yet.</p>
        ) : (
          <ul className="steer-list">
            {history.map((item) => (
              <li key={`${item.kind}-${item.id}`}>
                <span className="mono">{item.id}</span>
                <span>{item.kind}</span>
                <span>{item.delivered ? "delivered" : "pending"}</span>
              </li>
            ))}
          </ul>
        )}
      </div>
    </aside>
  );
}
