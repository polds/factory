import { useEffect, useState } from "react";
import { client } from "../../api/client";
import { consumeSSE, type SSERecord, type SSEStatus } from "../../api/sse";
import { ensureAnimationFrame, scheduleFrame } from "./frame";

export type StreamStatus = SSEStatus | "closed";

export type RunEvents = {
  events: SSERecord[];
  status: StreamStatus;
  resumedFrom: string | null;
  error: string | null;
};

export function useRunEvents(runId: string): RunEvents {
  ensureAnimationFrame();
  const [events, setEvents] = useState<SSERecord[]>([]);
  const [status, setStatus] = useState<StreamStatus>("connecting");
  const [resumedFrom, setResumedFrom] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    const pending: SSERecord[] = [];
    let cancelFrame: (() => void) | null = null;
    let lastId = "";
    let reconnecting = false;
    let active = true;

    const flush = () => {
      cancelFrame = null;
      if (!active || pending.length === 0) return;
      const batch = pending.splice(0, pending.length);
      setEvents((prev) => prev.concat(batch));
    };

    const schedule = () => {
      if (cancelFrame) return;
      cancelFrame = scheduleFrame(flush);
    };

    void consumeSSE({
      url: client.runEventsURL(runId),
      signal: controller.signal,
      onStatus: (next) => {
        if (!active) return;
        if (next === "reconnecting") reconnecting = true;
        setStatus(next);
      },
      onEvent: (event) => {
        if (!active) return;
        if (reconnecting) {
          reconnecting = false;
          if (lastId) setResumedFrom(lastId);
        }
        if (event.id) lastId = event.id;
        pending.push(event);
        schedule();
      },
    })
      .then(() => {
        if (!active) return;
        flush();
        if (!controller.signal.aborted) setStatus("closed");
      })
      .catch((err: unknown) => {
        if (!active || controller.signal.aborted) return;
        flush();
        setStatus("closed");
        setError(err instanceof Error ? err.message : "stream failed");
      });

    return () => {
      active = false;
      cancelFrame?.();
      controller.abort();
    };
  }, [runId]);

  return { events, status, resumedFrom, error };
}
