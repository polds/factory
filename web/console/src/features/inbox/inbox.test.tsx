import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import axe from "axe-core";
import { http, HttpResponse, type JsonBodyType } from "msw";
import { describe, expect, it } from "vitest";
import { setAPIKey } from "../../api/session";
import type { InboxItem } from "../../api/types";
import { demoAPIKey } from "../../mocks/demo-key";
import { server } from "../../mocks/server";
import { renderApp } from "../../test/render";
import goldenInbox from "./__fixtures__/inbox.json";
import { byOldest } from "./index";

const factoryId = "fac_payments";

function item(
  partial: Partial<InboxItem> & Pick<InboxItem, "kind" | "id" | "created_at" | "title">,
): InboxItem {
  return {
    work_item_id: "task_01",
    run_id: "run_01",
    detail: partial.detail ?? "",
    action: "open",
    ...partial,
  };
}

const shuffled: InboxItem[] = [
  item({
    kind: "failed_run",
    id: "run_fail",
    created_at: "2026-10-17T03:00:00Z",
    title: "Settlement cutoff failed",
    detail: "The agent reported the run as failed",
    work_item_id: "task_04",
    run_id: "run_fail",
    action: "open",
  }),
  item({
    kind: "approval",
    id: "apr_01",
    created_at: "2026-10-17T01:00:00Z",
    title: "Spec gate",
    detail: "spec",
    work_item_id: "task_02",
    run_id: "",
    action: "decide",
  }),
  item({
    kind: "question",
    id: "in_01",
    created_at: "2026-10-17T02:00:00Z",
    title: "Currency column position",
    detail: "Should the currency column come before the amount?",
    work_item_id: "task_03",
    run_id: "run_q",
    action: "answer",
  }),
];

async function expectClean(node: ParentNode) {
  const results = await axe.run(node as Element);
  const blocking = results.violations.filter(
    (violation) => violation.impact === "serious" || violation.impact === "critical",
  );
  expect(blocking, JSON.stringify(blocking, null, 2)).toEqual([]);
}

function install(
  rows: InboxItem[],
  options?: { decide?: () => Response; answer?: () => Response },
) {
  const live = { rows: [...rows] };
  const gets = { inbox: 0, approvals: 0 };
  const posts: { url: string; body: unknown }[] = [];
  server.use(
    http.get("/api/v1/factory/:factoryId/inbox", () => {
      gets.inbox += 1;
      return HttpResponse.json(live.rows.map((row) => ({ ...row, SECRET_PLANT: "SECRET_PLANT" })));
    }),
    http.get("/api/v1/factory/:factoryId/approvals", () => {
      gets.approvals += 1;
      return HttpResponse.json([]);
    }),
    http.get("/api/v1/agent/runs", () =>
      HttpResponse.json([
        {
          id: "run_fail",
          status: "failed",
          failure_class: "infrastructure",
          failure_summary: "Container was OOMKilled",
          queue_wait_ms: 10,
          exec_ms: 20,
        },
      ]),
    ),
    http.post("/api/v1/approvals/:approvalId", async ({ request }) => {
      posts.push({ url: request.url, body: await request.json() });
      return options?.decide
        ? options.decide()
        : HttpResponse.json({ id: "apr_01", decision: "approve" });
    }),
    http.post("/api/v1/work-items/:workItemId/input", async ({ request }) => {
      posts.push({ url: request.url, body: await request.json() });
      return (
        options?.answer?.() ??
        HttpResponse.json({
          id: "in_01",
          work_item_id: "task_03",
          answer: "before",
          answered_by: "api",
        })
      );
    }),
  );
  return {
    gets,
    posts,
    omit(id: string) {
      live.rows = live.rows.filter((row) => row.id !== id);
    },
  };
}

describe("byOldest", () => {
  it("orders fractional seconds by parsed time", () => {
    const half = item({
      kind: "approval",
      id: "apr_half",
      created_at: "2026-04-02T15:30:00.5Z",
      title: "Half",
    });
    const digits = item({
      kind: "approval",
      id: "apr_digits",
      created_at: "2026-04-02T15:30:00.123456Z",
      title: "Digits",
    });
    expect([half, digits].sort(byOldest).map((row) => row.id)).toEqual(["apr_digits", "apr_half"]);
  });
});

