import { describe, expect, it } from "vitest";
import { latestByWorkItem, type RunRow } from "./model";

function run(partial: Pick<RunRow, "id" | "created_at"> & Partial<RunRow>): RunRow {
  return {
    status: "succeeded",
    work_item_id: "task_04",
    ...partial,
  };
}

describe("latestByWorkItem", () => {
  const older = run({
    id: "run_old",
    created_at: "2026-10-17T09:00:00Z",
    agent: "planner",
    model_id: "old-model",
  });
  const newer = run({
    id: "run_new",
    created_at: "2026-10-17T12:00:00Z",
    agent: "implementer",
    model_id: "model-a",
  });

  it("keeps the newest run when the server lists newest first", () => {
    const picked = latestByWorkItem([newer, older]).get("task_04");
    expect(picked?.id).toBe("run_new");
    expect(picked?.agent).toBe("implementer");
  });

  it("keeps the newest run when the rows are not in server order", () => {
    expect(latestByWorkItem([older, newer]).get("task_04")?.id).toBe("run_new");
    expect(latestByWorkItem([older, newer, older]).get("task_04")?.id).toBe("run_new");
    const tiedEarlier = run({
      id: "run_a",
      created_at: "2026-10-17T12:00:00Z",
      work_item_id: "task_09",
    });
    const tiedLater = run({
      id: "run_z",
      created_at: "2026-10-17T12:00:00Z",
      work_item_id: "task_09",
    });
    expect(latestByWorkItem([tiedEarlier, tiedLater]).get("task_09")?.id).toBe("run_z");
    expect(latestByWorkItem([tiedLater, tiedEarlier]).get("task_09")?.id).toBe("run_z");
  });
});
