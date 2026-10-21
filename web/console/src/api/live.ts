import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import { useEffect, useRef } from "react";
import { client } from "./client";
import { useSSE } from "./useSSE";

export const STREAM_REFRESH_MS = 1000;

export type StreamStatus = "idle" | "connecting" | "open" | "reconnecting" | "closed";

// One refresh per factory per second. Events that arrive while the timer is
// armed are covered by that refresh; the payload is never rendered.
export function useFactoryEvents(factoryId: string): StreamStatus {
  const queryClient = useQueryClient();
  const { events, status } = useSSE(client.factoryEventsURL(factoryId));
  // Deadline of the refresh already armed. A later event in that window keeps
  // the same deadline, so a burst refreshes once per second.
  const due = useRef(0);

  useEffect(() => {
    if (events.length === 0) return undefined;
    const now = Date.now();
    let delay = STREAM_REFRESH_MS;
    if (due.current > now) delay = due.current - now;
    else due.current = now + STREAM_REFRESH_MS;
    const handle = setTimeout(() => {
      invalidateFactory(queryClient, factoryId);
    }, delay);
    return () => clearTimeout(handle);
  }, [events.length, factoryId, queryClient]);

  return status;
}

export function invalidateFactory(queryClient: QueryClient, factoryId: string): void {
  void queryClient.invalidateQueries({ queryKey: ["summary", factoryId] });
  void queryClient.invalidateQueries({ queryKey: ["runs"] });
  void queryClient.invalidateQueries({ queryKey: ["tasks", factoryId] });
  void queryClient.invalidateQueries({ queryKey: ["task", factoryId] });
  void queryClient.invalidateQueries({ queryKey: ["inbox", factoryId] });
  void queryClient.invalidateQueries({ queryKey: ["approvals", factoryId] });
  void queryClient.invalidateQueries({ queryKey: ["scorers", factoryId] });
  void queryClient.invalidateQueries({ queryKey: ["benchmarks", factoryId] });
  void queryClient.invalidateQueries({ queryKey: ["batches", factoryId] });
  void queryClient.invalidateQueries({ queryKey: ["metrics", factoryId] });
  void queryClient.invalidateQueries({ queryKey: ["scores", factoryId] });
}

function statusRank(status: StreamStatus): number {
  switch (status) {
    case "closed":
      return 4;
    case "reconnecting":
      return 3;
    case "connecting":
    case "idle":
      return 2;
    case "open":
      return 1;
    default: {
      const unreachable: never = status;
      throw new Error(`Unexpected value: ${String(unreachable)}`);
    }
  }
}

export function liveLabel(statuses: readonly StreamStatus[]): string {
  if (statuses.length === 0) return "Offline";
  const worst = Math.max(...statuses.map(statusRank));
  switch (worst) {
    case 4:
      return "Disconnected";
    case 3:
      return "Reconnecting";
    case 2:
      return "Connecting";
    case 1:
      return "Live";
    default:
      return "Offline";
  }
}