describe("inbox", () => {
  it("lists rows oldest first and filters by kind", async () => {
    setAPIKey(demoAPIKey);
    const user = userEvent.setup();
    install(shuffled);
    await renderApp(`/f/${factoryId}/inbox`);
    const titles = await screen.findAllByRole("button", {
      name: /Spec gate|Currency column|Settlement cutoff/,
    });
    expect(titles.map((node) => node.textContent)).toEqual([
      expect.stringContaining("Spec gate"),
      expect.stringContaining("Currency column position"),
      expect.stringContaining("Settlement cutoff failed"),
    ]);
    expect(screen.getByRole("button", { name: /All 3/ })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: /Approvals 1/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Questions 1/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Failed runs 1/ })).toBeInTheDocument();
    expect(screen.getByText(/Oldest first/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /Questions/ }));
    expect(screen.queryByText("Spec gate")).not.toBeInTheDocument();
    expect(screen.getByText("Currency column position")).toBeInTheDocument();
    expect(goldenInbox[0]?.kind).toBe("approval");
  });

  it("approves optimistically and rolls back a 409", async () => {
    setAPIKey(demoAPIKey);
    const user = userEvent.setup();
    let resolveDecision: (response: Response) => void = () => {};
    const pending = new Promise<Response>((resolve) => {
      resolveDecision = resolve;
    });
    const api = install([shuffled[1]!], {
      decide: () => pending as unknown as Response,
    });
    await renderApp(`/f/${factoryId}/inbox`);
    const row = await screen.findByRole("button", { name: /Spec gate/ });
    await user.click(row);
    await user.type(screen.getByRole("textbox", { name: /Note/ }), "ship it");
    const inboxBefore = api.gets.inbox;
    const approvalsBefore = api.gets.approvals;
    await user.click(screen.getByRole("button", { name: "Approve" }));
    expect(await screen.findByText("Approved by you · Saving…")).toBeInTheDocument();
    api.omit("apr_01");
    resolveDecision(
      HttpResponse.json(
        {
          title: "conflict",
          status: 409,
          detail: "Already decided by alice 2m ago (409). Refreshed.",
        },
        { status: 409 },
      ) as unknown as Response,
    );
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Already decided by alice 2m ago (409). Refreshed.",
    );
    expect(screen.queryByRole("button", { name: /Spec gate/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
    expect(screen.queryByText("Approved by you · Saving…")).not.toBeInTheDocument();
    await waitFor(() => expect(api.gets.inbox).toBeGreaterThan(inboxBefore));
    await waitFor(() => expect(api.gets.approvals).toBeGreaterThan(approvalsBefore));
    expect(api.posts).toHaveLength(1);
    expect(api.posts[0]?.body).toEqual({ decision: "approve", note: "ship it" });
    expect(api.posts[0]?.url).toContain("/api/v1/approvals/apr_01:decide");
  });

  it("shows a reject conflict after the inbox drops the row", async () => {
    setAPIKey(demoAPIKey);
    const user = userEvent.setup();
    const api = install([shuffled[1]!], {
      decide: () =>
        HttpResponse.json(
          { title: "conflict", status: 409, detail: "Already rejected by bob" },
          { status: 409 },
        ) as unknown as Response,
    });
    await renderApp(`/f/${factoryId}/inbox`);
    await user.click(await screen.findByRole("button", { name: /Spec gate/ }));
    api.omit("apr_01");
    await user.click(screen.getByRole("button", { name: "Reject" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Already rejected by bob");
    expect(screen.queryByRole("button", { name: /Spec gate/ })).not.toBeInTheDocument();
    expect(api.posts[0]?.body).toEqual({ decision: "reject", note: "" });
  });

  it("rejects with the same decision body", async () => {
    setAPIKey(demoAPIKey);
    const user = userEvent.setup();
    const api = install([shuffled[1]!]);
    await renderApp(`/f/${factoryId}/inbox`);
    await user.click(await screen.findByRole("button", { name: /Spec gate/ }));
    await user.click(screen.getByRole("button", { name: "Reject" }));
    expect(await screen.findByText("Rejected by you · Saving…")).toBeInTheDocument();
    await waitFor(() => expect(api.posts[0]?.body).toEqual({ decision: "reject", note: "" }));
  });

  it("removes an answered question and restores it when the answer conflicts", async () => {
    setAPIKey(demoAPIKey);
    const user = userEvent.setup();
    let resolveAnswer: (response: Response) => void = () => {};
    const pending = new Promise<Response>((resolve) => {
      resolveAnswer = resolve;
    });
    const api = install([shuffled[2]!], { answer: () => pending as unknown as Response });
    await renderApp(`/f/${factoryId}/inbox`);
    await user.click(await screen.findByRole("button", { name: /Currency column/ }));
    await user.type(screen.getByRole("textbox", { name: "Answer" }), "before the amount");
    await user.click(screen.getByRole("button", { name: "Send answer" }));
    await waitFor(() =>
      expect(screen.queryByText("Currency column position")).not.toBeInTheDocument(),
    );
    expect(api.posts).toHaveLength(1);
    expect(api.posts[0]?.body).toEqual({ text: "before the amount" });
    expect(api.posts[0]?.url).toContain("/api/v1/work-items/task_03/input");
    api.omit("in_01");
    resolveAnswer(
      HttpResponse.json(
        { title: "conflict", status: 409, detail: "Already answered" },
        { status: 409 },
      ) as unknown as Response,
    );
    expect(await screen.findByRole("alert")).toHaveTextContent("Already answered");
    expect(screen.queryByText("Currency column position")).not.toBeInTheDocument();
  });

  it("shows a failed run without a retry control", async () => {
    setAPIKey(demoAPIKey);
    const user = userEvent.setup();
    install(shuffled);
    await renderApp(`/f/${factoryId}/inbox`);
    await user.click(await screen.findByRole("button", { name: /Settlement cutoff failed/ }));
    expect(screen.getByText("Infrastructure")).toBeInTheDocument();
    expect(screen.getAllByText("The agent reported the run as failed").length).toBeGreaterThan(0);
    expect(screen.getByRole("link", { name: "Open run" })).toHaveAttribute(
      "href",
      "/runs/run_fail",
    );
    expect(screen.queryByRole("button", { name: /retry/i })).not.toBeInTheDocument();
    expect(document.body.textContent).not.toContain("SECRET_PLANT");
  });

  it("walks an approval from the keyboard", async () => {
    setAPIKey(demoAPIKey);
    const user = userEvent.setup();
    let release: (response: Response) => void = () => {};
    const pending = new Promise<Response>((resolve) => {
      release = resolve;
    });
    install([shuffled[1]!], { decide: () => pending as unknown as Response });
    await renderApp(`/f/${factoryId}/inbox`);
    const row = await screen.findByRole("button", { name: /Spec gate/ });
    for (let step = 0; step < 40 && document.activeElement !== row; step += 1) {
      await user.tab();
    }
    expect(row).toHaveFocus();
    await user.keyboard("{Enter}");
    expect(row).toHaveAttribute("aria-expanded", "true");
    await user.tab();
    const approve = screen.getByRole("button", { name: "Approve" });
    expect(approve).toHaveFocus();
    await user.keyboard("{Enter}");
    expect(await screen.findByText("Approved by you · Saving…")).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(row).toHaveAttribute("aria-expanded", "false");
    expect(row).toHaveFocus();
    release(HttpResponse.json({ id: "apr_01", decision: "approve" }) as unknown as Response);
  });

  it("has no serious accessibility violations when empty or full", async () => {
    setAPIKey(demoAPIKey);
    install([]);
    const empty = await renderApp(`/f/${factoryId}/inbox`);
    expect(await screen.findByText("Nothing needs you right now")).toBeInTheDocument();
    await expectClean(document.body);
    empty.unmount();
    install(shuffled);
    await renderApp(`/f/${factoryId}/inbox`);
    expect(await screen.findByRole("button", { name: /Spec gate/ })).toBeInTheDocument();
    await expectClean(document.body);
  });
});

void (0 as unknown as JsonBodyType);
