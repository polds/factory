import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import { setAPIKey } from "../../api/session";
import type { FactoryMetrics } from "../../api/types";
import { demoAPIKey } from "../../mocks/demo-key";
import goldenMetrics from "../../mocks/fixtures/m6-metrics.json";
import { server } from "../../mocks/server";
import { renderApp } from "../../test/render";
import {
  COST_CAVEAT,
  FORGE_CAVEAT,
  IMPROVEMENT_CAVEAT,
  MERGED_CAVEAT,
  SIZE_CAPTION,
  TOTAL_RUNS_CAVEAT,
} from "./metrics";

const golden = goldenMetrics as FactoryMetrics;

function metricsOf(partial: Partial<FactoryMetrics> = {}): FactoryMetrics {
  return { ...golden, ...partial };
}

function installMetrics(byId: Record<string, FactoryMetrics>): string[] {
  const urls: string[] = [];
  server.use(
    http.get("/api/v1/factory/:factoryId/metrics", ({ request, params }) => {
      urls.push(request.url);
      const id = String(params.factoryId);
      return HttpResponse.json(byId[id] ?? golden);
    }),
  );
  return urls;
}

async function factoryRegion(name: string) {
  return screen.findByRole("region", { name: `${name} metrics` });
}

function panel(root: HTMLElement, title: string): HTMLElement {
  const heading = within(root).getByRole("heading", { name: title });
  const section = heading.closest("section");
  if (!(section instanceof HTMLElement)) throw new Error(`missing section for ${title}`);
  return section;
}

