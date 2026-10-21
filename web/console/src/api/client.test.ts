import { http, HttpResponse } from "msw";
import { describe, expect, expectTypeOf, it } from "vitest";
import { demoAPIKey } from "../mocks/demo-key";
import { server } from "../mocks/server";
import { APIError, ConsoleClient } from "./client";
import { setAPIKey } from "./session";
import type { Run, RunRow } from "./types";

describe("console client fetch plumbing", () => {
  const api = new ConsoleClient();

  it("returns [] when a detailed delivery list has an empty body", async () => {
    setAPIKey(demoAPIKey);
    server.use(
      http.get("/api/v1/factory/:uid/deliveries", ({ request }) => {
        const url = new URL(request.url);
        expect(url.search).toBe("?status=unmatched&limit=5&include=body");
        return new HttpResponse(null, { status: 200 });
      }),
    );
    await expect(
      api.listDeliveriesDetailed("fac_1", { status: "unmatched", limit: 5 }),
    ).resolves.toEqual([]);
  });

  it("keeps a JSON null delivery list", async () => {
    setAPIKey(demoAPIKey);
    server.use(http.get("/api/v1/factory/:uid/deliveries", () => HttpResponse.json(null)));
    await expect(api.listDeliveriesDetailed("fac_1")).resolves.toBeNull();
  });

  it("throws APIError when apply has an empty body", async () => {
    setAPIKey(demoAPIKey);
    server.use(
      http.post("/api/v1/factory/:uid/definition:apply", async ({ request }) => {
        expect(await request.json()).toEqual({
          files: [{ path: "factory.yaml", content: "name: a\n" }],
        });
        return new HttpResponse(null, { status: 200 });
      }),
    );
    const error = await api
      .applyDefinitionTree("fac_1", [{ path: "factory.yaml", content: "name: a\n" }])
      .then(
        () => null,
        (caught: unknown) => caught,
      );
    expect(error).toBeInstanceOf(APIError);
    if (!(error instanceof APIError)) return;
    expect(error.status).toBe(200);
    expect(error.problem).toBeNull();
    expect(error.message).toBe("HTTP 200");
  });

  it("derives RunRow from Run", () => {
    expectTypeOf<RunRow>().toEqualTypeOf<
      Omit<
        Run,
        "failure" | "steer" | "pr_url" | "kubernetes_snapshot" | "queue_wait_ms" | "exec_ms"
      > & {
        queue_wait_ms: number;
        exec_ms: number;
      }
    >();
  });
});
