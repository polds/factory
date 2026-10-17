import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { client } from "../../api/client";
import { consumeSSE, defaultSleep, type SSERecord, type SSEStatus } from "../../api/sse";
import { ensureAnimationFrame, scheduleFrame } from "./frame";

export type StreamStatus = SSEStatus | "closed";

export type RunEvents = {
  events: SSERecord[];
  status: StreamStatus;
  resumedFrom: string | null;
  error: string | null;
};

export function useRunEvents(runId: string, terminal: boolean): RunEvents {
  ensureAnimationFrame();
  const terminalRef = useRef(terminal);
  const onTerminalDuringBackoff = useRef<(() => void) | null>(null);
  const [events, setEvents] = useState<SSERecord[]>([]);
  const [status, setStatus] = useState<StreamStatus>("connecting");
  const [resumedFrom, setResumedFrom] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  useLayoutEffect(() => {
    terminalRef.current = terminal;
    if (terminal) onTerminalDuringBackoff.current?.();
  }, [terminal]);

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

    const sleep = async (ms: number, signal: AbortSignal) => {
      if (signal.aborted) return;
      const stopIfTerminal = () => {
        if (terminalRef.current) controller.abort();
      };
      stopIfTerminal();
      if (signal.aborted) return;
      onTerminalDuringBackoff.current = stopIfTerminal;
      try {
        await defaultSleep(ms, signal);
      } finally {
        onTerminalDuringBackoff.current = null;
      }
      stopIfTerminal();
    };

    void consumeSSE({
      url: client.runEventsURL(runId),
      signal: controller.signal,
      sleep,
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
        setStatus("closed");
      })
      .catch((err: unknown) => {
        if (!active || controller.signal.aborted) return;
        flush();
        setStatus("closed");
        setError(err instanceof Error ? err.message : "stream failed");
      });

    return () => {
      active = false;
      onTerminalDuringBackoff.current = null;
      cancelFrame?.();
      controller.abort();
    };
  }, [runId]);

  return { events, status, resumedFrom, error };
}
