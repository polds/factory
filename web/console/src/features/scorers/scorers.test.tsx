import { screen } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import { setAPIKey } from "../../api/session";
import scorers from "../../mocks/fixtures/m6-scorers.json";
import { demoAPIKey } from "../../mocks/demo-key";
import { server } from "../../mocks/server";
import { renderApp } from "../../test/render";

const factory = "/f/fac_payments";
const markup = "<img src=x onerror=alert(1)>";
const secret = `ghp_${"a".repeat(36)}`;

function remember(paths: string[]) {
  const onStart = ({ request }: { request: Request }) => {
    paths.push(new URL(request.url).pathname);
  };
  server.events.on("request:start", onStart);
  return () => server.events.removeListener("request:start", onStart);
}

describe("scorers", () => {
  it("lists labels, passing score, sampling, model, and pass rate", async () => {
    const paths: string[] = [];
    const stop = remember(paths);
    server.use(
      http.get("/api/v1/factory/:uid/scorers", () =>
        HttpResponse.json([
          { ...scorers[0], self_improvement: true },
          {
            name: "style",
            agents: ["writer"],
            labels: ["tone"],
            passing_score: 1,
            sampling_rate: 0,
            model: "judge",
            self_improvement: false,
            pass_rate_by_day: [],
          },
        ]),
      ),
    );
    setAPIKey(demoAPIKey);
    try {
      await renderApp(`${factory}/scorers`);
      expect(await screen.findByRole("link", { name: "Results for correctness" })).toHaveAttribute(
        "href",
        `${factory}/scorers/correctness`,
      );
    } finally {
      stop();
    }
    expect(
      screen.getByText("Infrastructure-failed and measurement runs are never scored."),
    ).toBeInTheDocument();
    expect(screen.getByText("missed_tests, ran_tests")).toBeInTheDocument();
    expect(screen.getByText("0.5")).toBeInTheDocument();
    expect(screen.getByText("25%")).toBeInTheDocument();
    expect(screen.getAllByText("judge").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Self-improvement").some((node) => node.tagName === "SPAN")).toBe(
      true,
    );
    expect(
      screen.getByText("No self-improvement").parentElement?.querySelector("svg"),
    ).not.toBeNull();
    expect(screen.getByText("100%")).toBeInTheDocument();
    expect(screen.getByText("2026-04-02")).toBeInTheDocument();
    expect(screen.getByText("No pass rate yet")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Results for style" })).toHaveAttribute(
      "href",
      `${factory}/scorers/style`,
    );
    expect(paths).toContain("/api/v1/factory/fac_payments/events");
    expect(paths).toContain("/api/v1/factory/fac_payments/scorers");
  });

  it("renders poisoned reasoning as text and drops unknown result fields", async () => {
    server.use(
      http.get("/api/v1/factory/:uid/scorers/:id/results", () =>
        HttpResponse.json([
          {
            id: "sr_1",
            run_id: "run_1",
            label: "missed_tests",
            score: 0.25,
            passed: false,
            reasoning: `Judge said ${markup} and ${secret}`,
            status: "ok",
            created_at: "2026-04-02T16:00:00Z",
            value: "planted-result-value",
          },
        ]),
      ),
    );
    setAPIKey(demoAPIKey);
    await renderApp(`${factory}/scorers/correctness`);
    expect(await screen.findByRole("link", { name: "run_1" })).toHaveAttribute(
      "href",
      "/runs/run_1",
    );
    expect(screen.getByText("missed_tests")).toBeInTheDocument();
    expect(screen.getByText("0.25")).toBeInTheDocument();
    expect(screen.getByText("Failed")).toBeInTheDocument();
    expect(screen.getByText("OK")).toBeInTheDocument();
    expect(screen.getByText("2026-04-02T16:00:00Z")).toBeInTheDocument();
    expect(document.body).toHaveTextContent(markup);
    expect(document.body).toHaveTextContent(secret);
    expect(secret).toMatch(/^ghp_[A-Za-z0-9]{36}$/);
    expect(document.querySelector("img")).toBeNull();
    expect(document.body.innerHTML).not.toContain("<img");
    expect(document.body.innerHTML).toContain("&lt;img");
    expect(document.body.innerHTML).not.toContain("planted-result-value");
    expect(
      screen.getByText("Infrastructure-failed and measurement runs are never scored."),
    ).toBeInTheDocument();
  });
});
