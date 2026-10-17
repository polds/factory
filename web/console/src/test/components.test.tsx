import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { DurationPair } from "../components/DurationPair";
import { EmptyState } from "../components/EmptyState";
import { ErrorState } from "../components/ErrorState";
import { FailureClassFilter } from "../components/FailureClassFilter";
import { VirtualList } from "../components/VirtualList";
import type { FailureClass } from "../api/types";
import { renderWithTheme } from "./render";

describe("shared components", () => {
  it("shows queue wait beside execution", () => {
    renderWithTheme(<DurationPair queueWaitMs={660000} execMs={40000} />);
    expect(screen.getByText("11m").closest("p")).toHaveTextContent("Queue 11m | Execution 40s");
  });

  it("filters by failure class", async () => {
    const user = userEvent.setup();
    function Harness() {
      const [value, setValue] = useState<FailureClass | "">("");
      return <FailureClassFilter value={value} onChange={setValue} />;
    }
    renderWithTheme(<Harness />);
    const agent = screen.getByRole("button", { name: "Agent" });
    expect(screen.getByRole("button", { name: "All" })).toHaveAttribute("aria-pressed", "true");
    await user.click(agent);
    expect(agent).toHaveAttribute("aria-pressed", "true");
    await user.click(agent);
    expect(agent).toHaveAttribute("aria-pressed", "false");
  });

  it("virtualizes a list", () => {
    const height = vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(240);
    const width = vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(400);
    try {
      const items = Array.from({ length: 50 }, (_, index) => `row ${index}`);
      renderWithTheme(
        <VirtualList
          items={items}
          label="Transcript"
          getKey={(item) => item}
          renderItem={(item) => <span>{item}</span>}
        />,
      );
      expect(screen.getByRole("list", { name: "Transcript" })).toBeInTheDocument();
      expect(screen.getByText("row 0")).toBeInTheDocument();
      expect(screen.queryByText("row 49")).not.toBeInTheDocument();
    } finally {
      height.mockRestore();
      width.mockRestore();
    }
  });

  it("confirms a destructive action", async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    renderWithTheme(
      <ConfirmDialog
        open
        title="Cancel run"
        description="Stop this run?"
        confirmLabel="Cancel run"
        tone="danger"
        onConfirm={onConfirm}
        onOpenChange={() => {}}
      />,
    );
    expect(screen.getByRole("heading", { name: "Cancel run" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Cancel run" }));
    expect(onConfirm).toHaveBeenCalledOnce();
  });

  it("renders empty and error states", async () => {
    const user = userEvent.setup();
    const onRetry = vi.fn();
    renderWithTheme(
      <div>
        <EmptyState title="No runs" message="Nothing is queued." />
        <ErrorState
          title="Runs failed to load"
          message="Try the request again."
          onRetry={onRetry}
        />
      </div>,
    );
    expect(screen.getByRole("heading", { name: "No runs" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Try again" }));
    expect(onRetry).toHaveBeenCalledOnce();
  });
});
