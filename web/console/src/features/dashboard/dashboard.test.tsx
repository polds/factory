import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";
import { setAPIKey } from "../../api/session";
import type { FactorySummary } from "../../api/types";
import { demoAPIKey } from "../../mocks/demo-key";
import { server } from "../../mocks/server";
import { expectNoSeriousViolations } from "../../test/axe";
import { emit, installFactoryStream } from "../../test/factory-stream";
import { renderApp } from "../../test/render";
import goldenRuns from "./__fixtures__/runs.json";
import { STREAM_REFRESH_MS } from "./live";

function summary(partial: Partial<FactorySummary>): FactorySummary {
  return {
    runs_by_status: {},
    runs_by_failure_class: {},
    queue_wait_p50_ms: 0,
    active_runs: 0,
    waiting_on_human: 0,
    prs_opened: 0,
    deliveries_unmatched: 0,
    ...partial,
  };
}

const payments = summary({
  active_runs: 6,
  waiting_on_human: 1,
  prs_opened: 2,
  deliveries_unmatched: 1,
  queue_wait_p50_ms: 25000,
  runs_by_status: { queued: 4, running: 1, failed: 2 },
  runs_by_failure_class: { agent: 2, infrastructure: 6 },
});

const platform = summary({
  active_runs: 4,
  waiting_on_human: 7,
  prs_opened: 7,
  deliveries_unmatched: 2,
  queue_wait_p50_ms: 11000,
  runs_by_status: { running: 1, succeeded: 3 },
  runs_by_failure_class: { agent: 1, infrastructure: 2, configuration: 1, cancelled: 2 },
});

