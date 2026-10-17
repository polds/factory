import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import axe from "axe-core";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import { setAPIKey } from "../../api/session";
import type { Task } from "../../api/types";
import { demoAPIKey } from "../../mocks/demo-key";
import { server } from "../../mocks/server";
import { renderApp } from "../../test/render";

const factoryId = "fac_payments";

function task(partial: Partial<Task> & Pick<Task, "id" | "title" | "stage" | "status">): Task {
  return {
    factory_id: factoryId,
    source: "github",
    revision_count: 0,
    created_at: "2026-10-17T12:00:00Z",
    ...partial,
  };
}

const boardTasks: Task[] = [
  task({
    id: "task_01",
    title: "Refund webhook retries",
    stage: "triage",
    status: "open",
    source_ref: "PAY-1",
  }),
  task({
    id: "task_02",
    title: "Migrate invoice PDFs",
    stage: "planning",
    status: "awaiting_approval",
    pending_gate: "spec",
    source: "linear",
    source_ref: "PAY-231",
  }),
  task({
    id: "task_03",
    title: "Bump stripe-go",
    stage: "planning",
    status: "open",
    source: "cli",
  }),
  task({
    id: "task_04",
    title: "Flaky settlement cutoff",
    stage: "building",
    status: "awaiting_input",
    revision_count: 2,
    source_ref: "acme/payments-api#398",
  }),
  task({ id: "task_05", title: "Expose dispute deadline", stage: "building", status: "open" }),
  task({
    id: "task_06",
    title: "Weekly dependency refresh",
    stage: "building",
    status: "open",
    source: "schedule",
  }),
  task({ id: "task_07", title: "Rate-limit charges", stage: "reviewing", status: "open" }),
  task({
    id: "task_08",
    title: "Retry ACH returns",
    stage: "human_handoff",
    status: "open",
    source_ref: "PAY-204",
  }),
  task({ id: "task_09", title: "Finished the migration", stage: "reviewing", status: "complete" }),
  task({
    id: "task_10",
    title: "Dropped the plan",
    stage: "triage",
    status: "cancelled",
    source: "slack",
  }),
];

// ListRuns is newest first. The older row is last, which is what used to be chosen.
const boardRuns = [
  {
    id: "run_new",
    created_at: "2026-10-17T12:00:00Z",
    status: "running",
    work_item_id: "task_04",
    agent: "implementer",
    model_id: "model-a",
    queue_wait_ms: 1000,
    exec_ms: 2000,
  },
  {
    id: "run_old",
    created_at: "2026-10-17T09:00:00Z",
    status: "failed",
    work_item_id: "task_04",
    agent: "planner",
    model_id: "old-model",
    failure_class: "agent",
  },
];

async function expectClean(node: ParentNode) {
  const results = await axe.run(node as Element);
  const blocking = results.violations.filter(
    (violation) => violation.impact === "serious" || violation.impact === "critical",
  );
  expect(blocking, JSON.stringify(blocking, null, 2)).toEqual([]);
}

function installBoard(tasks: Task[] = boardTasks) {
  let runCalls = 0;
  server.use(
    http.get("/api/v1/factory/:factoryId/tasks", () => HttpResponse.json(tasks)),
    http.get("/api/v1/agent/runs", () => {
      runCalls += 1;
      return HttpResponse.json(boardRuns);
    }),
  );
  return {
    runCalls: () => runCalls,
  };
}

