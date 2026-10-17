import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setAPIKey } from "../../api/session";
import { demoAPIKey } from "../../mocks/demo-key";
import { server } from "../../mocks/server";
import { renderApp } from "../../test/render";
import { INFRA_CAVEAT, QUEUE_CAVEAT, shortId } from "./format";
import {
  createEventBus,
  goldenRun,
  goldenRuns,
  goldenUsage,
  installRuns,
  mockTranscriptViewport,
  runningRun,
  SECRET,
  sseEvent,
} from "./test-support";
import type { RunDetailWire, RunRow } from "./types";

const paths: string[] = [];

function rememberRequest(event: { request: Request }) {
  paths.push(new URL(event.request.url).pathname);
}

beforeEach(() => {
  paths.length = 0;
  setAPIKey(demoAPIKey);
  server.events.on("request:start", rememberRequest);
});

afterEach(() => {
  server.events.removeListener("request:start", rememberRequest);
  expect(paths.filter((path) => !path.startsWith("/api/v1/"))).toEqual([]);
  vi.useRealTimers();
});

function infraRun(): RunDetailWire {
  return {
    ...goldenRun,
    id: "run_infra_detail",
    failure_reason: "oom_killed",
    failure: {
      class: "infrastructure",
      code: "oom_killed",
      summary: "Container was OOMKilled",
      retryable: true,
      evidence: ["exit code 137", "pod reason OOMKilled"],
    },
    kubernetes_snapshot: { reason: "OOMKilled", "memory limit": "4Gi" },
  };
}

