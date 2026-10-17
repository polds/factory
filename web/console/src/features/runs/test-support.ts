import { http, HttpResponse } from "msw";
import { vi } from "vitest";
import type { HarnessUsage, TimelinePhase } from "../../api/types";
import { server } from "../../mocks/server";
import harnessUsageFixture from "./__fixtures__/harness-usage.json";
import runFixture from "./__fixtures__/run.json";
import runsFixture from "./__fixtures__/runs.json";
import timelineFixture from "./__fixtures__/timeline.json";
import type { RunDetailWire, RunRow } from "./types";

export const goldenRuns = runsFixture as RunRow[];
export const goldenRun = runFixture as RunDetailWire;
export const goldenTimeline = timelineFixture as TimelinePhase[];
export const goldenUsage = harnessUsageFixture as HarnessUsage;

export const SECRET = "rt_SECRET_PLANT";

type RunsHandlers = {
  run?: RunDetailWire;
  list?: RunRow[];
  onList?: (url: URL) => void;
  events?: (request: Request) => Response;
  onInterrupt?: () => void;
  onFollowUp?: (text: string) => void;
  followUp?: {
    status: number;
    body: {
      type?: string;
      title?: string;
      status?: number;
      detail?: string;
      reason: string;
    };
  };
  onCancel?: () => RunDetailWire | void;
};

export function installRuns(options: RunsHandlers = {}): void {
  const run = options.run ?? goldenRun;
  server.use(
    http.get("/api/v1/agent/runs", ({ request }) => {
      const url = new URL(request.url);
      options.onList?.(url);
      return HttpResponse.json(options.list ?? goldenRuns);
    }),
    http.get("/api/v1/agent/runs/:id/steers", () => HttpResponse.json([])),
    http.get("/api/v1/agent/runs/:id/timeline", () => HttpResponse.json(goldenTimeline)),
    http.get("/api/v1/agent/runs/:id/harness-usage", () => HttpResponse.json(goldenUsage)),
    http.get("/api/v1/agent/runs/:id/events", ({ request }) => {
      return options.events?.(request) ?? holdStream(request);
    }),
    http.post("/api/v1/agent/runs/:id/interrupt", () => {
      options.onInterrupt?.();
      return HttpResponse.json({ steer_id: "steer_01" }, { status: 202 });
    }),
    http.post("/api/v1/agent/runs/:id/followups", async ({ request }) => {
      const body = (await request.json()) as { text?: string };
      options.onFollowUp?.(body.text ?? "");
      if (options.followUp) {
        return HttpResponse.json(options.followUp.body, { status: options.followUp.status });
      }
      return HttpResponse.json({ steer_id: "steer_02" }, { status: 202 });
    }),
    http.post("/api/v1/agent/runs/:id/cancel", () => {
      const next = options.onCancel?.();
      return HttpResponse.json(next ?? { ...run, status: "cancelled" });
    }),
    http.get("/api/v1/agent/runs/:id", () => HttpResponse.json(run)),
  );
}

export function holdStream(request: Request, body = ""): Response {
  const encoder = new TextEncoder();
  let sent = false;
  return new Response(
    new ReadableStream({
      pull(controller) {
        if (request.signal.aborted) {
          controller.close();
          return;
        }
        if (!sent) {
          sent = true;
          if (body) controller.enqueue(encoder.encode(body));
          return;
        }
        return new Promise<void>((resolve) => {
          const finish = () => {
            try {
              controller.close();
            } catch {
              // The reader already cancelled the stream.
            }
            resolve();
          };
          if (request.signal.aborted) {
            finish();
            return;
          }
          request.signal.addEventListener("abort", finish, { once: true });
        });
      },
    }),
    { status: 200, headers: { "Content-Type": "text/event-stream" } },
  );
}

export function scriptedStream(request: Request, body: string): Response {
  const encoder = new TextEncoder();
  let pulls = 0;
  return new Response(
    new ReadableStream({
      pull(controller) {
        if (request.signal.aborted) {
          controller.close();
          return;
        }
        pulls += 1;
        if (pulls === 1) {
          controller.enqueue(encoder.encode(body));
          return;
        }
        // Let the consumer read the queued chunk before the drop. Erroring in
        // the same turn discards that chunk inside the intercepted stream.
        return new Promise<void>((resolve) => {
          const drop = () => {
            try {
              controller.error(new Error("dropped"));
            } catch {
              // The reader already cancelled.
            }
            resolve();
          };
          if (request.signal.aborted) {
            drop();
            return;
          }
          window.setTimeout(drop, 20);
        });
      },
    }),
    { status: 200, headers: { "Content-Type": "text/event-stream" } },
  );
}