describe("work items", () => {
  it("places tasks in stage columns and reads agent from one run list", async () => {
    setAPIKey(demoAPIKey);
    const board = installBoard();
    await renderApp(`/f/${factoryId}/work-items`);
    expect(await screen.findByRole("link", { name: /Refund webhook retries/ })).toBeInTheDocument();
    const triage = document.getElementById("col-triage")?.closest("section");
    const planning = document.getElementById("col-planning")?.closest("section");
    const building = document.getElementById("col-building")?.closest("section");
    const reviewing = document.getElementById("col-reviewing")?.closest("section");
    const handoff = document.getElementById("col-human_handoff")?.closest("section");
    const done = document.getElementById("col-done")?.closest("section");
    expect(triage).toHaveTextContent("Refund webhook retries");
    expect(triage).not.toHaveTextContent("Dropped the plan");
    expect(planning).toHaveTextContent("Migrate invoice PDFs");
    expect(building).toHaveTextContent("Flaky settlement cutoff");
    expect(reviewing).toHaveTextContent("Rate-limit charges");
    expect(handoff).toHaveTextContent("Handoff");
    expect(handoff).toHaveTextContent("Retry ACH returns");
    expect(done).toHaveTextContent("Finished the migration");
    expect(done).toHaveTextContent("Dropped the plan");
    expect(done).toHaveTextContent("2");
    const approval = document.querySelector("[data-waiting='awaiting_approval']");
    const input = document.querySelector("[data-waiting='awaiting_input']");
    expect(approval).toHaveTextContent("Awaiting approval");
    expect(input).toHaveTextContent("Awaiting input");
    expect(approval).toHaveAttribute("data-icon", "shield-question");
    expect(input).toHaveAttribute("data-icon", "message-circle-question");
    expect(approval).toHaveStyle({ borderStyle: "solid" });
    expect(input).toHaveStyle({ borderStyle: "dashed" });
    expect(approval?.querySelector("svg")).not.toBeNull();
    expect(input?.querySelector("svg")).not.toBeNull();
    expect(approval?.querySelector("svg")?.getAttribute("class")).not.toBe(
      input?.querySelector("svg")?.getAttribute("class"),
    );
    expect(screen.getByRole("link", { name: /Flaky settlement cutoff/ })).toHaveTextContent(
      "implementer · model-a",
    );
    expect(screen.getByRole("link", { name: /Flaky settlement cutoff/ })).toHaveTextContent(
      "rev 2",
    );
    expect(document.querySelectorAll("a.work-card")).toHaveLength(10);
    await waitFor(() => expect(board.runCalls()).toBe(1));
  });

  it("creates a work item and keeps the form open when the API rejects it", async () => {
    setAPIKey(demoAPIKey);
    const user = userEvent.setup();
    const bodies: unknown[] = [];
    let tasks = boardTasks.slice(0, 1);
    server.use(
      http.get("/api/v1/factory/:factoryId/tasks", () => HttpResponse.json(tasks)),
      http.get("/api/v1/agent/runs", () => HttpResponse.json([])),
      http.post("/api/v1/factory/:factoryId/tasks", async ({ request }) => {
        const body = await request.json();
        bodies.push(body);
        if (bodies.length === 1) {
          return HttpResponse.json(
            { type: "about:blank", title: "bad request", status: 400, detail: "source is invalid" },
            { status: 400 },
          );
        }
        const created = task({
          id: "task_new",
          title: "Pin the digest",
          stage: "triage",
          status: "open",
          source: "manual",
          source_ref: "acme/payments-api#420",
          body: "Pin every base image.",
        });
        tasks = [...tasks, created];
        return HttpResponse.json(created, { status: 201 });
      }),
    );
    await renderApp(`/f/${factoryId}/work-items`);
    const opener = await screen.findByRole("button", { name: "New work item" });
    await user.click(opener);
    const title = await screen.findByRole("textbox", { name: "Title" });
    expect(title).toHaveFocus();
    const dialog = screen.getByRole("dialog");
    const source = within(dialog).getByRole("combobox", { name: "Source" });
    expect(source).toHaveValue("manual");
    expect(
      within(source)
        .getAllByRole("option")
        .map((option) => option.textContent),
    ).toEqual(["manual", "issue", "api"]);
    await user.type(title, "Pin the digest");
    await user.type(screen.getByRole("textbox", { name: "Body" }), "Pin every base image.");
    await user.type(screen.getByRole("textbox", { name: "Source ref" }), "acme/payments-api#420");
    await user.click(screen.getByRole("button", { name: "Create" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("source is invalid");
    expect(screen.getByRole("textbox", { name: "Title" })).toHaveValue("Pin the digest");
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(bodies).toHaveLength(1);
    await user.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.getByRole("link", { name: /Pin the digest/ })).toBeInTheDocument();
    expect(bodies[0]).toEqual({
      title: "Pin the digest",
      body: "Pin every base image.",
      source: "manual",
      source_ref: "acme/payments-api#420",
    });
    expect(bodies).toHaveLength(2);
  });

  it("returns focus to New work item when the dialog closes", async () => {
    setAPIKey(demoAPIKey);
    const user = userEvent.setup();
    installBoard([boardTasks[0]!]);
    await renderApp(`/f/${factoryId}/work-items`);
    const opener = await screen.findByRole("button", { name: "New work item" });
    await user.click(opener);
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(opener).toHaveFocus();
  });

  it("keeps the board and the open dialog free of serious accessibility violations", async () => {
    setAPIKey(demoAPIKey);
    const user = userEvent.setup();
    installBoard();
    await renderApp(`/f/${factoryId}/work-items`);
    expect(await screen.findByRole("link", { name: /Refund webhook retries/ })).toBeInTheDocument();
    await expectClean(document.body);
    await user.click(screen.getByRole("button", { name: "New work item" }));
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    await expectClean(document.body);
  });

  it("marks the current stage, cancels once, and links runs", async () => {
    setAPIKey(demoAPIKey);
    const user = userEvent.setup();
    const secret = task({
      id: "task_04",
      title: "Flaky settlement cutoff",
      stage: "building",
      status: "awaiting_approval",
      pending_gate: "spec",
      body: "Look at the cutoff.",
    });
    let cancels = 0;
    let runLists = 0;
    let runGets = 0;
    let openedRun = "";
    const decisions: unknown[] = [];
    server.use(
      http.get("/api/v1/factory/:factoryId/tasks/:taskId", () =>
        HttpResponse.json({ ...secret, SECRET_PLANT: "SECRET_PLANT" }),
      ),
      http.get("/api/v1/factory/:factoryId/approvals", () =>
        HttpResponse.json([
          {
            id: "apr_9",
            work_item_id: "task_04",
            gate: "spec",
            requested_at: "2026-10-17T12:00:00Z",
          },
        ]),
      ),
      http.get("/api/v1/factory/:factoryId/inbox", () => HttpResponse.json([])),
      http.get("/api/v1/agent/runs", ({ request }) => {
        runLists += 1;
        expect(new URL(request.url).searchParams.get("work_item_id")).toBe("task_04");
        return HttpResponse.json([
          {
            id: "run_abc",
            created_at: "2026-10-17T12:00:00Z",
            status: "failed",
            title: "First attempt",
            work_item_id: "task_04",
            agent: "implementer",
            model_id: "model-a",
            stage: "building",
            failure_class: "agent",
            queue_wait_ms: 5000,
            exec_ms: 6000,
          },
          {
            id: "run_old",
            created_at: "2026-10-17T08:00:00Z",
            status: "failed",
            title: "Older attempt",
            work_item_id: "task_04",
            agent: "planner",
            model_id: "old-model",
            stage: "building",
          },
        ]);
      }),
      http.get("/api/v1/agent/runs/:runId", ({ params }) => {
        runGets += 1;
        openedRun = String(params.runId);
        return HttpResponse.json({
          id: openedRun,
          status: "failed",
          pr_url: openedRun === "run_abc" ? "https://example.test/pr/9" : "",
        });
      }),
      http.post("/api/v1/approvals/:approvalId", async ({ request }) => {
        decisions.push(await request.json());
        return HttpResponse.json({ id: "apr_9", decision: "approve" });
      }),
      http.post("/api/v1/factory/:factoryId/tasks/:taskId/cancel", () => {
        cancels += 1;
        return HttpResponse.json({ ...secret, status: "cancelled" });
      }),
    );
    await renderApp(`/f/${factoryId}/work-items/task_04`);
    expect(
      await screen.findByRole("heading", { name: "Flaky settlement cutoff" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("listitem", { current: "step" })).toHaveTextContent("Building");
    expect(screen.getByRole("listitem", { current: "step" })).toHaveTextContent("Current");
    expect(screen.getByRole("link", { name: "First attempt" })).toHaveAttribute(
      "href",
      "/runs/run_abc",
    );
    expect(
      await screen.findByRole("link", { name: "https://example.test/pr/9" }),
    ).toBeInTheDocument();
    expect(document.body.textContent).not.toContain("SECRET_PLANT");
    await user.click(screen.getByRole("button", { name: "Approve" }));
    await waitFor(() => expect(decisions).toEqual([{ decision: "approve", note: "" }]));
    await user.click(screen.getByRole("button", { name: "Cancel work item" }));
    const dialog = await screen.findByRole("dialog", { name: "Cancel this work item?" });
    await user.click(within(dialog).getByRole("button", { name: "Cancel work item" }));
    await waitFor(() => expect(cancels).toBe(1));
    expect(runLists).toBe(1);
    expect(runGets).toBe(1);
    expect(openedRun).toBe("run_abc");
    await expectClean(document.body);
  });

  it("answers a question from the work item", async () => {
    setAPIKey(demoAPIKey);
    const user = userEvent.setup();
    const bodies: unknown[] = [];
    server.use(
      http.get("/api/v1/factory/:factoryId/tasks/:taskId", () =>
        HttpResponse.json(
          task({
            id: "task_04",
            title: "Flaky settlement cutoff",
            stage: "building",
            status: "awaiting_input",
          }),
        ),
      ),
      http.get("/api/v1/factory/:factoryId/approvals", () => HttpResponse.json([])),
      http.get("/api/v1/factory/:factoryId/inbox", () =>
        HttpResponse.json([
          {
            kind: "question",
            id: "in_1",
            work_item_id: "task_04",
            run_id: "run_abc",
            created_at: "2026-10-17T12:00:00Z",
            title: "Flaky settlement cutoff",
            detail: "Should the budget be global?",
            action: "answer",
          },
        ]),
      ),
      http.get("/api/v1/agent/runs", () => HttpResponse.json([])),
      http.post("/api/v1/work-items/:workItemId/input", async ({ request, params }) => {
        expect(params.workItemId).toBe("task_04");
        bodies.push(await request.json());
        return HttpResponse.json({
          id: "in_1",
          work_item_id: "task_04",
          answer: "global",
          answered_by: "api",
        });
      }),
    );
    await renderApp(`/f/${factoryId}/work-items/task_04`);
    expect(await screen.findByText("Should the budget be global?")).toBeInTheDocument();
    await user.type(screen.getByRole("textbox", { name: "Answer" }), "global");
    await user.click(screen.getByRole("button", { name: "Send answer" }));
    await waitFor(() => expect(bodies).toEqual([{ text: "global" }]));
    expect(await screen.findByText("Answer sent")).toBeInTheDocument();
  });

  it("keeps an approval conflict visible after the approval leaves the page", async () => {
    setAPIKey(demoAPIKey);
    const user = userEvent.setup();
    let pending = true;
    server.use(
      http.get("/api/v1/factory/:factoryId/tasks/:taskId", () =>
        HttpResponse.json(
          task({
            id: "task_04",
            title: "Flaky settlement cutoff",
            stage: "building",
            status: "awaiting_approval",
            pending_gate: "spec",
          }),
        ),
      ),
      http.get("/api/v1/factory/:factoryId/approvals", () =>
        HttpResponse.json(
          pending
            ? [
                {
                  id: "apr_9",
                  work_item_id: "task_04",
                  gate: "spec",
                  requested_at: "2026-10-17T12:00:00Z",
                },
              ]
            : [],
        ),
      ),
      http.get("/api/v1/factory/:factoryId/inbox", () => HttpResponse.json([])),
      http.get("/api/v1/agent/runs", () => HttpResponse.json([])),
      http.post("/api/v1/approvals/:approvalId", () => {
        pending = false;
        return HttpResponse.json(
          { title: "conflict", status: 409, detail: "Already decided by alice" },
          { status: 409 },
        );
      }),
    );
    await renderApp(`/f/${factoryId}/work-items/task_04`);
    await user.click(await screen.findByRole("button", { name: "Approve" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Already decided by alice");
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
  });

  it("keeps an answer conflict visible after the question leaves the page", async () => {
    setAPIKey(demoAPIKey);
    const user = userEvent.setup();
    let waiting = true;
    server.use(
      http.get("/api/v1/factory/:factoryId/tasks/:taskId", () =>
        HttpResponse.json(
          task({
            id: "task_04",
            title: "Flaky settlement cutoff",
            stage: "building",
            status: waiting ? "awaiting_input" : "open",
          }),
        ),
      ),
      http.get("/api/v1/factory/:factoryId/approvals", () => HttpResponse.json([])),
      http.get("/api/v1/factory/:factoryId/inbox", () =>
        HttpResponse.json(
          waiting
            ? [
                {
                  kind: "question",
                  id: "in_1",
                  work_item_id: "task_04",
                  run_id: "run_abc",
                  created_at: "2026-10-17T12:00:00Z",
                  title: "Flaky settlement cutoff",
                  detail: "Should the budget be global?",
                  action: "answer",
                },
              ]
            : [],
        ),
      ),
      http.get("/api/v1/agent/runs", () => HttpResponse.json([])),
      http.post("/api/v1/work-items/:workItemId/input", () => {
        waiting = false;
        return HttpResponse.json(
          { title: "conflict", status: 409, detail: "Already answered" },
          { status: 409 },
        );
      }),
    );
    await renderApp(`/f/${factoryId}/work-items/task_04`);
    expect(await screen.findByText("Should the budget be global?")).toBeInTheDocument();
    await user.type(screen.getByRole("textbox", { name: "Answer" }), "global");
    await user.click(screen.getByRole("button", { name: "Send answer" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Already answered");
    expect(screen.queryByText("Should the budget be global?")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Send answer" })).not.toBeInTheDocument();
  });
});
