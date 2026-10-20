import { http, HttpResponse } from "msw";
import { demoAPIKey } from "./demo-key";
import automations from "./fixtures/automations.json";
import benchmarkAccepted from "./fixtures/m6-benchmark-accepted.json";
import benchmarks from "./fixtures/m6-benchmarks.json";
import benchmarkTrials from "./fixtures/m6-benchmark-trials.json";
import capturedTask from "./fixtures/m6-capture-benchmark-task.json";
import definition from "./fixtures/definition.json";
import factories from "./fixtures/factories.json";
import identities from "./fixtures/identities.json";
import improvementBatches from "./fixtures/m6-improvement-batches.json";
import improvementRunNow from "./fixtures/m6-improvement-run-now.json";
import inbox from "./fixtures/inbox.json";
import lifecycle from "./fixtures/factory-event-lifecycle.json";
import metrics from "./fixtures/m6-metrics.json";
import problem from "./fixtures/problem.json";
import runDetail from "./fixtures/run-detail.json";
import runList from "./fixtures/run-list.json";
import runScores from "./fixtures/m6-run-scores.json";
import scoreAccepted from "./fixtures/m6-score-accepted.json";
import scorerResults from "./fixtures/m6-scorer-results.json";
import scorers from "./fixtures/m6-scorers.json";
import secrets from "./fixtures/secrets.json";
import steerAccepted from "./fixtures/steer-accepted.json";
import steerPoll from "./fixtures/steer-poll.json";
import summary from "./fixtures/summary.json";
import timeline from "./fixtures/timeline.json";
import usage from "./fixtures/harness-usage.json";
import webhooks from "./fixtures/webhooks.json";
import workers from "./fixtures/workers.json";

function authorized(request: Request): boolean {
  return request.headers.get("authorization") === `Bearer ${demoAPIKey}`;
}

function deny() {
  return HttpResponse.json(
    { type: "about:blank", title: "unauthorized", status: 401, detail: "unauthorized" },
    { status: 401 },
  );
}

function factoryEvents() {
  const frame = `id: ${lifecycle.gid}\nevent: run.status\ndata: ${JSON.stringify(lifecycle)}\n\n`;
  const stream = new ReadableStream({
    start(controller) {
      controller.enqueue(new TextEncoder().encode(frame));
    },
  });
  return new HttpResponse(stream, {
    headers: { "Content-Type": "text/event-stream", "Cache-Control": "no-cache" },
  });
}

