import { useEffect, useState } from "react";
import { consumeSSE, type SSERecord, type SSEStatus } from "./sse";

export type UseSSEResult = {
  events: SSERecord[];
  status: "idle" | SSEStatus | "closed";
  error: string | null;
};

export function useSSE(url: string | null, options?: { cursor?: string }): UseSSEResult {
  const cursor = options?.cursor;
  const streamKey = `${url ?? ""}\n${cursor ?? ""}`;
  const [seenKey, setSeenKey] = useState(streamKey);
  const [events, setEvents] = useState<SSERecord[]>([]);
  const [status, setStatus] = useState<UseSSEResult["status"]>(url ? "connecting" : "idle");
  const [error, setError] = useState<string | null>(null);
  if (streamKey !== seenKey) {
    setSeenKey(streamKey);
    setEvents([]);
    setError(null);
    setStatus(url ? "connecting" : "idle");
  }

  useEffect(() => {
    if (!url) return;
    const controller = new AbortController();
    let active = true;
    void consumeSSE({
      url,
      cursor,
      signal: controller.signal,
      onStatus: (next) => {
        if (active) setStatus(next);
      },
      onEvent: (event) => {
        if (!active) return;
        setStatus("open");
        setEvents((prev) => [...prev, event]);
      },
    })
      .then(() => {
        if (active && !controller.signal.aborted) setStatus("closed");
      })
      .catch((err: unknown) => {
        if (!active || controller.signal.aborted) return;
        setStatus("closed");
        setError(err instanceof Error ? err.message : "stream failed");
      });
    return () => {
      active = false;
      controller.abort();
    };
  }, [url, cursor]);

  return { events, status, error };
}
