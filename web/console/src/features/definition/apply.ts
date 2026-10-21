import { client } from "../../api/client";
import type { ApplyResponse, DefinitionFile } from "../../api/types";

export async function applyDefinitionTree(
  factoryId: string,
  files: readonly DefinitionFile[],
): Promise<ApplyResponse> {
  return client.applyDefinitionTree(factoryId, files);
}
