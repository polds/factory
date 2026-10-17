import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import axe from "axe-core";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { setAPIKey } from "../../api/session";
import { demoAPIKey } from "../../mocks/demo-key";
import { server } from "../../mocks/server";
import { renderApp } from "../../test/render";
import "../../index.css";
import { goldenRun, goldenRuns, installRuns, runningRun } from "./test-support";
import type { RunDetailWire } from "./types";

const paths: string[] = [];

function rememberRequest(event: { request: Request }) {
  paths.push(new URL(event.request.url).pathname);
}

async function expectClean(node: Element) {
  const results = await axe.run(node);
  const blocking = results.violations.filter(
    (violation) => violation.impact === "serious" || violation.impact === "critical",
  );
  expect(blocking, JSON.stringify(blocking, null, 2)).toEqual([]);
}

beforeEach(() => {
  paths.length = 0;
  setAPIKey(demoAPIKey);
  server.events.on("request:start", rememberRequest);
});

afterEach(() => {
  server.events.removeListener("request:start", rememberRequest);
  expect(paths.filter((path) => !path.startsWith("/api/v1/"))).toEqual([]);
});

function infraRun(): RunDetailWire {
  return {
    ...goldenRun,
    failure_reason: "oom_killed",
    failure: {
      class: "infrastructure",
      code: "oom_killed",
      summary: "Container was OOMKilled",
      retryable: true,
      evidence: ["exit code 137", "pod reason OOMKilled"],
    },
  };
}

describe("runs accessibility", () => {
  it("has no serious or critical violations on the list", async () => {
    const agentRow = goldenRuns[0];
    expect(agentRow).toBeDefined();
    installRuns({
      list: [
        agentRow ?? goldenRuns[0],
        {
          id: "run_infra_a11y",
          status: "failed",
          agent: "planner",
          failure_class: "infrastructure",
          failure_summary: "Container was OOMKilled",
          queue_wait_ms: 1,
          exec_ms: 2,
        },
      ],
    });
    renderApp("/runs");
    expect(await screen.findByRole("table")).toBeInTheDocument();
    await expectClean(document.body);
  });

  it("has no serious or critical violations on a running detail", async () => {
    const run = runningRun();
    installRuns({ run });
    renderApp(`/runs/${run.id}`);
    expect(await screen.findByRole("heading", { name: "Settle the batch" })).toBeInTheDocument();
    await expectClean(document.body);
  });

  it("has no serious or critical violations on an agent failure", async () => {
    installRuns();
    renderApp(`/runs/${goldenRun.id}`);
    expect(await screen.findByRole("region", { name: "Failure" })).toBeInTheDocument();
    await expectClean(document.body);
  });

  it("has no serious or critical violations on an infrastructure failure", async () => {
    installRuns({ run: infraRun() });
    renderApp(`/runs/${goldenRun.id}`);
    expect(await screen.findByRole("region", { name: "Failure" })).toBeInTheDocument();
    await expectClean(document.body);
  });

  it("keeps the confirm dialog clean and restores focus", async () => {
    const user = userEvent.setup();
    const run = runningRun();
    installRuns({ run });
    renderApp(`/runs/${run.id}`);
    const trigger = await screen.findByRole("button", { name: "Interrupt" });
    trigger.focus();
    await user.click(trigger);
    const dialog = await screen.findByRole("dialog", { name: "Interrupt this turn?" });
    await expectClean(document.body);
    const seen = new Set<string>();
    for (let step = 0; step < 6; step += 1) {
      if (document.activeElement instanceof HTMLElement) {
        seen.add((document.activeElement.textContent ?? "").trim());
      }
      await user.tab();
    }
    expect(seen.has("Cancel")).toBe(true);
    expect(seen.has("Interrupt")).toBe(true);
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await waitFor(() => expect(trigger).toHaveFocus());
    expect(dialog).not.toBeInTheDocument();
  });

  it("reaches filters, opens a row with Enter, and switches tabs from the keyboard", async () => {
    const user = userEvent.setup();
    const run = runningRun({ id: goldenRuns[1]?.id ?? runningRun().id, title: "Settle the batch" });
    installRuns({
      run,
      list: [
        {
          ...(goldenRuns[1] ?? run),
          id: run.id,
          title: "Settle the batch",
          status: "running",
          agent: "implementer",
        },
      ],
    });
    renderApp("/runs");
    const agent = await screen.findByLabelText("Agent");
    for (let step = 0; step < 40 && document.activeElement !== agent; step += 1) {
      await user.tab();
    }
    expect(agent).toHaveFocus();
    await user.tab();
    expect(screen.getByLabelText("Status")).toHaveFocus();

    const link = screen.getByRole("link", { name: "Settle the batch" });
    link.focus();
    await user.keyboard("{Enter}");
    expect(await screen.findByRole("heading", { name: "Settle the batch" })).toBeInTheDocument();

    const transcript = screen.getByRole("tab", { name: "Transcript" });
    for (let step = 0; step < 40 && document.activeElement !== transcript; step += 1) {
      await user.tab();
    }
    expect(transcript).toHaveFocus();
    await user.keyboard("{ArrowRight}");
    expect(screen.getByRole("tab", { name: "Timeline" })).toHaveAttribute("aria-selected", "true");
    await user.keyboard("{ArrowLeft}");
    expect(transcript).toHaveAttribute("aria-selected", "true");

    const followUp = screen.getByLabelText("Send a follow-up to the agent");
    for (let step = 0; step < 30 && document.activeElement !== followUp; step += 1) {
      await user.tab();
    }
    expect(followUp).toHaveFocus();
    await user.keyboard("a");
    await user.tab();
    expect(screen.getByRole("button", { name: "Send follow-up" })).toHaveFocus();
    await user.tab();
    const interrupt = screen.getByRole("button", { name: "Interrupt" });
    expect(interrupt).toHaveFocus();
    await user.keyboard("{Enter}");
    const dialog = await screen.findByRole("dialog", { name: "Interrupt this turn?" });
    expect(within(dialog).getByRole("button", { name: "Cancel" })).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await waitFor(() => expect(interrupt).toHaveFocus());
  });
});
