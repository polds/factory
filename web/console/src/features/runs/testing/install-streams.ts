import { http, HttpResponse } from "msw";
import { server } from "../../../mocks/server";

type Listed = { info?: { method?: unknown; path?: unknown } };

function coversFactoryEvents(handlers: readonly Listed[]): boolean {
  return handlers.some((handler) => {
    const method = handler.info?.method;
    const path = handler.info?.path;
    return (
      method === "GET" &&
      typeof path === "string" &&
      path.includes("/factory/") &&
      path.includes("/events")
    );
  });
}

// Runs list tests render /runs against the shared MSW server. Factory event
// streams are not in those handlers, and this task cannot edit them.
// Install a quiet stream only when a test has not registered its own.
export function installRunsStreamFallback(): void {
  if (coversFactoryEvents(server.listHandlers() as readonly Listed[])) return;
  server.use(
    http.get("/api/v1/factory/:factoryId/events", () => {
      const stream = new ReadableStream({
        start() {
          // Stay open. A data event would refresh the runs list.
        },
      });
      return new HttpResponse(stream, {
        headers: { "Content-Type": "text/event-stream" },
      });
    }),
  );
}
