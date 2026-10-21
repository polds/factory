import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { FailureClassFilter } from "../components/FailureClassFilter";
import { StatusBadge } from "../components/StatusBadge";
import type { FailureClass } from "../api/types";
import { setAPIKey } from "../api/session";
import { demoAPIKey } from "../mocks/demo-key";
import { expectNoSeriousViolations } from "./axe";
import { renderApp, renderWithTheme } from "./render";

const classes = [
  "agent",
  "infrastructure",
  "configuration",
  "cancelled",
] as const satisfies readonly FailureClass[];

describe("accessibility", () => {
  it("gives each failure class an icon, a colour, and a label", async () => {
    const { container } = renderWithTheme(
      <div>
        {classes.map((failureClass) => (
          <StatusBadge key={failureClass} failureClass={failureClass} />
        ))}
        <FailureClassFilter value="agent" onChange={() => {}} />
      </div>,
    );
    for (const label of ["Agent", "Infrastructure", "Configuration", "Cancelled"]) {
      expect(screen.getAllByText(label).length).toBeGreaterThan(0);
    }
    const colors = classes.map((failureClass) => {
      const badge = container.querySelector(`[data-failure-class="${failureClass}"]`);
      expect(badge?.querySelector("svg")).not.toBeNull();
      return badge?.getAttribute("style");
    });
    expect(new Set(colors).size).toBe(classes.length);
    await expectNoSeriousViolations(container);
  });

  it("keeps the shell clean in light and dark themes", async () => {
    const user = userEvent.setup();
    setAPIKey(demoAPIKey);
    const { container } = await renderApp("/");
    expect(await screen.findByRole("navigation", { name: "Sections" })).toBeInTheDocument();
    await expectNoSeriousViolations(container);
    await user.click(screen.getByRole("button", { name: "Switch to dark theme" }));
    expect(document.documentElement.dataset.theme).toBe("dark");
    await expectNoSeriousViolations(container);
  });

  it("renders badge colours for the dark theme", async () => {
    document.documentElement.dataset.theme = "dark";
    const { container } = renderWithTheme(
      <div>
        {classes.map((failureClass) => (
          <StatusBadge key={failureClass} failureClass={failureClass} />
        ))}
      </div>,
    );
    await expectNoSeriousViolations(container);
  });
});