describe("run detail", () => {
  it("renders /runs/$runId and the failure panel before the tabs", async () => {
    const user = userEvent.setup();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });
    installRuns();
    await renderApp(`/runs/${goldenRun.id}`);

    expect(await screen.findByRole("heading", { name: shortId(goldenRun.id) })).toBeInTheDocument();
    expect(screen.getByText(goldenRun.id)).toBeInTheDocument();
    const header = document.querySelector(".run-header");
    const panel = document.querySelector(".failure-panel");
    const layout = document.querySelector(".detail-layout");
    expect(header).not.toBeNull();
    expect(panel).not.toBeNull();
    expect(layout).not.toBeNull();
    if (!header || !panel || !layout) return;
    expect(header.compareDocumentPosition(panel) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(panel.compareDocumentPosition(layout) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();

    expect(panel).toHaveAttribute("data-failure-class", "agent");
    expect(
      within(panel as HTMLElement).getByRole("heading", { name: goldenRun.failure?.summary }),
    ).toBeInTheDocument();
    expect(within(panel as HTMLElement).getAllByText("agent_reported").length).toBeGreaterThan(0);
    const retryable = (panel as HTMLElement).querySelector(".retryable");
    expect(retryable?.querySelector("svg")).toHaveClass("lucide-x-circle");
    expect(retryable).toHaveTextContent("Retryable: no");
    const evidence = within(panel as HTMLElement).getByRole("list", { name: "Evidence" });
    expect(
      within(evidence)
        .getAllByRole("listitem")
        .map((item) => item.textContent),
    ).toEqual(["exit code 10", "harness status failed_by_agent"]);
    const raw = (panel as HTMLElement).querySelector("details.raw-details");
    expect(raw).not.toHaveAttribute("open");
    expect([...(raw?.querySelectorAll("dt") ?? [])].map((node) => node.textContent)).toEqual([
      "failure_reason",
      "code",
      "retryable",
      "evidence",
    ]);
    expect(screen.getByText(QUEUE_CAVEAT)).toBeInTheDocument();
    expect(screen.getAllByText(/Queue /).length).toBeGreaterThan(0);
    expect(screen.getByRole("link", { name: "Open PR #94" })).toHaveAttribute(
      "href",
      goldenRun.pr_url,
    );
    expect(screen.queryByRole("button", { name: "Cancel run" })).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Copy as text" }));
    await waitFor(() => expect(writeText).toHaveBeenCalledTimes(1));
    expect(writeText.mock.calls[0]?.[0]).toBe(
      [
        "class: agent",
        "code: agent_reported",
        "summary: The agent reported the run as failed",
        "evidence:",
        "- exit code 10",
        "- harness status failed_by_agent",
        `run id: ${goldenRun.id}`,
        "failure_reason: container_exit",
      ].join("\n"),
    );
    expect(screen.getByRole("status")).toHaveTextContent("Failure details copied.");
  });

  it("tints an infrastructure failure and keeps the snapshot inside raw details", async () => {
    installRuns({ run: infraRun() });
    await renderApp("/runs/run_infra_detail");
    const panel = await screen.findByRole("region", { name: "Failure" });
    expect(panel).toHaveAttribute("data-failure-class", "infrastructure");
    expect(within(panel).getByText("Infrastructure")).toBeInTheDocument();
    expect(panel.querySelector("svg.lucide-server-crash")).not.toBeNull();
    expect(within(panel).getByText("Retryable: yes")).toBeInTheDocument();
    expect(panel.querySelector(".retryable svg")).toHaveClass("lucide-circle-check");
    expect(within(panel).getByText(INFRA_CAVEAT)).toBeInTheDocument();
    const raw = panel.querySelector("details.raw-details");
    expect(raw).not.toHaveAttribute("open");
    expect(
      within(raw as HTMLElement).getByRole("table", { name: "Kubernetes snapshot" }),
    ).toBeInTheDocument();
    expect(
      within(raw as HTMLElement).getByRole("rowheader", { name: "memory limit" }),
    ).toBeInTheDocument();
    expect(within(raw as HTMLElement).getByRole("cell", { name: "4Gi" })).toBeInTheDocument();
  });

  it("omits the failure panel when the run succeeded or is still running", async () => {
    installRuns({
      run: {
        id: "run_ok",
        status: "succeeded",
        title: "Clean run",
        queue_wait_ms: 10,
        exec_ms: 20,
        cost_cents: 0,
        steer: {
          enabled: false,
          reason: "run finished; idle window ended at 2026-04-02T15:30:00Z",
        },
      },
    });
    const succeeded = await renderApp("/runs/run_ok");
    expect(await screen.findByRole("heading", { name: "Clean run" })).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Failure" })).not.toBeInTheDocument();
    succeeded.unmount();

    installRuns({ run: runningRun() });
    await renderApp(`/runs/${runningRun().id}`);
    expect(await screen.findByRole("heading", { name: "Settle the batch" })).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Failure" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Cancel run" })).toBeInTheDocument();
  });

  it("interrupts, sends a follow-up, and marks that steer delivered", async () => {
    const user = userEvent.setup();
    const bus = createEventBus();
    let interrupts = 0;
    const followUps: string[] = [];
    const run = runningRun();
    installRuns({
      run,
      events: (request) => bus.response(request),
      onInterrupt() {
        interrupts += 1;
      },
      onFollowUp(text) {
        followUps.push(text);
      },
    });
    await renderApp(`/runs/${run.id}`);
    await screen.findByRole("heading", { name: "Settle the batch" });

    await user.click(screen.getByRole("button", { name: "Interrupt" }));
    const dialog = await screen.findByRole("dialog", { name: "Interrupt this turn?" });
    await user.click(within(dialog).getByRole("button", { name: "Interrupt" }));
    const interruptRow = await screen.findByText("steer_01");
    expect(interruptRow.closest("li")).toHaveTextContent("pending");
    expect(interruptRow.closest("li")).toHaveTextContent("interrupt");
    expect(interrupts).toBe(1);

    const followUp = screen.getByLabelText("Send a follow-up to the agent");
    await user.type(followUp, "ship the cutoff");
    await user.click(screen.getByRole("button", { name: "Send follow-up" }));
    const followRow = await screen.findByText("steer_02");
    expect(followUps).toEqual(["ship the cutoff"]);
    expect(followUp).toHaveValue("");
    expect(followRow.closest("li")).toHaveTextContent("pending");
    expect(followRow.closest("li")).toHaveTextContent("followup");

    bus.send(sseEvent(4, "followup.received", { steer_id: "steer_02" }));
    await waitFor(() => expect(followRow.closest("li")).toHaveTextContent("delivered"));
    expect(interruptRow.closest("li")).toHaveTextContent("pending");
  });

  it("disables steering with the server reason and keeps text after a 409", async () => {
    const user = userEvent.setup();
    installRuns();
    const disabled = await renderApp(`/runs/${goldenRun.id}`);
    const reason = "run finished; idle window ended at 2026-04-02T15:30:00Z";
    expect(await screen.findByText(reason)).toBeInTheDocument();
    const area = screen.getByLabelText("Send a follow-up to the agent");
    const send = screen.getByRole("button", { name: "Send follow-up" });
    const interrupt = screen.getByRole("button", { name: "Interrupt" });
    expect(area).toBeDisabled();
    expect(send).toBeDisabled();
    expect(interrupt).toBeDisabled();
    expect(area.getAttribute("aria-describedby")).toContain("steer-reason");
    expect(send.getAttribute("aria-describedby")).toContain("steer-reason");
    expect(interrupt.getAttribute("aria-describedby")).toContain("steer-reason");
    expect(document.getElementById("steer-reason")).toHaveTextContent(reason);
    disabled.unmount();

    const run = runningRun();
    installRuns({
      run,
      followUp: {
        status: 409,
        body: {
          type: "about:blank",
          title: "conflict",
          status: 409,
          detail: "conflict",
          reason: "run has not started",
        },
      },
    });
    await renderApp(`/runs/${run.id}`);
    const enabled = await screen.findByLabelText("Send a follow-up to the agent");
    await user.type(enabled, "keep this text");
    await user.click(screen.getByRole("button", { name: "Send follow-up" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("run has not started");
    expect(enabled).toHaveValue("keep this text");
    expect(enabled.getAttribute("aria-describedby")).toContain("steer-error");
    expect(screen.getByText("None yet.")).toBeInTheDocument();
  });

  it("posts one follow-up when Send is clicked twice before the response", async () => {
    const user = userEvent.setup();
    const run = runningRun();
    const bodies: { text?: string }[] = [];
    let release: (() => void) | undefined;
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    let interrupts = 0;
    installRuns({
      run,
      onInterrupt() {
        interrupts += 1;
      },
    });
    server.use(
      http.post("/api/v1/agent/runs/:id/followups", async ({ request }) => {
        bodies.push((await request.json()) as { text?: string });
        await gate;
        return HttpResponse.json({ steer_id: "steer_held" }, { status: 202 });
      }),
    );
    await renderApp(`/runs/${run.id}`);
    const area = await screen.findByLabelText("Send a follow-up to the agent");
    await user.type(area, "ship the cutoff");
    const send = screen.getByRole("button", { name: "Send follow-up" });
    const interrupt = screen.getByRole("button", { name: "Interrupt" });
    send.click();
    send.click();
    await waitFor(() => expect(bodies).toEqual([{ text: "ship the cutoff" }]));
    await waitFor(() => expect(send).toBeDisabled());
    expect(interrupt).toBeDisabled();
    interrupt.click();
    expect(interrupts).toBe(0);
    expect(area).toHaveValue("ship the cutoff");
    if (!release) throw new Error("follow-up gate was not installed");
    release();
    const row = await screen.findByText("steer_held");
    expect(row.closest("li")).toHaveTextContent("pending");
    expect(row.closest("li")).toHaveTextContent("followup");
    expect(area).toHaveValue("");
    expect(send).toBeDisabled();
    await waitFor(() => expect(interrupt).toBeEnabled());
  });

  it("shows the interrupt conflict reason", async () => {
    const user = userEvent.setup();
    const run = runningRun();
    installRuns({ run });
    server.use(
      http.post("/api/v1/agent/runs/:id/interrupt", () =>
        HttpResponse.json(
          {
            type: "about:blank",
            title: "conflict",
            status: 409,
            detail: "conflict",
            reason: "worker/harness does not support steering",
          },
          { status: 409 },
        ),
      ),
    );
    await renderApp(`/runs/${run.id}`);
    await screen.findByRole("heading", { name: "Settle the batch" });
    await user.click(screen.getByRole("button", { name: "Interrupt" }));
    const dialog = await screen.findByRole("dialog", { name: "Interrupt this turn?" });
    await user.click(within(dialog).getByRole("button", { name: "Interrupt" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "worker/harness does not support steering",
    );
    expect(screen.getByText("None yet.")).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("counts the idle window down and disables steering at zero", async () => {
    vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"] });
    const idleUntil = "2026-04-02T15:30:00Z";
    vi.setSystemTime(Date.parse(idleUntil) - 252_000);
    const run = runningRun({ steer: { enabled: true, reason: "", idle_until: idleUntil } });
    installRuns({ run });
    await renderApp(`/runs/${run.id}`);

    expect(await screen.findByText(/Idle: accepting follow-ups for 4m 12s/)).toBeInTheDocument();
    expect(screen.getByLabelText("Send a follow-up to the agent")).toBeEnabled();

    await vi.advanceTimersByTimeAsync(1000);
    expect(screen.getByText(/Idle: accepting follow-ups for 4m 11s/)).toBeInTheDocument();

    await vi.advanceTimersByTimeAsync(251_000);
    expect(screen.queryByText(/Idle: accepting follow-ups/)).not.toBeInTheDocument();
    const reason = `run finished; idle window ended at ${idleUntil}`;
    expect(screen.getByText(reason)).toBeInTheDocument();
    const area = screen.getByLabelText("Send a follow-up to the agent");
    expect(area).toBeDisabled();
    expect(screen.getByRole("button", { name: "Send follow-up" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Interrupt" })).toBeDisabled();
    expect(area.getAttribute("aria-describedby")).toContain("steer-reason");
    expect(screen.getByRole("heading", { name: "Settle the batch" })).toBeInTheDocument();
  });

  it("cancels only a live run and refetches the detail", async () => {
    const user = userEvent.setup();
    const base = runningRun();
    for (const status of ["queued", "assigned", "running"]) {
      installRuns({ run: { ...base, status } });
      const view = await renderApp(`/runs/${base.id}`);
      expect(await screen.findByRole("button", { name: "Cancel run" })).toBeInTheDocument();
      view.unmount();
    }
    for (const status of ["idle", "succeeded", "failed", "cancelled"]) {
      installRuns({
        run: {
          ...base,
          status,
          steer: {
            enabled: false,
            reason: "run finished; idle window ended at 2026-04-02T15:30:00Z",
          },
        },
      });
      const view = await renderApp(`/runs/${base.id}`);
      expect(await screen.findByRole("heading", { name: "Settle the batch" })).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Cancel run" })).not.toBeInTheDocument();
      view.unmount();
    }

    let status = "running";
    let cancels = 0;
    installRuns({ run: { ...base, status: "running" } });
    server.use(
      http.get("/api/v1/agent/runs/:id", () => HttpResponse.json({ ...base, status })),
      http.post("/api/v1/agent/runs/:id/cancel", () => {
        cancels += 1;
        status = "cancelled";
        return HttpResponse.json({ ...base, status });
      }),
    );
    await renderApp(`/runs/${base.id}`);
    await user.click(await screen.findByRole("button", { name: "Cancel run" }));
    const dialog = await screen.findByRole("dialog", { name: "Cancel this run?" });
    await user.click(within(dialog).getByRole("button", { name: "Cancel run" }));
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "Cancel run" })).not.toBeInTheDocument(),
    );
    expect(cancels).toBe(1);
    expect(screen.getByText("Cancelled")).toBeInTheDocument();
  });

  it("fills timeline, usage, files, and the child tree from the API", async () => {
    const user = userEvent.setup();
    const bus = createEventBus();
    const child: RunRow = {
      id: "run_child_settlement",
      parent_run_id: goldenRun.id,
      status: "succeeded",
      title: "Child settlement",
      agent: "reviewer",
      queue_wait_ms: 5,
      exec_ms: 6,
    };
    const stranger: RunRow = {
      id: "run_stranger",
      parent_run_id: "run_else",
      status: "running",
      title: "Stranger run",
      queue_wait_ms: 1,
      exec_ms: 1,
    };
    installRuns({
      list: [child, stranger, ...goldenRuns],
      events: (request) => bus.response(request),
    });
    await renderApp(`/runs/${goldenRun.id}`);
    await screen.findByRole("region", { name: "Failure" });
    bus.send(
      sseEvent(1, "file.changed", {
        path: "internal/settlement/cutoff.go",
        additions: 9,
        deletions: 7,
      }),
    );

    await user.click(screen.getByRole("tab", { name: "Timeline" }));
    expect(await screen.findAllByText("queued")).not.toHaveLength(0);
    expect(document.querySelectorAll("[data-execution-id]")).toHaveLength(2);
    expect(document.querySelector('[data-execution-id="exec_1"]')).not.toBeNull();
    expect(document.querySelector(".phase-name")?.textContent).toBe("queued");
    expect(screen.queryByText("idle")).not.toBeInTheDocument();

    await user.click(screen.getByRole("tab", { name: "Usage" }));
    const input = goldenUsage.calls.reduce((sum, call) => sum + call.input_tokens, 0);
    const output = goldenUsage.calls.reduce((sum, call) => sum + call.output_tokens, 0);
    expect(input).toBe(goldenUsage.total.input_tokens);
    expect(output).toBe(goldenUsage.total.output_tokens);
    expect(await screen.findByTestId("usage-input-tokens")).toHaveTextContent("1,000");
    expect(screen.getByTestId("usage-output-tokens")).toHaveTextContent("200");
    expect(screen.getByTestId("usage-cost")).toHaveTextContent("$0.03");

    await user.click(screen.getByRole("tab", { name: "Files & PR" }));
    expect(await screen.findByText("internal/settlement/cutoff.go")).toBeInTheDocument();
    expect(screen.getAllByRole("link", { name: "Open PR #94" }).length).toBeGreaterThan(0);
    expect(screen.queryByText(/check/i)).not.toBeInTheDocument();

    await user.click(screen.getByRole("tab", { name: "Tree" }));
    expect(await screen.findByText("Child settlement")).toBeInTheDocument();
    expect(screen.queryByText("Stranger run")).not.toBeInTheDocument();
    expect(screen.getByText("parent")).toBeInTheDocument();
    expect(screen.getByText("(this run)")).toBeInTheDocument();
  });

  it("does not render a planted secret from the run or an event", async () => {
    const restore = mockTranscriptViewport(320);
    const bus = createEventBus();
    const leaked = { ...goldenRun, token: SECRET };
    try {
      installRuns({
        run: leaked,
        events: (request) => bus.response(request),
      });
      await renderApp(`/runs/${goldenRun.id}`);
      await screen.findByRole("region", { name: "Failure" });
      bus.send(
        sseEvent(1, "tool.call", {
          tool: "bash",
          redacted_input: "echo ok",
          input: SECRET,
          leak: SECRET,
        }),
      );
      expect(await screen.findByText("echo ok")).toBeInTheDocument();
      expect(document.body.textContent).not.toContain(SECRET);
      expect(document.body.innerHTML).not.toContain(SECRET);
    } finally {
      restore();
    }
  });
});
