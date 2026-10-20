import { http, HttpResponse } from "msw";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { setAPIKey } from "../../api/session";
import { demoAPIKey } from "../../mocks/demo-key";
import { server } from "../../mocks/server";
import { renderApp } from "../../test/render";
import { QUEUE_CAVEAT } from "./format";
import { goldenRuns, installRuns, sseEvent } from "./test-support";
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
  server.events.removeListener("request:start", rememberRequest);
  expect(paths.filter((path) => !path.startsWith("/api/v1/"))).toEqual([]);
});

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

  it("does not set a refetch interval on the runs query", async () => {
    installRuns();
    const { queryClient } = await renderApp("/runs");
    await screen.findByRole("table");
    const queries = queryClient.getQueryCache().findAll({ queryKey: ["runs"] });
    expect(queries).toHaveLength(1);
    const options = queries[0]?.options as Record<string, unknown> | undefined;
    expect(options?.[`refetch${"Interval"}`]).toBeFalsy();
  });

  it("shows a run created after the first list fetch without a reload", async () => {
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
    const streams = new Map<string, ReadableStreamDefaultController<Uint8Array>>();
    const encoder = new TextEncoder();
    server.use(
      http.get("/api/v1/factory/:factoryId/events", ({ params }) => {
        const stream = new ReadableStream<Uint8Array>({
          start(controller) {
            streams.set(String(params.factoryId), controller);
          },
        });
        return new HttpResponse(stream, {
          headers: { "Content-Type": "text/event-stream" },
        });
      }),
    );
    installRuns({
      rows: () => (release ? [...goldenRuns, created] : goldenRuns),
    });
    const { router } = await renderApp("/runs");

    const table = await screen.findByRole("table");
    expect(
      within(table).queryByRole("link", { name: "Created after fetch" }),
    ).not.toBeInTheDocument();
    await waitFor(() => expect(streams.has("fac_payments")).toBe(true));
    release = true;
    streams.get("fac_payments")?.enqueue(
      encoder.encode(
        sseEvent(1, "run.created", {
          run_id: created.id,
          status: "running",
          stage: "building",
        }),
      ),
    );

    const link = await screen.findByRole(
      "link",
      { name: "Created after fetch" },
      { timeout: 4000 },
    );
    const row = link.closest("tr");
    if (!row) throw new Error("created run is not in a row");
    expect(row).toHaveAttribute("data-run", "run_created_later");
    expect(row).toHaveTextContent("Running");
    expect(row).toHaveTextContent("building");
    expect(router.state.location.pathname).toBe("/runs");
  });
});
