import axe from "axe-core";
import { expect } from "vitest";

export async function expectNoSeriousViolations(node: Element = document.body): Promise<void> {
  const results = await axe.run(node);
  const blocking = results.violations.filter(
    (violation) => violation.impact === "serious" || violation.impact === "critical",
  );
  expect(blocking, JSON.stringify(blocking, null, 2)).toEqual([]);
}
