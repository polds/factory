import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import { APIError, ConsoleClient } from "./client";
import { setAPIKey } from "./session";
import { demoAPIKey } from "../mocks/demo-key";
import { server } from "../mocks/server";
import benchmarkAccepted from "../mocks/fixtures/m6-benchmark-accepted.json";
import benchmarks from "../mocks/fixtures/m6-benchmarks.json";
import benchmarkTrials from "../mocks/fixtures/m6-benchmark-trials.json";
import capturedTask from "../mocks/fixtures/m6-capture-benchmark-task.json";
import improvementBatches from "../mocks/fixtures/m6-improvement-batches.json";
import improvementRunNow from "../mocks/fixtures/m6-improvement-run-now.json";
import metrics from "../mocks/fixtures/m6-metrics.json";
import runScores from "../mocks/fixtures/m6-run-scores.json";
import scoreAccepted from "../mocks/fixtures/m6-score-accepted.json";
import scorerResults from "../mocks/fixtures/m6-scorer-results.json";
import scorers from "../mocks/fixtures/m6-scorers.json";

const problem = {
  type: "about:blank",
  title: "Unprocessable Entity",
  status: 422,
  detail: "unknown scorer",
};

type Seen = { url: string; method: string; body: string };

function install(
  method: "GET" | "POST",
  path: string,
  status: number,
  payload: object,
): () => Seen {
  const seen: Seen = { url: "", method: "", body: "" };
  const respond = async ({ request }: { request: Request }) => {
    seen.url = request.url;
    seen.method = request.method;
    seen.body = await request.text();
    return HttpResponse.json(payload, { status });
  };
  server.use(method === "GET" ? http.get(path, respond) : http.post(path, respond));
  return () => seen;
}

function expectProblem(error: unknown) {
  expect(error).toBeInstanceOf(APIError);
  const api = error as APIError;
  expect(api.status).toBe(422);
  expect(api.problem?.detail).toBe("unknown scorer");
  expect(String(error)).not.toContain(demoAPIKey);
  expect(JSON.stringify(error)).not.toContain(demoAPIKey);
}

