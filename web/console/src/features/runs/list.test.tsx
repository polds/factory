import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setAPIKey } from "../../api/session";
import { demoAPIKey } from "../../mocks/demo-key";
import { server } from "../../mocks/server";
import { renderApp } from "../../test/render";
import { QUEUE_CAVEAT } from "./format";
import { RUNS_LIST_CATCHUP_MS, RUNS_LIST_REFRESH_MS } from "./list-page";
import { goldenRuns, installRuns } from "./test-support";
import type { RunRow } from "./types";

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
  vi.useRealTimers();
  server.events.removeListener("request:start", rememberRequest);
  expect(paths.filter((path) => !path.startsWith("/api/v1/"))).toEqual([]);
});

function useListClock(): void {
  vi.useFakeTimers({
    toFake: ["Date", "setTimeout", "clearTimeout", "setInterval", "clearInterval"],
  });
}

async function advanceList(ms: number): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
  // A refetch started on the interval tick notifies React with setTimeout(0).
  // Fake timers schedule that as 1ms while a tick is in progress, so a
  // same-instant flush never paints the new rows.
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1);
  });
}

async function settleList(): Promise<void> {
  for (let attempt = 0; attempt < 20; attempt += 1) {
    await advanceList(0);
    const settled =
      screen.queryByText(/Showing /) !== null ||
      screen.queryByText("No runs match these filters") !== null;
    if (screen.queryByRole("heading", { name: "Runs" }) && settled) return;
  }
  throw new Error("runs list did not render");
}

