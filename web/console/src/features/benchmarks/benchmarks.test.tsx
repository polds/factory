import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import { setAPIKey } from "../../api/session";
import benchmarks from "../../mocks/fixtures/m6-benchmarks.json";
import { demoAPIKey } from "../../mocks/demo-key";
import { server } from "../../mocks/server";
import { renderApp } from "../../test/render";

const factory = "/f/fac_payments";

const candidate = {
  configuration: "Candidate",
  task: "broken-doc-link",
  n: 2,
  pass: 0,
  errored: 3,
  pass_rate: 0.4,
  ci_low: 0.1,
  ci_high: 0.2,
  median_cost_cents: 12,
  median_duration_ms: 45000,
  tokens_by_model: [{ model_id: "judge", input_tokens: 1, output_tokens: 1 }],
  value: "planted-cell-value",
};

const baseline = {
  configuration: "Baseline",
  task: "broken-doc-link",
  n: 2,
  pass: 2,
  errored: 0,
  pass_rate: 0.5,
  ci_low: 0.25,
  ci_high: 0.75,
  median_cost_cents: 20,
  median_duration_ms: 60000,
  tokens_by_model: [],
};

function trials(winner: string, reason: string) {
  return HttpResponse.json({
    cells: [candidate, baseline],
    report: { winner, reason },
  });
}

async function tabTo(user: { tab: () => Promise<void> }, target: HTMLElement) {
  for (let step = 0; step < 40 && document.activeElement !== target; step += 1) {
    await user.tab();
  }
  expect(document.activeElement).toBe(target);
}

