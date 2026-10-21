import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setAPIKey } from "../../api/session";
import type { RunScore } from "../../api/types";
import { demoAPIKey } from "../../mocks/demo-key";
import captured from "../../mocks/fixtures/m6-capture-benchmark-task.json";
import { server } from "../../mocks/server";
import { expectNoSeriousViolations } from "../../test/axe";
import { renderApp } from "../../test/render";
import { goldenRun, installRuns } from "./test-support";

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

const oldScore: RunScore = {
  scorer: "correctness",
  label: "missed_tests",
  score: 0.25,
  passed: false,
  reasoning: "The agent opened the PR without running tests.",
  status: "ok",
};

describe("run scores and capture", () => {
  it("renders score fields as text and drops unknown fields", async () => {
    installRuns();
    const poison = '<img src=x onerror="alert(1)"> ghp_PlantedSecretValue';
    const planted = "UNKNOWN_FIELD_PLANT";
    server.use(
      http.get("/api/v1/agent/runs/:runId/scores", () =>
        HttpResponse.json([
          {
            ...oldScore,
            reasoning: poison,
            planted_secret: planted,
          },
        ]),
      ),
    );
    await renderApp(`/runs/${goldenRun.id}`);
    const table = await screen.findByRole("table", { name: "Scores" });
    expect(within(table).getByRole("rowheader", { name: "correctness" })).toBeInTheDocument();
    expect(within(table).getByText("missed_tests")).toBeInTheDocument();
    expect(within(table).getByText("Not passed")).toBeInTheDocument();
    expect(within(table).getByText("0.25")).toBeInTheDocument();
    expect(within(table).getByText("OK")).toBeInTheDocument();
    expect(within(table).getByText(poison)).toBeInTheDocument();
    expect(document.querySelector("img")).toBeNull();
    expect(document.body.innerHTML).not.toContain("<img");
    expect(document.body.textContent).toContain("ghp_PlantedSecretValue");
    expect(document.body.textContent).not.toContain(planted);
  });

  it("replaces the shown score after score now accepts", async () => {
    const user = userEvent.setup();
    installRuns();
    let scores: RunScore[] = [oldScore];
    server.use(
      http.get("/api/v1/agent/runs/:runId/scores", () => HttpResponse.json(scores)),
      http.post("/api/v1/agent/runs/:runId/score", async ({ request }) => {
        const body = (await request.json()) as { scorer?: string };
        expect(body.scorer).toBe("correctness");
        return HttpResponse.json(
          { result_id: "sr_1", judge_run_id: "run_judge_1" },
          { status: 202 },
        );
      }),
    );
    const view = await renderApp(`/runs/${goldenRun.id}`);
    expect(await screen.findByText("0.25")).toBeInTheDocument();
    await user.selectOptions(screen.getByLabelText("Scorer"), "correctness");
    await user.click(screen.getByRole("button", { name: "Score now" }));
    const dialog = await screen.findByRole("dialog", { name: "Score this run?" });
    expect(dialog).toHaveTextContent("Score this run with correctness.");
    await user.click(within(dialog).getByRole("button", { name: "Score now" }));
    const table = await screen.findByRole("table", { name: "Scores" });
    expect(await within(table).findByText("Pending")).toBeInTheDocument();
    expect(within(table).queryByText("0.25")).not.toBeInTheDocument();
    expect(
      within(table).queryByText("The agent opened the PR without running tests."),
    ).not.toBeInTheDocument();
    expect(within(table).getAllByRole("row")).toHaveLength(2);
    scores = [
      {
        scorer: "correctness",
        label: "ran_tests",
        score: 0.9,
        passed: true,
        reasoning: "Tests ran.",
        status: "ok",
      },
    ];
    await view.queryClient.invalidateQueries({
      queryKey: ["scores", goldenRun.factory_id, goldenRun.id],
    });
    const replaced = await screen.findByRole("table", { name: "Scores" });
    expect(await within(replaced).findByText("0.9")).toBeInTheDocument();
    expect(within(replaced).getByText("Passed", { selector: "span" })).toBeInTheDocument();
    expect(within(replaced).getByText("Tests ran.")).toBeInTheDocument();
    expect(within(replaced).queryByText("Pending")).not.toBeInTheDocument();
    expect(within(replaced).queryByText("0.25")).not.toBeInTheDocument();
    expect(within(replaced).getAllByRole("row")).toHaveLength(2);
  });

  it("shows the 422 eligibility detail instead of Score now", async () => {
    const user = userEvent.setup();
    installRuns();
    server.use(
      http.post("/api/v1/agent/runs/:runId/score", () =>
        HttpResponse.json(
          { title: "unprocessable", status: 422, detail: "run has not finished" },
          { status: 422 },
        ),
      ),
    );
    await renderApp(`/runs/${goldenRun.id}`);
    await user.click(await screen.findByRole("button", { name: "Score now" }));
    const dialog = await screen.findByRole("dialog", { name: "Score this run?" });
    await user.click(within(dialog).getByRole("button", { name: "Score now" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("run has not finished");
    expect(screen.queryByRole("button", { name: "Score now" })).not.toBeInTheDocument();
  });

  it("shows captured benchmark YAML and 40-hex head refs", async () => {
    const user = userEvent.setup();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });
    installRuns();
    let body: unknown = null;
    server.use(
      http.post(
        ({ request }) => new URL(request.url).pathname.endsWith(":capture-benchmark-task"),
        async ({ request }) => {
          body = await request.json();
          return HttpResponse.json(captured);
        },
      ),
    );
    await renderApp(`/runs/${goldenRun.id}`);
    await screen.findByRole("button", { name: "Capture as benchmark task" });
    await user.type(screen.getByLabelText("Suite"), "pr-review");
    await user.type(screen.getByLabelText("Name"), "broken-doc-link");
    await user.click(screen.getByRole("button", { name: "Capture as benchmark task" }));
    expect(await screen.findByText(captured.head_refs[0]?.sha ?? "")).toBeInTheDocument();
    expect(captured.head_refs[0]?.sha).toMatch(/^[0-9a-f]{40}$/);
    expect(document.querySelector("pre")).toHaveTextContent(
      "title: Fix a broken documentation link",
    );
    expect(body).toEqual({ suite: "pr-review", name: "broken-doc-link" });
    await user.click(screen.getByRole("button", { name: "Copy benchmark task" }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(captured.content));
    expect(screen.getByText("Benchmark task copied.")).toBeInTheDocument();
  });

  it("has no serious accessibility violations with scores", async () => {
    const user = userEvent.setup();
    installRuns();
    await renderApp(`/runs/${goldenRun.id}`);
    expect(await screen.findByRole("button", { name: "Score now" })).toBeInTheDocument();
    expect(screen.getByText("The agent opened the PR without running tests.")).toBeInTheDocument();
    await expectNoSeriousViolations(document.body);
    await user.click(screen.getByRole("button", { name: "Switch to dark theme" }));
    await expectNoSeriousViolations(document.body);
  });
});