export function createEventBus() {
  const encoder = new TextEncoder();
  let queued: Uint8Array[] = [];
  let wake: (() => void) | null = null;
  let closed = false;
  let controller: ReadableStreamDefaultController<Uint8Array> | null = null;

  return {
    send(text: string) {
      if (closed) return;
      queued.push(encoder.encode(text));
      const fn = wake;
      wake = null;
      fn?.();
    },
    close() {
      closed = true;
      try {
        controller?.close();
      } catch {
        // The reader already finished this stream.
      }
      const fn = wake;
      wake = null;
      fn?.();
    },
    response(request: Request): Response {
      queued = [];
      wake = null;
      closed = false;
      controller = null;
      return new Response(
        new ReadableStream({
          start(ctrl) {
            controller = ctrl;
          },
          pull(ctrl) {
            if (request.signal.aborted || closed) {
              try {
                ctrl.close();
              } catch {
                // Already closed.
              }
              return;
            }
            if (queued.length > 0) {
              const next = queued.shift();
              if (next) ctrl.enqueue(next);
              return;
            }
            return new Promise<void>((resolve) => {
              const finish = () => {
                wake = null;
                if (request.signal.aborted || closed) {
                  try {
                    ctrl.close();
                  } catch {
                    // Already closed.
                  }
                  resolve();
                  return;
                }
                const next = queued.shift();
                if (next) ctrl.enqueue(next);
                resolve();
              };
              wake = finish;
              request.signal.addEventListener("abort", finish, { once: true });
            });
          },
        }),
        { status: 200, headers: { "Content-Type": "text/event-stream" } },
      );
    },
  };
}

export function sseEvent(id: number, event: string, data: Record<string, unknown>): string {
  return `id: ${id}\nevent: ${event}\ndata: ${JSON.stringify(data)}\n\n`;
}

export function statusEvents(from: number, to: number): string {
  let out = "";
  for (let id = from; id <= to; id += 1) {
    out += sseEvent(id, "run.status", { status: `marker-${id}` });
  }
  return out;
}

export function repeatedStatus(count: number): string {
  const parts = new Array<string>(count);
  for (let index = 0; index < count; index += 1) {
    parts[index] = `id: ${index + 1}\nevent: run.status\ndata: {"status":"running"}\n\n`;
  }
  return parts.join("");
}

export function seqValues(root: ParentNode = document): string[] {
  return [...root.querySelectorAll("[data-seq]")].map(
    (node) => node.getAttribute("data-seq") ?? "",
  );
}

export function mockTranscriptViewport(height: number): () => void {
  const heightDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "clientHeight");
  const scrollDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollHeight");
  const clientHeight = vi
    .spyOn(HTMLElement.prototype, "clientHeight", "get")
    .mockImplementation(function clientHeightOf(this: HTMLElement) {
      if (this.classList.contains("transcript-log")) return height;
      return heightDescriptor?.get?.call(this) ?? 0;
    });
  const scrollHeight = vi
    .spyOn(HTMLElement.prototype, "scrollHeight", "get")
    .mockImplementation(function scrollHeightOf(this: HTMLElement) {
      if (this.classList.contains("transcript-log")) {
        const inner = this.querySelector(".transcript-inner");
        if (inner instanceof HTMLElement) {
          const parsed = Number.parseFloat(inner.style.height);
          if (Number.isFinite(parsed) && parsed > 0) return parsed;
        }
        return height;
      }
      return scrollDescriptor?.get?.call(this) ?? 0;
    });
  return () => {
    clientHeight.mockRestore();
    scrollHeight.mockRestore();
  };
}

export function runningRun(extra: Partial<RunDetailWire> = {}): RunDetailWire {
  return {
    id: "run_01M53XJ9A6NZ62RF26AK9ZDNQL",
    status: "running",
    factory_id: "fac_1",
    agent: "implementer",
    title: "Settle the batch",
    stage: "building",
    model_id: "gpt-test",
    work_item_id: "task_01",
    queue_wait_ms: 0,
    exec_ms: 1200,
    steer: { enabled: true, reason: "" },
    ...extra,
  };
}