describe("runs list", () => {
  it("renders /runs and round-trips every filter through the URL", async () => {
    const user = userEvent.setup();
    const searches: string[] = [];
    installRuns({
      onList(url) {
        searches.push(url.search);
      },
    });
    const { router } = await renderApp("/runs?failure_class=infrastructure&agent=reviewer");

    expect(await screen.findByRole("heading", { name: "Runs" })).toBeInTheDocument();
    expect(screen.getByText("All factories")).toBeInTheDocument();
    await waitFor(() => {
      expect(searches).toContain("?failure_class=infrastructure&agent=reviewer");
    });
    expect(screen.getByLabelText("Agent")).toHaveValue("reviewer");
    expect(screen.getByRole("button", { name: "Infrastructure" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(screen.getByText("2 filters active")).toBeInTheDocument();
    expect(router.state.location.searchStr).toContain("failure_class=infrastructure");
    expect(router.state.location.searchStr).toContain("agent=reviewer");

    await user.selectOptions(screen.getByLabelText("Status"), "running");
    await waitFor(() => {
      expect(searches.at(-1)).toBe("?failure_class=infrastructure&agent=reviewer&status=running");
    });
    expect(router.state.location.href).toContain("/runs?");
    expect(router.state.location.searchStr).toContain("status=running");
    expect(router.state.location.searchStr).toContain("agent=reviewer");
    expect(router.state.location.searchStr).toContain("failure_class=infrastructure");

    await user.click(screen.getByRole("button", { name: "Clear filters" }));
    await waitFor(() => {
      expect(searches.at(-1)).toBe("");
    });
    expect(router.state.location.pathname).toBe("/runs");
    expect(router.state.location.searchStr).toBe("");
    expect(screen.getByLabelText("Status")).toHaveValue("");
    expect(screen.getByText("No filters")).toBeInTheDocument();
  });

  it("renders agent and infrastructure with different icon, colour, and text", async () => {
    const rows: RunRow[] = [
      {
        ...goldenRuns[0],
        failure_class: "agent",
        failure_summary: "The agent reported the run as failed",
      },
      {
        id: "run_infra_row",
        status: "failed",
        factory_id: "fac_1",
        agent: "planner",
        failure_class: "infrastructure",
        failure_summary: "Container was OOMKilled",
        queue_wait_ms: 4,
        exec_ms: 9,
        model_id: "model-from-api",
      },
      {
        id: "run_clean_row",
        status: "succeeded",
        factory_id: "fac_1",
        agent: "implementer",
        queue_wait_ms: 0,
        exec_ms: 20,
        cost_cents: 0,
      },
    ];
    installRuns({ list: rows });
    await renderApp("/runs");

    const table = await screen.findByRole("table");
    const agent = table.querySelector('[data-failure-class="agent"]');
    const infra = table.querySelector('[data-failure-class="infrastructure"]');
    expect(agent).toHaveTextContent("Agent");
    expect(infra).toHaveTextContent("Infrastructure");
    expect(agent?.querySelector("svg")).toHaveClass("lucide-bot");
    expect(infra?.querySelector("svg")).toHaveClass("lucide-server-crash");
    expect(agent?.getAttribute("style")).not.toBe(infra?.getAttribute("style"));

    const clean = table.querySelector('[data-run="run_clean_row"]');
    const failure = clean?.querySelector(".failure-cell");
    expect(failure).toHaveTextContent("—");
    expect(failure?.querySelector("[data-failure-class]")).toBeNull();
    expect(within(table).getByText("model-from-api")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Next" })).not.toBeInTheDocument();
    expect(screen.getByText("Showing 3")).toBeInTheDocument();
  });

  it("shows queue and execution time on every row with the caveat", async () => {
    installRuns();
    await renderApp("/runs");
    const table = await screen.findByRole("table");
    const bodyRows = within(table).getAllByRole("row").slice(1);
    expect(bodyRows).toHaveLength(goldenRuns.length);
    for (const row of bodyRows) {
      expect(row).toHaveTextContent(/Queue /);
      expect(row).toHaveTextContent(/Execution /);
    }
    expect(table.textContent).toContain("Queue 1s");
    expect(table.textContent).toContain("Execution 45s");
    expect(screen.getByText("$0.03")).toBeInTheDocument();
    expect(screen.getByText("$0.00")).toBeInTheDocument();
    expect(screen.getAllByText("—").length).toBeGreaterThan(0);
    expect(screen.getByText(QUEUE_CAVEAT)).toBeInTheDocument();
  });

  it("stops polling an all-terminal or empty list after the catch-up window", async () => {
    useListClock();
    const terminal = goldenRuns.map((row, index) => ({
      ...row,
      status: (["succeeded", "failed", "cancelled"] as const)[index] ?? "succeeded",
    }));
    for (const list of [terminal, [] as RunRow[]]) {
      let calls = 0;
      installRuns({
        list,
        onList() {
          calls += 1;
        },
      });
      const view = await renderApp("/runs");
      await settleList();
      const afterFirst = calls;
      expect(afterFirst).toBeGreaterThan(0);

      await advanceList(RUNS_LIST_REFRESH_MS);
      expect(calls).toBeGreaterThan(afterFirst);

      await advanceList(RUNS_LIST_CATCHUP_MS);
      const settled = calls;
      await advanceList(RUNS_LIST_REFRESH_MS * 3);
      expect(calls).toBe(settled);
      view.unmount();
    }
  });

  it("keeps polling while a listed run is non-terminal", async () => {
    useListClock();
    for (const status of ["queued", "assigned", "running", "idle"] as const) {
      let calls = 0;
      installRuns({
        list: [
          {
            id: `run_${status}`,
            status,
            factory_id: "fac_1",
            title: `Still ${status}`,
          },
        ],
        onList() {
          calls += 1;
        },
      });
      const view = await renderApp("/runs");
      await settleList();
      expect(screen.getByRole("link", { name: `Still ${status}` })).toBeInTheDocument();
      await advanceList(RUNS_LIST_CATCHUP_MS + RUNS_LIST_REFRESH_MS);
      const afterWindow = calls;
      expect(afterWindow).toBeGreaterThan(1);
      await advanceList(RUNS_LIST_REFRESH_MS);
      expect(calls).toBeGreaterThan(afterWindow);
      view.unmount();
    }
  });

  it("shows a run created after the first list fetch without a reload", async () => {
    useListClock();
    const created: RunRow = {
      id: "run_created_later",
      status: "running",
      factory_id: "fac_payments",
      agent: "implementer",
      title: "Created after fetch",
      stage: "building",
      queue_wait_ms: 1,
      exec_ms: 2,
    };
    let release = false;
    let calls = 0;
    installRuns({
      onList() {
        calls += 1;
      },
      rows: () => (release ? [...goldenRuns, created] : goldenRuns),
    });
    const { router } = await renderApp("/runs");

    await settleList();
    const table = screen.getByRole("table");
    expect(calls).toBeGreaterThan(0);
    expect(
      within(table).queryByRole("link", { name: "Created after fetch" }),
    ).not.toBeInTheDocument();
    const afterFirst = calls;
    release = true;

    await advanceList(RUNS_LIST_REFRESH_MS);
    expect(calls).toBeGreaterThan(afterFirst);
    const link = screen.getByRole("link", { name: "Created after fetch" });
    const row = link.closest("tr");
    if (!row) throw new Error("created run is not in a row");
    expect(row).toHaveAttribute("data-run", "run_created_later");
    expect(row).toHaveTextContent("Running");
    expect(row).toHaveTextContent("building");
    expect(router.state.location.pathname).toBe("/runs");
  });
});