describe("benchmarks", () => {
  it("lists suites with the agent, configurations, tasks, and last run", async () => {
    const paths: string[] = [];
    const onStart = ({ request }: { request: Request }) => {
      paths.push(new URL(request.url).pathname);
    };
    server.events.on("request:start", onStart);
    setAPIKey(demoAPIKey);
    try {
      await renderApp(`${factory}/benchmarks`);
      expect(await screen.findByRole("link", { name: "pr-review" })).toHaveAttribute(
        "href",
        `${factory}/benchmarks/pr-review`,
      );
    } finally {
      server.events.removeListener("request:start", onStart);
    }
    expect(screen.getByText("reviewer")).toBeInTheDocument();
    expect(screen.getByText("Baseline, Candidate")).toBeInTheDocument();
    expect(screen.getByText("broken-doc-link")).toBeInTheDocument();
    expect(screen.getByText(/br_1/)).toBeInTheDocument();
    expect(screen.getByText(/5 repetitions/)).toBeInTheDocument();
    expect(screen.getByText(benchmarks[0].last_run.created_at)).toBeInTheDocument();
    expect(paths).toContain("/api/v1/factory/fac_payments/events");
    expect(paths).toContain("/api/v1/factory/fac_payments/benchmarks");
  });

  it("renders server trial numbers, marks the winner, and drops unknown cell fields", async () => {
    server.use(
      http.get("/api/v1/factory/:uid/benchmarks/:suite/trials", () =>
        trials("Candidate", "higher pass rate than Baseline"),
      ),
    );
    setAPIKey(demoAPIKey);
    await renderApp(`${factory}/benchmarks/pr-review`);
    expect(await screen.findByText("40%")).toBeInTheDocument();
    expect(screen.getByText("50%")).toBeInTheDocument();
    expect(screen.getByText("10%–20%")).toBeInTheDocument();
    expect(screen.getByText("25%–75%")).toBeInTheDocument();
    expect(screen.queryByText("0%")).not.toBeInTheDocument();
    expect(screen.getByText("$0.12")).toBeInTheDocument();
    expect(screen.getByText("45s")).toBeInTheDocument();
    expect(screen.getByText("$0.20")).toBeInTheDocument();
    expect(screen.getByText("1m")).toBeInTheDocument();
    expect(screen.getByText("Infrastructure")).toBeInTheDocument();
    expect(screen.getByText("3")).toBeInTheDocument();
    const candidateRow = screen.getByRole("rowheader", { name: /Candidate/ });
    expect(within(candidateRow).getByText("Winner")).toBeInTheDocument();
    expect(
      within(screen.getByRole("rowheader", { name: "Baseline" })).queryByText("Winner"),
    ).toBeNull();
    expect(screen.getByText("higher pass rate than Baseline")).toBeInTheDocument();
    expect(document.querySelector(".callout")).toBeNull();
    expect(document.body.innerHTML).not.toContain("planted-cell-value");
  });

  it("shows the refusal and highlights no cell when the winner is empty", async () => {
    server.use(
      http.get("/api/v1/factory/:uid/benchmarks/:suite/trials", () =>
        trials("", "fewer than 5 repetitions"),
      ),
    );
    setAPIKey(demoAPIKey);
    await renderApp(`${factory}/benchmarks/pr-review`);
    const callout = await screen.findByText("fewer than 5 repetitions");
    expect(callout.closest(".callout")).not.toBeNull();
    expect(screen.queryByText("Winner")).not.toBeInTheDocument();
    expect(screen.getByText("40%")).toBeInTheDocument();
    expect(document.querySelectorAll("[data-winner]")).toHaveLength(0);
  });

  it("shows a 422 detail inline when repetitions are rejected", async () => {
    const user = userEvent.setup();
    let body = "";
    server.use(
      http.post("/api/v1/factory/:uid/benchmarks/:suite", async ({ request }) => {
        body = await request.text();
        return HttpResponse.json(
          { title: "unprocessable", status: 422, detail: "repetitions must be at least 1" },
          { status: 422 },
        );
      }),
    );
    setAPIKey(demoAPIKey);
    await renderApp(`${factory}/benchmarks/pr-review`);
    const input = await screen.findByRole("spinbutton", { name: "Repetitions" });
    await user.clear(input);
    await user.type(input, "8");
    await user.click(screen.getByRole("button", { name: "Run benchmark" }));
    expect(await screen.findByRole("dialog", { name: "Run benchmark?" })).toHaveTextContent(
      "Run pr-review with 8 repetitions.",
    );
    await user.click(screen.getByRole("button", { name: "Run benchmark" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("repetitions must be at least 1");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(body).toBe(JSON.stringify({ repetitions: 8 }));
  });

  it("runs a benchmark from the keyboard", async () => {
    const user = userEvent.setup();
    let posts = 0;
    let body = "";
    server.use(
      http.post("/api/v1/factory/:uid/benchmarks/:suite", async ({ request }) => {
        posts += 1;
        body = await request.text();
        return HttpResponse.json({ benchmark_run_id: "br_9" }, { status: 202 });
      }),
    );
    setAPIKey(demoAPIKey);
    await renderApp(`${factory}/benchmarks/pr-review`);
    const opener = await screen.findByRole("button", { name: "Run benchmark" });
    await tabTo(user, opener);
    await user.keyboard("{Enter}");
    expect(await screen.findByRole("dialog", { name: "Run benchmark?" })).toHaveTextContent(
      "Run pr-review with 5 repetitions.",
    );
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(posts).toBe(0);
    await tabTo(user, screen.getByRole("button", { name: "Run benchmark" }));
    await user.keyboard("{Enter}");
    const dialog = await screen.findByRole("dialog", { name: "Run benchmark?" });
    await tabTo(user, within(dialog).getByRole("button", { name: "Run benchmark" }));
    await user.keyboard("{Enter}");
    expect(await screen.findByRole("status")).toHaveTextContent("Benchmark run br_9 started.");
    expect(posts).toBe(1);
    expect(body).toBe(JSON.stringify({ repetitions: 5 }));
  });

  it("returns focus to Run benchmark after escape and confirm", async () => {
    const user = userEvent.setup();
    server.use(
      http.post("/api/v1/factory/:uid/benchmarks/:suite", () => {
        return HttpResponse.json({ benchmark_run_id: "br_9" }, { status: 202 });
      }),
    );
    setAPIKey(demoAPIKey);
    await renderApp(`${factory}/benchmarks/pr-review`);
    const opener = await screen.findByRole("button", { name: "Run benchmark" });
    await tabTo(user, opener);
    await user.keyboard("{Enter}");
    expect(await screen.findByRole("dialog", { name: "Run benchmark?" })).toBeInTheDocument();
    await user.keyboard("{Escape}");
    await waitFor(() => expect(opener).toHaveFocus());

    await user.keyboard("{Enter}");
    const dialog = await screen.findByRole("dialog", { name: "Run benchmark?" });
    await tabTo(user, within(dialog).getByRole("button", { name: "Run benchmark" }));
    await user.keyboard("{Enter}");
    await waitFor(() => expect(opener).toHaveFocus());
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});
