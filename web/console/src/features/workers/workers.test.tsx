import { screen, within } from "@testing-library/react";
import axe from "axe-core";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import { setAPIKey } from "../../api/session";
import { demoAPIKey } from "../../mocks/demo-key";
import { server } from "../../mocks/server";
import { renderApp } from "../../test/render";
import workers from "./__fixtures__/workers.json";

async function renderWorkers() {
  setAPIKey(demoAPIKey);
  return await renderApp("/workers");
}

async function expectClean() {
  const results = await axe.run(document.body);
  const blocking = results.violations.filter(
    (violation) => violation.impact === "serious" || violation.impact === "critical",
  );
  expect(blocking, JSON.stringify(blocking, null, 2)).toEqual([]);
}

describe("workers", () => {
  it("labels time to first start and warns when a pool is rejecting", async () => {
    const urls: string[] = [];
    server.events.on("request:start", ({ request }) => urls.push(request.url));
    server.use(
      http.get("/api/v1/agent/connected-self-hosted-workers", () => HttpResponse.json(workers)),
    );
    await renderWorkers();
    expect(await screen.findByRole("heading", { name: "Workers" })).toBeInTheDocument();
    expect(screen.getByText("p50 time to first start")).toBeInTheDocument();
    expect(
      screen.getByText("measured to the first start; a requeue doesn't restart it"),
    ).toBeInTheDocument();
    expect(
      screen.getByText("Sustained rejections usually mean the pool is too small."),
    ).toBeInTheDocument();
    expect(
      urls.map((url) => new URL(url).pathname).every((path) => path.startsWith("/api/v1")),
    ).toBe(true);
    await expectClean();
  });

  it("shows stale, never, unknown, and an empty p50 as a dash", async () => {
    server.use(
      http.get("/api/v1/agent/connected-self-hosted-workers", () =>
        HttpResponse.json({
          workers: [
            {
              id: "stale-1",
              pool: "gpu",
              platform: "linux/amd64",
              backend: "",
              version: "",
              in_flight: 1,
              capacity: 2,
              heartbeat_age_ms: 45000,
              reconnects: 2,
              rejected_1h: 4,
            },
            {
              id: "quiet-1",
              pool: "idle",
              platform: "linux/amd64",
              backend: "docker",
              version: "1.2.0",
              in_flight: 0,
              capacity: 1,
              heartbeat_age_ms: -1,
              reconnects: 0,
              rejected_1h: 0,
            },
          ],
          pools: [
            { pool: "gpu", queue_depth: 2, p50_time_to_assign_ms: 11000, rejected_1h: 4 },
            { pool: "idle", queue_depth: 0, p50_time_to_assign_ms: 0, rejected_1h: 0 },
          ],
        }),
      ),
    );
    await renderWorkers();
    expect(await screen.findByText("Stale")).toBeInTheDocument();
    expect(screen.getByText("never")).toBeInTheDocument();
    expect(screen.getAllByText("unknown").length).toBeGreaterThanOrEqual(2);
    const idle = screen.getByRole("region", { name: "idle" });
    expect(within(idle).getByText("—")).toBeInTheDocument();
    expect(within(idle).queryByText("0ms")).not.toBeInTheDocument();
    expect(
      screen.getByText("Sustained rejections usually mean the pool is too small."),
    ).toBeInTheDocument();
    await expectClean();
  });
});
