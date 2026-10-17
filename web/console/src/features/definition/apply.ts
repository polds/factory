import { APIError, UnauthorizedError } from "../../api/client";
import { clearAPIKey, getAPIKey } from "../../api/session";
import type { ApplyResponse, DefinitionFile, Problem } from "../../api/types";

// Lift candidate: ConsoleClient.applyDefinition posts no body. Validate and
// apply both need the edited tree, so this stays next to the editor until
// the shared client grows an applyDefinitionTree method.
export async function applyDefinitionTree(
  factoryId: string,
  files: readonly DefinitionFile[],
): Promise<ApplyResponse> {
  const headers = new Headers({
    Accept: "application/json",
    "Content-Type": "application/json",
  });
  const key = getAPIKey();
  if (key) headers.set("Authorization", `Bearer ${key}`);
  const response = await fetch(
    `/api/v1/factory/${encodeURIComponent(factoryId)}/definition:apply`,
    {
      method: "POST",
      headers,
      body: JSON.stringify({
        files: files.map((file) => ({ path: file.path, content: file.content })),
      }),
    },
  );
  const text = await response.text();
  if (response.status === 401) {
    clearAPIKey();
    throw new UnauthorizedError();
  }
  if (!response.ok) throw new APIError(response.status, readProblem(text));
  if (!text) throw new APIError(response.status, null);
  try {
    return JSON.parse(text) as ApplyResponse;
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