describe("dashboard metrics", () => {
  it("shows the total-runs caveat and the measurement count", async () => {
    setAPIKey(demoAPIKey);
    installMetrics({ fac_payments: metricsOf(), fac_platform: metricsOf() });
    await renderApp("/");
    const payments = await factoryRegion("payments");
    expect(await within(payments).findByText(TOTAL_RUNS_CAVEAT)).toBeInTheDocument();
    expect(within(payments).getByText("Measurement")).toBeInTheDocument();
    expect(within(payments).getAllByText("1").length).toBeGreaterThan(0);
    const breakdown = panel(payments, "payments · run breakdown");
    expect(within(breakdown).getByRole("rowheader", { name: "reviewer" })).toBeInTheDocument();
    expect(within(breakdown).getByRole("rowheader", { name: "succeeded" })).toBeInTheDocument();
    expect(within(breakdown).getByRole("rowheader", { name: "issue" })).toBeInTheDocument();
    expect(within(breakdown).getByRole("rowheader", { name: "judge" })).toBeInTheDocument();
    expect(payments.querySelector(".tile-hero")).toBeNull();
  });

  it("shows both numbers when merged exceeds opened", async () => {
    setAPIKey(demoAPIKey);
    installMetrics({
      fac_payments: metricsOf({ prs_opened: 1, prs_merged: 5 }),
      fac_platform: metricsOf({ prs_opened: 0, prs_merged: 0 }),
    });
    await renderApp("/");
    const payments = await factoryRegion("payments");
    const outcomes = panel(payments, "payments · PR outcomes");
    expect(within(outcomes).getByRole("rowheader", { name: "Opened" })).toBeInTheDocument();
    expect(within(outcomes).getByRole("cell", { name: "1" })).toBeInTheDocument();
    expect(within(outcomes).getByRole("rowheader", { name: "Merged" })).toBeInTheDocument();
    expect(within(outcomes).getByRole("cell", { name: "5" })).toBeInTheDocument();
    expect(within(outcomes).getByText(MERGED_CAVEAT)).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "PRs opened" })).toBeInTheDocument();
  });

  it("shows forge connection time and forge not connected", async () => {
    setAPIKey(demoAPIKey);
    installMetrics({
      fac_payments: metricsOf(),
      fac_platform: metricsOf({
        autonomy: { share: 0, n: 0 },
        cycle_time: { p50_ms: 0, stages: [] },
      }),
    });
    await renderApp("/");
    const payments = await factoryRegion("payments");
    expect(await within(payments).findAllByText("2026-01-15T00:00:00Z")).toHaveLength(2);
    expect(within(payments).getAllByText(FORGE_CAVEAT)).toHaveLength(2);
    expect(within(payments).getByText("kickoff_to_pr")).toBeInTheDocument();
    expect(within(payments).getByText("50%")).toBeInTheDocument();
    const platform = await factoryRegion("platform");
    expect(within(platform).getAllByText("forge not connected")).toHaveLength(2);
    expect(within(platform).getAllByText(FORGE_CAVEAT)).toHaveLength(2);
  });

  it("shows the cost caveat, size medians, and https-only PR links", async () => {
    setAPIKey(demoAPIKey);
    const httpUrl = "http://evil.example/pull/2";
    const scriptUrl = "javascript:alert(1)";
    installMetrics({
      fac_payments: metricsOf({
        most_expensive_prs: [
          {
            repo: "acme/payments",
            number: 42,
            cost_cents: 1200,
            changed_lines: 800,
            url: "https://github.com/acme/payments/pull/42",
          },
          {
            repo: "acme/evil",
            number: 2,
            cost_cents: 50,
            changed_lines: 10,
            url: httpUrl,
          },
          {
            repo: "acme/js",
            number: 3,
            cost_cents: 50,
            changed_lines: 10,
            url: scriptUrl,
          },
        ],
      }),
      fac_platform: metricsOf({ most_expensive_prs: [] }),
    });
    await renderApp("/");
    const payments = await factoryRegion("payments");
    expect(await within(payments).findByText(COST_CAVEAT)).toBeInTheDocument();
    const sizes = panel(payments, "payments · cost by size");
    expect(within(sizes).getByText(SIZE_CAPTION)).toBeInTheDocument();
    expect(within(sizes).getByRole("cell", { name: "$0.10" })).toBeInTheDocument();
    expect(within(sizes).getByRole("cell", { name: "$0.40" })).toBeInTheDocument();
    expect(within(sizes).getByRole("cell", { name: "$0.90" })).toBeInTheDocument();
    expect(within(sizes).getByRole("cell", { name: "$2.00" })).toBeInTheDocument();
    const expensive = panel(payments, "payments · expensive PRs");
    expect(within(expensive).getByRole("link", { name: "acme/payments#42" })).toHaveAttribute(
      "href",
      "https://github.com/acme/payments/pull/42",
    );
    expect(within(expensive).getByText(httpUrl)).toBeInTheDocument();
    expect(within(expensive).getByText(scriptUrl)).toBeInTheDocument();
    expect(expensive.querySelector('a[href^="http:"]')).toBeNull();
    expect(expensive.querySelector('a[href^="javascript:"]')).toBeNull();
  });

  it("lists improvement PRs from outside the selected range", async () => {
    setAPIKey(demoAPIKey);
    const user = userEvent.setup();
    const urls = installMetrics({
      fac_payments: metricsOf({
        improvement_prs: [
          {
            batch_id: "ib_new",
            agent: "reviewer",
            url: "https://github.com/acme/factory/pull/8",
            created_at: "2026-04-02T18:00:00Z",
          },
          {
            batch_id: "ib_old",
            agent: "reviewer",
            url: "http://github.com/acme/factory/pull/7",
            created_at: "2020-01-01T00:00:00Z",
          },
        ],
      }),
      fac_platform: metricsOf({ improvement_prs: [], scorer_cards: [] }),
    });
    await renderApp("/");
    const payments = await factoryRegion("payments");
    const improvement = panel(payments, "payments · improvement PRs");
    expect(within(improvement).getByText(IMPROVEMENT_CAVEAT)).toBeInTheDocument();
    const items = within(improvement).getAllByRole("listitem");
    expect(items[0]).toHaveTextContent("2026-04-02T18:00:00Z");
    expect(within(items[0]!).getByRole("link", { name: "Review PR" })).toHaveAttribute(
      "href",
      "https://github.com/acme/factory/pull/8",
    );
    expect(items[1]).toHaveTextContent("http://github.com/acme/factory/pull/7");
    expect(within(items[1]!).queryByRole("link")).not.toBeInTheDocument();
    expect(within(payments).getByRole("link", { name: "correctness" })).toHaveAttribute(
      "href",
      "/f/fac_payments/scorers/correctness",
    );
    expect(urls.length).toBeGreaterThan(0);
    for (const url of urls) {
      const from = new URL(url).searchParams.get("from");
      expect(from).toBeTruthy();
      expect(Date.parse(from ?? "")).toBeGreaterThan(Date.parse("2020-01-02T00:00:00Z"));
      expect(new URL(url).searchParams.get("to")).toBeTruthy();
    }
    const before = urls.length;
    await user.selectOptions(screen.getByLabelText("Range"), "7d");
    await waitFor(() => {
      expect(urls.length).toBeGreaterThan(before);
      expect(
        within(payments).getByRole("heading", { name: "payments · improvement PRs" }),
      ).toBeInTheDocument();
    });
    const afterRange = panel(payments, "payments · improvement PRs");
    expect(within(afterRange).getByRole("link", { name: "Review PR" })).toBeInTheDocument();
    expect(within(afterRange).getByText("2020-01-01T00:00:00Z")).toBeInTheDocument();
  });
});
