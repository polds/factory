import { renderHook, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { SESSION_KEY, setAPIKey } from "../api/session";
import { consumeSSE, takeCompleteEvents } from "../api/sse";
import { useSSE } from "../api/useSSE";
import { demoAPIKey } from "../mocks/demo-key";

function stream(chunks: string[], fail = false): Response {
  const encoder = new TextEncoder();
  let sent = false;
  return new Response(
    new ReadableStream({
      pull(controller) {
        if (!sent) {
          sent = true;
          for (const chunk of chunks) controller.enqueue(encoder.encode(chunk));
          if (!fail) controller.close();
          return;
        }
        controller.error(new Error("dropped"));
      },
    }),
    { status: 200, headers: { "Content-Type": "text/event-stream" } },
  );
}

describe("SSE client", () => {
  it("keeps an unfinished event in the buffer", () => {
    const taken = takeCompleteEvents("id: 1\ndata: a\ndata: b\n\nid: 2\ndata: partial");
    expect(taken.events).toEqual([{ id: "1", event: "", data: "a\nb" }]);
    expect(taken.rest).toContain("partial");
  });

  it("resumes after a connection dropped mid-event", async () => {
    setAPIKey(demoAPIKey);
    const calls: { lastEventID: string | null; url: string; authorization: string | null }[] = [];
    const events: { id: string; data: string }[] = [];
    const controller = new AbortController();
    await consumeSSE({
      url: "/api/v1/agent/runs/run_01/events",
      cursor: "9",
      signal: controller.signal,
      sleep: async () => {},
      fetchImpl: (input, init) => {
        const headers = new Headers(init?.headers);
        calls.push({
          lastEventID: headers.get("Last-Event-ID"),
          url: String(input),
          authorization: headers.get("Authorization"),
        });
        if (calls.length === 1) {
          return Promise.resolve(
            stream(["id: 10\nevent: message.delta\ndata: ten\n\nid: 11\ndata: ele"], true),
          );
        }
        if (calls.length === 2) {
          return Promise.resolve(
            stream(["id: 10\ndata: ten\n\nid: 11\ndata: eleven\n\nid: 12\ndata: twelve\n\n"]),
          );
        }
        return new Promise<Response>((_resolve, reject) => {
          init?.signal?.addEventListener("abort", () =>
            reject(new DOMException("aborted", "AbortError")),
          );
        });
      },
      onEvent(event) {
        events.push({ id: event.id, data: event.data });
        if (event.id === "12") controller.abort();
      },
    });
    expect(events).toEqual([
      { id: "10", data: "ten" },
      { id: "11", data: "eleven" },
      { id: "12", data: "twelve" },
    ]);
    expect(calls[0]?.lastEventID).toBe("9");
    expect(calls[0]?.url).toContain("from_seq=9");
    expect(calls[1]?.lastEventID).toBe("10");
    expect(calls[1]?.url).toContain("from_seq=10");
    expect(calls[0]?.authorization).toBe(`Bearer ${demoAPIKey}`);
    expect(sessionStorage.getItem(SESSION_KEY)).toBe(demoAPIKey);
    expect(JSON.stringify(events)).not.toContain(demoAPIKey);
  });

  it("useSSE publishes events from the stream reader", async () => {
    const previous = globalThis.fetch;
    globalThis.fetch = () =>
      Promise.resolve(stream(["id: 1\nevent: message.delta\ndata: one\n\n"]));
    try {
      const { result, unmount } = renderHook(() => useSSE("/api/v1/agent/runs/run_01/events"));
      await waitFor(() =>
        expect(result.current.events.map((event) => event.data)).toEqual(["one"]),
      );
      unmount();
    } finally {
      globalThis.fetch = previous;
    }
  });
});
