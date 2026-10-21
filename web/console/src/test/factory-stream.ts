import { http, HttpResponse } from "msw";
import { server } from "../mocks/server";

const encoder = new TextEncoder();

let streams = new Map<string, ReadableStreamDefaultController<Uint8Array>>();

export function installFactoryStream(): Map<string, ReadableStreamDefaultController<Uint8Array>> {
  streams = new Map();
  server.use(
    http.get("/api/v1/factory/:factoryId/events", ({ params }) => {
      const id = String(params.factoryId);
      const stream = new ReadableStream<Uint8Array>({
        start(controller) {
          streams.set(id, controller);
        },
      });
      return new HttpResponse(stream, {
        headers: { "Content-Type": "text/event-stream" },
      });
    }),
  );
  return streams;
}

export function emit(factoryId: string, frame: string): void {
  streams.get(factoryId)?.enqueue(encoder.encode(frame));
}
