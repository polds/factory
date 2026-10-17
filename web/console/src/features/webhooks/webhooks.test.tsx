import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import axe from "axe-core";
import { http, HttpResponse } from "msw";
import { describe, expect, it, vi } from "vitest";
import { setAPIKey } from "../../api/session";
import { demoAPIKey } from "../../mocks/demo-key";
import { server } from "../../mocks/server";
import { renderApp } from "../../test/render";
import webhooks from "./__fixtures__/webhooks.json";

const github = webhooks[0];

async function renderWebhooks() {
  setAPIKey(demoAPIKey);
  return await renderApp("/f/fac_1/webhooks");
}

async function expectClean() {
  const results = await axe.run(document.body);
  const blocking = results.violations.filter(
    (violation) => violation.impact === "serious" || violation.impact === "critical",
  );
  expect(blocking, JSON.stringify(blocking, null, 2)).toEqual([]);
}

describe("webhooks", () => {
  it("shows the ingress URL and immutable auth without secret fields", async () => {
    const user = userEvent.setup();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });
    const urls: string[] = [];
    server.events.on("request:start", ({ request }) => urls.push(request.url));
    server.use(
      http.get("/api/v1/factory/:uid/webhooks", () =>
        HttpResponse.json([
          {
            ...github,
            secret: "whsec-PLANTED",
            url_token: "urltok-PLANTED",
          },
        ]),
      ),
    );
    await renderWebhooks();
    expect(await screen.findByRole("heading", { name: "Webhooks" })).toBeInTheDocument();
    const url = `${window.location.origin}${github?.ingress_path}`;
    expect(screen.getByText(url)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "github" })).toBeInTheDocument();
    expect(screen.getByText("signature")).toBeInTheDocument();
    expect(
      screen.getByText("Immutable: delete and recreate the webhook to change this"),
    ).toBeInTheDocument();
    expect(document.body.innerHTML).not.toContain("whsec-PLANTED");
    expect(document.body.innerHTML).not.toContain("urltok-PLANTED");
    await user.click(screen.getByRole("button", { name: "Copy ingress URL for github" }));
    expect(writeText).toHaveBeenCalledWith(url);
    expect(await screen.findByRole("status")).toHaveTextContent("Copied");
    expect(
      urls.map((item) => new URL(item).pathname).every((path) => path.startsWith("/api/v1")),
    ).toBe(true);
    await expectClean();
  });

  it("expands a row to deliveries for that webhook only", async () => {
    const user = userEvent.setup();
    server.use(
      http.get("/api/v1/factory/:uid/webhooks", () => HttpResponse.json(webhooks)),
      http.get("/api/v1/factory/:uid/deliveries", () =>
        HttpResponse.json([
          {
            id: "dlv_github",
            provider: "github",
            webhook_uid: "wh_1",
            delivery_id: "gh-delivery-1",
            received_at: "2026-04-02T15:00:00Z",
            headers: { "X-GitHub-Event": "issues" },
            body: { action: "opened" },
            truncated: false,
            status: "unmatched",
            matched_automations: [],
          },
          {
            id: "dlv_other",
            provider: "slack",
            webhook_uid: "wh_other",
            delivery_id: "other-webhook-only",
            received_at: "2026-04-02T15:00:00Z",
            headers: {},
            body: "nope",
            truncated: false,
            status: "matched",
            matched_automations: ["elsewhere"],
          },
        ]),
      ),
    );
    await renderWebhooks();
    await screen.findByRole("heading", { name: "Webhooks" });
    await user.click(screen.getByRole("button", { name: "github" }));
    expect(await screen.findByRole("heading", { name: "Recent deliveries" })).toBeInTheDocument();
    expect(screen.getByText("gh-delivery-1")).toBeInTheDocument();
    expect(screen.getByText("Unmatched")).toBeInTheDocument();
    expect(screen.queryByText("other-webhook-only")).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "All deliveries" })).toHaveAttribute(
      "href",
      "/f/fac_1/deliveries",
    );
  });
});
