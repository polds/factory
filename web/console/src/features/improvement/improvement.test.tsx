import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import { setAPIKey } from "../../api/session";
import batches from "../../mocks/fixtures/m6-improvement-batches.json";
import { demoAPIKey } from "../../mocks/demo-key";
import { server } from "../../mocks/server";
import { renderApp } from "../../test/render";

const factory = "/f/fac_payments";

const extra = [
  {
    id: "ib_http",
    agent: "writer",
    status: "closed",
    failures: [],
    pr_url: "http://example.com/pull/7",
    created_at: "2026-04-01T00:00:00Z",
  },
  {
    id: "ib_js",
    agent: "writer",
    status: "failed",
    failures: [],
    pr_url: "javascript:alert(1)",
    created_at: "2026-04-01T00:00:00Z",
  },
];

async function tabTo(user: { tab: () => Promise<void> }, target: HTMLElement) {
  for (let step = 0; step < 40 && document.activeElement !== target; step += 1) {
    await user.tab();
  }
  expect(document.activeElement).toBe(target);
}

describe("improvement", () => {
  it("lists batch status, regressions, and only https pull request links", async () => {
    const paths: string[] = [];
    const onStart = ({ request }: { request: Request }) => {
      paths.push(new URL(request.url).pathname);
    };
    server.events.on("request:start", onStart);
    server.use(
      http.get("/api/v1/factory/:uid/improvement-batches", () =>
        HttpResponse.json([...batches, ...extra]),
      ),
    );
    setAPIKey(demoAPIKey);
    try {
      await renderApp(`${factory}/improvement`);
      expect(
        await screen.findByRole("heading", { name: "Regressions addressed" }),
      ).toBeInTheDocument();
    } finally {
      server.events.removeListener("request:start", onStart);
    }
    expect(screen.getByText("PR open")).toBeInTheDocument();
    expect(screen.getByText("Closed")).toBeInTheDocument();
    expect(screen.getByText("Failed")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: batches[0].pr_url })).toHaveAttribute(
      "href",
      batches[0].pr_url,
    );
    expect(screen.getByText("http://example.com/pull/7").closest("a")).toBeNull();
    expect(screen.getByText("javascript:alert(1)").closest("a")).toBeNull();
    expect(document.querySelectorAll('a[href^="javascript:"], a[href^="http:"]')).toHaveLength(0);
    const regressions = screen
      .getByRole("heading", { name: "Regressions addressed" })
      .closest("section");
    expect(regressions).not.toBeNull();
    if (!regressions) return;
    expect(within(regressions).getByRole("link", { name: "run_1" })).toHaveAttribute(
      "href",
      "/runs/run_1",
    );
    expect(within(regressions).getByText("correctness")).toBeInTheDocument();
    expect(within(regressions).getByText("missed_tests")).toBeInTheDocument();
    expect(screen.getAllByText("No failures recorded")).toHaveLength(2);
    expect(paths).toContain("/api/v1/factory/fac_payments/events");
    expect(paths).toContain("/api/v1/factory/fac_payments/improvement-batches");
  });

  it("shows a 409 inline when a batch is already in flight", async () => {
    const user = userEvent.setup();
    server.use(
      http.post("/api/v1/factory/:uid/improvement:run-now", () =>
        HttpResponse.json(
          {
            title: "conflict",
            status: 409,
            detail: "improvement batch already in flight",
          },
          { status: 409 },
        ),
      ),
    );
    setAPIKey(demoAPIKey);
    await renderApp(`${factory}/improvement`);
    await screen.findByRole("heading", { name: "Regressions addressed" });
    await user.click(screen.getByRole("button", { name: "Run now" }));
    const dialog = await screen.findByRole("dialog", { name: "Run now?" });
    await user.click(within(dialog).getByRole("button", { name: "Run now" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("improvement batch already in flight");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Run now" }).compareDocumentPosition(alert)).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING,
    );
  });

  it("confirms run now from the keyboard and lists checked agents", async () => {
    const user = userEvent.setup();
    let posts = 0;
    let gets = 0;
    const onStart = ({ request }: { request: Request }) => {
      const path = new URL(request.url).pathname;
      if (path.endsWith("/improvement-batches")) gets += 1;
    };
    server.events.on("request:start", onStart);
    server.use(
      http.post("/api/v1/factory/:uid/improvement:run-now", () => {
        posts += 1;
        return HttpResponse.json(
          { checked_agents: ["reviewer", "writer"], batches: [] },
          { status: 202 },
        );
      }),
    );
    setAPIKey(demoAPIKey);
    try {
      await renderApp(`${factory}/improvement`);
      const opener = await screen.findByRole("button", { name: "Run now" });
      const getsBefore = gets;
      await tabTo(user, opener);
      await user.keyboard("{Enter}");
      expect(await screen.findByRole("dialog", { name: "Run now?" })).toBeInTheDocument();
      await user.keyboard("{Escape}");
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      expect(posts).toBe(0);
      await tabTo(user, screen.getByRole("button", { name: "Run now" }));
      await user.keyboard("{Enter}");
      const dialog = await screen.findByRole("dialog", { name: "Run now?" });
      await tabTo(user, within(dialog).getByRole("button", { name: "Run now" }));
      await user.keyboard("{Enter}");
      const status = await screen.findByRole("status");
      expect(within(status).getByText("Checked agents")).toBeInTheDocument();
      expect(within(status).getByText("reviewer")).toBeInTheDocument();
      expect(within(status).getByText("writer")).toBeInTheDocument();
      expect(posts).toBe(1);
      expect(gets).toBeGreaterThan(getsBefore);
    } finally {
      server.events.removeListener("request:start", onStart);
    }
  });

  it("returns focus to Run now after escape and confirm", async () => {
    const user = userEvent.setup();
    server.use(
      http.post("/api/v1/factory/:uid/improvement:run-now", () => {
        return HttpResponse.json({ checked_agents: ["reviewer"], batches: [] }, { status: 202 });
      }),
    );
    setAPIKey(demoAPIKey);
    await renderApp(`${factory}/improvement`);
    const opener = await screen.findByRole("button", { name: "Run now" });
    await tabTo(user, opener);
    await user.keyboard("{Enter}");
    expect(await screen.findByRole("dialog", { name: "Run now?" })).toBeInTheDocument();
    await user.keyboard("{Escape}");
    await waitFor(() => expect(opener).toHaveFocus());

    await user.keyboard("{Enter}");
    const dialog = await screen.findByRole("dialog", { name: "Run now?" });
    await tabTo(user, within(dialog).getByRole("button", { name: "Run now" }));
    await user.keyboard("{Enter}");
    await waitFor(() => expect(opener).toHaveFocus());
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});
