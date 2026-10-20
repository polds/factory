import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import { APIError, ConsoleClient } from "../api/client";
import { setAPIKey } from "../api/session";
import { demoAPIKey } from "../mocks/demo-key";
import { server } from "../mocks/server";

function keysNamed(value: unknown, name: string, found: string[]) {
  if (Array.isArray(value)) {
    for (const item of value) keysNamed(item, name, found);
    return;
  }
  if (!value || typeof value !== "object") return;
  for (const [key, child] of Object.entries(value)) {
    if (key === name) found.push(key);
    keysNamed(child, name, found);
  }
}

describe("console client", () => {
  it("reads the contract routes", async () => {
    setAPIKey(demoAPIKey);
    const api = new ConsoleClient();
    let runsQuery = "";
    server.use(
      http.get("/api/v1/agent/runs", ({ request }) => {
        runsQuery = new URL(request.url).search;
        return HttpResponse.json([
          {
            id: "run_01",
            status: "failed",
            failure: {
              class: "agent",
              code: "agent_reported",
              summary: "exit 10",
              retryable: false,
              evidence: [],
            },
          },
        ]);
      }),
    );

    const runs = await api.listRuns({ failure_class: "agent", work_item_id: "task_01" });
    expect(runsQuery).toContain("failure_class=agent");
    expect(runsQuery).toContain("work_item_id=task_01");
    expect(runs[0]?.id).toBe("run_01");
    expect(runs[0]?.status).toBe("failed");

    const detail = await api.getRun("run_01");
    expect(detail.failure?.code).toBe("agent_reported");
    expect(detail.queue_wait_ms).toBe(660000);
    expect(detail.steer?.enabled).toBe(false);

    expect((await api.getDefinition("fac_payments")).source).toBe("git");
    expect((await api.runTimeline("run_01"))[0]?.phase).toBe("queued");
    expect((await api.runUsage("run_01")).total.input_tokens).toBe(10);
    expect((await api.interruptRun("run_01")).steer_id).toBe("steer_01");
    expect((await api.followUp("run_01", "continue")).steer_id).toBe("steer_01");
    expect((await api.nextSteers("run_01", "steer_00", "rt_test_run_token"))[0]?.kind).toBe(
      "followup",
    );
    expect((await api.inbox("fac_payments"))[0]?.kind).toBe("approval");
    expect((await api.listAutomations("fac_payments"))[0]?.name).toBe("labeled-intake");
    await api.pauseAutomation("fac_payments", "labeled-intake");
    await api.resumeAutomation("fac_payments", "labeled-intake");
    expect((await api.listWebhooks("fac_payments"))[0]?.uid).toBe("wh_01");
    expect((await api.listWorkers()).workers[0]?.id).toBe("worker_01");
    const secrets = await api.listSecrets("fac_payments");
    const valueKeys: string[] = [];
    keysNamed(secrets, "value", valueKeys);
    expect(valueKeys).toEqual([]);
    expect((await api.listIdentities())[0]?.kind).toBe("github_app");
    expect(
      (await api.summary("fac_payments", { from: "2026-10-01", to: "2026-10-17" }))
        .waiting_on_human,
    ).toBe(2);
  });

  it("keeps a run token out of the error", async () => {
    setAPIKey(demoAPIKey);
    const token = "rt_super_secret_value";
    server.use(
      http.get("/api/v1/agent/runs/:id/steer", () =>
        HttpResponse.json({ title: "unavailable" }, { status: 500 }),
      ),
    );
    const api = new ConsoleClient();
    const err = await api.nextSteers("run_01", "after", token).then(
      () => null,
      (error: unknown) => error,
    );
    expect(err).toBeInstanceOf(APIError);
    expect(String(err)).not.toContain(token);
    expect(JSON.stringify(err)).not.toContain(token);
  });
});
