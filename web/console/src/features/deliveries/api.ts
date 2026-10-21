import { client } from "../../api/client";
import type { Delivery, NormalizedEvent } from "../../api/types";

export type HeaderEntry = {
  name: string;
  value: string;
};

export type DeliveryRow = {
  id: string;
  provider: string;
  webhook_uid: string;
  delivery_id: string;
  received_at: string;
  headers: HeaderEntry[];
  bodyText: string;
  truncated: boolean;
  status: string;
  matched_automations: string[];
  events: NormalizedEventView[];
};

export type NormalizedEventView = {
  provider: string;
  event: string;
  subjectKind: string;
  subjectId: string;
  subjectNumber: string;
  subjectTitle: string;
  actorHandle: string;
  thread: string;
};

const SENSITIVE_HEADER = /signature|secret|token|authorization/i;

export async function listDeliveriesDetailed(
  factoryId: string,
  query: { status?: string; limit?: number } = {},
): Promise<DeliveryRow[]> {
  const rows = await client.listDeliveriesDetailed(factoryId, query);
  return rows.map(presentDelivery);
}

export function presentDelivery(raw: Delivery): DeliveryRow {
  return {
    id: raw.id,
    provider: raw.provider,
    webhook_uid: raw.webhook_uid ?? "",
    delivery_id: raw.delivery_id,
    received_at: raw.received_at,
    headers: headerEntries(raw.headers),
    bodyText: formatBody(raw.body),
    truncated: Boolean(raw.truncated),
    status: raw.status,
    matched_automations: Array.isArray(raw.matched_automations) ? raw.matched_automations : [],
    events: Array.isArray(raw.normalized_events) ? raw.normalized_events.map(presentEvent) : [],
  };
}

function presentEvent(raw: NormalizedEvent): NormalizedEventView {
  const thread = [raw.thread?.kind, raw.thread?.external_ref].filter((part): part is string =>
    Boolean(part),
  );
  return {
    provider: raw.provider ?? "",
    event: raw.event ?? "",
    subjectKind: raw.subject?.kind ?? "",
    subjectId: raw.subject?.id ?? "",
    subjectNumber: raw.subject?.number === undefined ? "" : String(raw.subject.number),
    subjectTitle: raw.subject?.title ?? "",
    actorHandle: raw.actor?.handle ?? "",
    thread: thread.join(" "),
  };
}

export function headerEntries(headers: Record<string, string> | undefined): HeaderEntry[] {
  if (!headers) return [];
  return Object.keys(headers).map((name) => ({
    name,
    value: SENSITIVE_HEADER.test(name) ? "[redacted]" : (headers[name] ?? ""),
  }));
}

export function formatBody(body: unknown): string {
  if (body === undefined || body === null) return "";
  if (typeof body === "string") return body;
  if (typeof body === "number" || typeof body === "boolean") return String(body);
  try {
    return JSON.stringify(body, null, 2);
  } catch {
    return "";
  }
}
