import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { NativePanel, type NativeTreeView } from "./native-panel";

const view: NativeTreeView = {
  invocationId: "inv_1",
  stageOwner: "coordinator",
  nodes: [
    { taskId: "ntask_1", assignee: "coordinator", status: "parked", depth: 0, stageOwner: true },
  ],
  budgets: [
    {
      scopeId: "invocation:inv_1",
      kind: "invocation",
      ceilingCents: 2500,
      spentCents: 300,
      reservedCents: 0,
      paused: false,
    },
  ],
  blockers: [
    {
      parentTaskId: "ntask_1",
      childTaskId: "ntask_2",
      policy: "fail_parent",
      childStatus: "running",
    },
  ],
};

describe("NativePanel", () => {
  it("shows budget, blockers, and a custom stage", () => {
    render(<NativePanel views={[view]} customStages={["dispute", "building"]} />);
    expect(screen.getByText("Budget invocation 300 of 2500")).toBeInTheDocument();
    expect(screen.getByText("Blocker ntask_2 running fail_parent")).toBeInTheDocument();
    expect(screen.getByText("Custom stage dispute")).toBeInTheDocument();
    expect(screen.getByText("Custom stage building")).toBeInTheDocument();
    expect(screen.getByText("Stage owner coordinator")).toBeInTheDocument();
  });
});
