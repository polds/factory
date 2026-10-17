import { http, HttpResponse } from "msw";
import { demoAPIKey } from "./demo-key";
import automations from "./fixtures/automations.json";
import definition from "./fixtures/definition.json";
import factories from "./fixtures/factories.json";
import identities from "./fixtures/identities.json";
import inbox from "./fixtures/inbox.json";
import problem from "./fixtures/problem.json";
import runDetail from "./fixtures/run-detail.json";
import runList from "./fixtures/run-list.json";
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
  http.post("/api/v1/agent/runs/:id/interrupt-conflict", () =>
    HttpResponse.json(problem, { status: 409 }),
  ),
];