export const handlers = [
  http.get("/api/v1/factory", ({ request }) => {
    if (!authorized(request)) return deny();
    return HttpResponse.json(factories);
  }),
  http.get("/api/v1/factory/:uid/definition", ({ request }) => {
    if (!authorized(request)) return deny();
    return HttpResponse.json(definition);
  }),
  http.get("/api/v1/agent/runs", ({ request }) => {
    if (!authorized(request)) return deny();
    return HttpResponse.json(runList);
  }),
  http.get("/api/v1/agent/runs/:id/steers", ({ request }) => {
    if (!authorized(request)) return deny();
    return HttpResponse.json([]);
  }),
  http.get("/api/v1/agent/runs/:id", ({ request }) => {
    if (!authorized(request)) return deny();
    return HttpResponse.json(runDetail);
  }),
  http.get("/api/v1/agent/runs/:id/timeline", ({ request }) => {
    if (!authorized(request)) return deny();
    return HttpResponse.json(timeline);
  }),
  http.get("/api/v1/agent/runs/:id/harness-usage", ({ request }) => {
    if (!authorized(request)) return deny();
    return HttpResponse.json(usage);
  }),
  http.post("/api/v1/agent/runs/:id/interrupt", ({ request }) => {
    if (!authorized(request)) return deny();
    return HttpResponse.json(steerAccepted, { status: 202 });
  }),
  http.post("/api/v1/agent/runs/:id/followups", ({ request }) => {
    if (!authorized(request)) return deny();
    return HttpResponse.json(steerAccepted, { status: 202 });
  }),
  http.get("/api/v1/agent/runs/:id/steer", ({ request }) => {
    if (request.headers.get("authorization") !== "Bearer rt_test_run_token") return deny();
    return HttpResponse.json(steerPoll);
  }),
  http.get("/api/v1/factory/:uid/inbox", ({ request }) => {
    if (!authorized(request)) return deny();
    return HttpResponse.json(inbox);
  }),
  http.get("/api/v1/factory/:uid/automations", ({ request }) => {
    if (!authorized(request)) return deny();
    return HttpResponse.json(automations);
  }),
  http.post("/api/v1/factory/:uid/automations/:name/pause", ({ request }) => {
    if (!authorized(request)) return deny();
    return new HttpResponse(null, { status: 204 });
  }),
  http.post("/api/v1/factory/:uid/automations/:name/resume", ({ request }) => {
    if (!authorized(request)) return deny();
    return new HttpResponse(null, { status: 204 });
  }),
  http.get("/api/v1/factory/:uid/webhooks/:webhookUid/deliveries", ({ request }) => {
    if (!authorized(request)) return deny();
    return HttpResponse.json([]);
  }),
  http.get("/api/v1/factory/:uid/webhooks", ({ request }) => {
    if (!authorized(request)) return deny();
    return HttpResponse.json(webhooks);
  }),
  http.get("/api/v1/agent/connected-self-hosted-workers", ({ request }) => {
    if (!authorized(request)) return deny();
    return HttpResponse.json(workers);
  }),
  http.get("/api/v1/factory/:uid/secrets", ({ request }) => {
    if (!authorized(request)) return deny();
    return HttpResponse.json(secrets);
  }),
  http.get("/api/v1/agent/identities", ({ request }) => {
    if (!authorized(request)) return deny();
    return HttpResponse.json(identities);
  }),
  http.get("/api/v1/factory/:uid/summary", ({ request }) => {
    if (!authorized(request)) return deny();
    return HttpResponse.json(summary);
  }),
  http.get("/api/v1/factory/:uid/events", ({ request }) => {
    if (!authorized(request)) return deny();
    return factoryEvents();
  }),
  http.post("/api/v1/agent/runs/:runId/score", ({ request }) => {
    if (!authorized(request)) return deny();
    return HttpResponse.json(scoreAccepted, { status: 202 });
  }),
  http.get("/api/v1/factory/:uid/scorers/:id/results", ({ request }) => {
    if (!authorized(request)) return deny();
    return HttpResponse.json(scorerResults);
  }),
  http.get("/api/v1/factory/:uid/scorers", ({ request }) => {
    if (!authorized(request)) return deny();
    return HttpResponse.json(scorers);
  }),
  http.get("/api/v1/agent/runs/:runId/scores", ({ request }) => {
    if (!authorized(request)) return deny();
    return HttpResponse.json(runScores);
  }),
  http.post("/api/v1/factory/:uid/benchmarks/:suite", ({ request }) => {
    if (!authorized(request)) return deny();
    return HttpResponse.json(benchmarkAccepted, { status: 202 });
  }),
  http.get("/api/v1/factory/:uid/benchmarks/:suite/trials", ({ request }) => {
    if (!authorized(request)) return deny();
    return HttpResponse.json(benchmarkTrials);
  }),
  http.get("/api/v1/factory/:uid/benchmarks", ({ request }) => {
    if (!authorized(request)) return deny();
    return HttpResponse.json(benchmarks);
  }),
  http.post("/api/v1/agent/runs/:runId", ({ request }) => {
    if (!authorized(request)) return deny();
    return HttpResponse.json(capturedTask);
  }),
  http.get("/api/v1/factory/:uid/improvement-batches", ({ request }) => {
    if (!authorized(request)) return deny();
    return HttpResponse.json(improvementBatches);
  }),
  http.post("/api/v1/factory/:uid/improvement:run-now", ({ request }) => {
    if (!authorized(request)) return deny();
    return HttpResponse.json(improvementRunNow, { status: 202 });
  }),
  http.get("/api/v1/factory/:uid/metrics", ({ request }) => {
    if (!authorized(request)) return deny();
    return HttpResponse.json(metrics);
  }),
  http.post("/api/v1/agent/runs/:id/interrupt-conflict", () =>
    HttpResponse.json(problem, { status: 409 }),
  ),
];
