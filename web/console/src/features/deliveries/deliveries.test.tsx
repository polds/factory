import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import axe from "axe-core";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import { setAPIKey } from "../../api/session";
import { demoAPIKey } from "../../mocks/demo-key";
import { server } from "../../mocks/server";
import { renderApp } from "../../test/render";

const unmatched = {
  id: "dlv_1",
  provider: "github",
  webhook_uid: "wh_1",
  delivery_id: "gh-1",
  received_at: "2026-04-02T15:04:00Z",
  headers: {
    "X-GitHub-Event": "issues",
    "X-Hub-Signature-256": "sha256=PLANTED-SIGNATURE",
  },
  body: { action: "opened" },
  truncated: true,
  status: "unmatched",
  matched_automations: [],
  normalized_events: [
    {
      provider: "github",
      event: "issues.labeled",
      actor: { handle: "octocat", token: "PLANTED-ACTOR" },
      subject: { kind: "issue", id: "77", number: 77, title: "Cutoff", raw: "PLANTED-RAW" },
      thread: { kind: "issue", external_ref: "acme/factory#77" },
      raw: { planted: "PLANTED-RAW" },
    },
  ],
};

const matched = {
  id: "dlv_2",
  provider: "slack",
  webhook_uid: "wh_2",
  delivery_id: "sl-2",
  received_at: "2026-04-02T15:00:00Z",
  headers: { "X-Slack-Request-Timestamp": "1712070000" },
  body: "reaction",
  truncated: false,
  status: "matched",
  matched_automations: ["on-label"],
};

async function renderDeliveries() {
  setAPIKey(demoAPIKey);
  return await renderApp("/f/fac_1/deliveries");
}

async function expectClean() {
  const results = await axe.run(document.body);
  const blocking = results.violations.filter(
    (violation) => violation.impact === "serious" || violation.impact === "critical",
  );
  expect(blocking, JSON.stringify(blocking, null, 2)).toEqual([]);
}

describe("deliveries", () => {
  it("opens unmatched deliveries and replays once with redacted signatures", async () => {
    const user = userEvent.setup();
    const searches: string[] = [];
    let replays = 0;
    server.events.on("request:start", ({ request }) => {
      searches.push(new URL(request.url).search);
      expect(new URL(request.url).pathname.startsWith("/api/v1")).toBe(true);
    });
    server.use(
      http.get("/api/v1/factory/:uid/deliveries", ({ request }) => {
        const status = new URL(request.url).searchParams.get("status");
        if (status === "unmatched") return HttpResponse.json([unmatched]);
        return HttpResponse.json([unmatched, matched]);
      }),
      http.post(
        ({ request }) => new URL(request.url).pathname.endsWith(":replay"),
        () => {
          replays += 1;
          return HttpResponse.json(unmatched);
        },
      ),
    );
    await renderDeliveries();
    const unmatchedTab = await screen.findByRole("tab", { name: /Unmatched \(1\)/ });
    expect(unmatchedTab).toHaveAttribute("aria-selected", "true");
    expect(searches.some((search) => search.includes("status=unmatched"))).toBe(true);
    expect(searches.some((search) => search.includes("include=body"))).toBe(true);
    expect(screen.getByText("gh-1")).toBeInTheDocument();
    expect(screen.queryByText("sl-2")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "gh-1" }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("[redacted]");
    expect(dialog).toHaveTextContent("issues");
    expect(dialog).toHaveTextContent("truncated");
    expect(dialog).toHaveTextContent("Normalized event");
    expect(dialog).toHaveTextContent("issues.labeled");
    expect(dialog).toHaveTextContent("octocat");
    expect(dialog).toHaveTextContent("acme/factory#77");
    expect(document.body.innerHTML).not.toContain("PLANTED-SIGNATURE");
    expect(document.body.innerHTML).not.toContain("PLANTED-RAW");
    expect(document.body.innerHTML).not.toContain("PLANTED-ACTOR");
    await expectClean();
    await user.click(screen.getByRole("button", { name: "Replay delivery" }));
    await user.dblClick(await screen.findByRole("button", { name: "Replay" }));
    expect((await screen.findAllByText("Replayed just now")).length).toBeGreaterThan(0);
    await waitFor(() => expect(replays).toBe(1));
    expect(screen.getByRole("button", { name: "Replay delivery" })).toBeDisabled();
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.getByRole("button", { name: "gh-1" })).toHaveFocus();
  });

  it("moves between delivery tabs with the arrow keys", async () => {
    const user = userEvent.setup();
    server.use(
      http.get("/api/v1/factory/:uid/deliveries", ({ request }) => {
        const status = new URL(request.url).searchParams.get("status");
        if (status === "unmatched") return HttpResponse.json([unmatched]);
        return HttpResponse.json([unmatched, matched]);
      }),
    );
    await renderDeliveries();
    const unmatchedTab = await screen.findByRole("tab", { name: /Unmatched/ });
    unmatchedTab.focus();
    await user.keyboard("{ArrowRight}");
    const allTab = await screen.findByRole("tab", { name: /All \(2\)/ });
    await waitFor(() => expect(allTab).toHaveAttribute("aria-selected", "true"));
    expect(await screen.findByText("sl-2")).toBeInTheDocument();
  });
});
