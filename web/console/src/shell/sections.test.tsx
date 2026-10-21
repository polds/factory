import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient } from "@tanstack/react-query";
import axe from "axe-core";
import { http, HttpResponse } from "msw";
import { describe, expect, it, vi } from "vitest";
import { invalidateFactory } from "../api/live";
import { setAPIKey } from "../api/session";
import { demoAPIKey } from "../mocks/demo-key";
import { server } from "../mocks/server";
import { renderApp } from "../test/render";

const measurePaths = [
  "/f/fac_payments/scorers",
  "/f/fac_payments/scorers/correctness",
  "/f/fac_payments/benchmarks",
  "/f/fac_payments/benchmarks/pr-review",
  "/f/fac_payments/improvement",
] as const;

async function settleMeasurePage(path: string) {
  if (path.endsWith("/scorers/correctness")) {
    expect(
      await screen.findByText("The agent opened the PR without running tests."),
    ).toBeInTheDocument();
    return;
  }
  if (path.endsWith("/scorers")) {
    expect(
      await screen.findByRole("link", { name: "Results for correctness" }),
    ).toBeInTheDocument();
    return;
  }
  if (path.endsWith("/benchmarks/pr-review")) {
    expect(await screen.findByText("Winner")).toBeInTheDocument();
    return;
  }
  if (path.endsWith("/benchmarks")) {
    expect(await screen.findByRole("link", { name: "pr-review" })).toBeInTheDocument();
    return;
  }
  expect(await screen.findByRole("heading", { name: "Regressions addressed" })).toBeInTheDocument();
}

async function expectNoSeriousViolations(node: Element) {
  const results = await axe.run(node);
  const blocking = results.violations.filter(
    (violation) => violation.impact === "serious" || violation.impact === "critical",
  );
  expect(blocking, JSON.stringify(blocking, null, 2)).toEqual([]);
}

describe("sections", () => {
  it("places Scorers, Benchmarks, and Improvement after Definition", async () => {
    setAPIKey(demoAPIKey);
    await renderApp("/f/fac_payments/scorers");
    const nav = screen.getByRole("navigation", { name: "Sections" });
    const labels = within(nav)
      .getAllByRole("link")
      .map((link) => link.textContent);
    const definition = labels.indexOf("Definition");
    expect(labels.slice(definition, definition + 4)).toEqual([
      "Definition",
      "Scorers",
      "Benchmarks",
      "Improvement",
    ]);
    expect(within(nav).getByRole("link", { name: "Scorers" })).toHaveAttribute(
      "href",
      "/f/fac_payments/scorers",
    );
    expect(within(nav).getByRole("link", { name: "Benchmarks" })).toHaveAttribute(
      "href",
      "/f/fac_payments/benchmarks",
    );
    expect(within(nav).getByRole("link", { name: "Improvement" })).toHaveAttribute(
      "href",
      "/f/fac_payments/improvement",
    );
  });

  it("disables the measure sections until a factory is chosen", async () => {
    server.use(http.get("/api/v1/factory", () => HttpResponse.json([])));
    setAPIKey(demoAPIKey);
    await renderApp("/runs");
    const nav = screen.getByRole("navigation", { name: "Sections" });
    for (const label of ["Scorers", "Benchmarks", "Improvement"]) {
      expect(within(nav).getByText(label)).toHaveAttribute("aria-disabled", "true");
    }
    expect(within(nav).getByRole("link", { name: "Runs" })).toBeInTheDocument();
  });

  it("has no serious or critical axe violations on the measure pages", async () => {
    const user = userEvent.setup();
    for (const path of measurePaths) {
      document.documentElement.dataset.theme = "light";
      localStorage.clear();
      setAPIKey(demoAPIKey);
      const view = await renderApp(path);
      await settleMeasurePage(path);
      await expectNoSeriousViolations(view.container);
      await user.click(screen.getByRole("button", { name: "Switch to dark theme" }));
      expect(document.documentElement.dataset.theme).toBe("dark");
      await expectNoSeriousViolations(view.container);
      view.unmount();
    }
  });

  it("invalidates the M6 query keys for the factory", () => {
    const queryClient = new QueryClient();
    const invalidateQueries = vi.spyOn(queryClient, "invalidateQueries");
    invalidateFactory(queryClient, "fac_payments");
    const keys = invalidateQueries.mock.calls.map((call) => call[0]?.queryKey);
    expect(keys).toEqual([
      ["summary", "fac_payments"],
      ["runs"],
      ["tasks", "fac_payments"],
      ["task", "fac_payments"],
      ["inbox", "fac_payments"],
      ["approvals", "fac_payments"],
      ["scorers", "fac_payments"],
      ["benchmarks", "fac_payments"],
      ["batches", "fac_payments"],
      ["metrics", "fac_payments"],
      ["scores", "fac_payments"],
    ]);
  });
});
