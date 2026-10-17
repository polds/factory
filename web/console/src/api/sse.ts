import { clearAPIKey, getAPIKey } from "./session";
import { UnauthorizedError } from "./client";

export type SSERecord = {
  id: string;
  event: string;
  data: string;
};

export type SSEStatus = "connecting" | "open" | "reconnecting";

export type ConsumeSSEOptions = {
  url: string;
  cursor?: string;
  signal: AbortSignal;
  fetchImpl?: typeof fetch;
  sleep?: (ms: number, signal: AbortSignal) => Promise<void>;
  backoffMs?: (attempt: number) => number;
  onEvent: (event: SSERecord) => void;
  onStatus?: (status: SSEStatus) => void;
};

export function takeCompleteEvents(buffer: string): { events: SSERecord[]; rest: string } {
  const events: SSERecord[] = [];
  let consumed = 0;
  let cursor = 0;
  const pending: string[] = [];
  while (cursor < buffer.length) {
    const nl = buffer.indexOf("\n", cursor);
    if (nl === -1) break;
    let line = buffer.slice(cursor, nl);
    if (line.endsWith("\r")) line = line.slice(0, -1);
    cursor = nl + 1;
    if (line === "") {
      const parsed = parseBlock(pending);
      pending.length = 0;
      if (parsed) events.push(parsed);
      consumed = cursor;
      continue;
    }
    pending.push(line);
  }
  return { events, rest: buffer.slice(consumed) };
}

function parseBlock(lines: string[]): SSERecord | null {
  let event = "";
  let id = "";
  let hasID = false;
  const data: string[] = [];
  for (const line of lines) {
    if (line.startsWith(":")) continue;
    const colon = line.indexOf(":");
    const field = colon === -1 ? line : line.slice(0, colon);
    let value = colon === -1 ? "" : line.slice(colon + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    switch (field) {
      case "event":
        event = value;
        break;
      case "data":
        data.push(value);
        break;
      case "id":
        if (!value.includes("\u0000")) {
          id = value;
          hasID = true;
        }
        break;
      default:
        break;
    }
  }
  if (data.length === 0) return null;
  return { id: hasID ? id : "", event, data: data.join("\n") };
}

export function withCursor(raw: string, cursor: string): string {
  const url = new URL(raw, "http://console.local");
  if (cursor) url.searchParams.set("from_seq", cursor);
  if (/^[a-z][a-z0-9+.-]*:/i.test(raw)) return url.toString();
  return `${url.pathname}${url.search}`;
}

export function defaultBackoff(attempt: number): number {
  const step = Math.max(attempt, 1);
  return Math.min(10_000, 250 * 2 ** (step - 1));
}

export async function defaultSleep(ms: number, signal: AbortSignal): Promise<void> {
  if (signal.aborted || ms <= 0) return;
  await new Promise<void>((resolve) => {
    const timer = setTimeout(() => {
      signal.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    const onAbort = () => {
      clearTimeout(timer);
      resolve();
    };
    signal.addEventListener("abort", onAbort, { once: true });
  });
}

export async function consumeSSE(options: ConsumeSSEOptions): Promise<void> {
  const fetchImpl = options.fetchImpl ?? globalThis.fetch;
  const sleep = options.sleep ?? defaultSleep;
  const backoffMs = options.backoffMs ?? defaultBackoff;
  const seen = new Set<string>();
  let lastID = options.cursor ?? "";
  let attempt = 0;

  while (!options.signal.aborted) {
    options.onStatus?.(attempt === 0 ? "connecting" : "reconnecting");
    let received: boolean;
    try {
      received = await readOnce(
        options,
        fetchImpl,
        seen,
        () => lastID,
        (id) => {
          lastID = id;
        },
      );
    } catch (err) {
      if (options.signal.aborted) return;
      if (err instanceof UnauthorizedError) throw err;
      received = false;
    }
    if (options.signal.aborted) return;
    if (received) attempt = 0;
    attempt += 1;
    await sleep(backoffMs(attempt), options.signal);
  }
}

async function readOnce(
  options: ConsumeSSEOptions,
  fetchImpl: typeof fetch,
  seen: Set<string>,
  lastID: () => string,
  setLastID: (id: string) => void,
): Promise<boolean> {
  const cursor = lastID();
  const headers = new Headers();
  headers.set("Accept", "text/event-stream");
  const key = getAPIKey();
  if (key) headers.set("Authorization", `Bearer ${key}`);
  if (cursor) headers.set("Last-Event-ID", cursor);
  const response = await fetchImpl(withCursor(options.url, cursor), {
    headers,
    signal: options.signal,
  });
  if (response.status === 401) {
    clearAPIKey();
    throw new UnauthorizedError();
  }
  const contentType = response.headers.get("Content-Type") ?? "";
  if (!response.ok || !response.body || !contentType.includes("text/event-stream")) {
    return false;
  }
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  let received = false;
  try {
    while (!options.signal.aborted) {
      const chunk = await reader.read();
      if (chunk.done) {
        buffer += decoder.decode();
        break;
      }
      buffer += decoder.decode(chunk.value, { stream: true });
      const taken = takeCompleteEvents(buffer);
      buffer = taken.rest;
      if (dispatch(taken.events, seen, setLastID, options.onEvent)) {
        received = true;
        options.onStatus?.("open");
      }
    }
  } finally {
    try {
      await reader.cancel();
    } catch {
      // The stream already failed or closed.
    }
  }
  return received;
}

function dispatch(
  events: SSERecord[],
  seen: Set<string>,
  setLastID: (id: string) => void,
  onEvent: (event: SSERecord) => void,
): boolean {
  let any = false;
  for (const event of events) {
    if (event.id !== "" && seen.has(event.id)) continue;
    if (event.id !== "") {
      seen.add(event.id);
      setLastID(event.id);
    }
    any = true;
    onEvent(event);
  }
  return any;
}