describe("m6 client", () => {
  const api = new ConsoleClient();

  it("scores a run", async () => {
    setAPIKey(demoAPIKey);
    const seen = install("POST", "/api/v1/agent/runs/:runId/score", 202, scoreAccepted);
    const accepted = await api.scoreRun("run_1", "correctness");
    expect(seen().method).toBe("POST");
    expect(new URL(seen().url).pathname).toBe("/api/v1/agent/runs/run_1/score");
    expect(seen().body).toBe(JSON.stringify({ scorer: "correctness" }));
    expect(accepted.result_id).toBe("sr_1");
    install("POST", "/api/v1/agent/runs/:runId/score", 422, problem);
    expectProblem(
      await api.scoreRun("run_1", "missing").then(
        () => null,
        (error: unknown) => error,
      ),
    );
  });

  it("lists scorer results", async () => {
    setAPIKey(demoAPIKey);
    const seen = install("GET", "/api/v1/factory/:uid/scorers/:id/results", 200, scorerResults);
    const rows = await api.scorerResults("fac_1", "correctness", {
      from: "2026-04-01T00:00:00Z",
      to: "2026-04-02T00:00:00Z",
      passed: false,
    });
    const url = new URL(seen().url);
    expect(seen().method).toBe("GET");
    expect(url.pathname).toBe("/api/v1/factory/fac_1/scorers/correctness/results");
    expect(url.searchParams.get("passed")).toBe("false");
    expect(url.searchParams.get("from")).toBe("2026-04-01T00:00:00Z");
    expect(seen().body).toBe("");
    expect(rows[0]?.label).toBe("missed_tests");
    install("GET", "/api/v1/factory/:uid/scorers/:id/results", 422, problem);
    expectProblem(
      await api.scorerResults("fac_1", "correctness").then(
        () => null,
        (error: unknown) => error,
      ),
    );
  });

  it("lists scorers", async () => {
    setAPIKey(demoAPIKey);
    const seen = install("GET", "/api/v1/factory/:uid/scorers", 200, scorers);
    const rows = await api.listScorers("fac_1");
    expect(seen().method).toBe("GET");
    expect(new URL(seen().url).pathname).toBe("/api/v1/factory/fac_1/scorers");
    expect(rows[0]?.name).toBe("correctness");
    install("GET", "/api/v1/factory/:uid/scorers", 422, problem);
    expectProblem(
      await api.listScorers("fac_1").then(
        () => null,
        (error: unknown) => error,
      ),
    );
  });

  it("lists run scores", async () => {
    setAPIKey(demoAPIKey);
    const seen = install("GET", "/api/v1/agent/runs/:runId/scores", 200, runScores);
    const rows = await api.runScores("run_1");
    expect(seen().method).toBe("GET");
    expect(new URL(seen().url).pathname).toBe("/api/v1/agent/runs/run_1/scores");
    expect(rows[0]?.scorer).toBe("correctness");
    install("GET", "/api/v1/agent/runs/:runId/scores", 422, problem);
    expectProblem(
      await api.runScores("run_1").then(
        () => null,
        (error: unknown) => error,
      ),
    );
  });

  it("starts a benchmark", async () => {
    setAPIKey(demoAPIKey);
    const seen = install("POST", "/api/v1/factory/:uid/benchmarks/:suite", 202, benchmarkAccepted);
    const accepted = await api.startBenchmark("fac_1", "pr-review", { repetitions: 5 });
    expect(seen().method).toBe("POST");
    expect(new URL(seen().url).pathname).toBe("/api/v1/factory/fac_1/benchmarks/pr-review:run");
    expect(seen().body).toBe(JSON.stringify({ repetitions: 5 }));
    expect(accepted.benchmark_run_id).toBe("br_1");
    install("POST", "/api/v1/factory/:uid/benchmarks/:suite", 422, problem);
    expectProblem(
      await api.startBenchmark("fac_1", "pr-review", { repetitions: 0 }).then(
        () => null,
        (error: unknown) => error,
      ),
    );
  });

  it("lists benchmark trials", async () => {
    setAPIKey(demoAPIKey);
    const seen = install(
      "GET",
      "/api/v1/factory/:uid/benchmarks/:suite/trials",
      200,
      benchmarkTrials,
    );
    const view = await api.benchmarkTrials("fac_1", "pr-review", { benchmark_run_id: "br_1" });
    const url = new URL(seen().url);
    expect(seen().method).toBe("GET");
    expect(url.pathname).toBe("/api/v1/factory/fac_1/benchmarks/pr-review/trials");
    expect(url.searchParams.get("benchmark_run_id")).toBe("br_1");
    expect(view.report.winner).toBe("Candidate");
    install("GET", "/api/v1/factory/:uid/benchmarks/:suite/trials", 422, problem);
    expectProblem(
      await api.benchmarkTrials("fac_1", "pr-review").then(
        () => null,
        (error: unknown) => error,
      ),
    );
  });

  it("lists benchmarks", async () => {
    setAPIKey(demoAPIKey);
    const seen = install("GET", "/api/v1/factory/:uid/benchmarks", 200, benchmarks);
    const rows = await api.listBenchmarks("fac_1");
    expect(seen().method).toBe("GET");
    expect(new URL(seen().url).pathname).toBe("/api/v1/factory/fac_1/benchmarks");
    expect(rows[0]?.suite).toBe("pr-review");
    install("GET", "/api/v1/factory/:uid/benchmarks", 422, problem);
    expectProblem(
      await api.listBenchmarks("fac_1").then(
        () => null,
        (error: unknown) => error,
      ),
    );
  });

  it("captures a benchmark task", async () => {
    setAPIKey(demoAPIKey);
    const seen = install("POST", "/api/v1/agent/runs/:runId", 200, capturedTask);
    const task = await api.captureBenchmarkTask("run_1", {
      suite: "pr-review",
      name: "broken-doc-link",
    });
    expect(seen().method).toBe("POST");
    expect(new URL(seen().url).pathname).toBe("/api/v1/agent/runs/run_1:capture-benchmark-task");
    expect(seen().body).toBe(JSON.stringify({ suite: "pr-review", name: "broken-doc-link" }));
    expect(task.content).toContain("sourceRunId: run_1");
    expect(task.head_refs[0]?.sha).toHaveLength(40);
    install("POST", "/api/v1/agent/runs/:runId", 422, problem);
    expectProblem(
      await api.captureBenchmarkTask("run_1", { suite: "pr-review", name: "broken-doc-link" }).then(
        () => null,
        (error: unknown) => error,
      ),
    );
  });

  it("lists improvement batches", async () => {
    setAPIKey(demoAPIKey);
    const seen = install(
      "GET",
      "/api/v1/factory/:uid/improvement-batches",
      200,
      improvementBatches,
    );
    const rows = await api.improvementBatches("fac_1");
    expect(seen().method).toBe("GET");
    expect(new URL(seen().url).pathname).toBe("/api/v1/factory/fac_1/improvement-batches");
    expect(rows[0]?.status).toBe("pr_open");
    install("GET", "/api/v1/factory/:uid/improvement-batches", 422, problem);
    expectProblem(
      await api.improvementBatches("fac_1").then(
        () => null,
        (error: unknown) => error,
      ),
    );
  });

  it("runs improvement now", async () => {
    setAPIKey(demoAPIKey);
    const seen = install(
      "POST",
      "/api/v1/factory/:uid/improvement:run-now",
      202,
      improvementRunNow,
    );
    const out = await api.runImprovementNow("fac_1");
    expect(seen().method).toBe("POST");
    expect(new URL(seen().url).pathname).toBe("/api/v1/factory/fac_1/improvement:run-now");
    expect(seen().body).toBe("");
    expect(out.checked_agents).toEqual(["reviewer"]);
    install("POST", "/api/v1/factory/:uid/improvement:run-now", 422, problem);
    expectProblem(
      await api.runImprovementNow("fac_1").then(
        () => null,
        (error: unknown) => error,
      ),
    );
  });

  it("reads metrics", async () => {
    setAPIKey(demoAPIKey);
    const seen = install("GET", "/api/v1/factory/:uid/metrics", 200, metrics);
    const view = await api.metrics("fac_1", {
      from: "2026-04-01T00:00:00Z",
      to: "2026-04-02T00:00:00Z",
    });
    const url = new URL(seen().url);
    expect(seen().method).toBe("GET");
    expect(url.pathname).toBe("/api/v1/factory/fac_1/metrics");
    expect(url.searchParams.get("from")).toBe("2026-04-01T00:00:00Z");
    expect(url.searchParams.get("to")).toBe("2026-04-02T00:00:00Z");
    expect(view.prs_opened).toBe(2);
    expect(view.cost_per_pr.by_size.XL).toBe(200);
    install("GET", "/api/v1/factory/:uid/metrics", 422, problem);
    expectProblem(
      await api.metrics("fac_1").then(
        () => null,
        (error: unknown) => error,
      ),
    );
  });
});