describe("dashboard", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("sums both factories and lists p50 per factory", async () => {
    setAPIKey(demoAPIKey);
    const urls: string[] = [];
    server.use(
      http.get("/api/v1/factory/:factoryId/summary", ({ request, params }) => {
        urls.push(request.url);
        const id = String(params.factoryId);
        return HttpResponse.json(id === "fac_platform" ? platform : payments);
      }),
      http.get("/api/v1/agent/runs", () => HttpResponse.json(goldenRuns)),
    );
    const { container } = await renderApp("/");
    expect(await screen.findByText("10")).toBeInTheDocument();
    const waiting = screen.getByRole("region", { name: "Waiting on human" });
    expect(waiting).toHaveTextContent("8");
    expect(screen.getByRole("region", { name: "PRs opened" })).toHaveTextContent("9");
    expect(screen.getByRole("region", { name: "Unmatched deliveries" })).toHaveTextContent("3");
    const queue = screen.getByRole("region", { name: "Queue wait p50" });
    expect(queue).toHaveTextContent("fac_payments");
    expect(queue).toHaveTextContent("25s");
    expect(queue).toHaveTextContent("fac_platform");
    expect(queue).toHaveTextContent("11s");
    expect(queue).toHaveTextContent("time to first start; a requeue doesn't restart the clock");
    expect(screen.getByText("queued + running")).toBeInTheDocument();
    expect(screen.getByText("approvals + agent questions")).toBeInTheDocument();
    expect(
      screen.getByText("work items created in this window that have a PR"),
    ).toBeInTheDocument();
    expect(screen.getByText("webhook deliveries no trigger matched")).toBeInTheDocument();
    expect(screen.getByText("Infrastructure failures are not agent quality.")).toBeInTheDocument();
    const grid = container.querySelector(".class-grid");
    const cells = grid ? [...grid.querySelectorAll(".class-cell")] : [];
    expect(cells[0]).toHaveTextContent("Agent");
    expect(cells[0]).toHaveTextContent("3");
    expect(cells[1]).toHaveTextContent("Infrastructure");
    expect(cells[1]).toHaveTextContent("8");
    expect(cells[0]?.querySelector("[data-failure-class='agent']")).not.toBeNull();
    expect(cells[1]?.querySelector("[data-failure-class='infrastructure']")).not.toBeNull();
    expect(within(waiting).getByRole("link", { name: /fac_payments/ })).toHaveAttribute(
      "href",
      "/f/fac_payments/inbox",
    );
    expect(screen.getByRole("link", { name: /Deliveries fac_platform/ })).toHaveAttribute(
      "href",
      "/f/fac_platform/deliveries",
    );
    expect(urls.every((url) => url.includes("from=") && url.includes("to="))).toBe(true);
    const agentRun = screen.getByRole("link", { name: "run_01M53XJ9A6NZ62RF26AK9ZDNQK" });
    expect(agentRun).toHaveAttribute("href", "/runs/run_01M53XJ9A6NZ62RF26AK9ZDNQK");
    expect(screen.getByRole("link", { name: "All runs" })).toHaveAttribute("href", "/runs");
    await expectNoSeriousViolations(container);
  });

  it("refetches one factory summary once inside the debounce window", async () => {
    setAPIKey(demoAPIKey);
    const counts = new Map<string, number>();
    const streams = installFactoryStream();
    server.use(
      http.get("/api/v1/factory/:factoryId/summary", ({ params }) => {
        const id = String(params.factoryId);
        counts.set(id, (counts.get(id) ?? 0) + 1);
        return HttpResponse.json(id === "fac_platform" ? platform : payments);
      }),
    );
    await renderApp("/");
    expect(await screen.findByText("10")).toBeInTheDocument();
    await waitFor(() => expect(streams.has("fac_payments")).toBe(true));
    const before = counts.get("fac_payments") ?? 0;
    const other = counts.get("fac_platform") ?? 0;
    // useFactoryEvents measures the remaining delay with Date.now() and arms a
    // setTimeout. After the axe test the SSE frames can land during a later
    // timer advance, and a few real milliseconds between the two effects make
    // that delay shorter than STREAM_REFRESH_MS, so the refetch fires inside
    // the "not yet" window. Freeze Date.now and flush until the timeout is
    // armed before advancing the window.
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const now = Date.now();
    const dateNow = vi.spyOn(Date, "now").mockReturnValue(now);
    const timeoutSpy = vi.spyOn(globalThis, "setTimeout");
    try {
      const payload = JSON.stringify({ SECRET_PLANT: "SECRET_PLANT", run_id: "run_1" });
      emit("fac_payments", `id: 1\nevent: run.terminal\ndata: ${payload}\n\n`);
      emit("fac_payments", `id: 2\nevent: run.terminal\ndata: ${payload}\n\n`);
      let armed = false;
      for (let attempt = 0; attempt < 40 && !armed; attempt += 1) {
        const calls = timeoutSpy.mock.calls.length;
        await act(async () => {
          await vi.advanceTimersByTimeAsync(0);
        });
        const debounceArmed = timeoutSpy.mock.calls.some(
          (call) => Number(call[1]) === STREAM_REFRESH_MS,
        );
        armed = debounceArmed && timeoutSpy.mock.calls.length === calls;
      }
      expect(timeoutSpy.mock.calls.some((call) => Number(call[1]) === STREAM_REFRESH_MS)).toBe(
        true,
      );
      await act(async () => {
        await vi.advanceTimersByTimeAsync(STREAM_REFRESH_MS - 1);
      });
      expect(counts.get("fac_payments")).toBe(before);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1);
      });
      expect(counts.get("fac_payments")).toBe(before + 1);
      expect(counts.get("fac_platform")).toBe(other);
      expect(document.body.textContent).not.toContain("SECRET_PLANT");
    } finally {
      timeoutSpy.mockRestore();
      dateNow.mockRestore();
    }
  });

  it("changes the live indicator when the stream drops", async () => {
    setAPIKey(demoAPIKey);
    const streams = installFactoryStream();
    server.use(
      http.get("/api/v1/factory/:factoryId/summary", ({ params }) =>
        HttpResponse.json(String(params.factoryId) === "fac_platform" ? platform : payments),
      ),
    );
    await renderApp("/");
    await screen.findByText("10");
    await waitFor(() => expect(streams.size).toBe(2));
    for (const id of streams.keys()) {
      emit(id, `id: ${id}\nevent: run.terminal\ndata: {}\n\n`);
    }
    expect(await screen.findByRole("status", { name: "Live" })).toBeInTheDocument();
    streams.get("fac_payments")?.error(new Error("dropped"));
    expect(await screen.findByRole("status", { name: "Reconnecting" })).toBeInTheDocument();
  });

  it("has no serious accessibility violations", async () => {
    setAPIKey(demoAPIKey);
    const user = userEvent.setup();
    const { container } = await renderApp("/");
    expect(await screen.findByRole("heading", { name: "Dashboard" })).toBeInTheDocument();
    expect(
      (await screen.findAllByText("Comparison metric, not a billing figure")).length,
    ).toBeGreaterThan(0);
    await expectNoSeriousViolations(container);
    await user.click(screen.getByRole("button", { name: "Switch to dark theme" }));
    await expectNoSeriousViolations(document.body);
  });
});
