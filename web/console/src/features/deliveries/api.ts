import { APIError, UnauthorizedError } from "../../api/client";
import { clearAPIKey, getAPIKey } from "../../api/session";
import type { Delivery, Problem } from "../../api/types";

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
};

const SENSITIVE_HEADER = /signature|secret|token|authorization/i;

// Lift candidate: client.listDeliveries cannot pass include=body, so the
// detail view would never receive a body from factoryd.
export async function listDeliveriesDetailed(
  factoryId: string,
  query: { status?: string; limit?: number } = {},
): Promise<DeliveryRow[]> {
  const params = new URLSearchParams();
  if (query.status) params.set("status", query.status);
  if (query.limit !== undefined) params.set("limit", String(query.limit));
  params.set("include", "body");
  const path = `/api/v1/factory/${encodeURIComponent(factoryId)}/deliveries?${params.toString()}`;
  const rows = await authedGet<Delivery[]>(path);
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

async function authedGet<T>(path: string): Promise<T> {
  const headers = new Headers({ Accept: "application/json" });
  const key = getAPIKey();
  if (key) headers.set("Authorization", `Bearer ${key}`);
  const response = await fetch(path, { headers });
  const text = await response.text();
  if (response.status === 401) {
    clearAPIKey();
    throw new UnauthorizedError();
  }
  if (!response.ok) throw new APIError(response.status, readProblem(text));
  if (!text) return [] as T;
  try {
    return JSON.parse(text) as T;
  } catch {
    throw new APIError(response.status, null);
  }
}

function readProblem(text: string): Problem | null {
  if (!text) return null;
  try {
    return JSON.parse(text) as Problem;
  } catch {
    return null;
  }
}
