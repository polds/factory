import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { DayPassRate } from "../api/types";
import { formatPercent } from "../lib/format";
import { renderWithTheme } from "../test/render";
import { PassRateByDay } from "./PassRateByDay";

const days: DayPassRate[] = [
  { day: "2026-04-01", pass_rate: 0.5, n: 2 },
  { day: "2026-04-02", pass_rate: 1, n: 1000 },
];

describe("formatPercent", () => {
  it("formats fractions as percents", () => {
    expect(formatPercent(0)).toBe("0%");
    expect(formatPercent(0.5)).toBe("50%");
    expect(formatPercent(1)).toBe("100%");
    expect(formatPercent(0.333)).toBe("33.3%");
    expect(formatPercent(Number.NaN)).toBe("—");
  });
});

describe("PassRateByDay", () => {
  it("renders the server rate in a table and a decorative sparkline", () => {
    const { container } = renderWithTheme(<PassRateByDay days={days} />);
    expect(screen.getByText("2026-04-01")).toBeInTheDocument();
    expect(screen.getByText("50%")).toBeInTheDocument();
    expect(screen.getByText("100%")).toBeInTheDocument();
    expect(screen.getByText("1,000")).toBeInTheDocument();
    expect(container.querySelector("svg")).toHaveAttribute("aria-hidden", "true");
    expect(container.querySelector("svg")).not.toBeNull();
  });

  it("says when there is no pass rate", () => {
    renderWithTheme(<PassRateByDay days={[]} />);
    expect(screen.getByText("No pass rate yet")).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });
});
