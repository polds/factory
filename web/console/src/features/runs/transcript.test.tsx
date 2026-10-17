import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { setAPIKey } from "../../api/session";
import { demoAPIKey } from "../../mocks/demo-key";
import { server } from "../../mocks/server";
import { renderApp } from "../../test/render";
import {
  createEventBus,
  goldenRun,
  holdStream,
  installRuns,
  mockTranscriptViewport,
  repeatedStatus,
  runningRun,
  scriptedStream,
  seqValues,
  sseEvent,
  statusEvents,
} from "./test-support";

const paths: string[] = [];

function rememberRequest(event: { request: Request }) {
  paths.push(new URL(event.request.url).pathname);
}

beforeEach(() => {
  paths.length = 0;
  setAPIKey(demoAPIKey);
  server.events.on("request:start", rememberRequest);
});

afterEach(() => {
  server.events.removeListener("request:start", rememberRequest);
  expect(paths.filter((path) => !path.startsWith("/api/v1/"))).toEqual([]);
});

describe("run transcript", () => {
  it("resumes after a drop and shows each seq once", async () => {
    const restore = mockTranscriptViewport(320);
    const calls: { last: string | null; from: string | null }[] = [];
    const first = statusEvents(1, 5) + 'id: 6\nevent: usage\ndata: {"seq":';
    const bus = createEventBus();
    const run = runningRun();
    try {
      installRuns({
        run,
        events(request) {
          const url = new URL(request.url);
          calls.push({
            last: request.headers.get("Last-Event-ID"),
            from: url.searchParams.get("from_seq"),
          });
          if (calls.length === 1) return scriptedStream(request, first);
          return bus.response(request);
        },
      });
      renderApp(`/runs/${run.id}`);
      await waitFor(() => expect(seqValues().sort()).toEqual(["1", "2", "3", "4", "5"]));
      await waitFor(() => expect(calls.length).toBeGreaterThan(1));
      expect(calls[1]).toEqual({ last: "5", from: "5" });
      await waitFor(() =>
        expect(document.querySelector(".transcript-strip")?.textContent).toContain("Reconnecting"),
      );
      expect(document.querySelector(".transcript-strip")?.textContent).not.toContain(
        "Stream closed",
      );
      bus.send(statusEvents(6, 8));
      await waitFor(() =>
        expect(seqValues().sort()).toEqual(["1", "2", "3", "4", "5", "6", "7", "8"]),
      );
      const values = seqValues();
      expect(new Set(values).size).toBe(values.length);
      expect(screen.getByText(/Resumed from seq 5/)).toBeInTheDocument();
    } finally {
      restore();
    }
  });

  it("virtualizes 50000 events under the render budget", async () => {
    const restore = mockTranscriptViewport(320);
    const body = repeatedStatus(50_000);
    try {
      installRuns({
        events: (request) => holdStream(request, body),
      });
      const started = performance.now();
      renderApp(`/runs/${goldenRun.id}`);
      let elapsed = 0;
      try {
        await waitFor(
          () => {
            expect(document.querySelector(".transcript-count")?.textContent).toMatch(
              /50,000 events, rendering/,
            );
          },
          { timeout: 10_000 },
        );
      } finally {
        elapsed = performance.now() - started;
        console.info(`transcript 50000 render ms: ${elapsed.toFixed(1)}`);
      }
      expect(elapsed).toBeLessThan(3000);
      const rows = document.querySelectorAll(".transcript-row");
      expect(rows.length).toBeGreaterThan(0);
      expect(rows.length).toBeLessThanOrEqual(200);
    } finally {
      restore();
    }
  }, 15_000);

  it("stays pinned until the reader scrolls, then jumps back", async () => {
    const user = userEvent.setup();
    const restore = mockTranscriptViewport(100);
    const bus = createEventBus();
    try {
      installRuns({
        run: {
          id: goldenRun.id,
          status: "running",
          title: "Live transcript",
          steer: { enabled: true, reason: "" },
        },
        events: (request) => bus.response(request),
      });
      renderApp(`/runs/${goldenRun.id}`);
      await screen.findByRole("log", { name: "Run transcript" });
      bus.send(statusEvents(1, 40));
      await waitFor(() => expect(screen.getByText("marker-40")).toBeInTheDocument());
      expect(screen.queryByText("marker-1")).not.toBeInTheDocument();
      const log = document.querySelector(".transcript-log");
      expect(log).toBeInstanceOf(HTMLDivElement);
      if (!(log instanceof HTMLDivElement)) return;
      expect(log.scrollTop).toBeGreaterThan(0);

      log.scrollTop = 0;
      log.dispatchEvent(new Event("scroll"));
      await waitFor(() => expect(screen.getByText("marker-1")).toBeInTheDocument());
      expect(screen.queryByText("marker-40")).not.toBeInTheDocument();
      const parked = log.scrollTop;

      bus.send(statusEvents(41, 45));
      const jump = await screen.findByRole("button", { name: "Jump to latest (5 new)" });
      expect(log.scrollTop).toBe(parked);
      expect(screen.getByText("marker-1")).toBeInTheDocument();
      expect(screen.queryByText("marker-45")).not.toBeInTheDocument();

      await user.click(jump);
      await waitFor(() => expect(screen.getByText("marker-45")).toBeInTheDocument());
      expect(screen.queryByText("marker-1")).not.toBeInTheDocument();
      expect(log.scrollTop).toBeGreaterThan(0);
      expect(screen.queryByRole("button", { name: /Jump to latest/ })).not.toBeInTheDocument();
    } finally {
      restore();
    }
  });

  it("stops reconnecting when a terminal run's stream closes", async () => {
    const calls: string[] = [];
    const bus = createEventBus();
    installRuns({
      events(request) {
        calls.push(request.headers.get("Last-Event-ID") ?? "");
        return bus.response(request);
      },
    });
    renderApp(`/runs/${goldenRun.id}`);
    await screen.findByText("The agent reported the run as failed");
    bus.send(statusEvents(1, 2));
    await waitFor(() =>
      expect(document.querySelector(".transcript-count")?.textContent).toMatch(/2 events/),
    );
    bus.close();
    await waitFor(() =>
      expect(document.querySelector(".transcript-strip")?.textContent).toContain("Stream closed"),
    );
    await new Promise((resolve) => setTimeout(resolve, 500));
    expect(calls).toEqual([""]);
  });

  it("keeps a live run connected through a terminal event and reconnects after a clean close", async () => {
    const calls: string[] = [];
    const bus = createEventBus();
    const run = runningRun();
    installRuns({
      run,
      events(request) {
        calls.push(request.headers.get("Last-Event-ID") ?? "");
        return bus.response(request);
      },
    });
    renderApp(`/runs/${run.id}`);
    await screen.findByRole("heading", { name: "Settle the batch" });
    bus.send(sseEvent(1, "terminal", { status: "idle" }));
    await waitFor(() =>
      expect(document.querySelector(".transcript-count")?.textContent).toMatch(/1 events/),
    );
    await new Promise((resolve) => setTimeout(resolve, 400));
    expect(calls).toEqual([""]);
    expect(document.querySelector(".transcript-strip")?.textContent).not.toContain("Stream closed");
    bus.close();
    await waitFor(() => expect(calls.length).toBe(2));
    await waitFor(() =>
      expect(document.querySelector(".transcript-strip")?.textContent).toContain("Reconnecting"),
    );
  });
});
