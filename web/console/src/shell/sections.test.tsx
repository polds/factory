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
  "/f/fac_payments/benchmarks",
  "/f/fac_payments/improvement",
] as const;

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

  it("renders the Scorers placeholder", async () => {
    setAPIKey(demoAPIKey);
    await renderApp("/f/fac_payments/scorers");
    expect(screen.getByRole("heading", { name: "Scorers", level: 2 })).toBeInTheDocument();
    expect(screen.getByText("This page lands in a later change.")).toBeInTheDocument();
  });

  it("renders the scorer detail route on the Scorers placeholder", async () => {
    setAPIKey(demoAPIKey);
    await renderApp("/f/fac_payments/scorers/tests-run");
    expect(screen.getByRole("heading", { name: "Scorers", level: 2 })).toBeInTheDocument();
    expect(screen.getByText("tests-run")).toBeInTheDocument();
    expect(screen.getByText("This page lands in a later change.")).toBeInTheDocument();
  });

  it("renders the Benchmarks placeholder", async () => {
    setAPIKey(demoAPIKey);
    await renderApp("/f/fac_payments/benchmarks");
    expect(screen.getByRole("heading", { name: "Benchmarks", level: 2 })).toBeInTheDocument();
    expect(screen.getByText("This page lands in a later change.")).toBeInTheDocument();
  });

  it("renders the benchmark suite route on the Benchmarks placeholder", async () => {
    setAPIKey(demoAPIKey);
    await renderApp("/f/fac_payments/benchmarks/payments-day");
    expect(screen.getByRole("heading", { name: "Benchmarks", level: 2 })).toBeInTheDocument();
    expect(screen.getByText("payments-day")).toBeInTheDocument();
    expect(screen.getByText("This page lands in a later change.")).toBeInTheDocument();
  });

  it("renders the Improvement placeholder", async () => {
    setAPIKey(demoAPIKey);
    await renderApp("/f/fac_payments/improvement");
    expect(screen.getByRole("heading", { name: "Improvement", level: 2 })).toBeInTheDocument();
    expect(screen.getByText("This page lands in a later change.")).toBeInTheDocument();
  });

  it("does not fetch measure data for the placeholders", async () => {
    const requested: string[] = [];
    const onStart = ({ request }: { request: Request }) => {
      requested.push(new URL(request.url).pathname);
    };
    server.events.on("request:start", onStart);
    try {
      setAPIKey(demoAPIKey);
      for (const path of [
        ...measurePaths,
        "/f/fac_payments/scorers/tests-run",
        "/f/fac_payments/benchmarks/payments-day",
      ]) {
        const view = await renderApp(path);
        view.unmount();
      }
    } finally {
      server.events.removeListener("request:start", onStart);
    }
    expect(
      requested.filter((path) =>
        /scorers|benchmarks|improvement|metrics|scores|batches/.test(path),
      ),
    ).toEqual([]);
  });

  it("has no serious or critical axe violations on the three placeholders", async () => {
    const user = userEvent.setup();
    for (const path of measurePaths) {
      document.documentElement.dataset.theme = "light";
      localStorage.clear();
      setAPIKey(demoAPIKey);
      const view = await renderApp(path);
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
