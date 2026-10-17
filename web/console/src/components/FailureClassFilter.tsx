import type { FailureClass } from "../api/types";
import { StatusBadge } from "./StatusBadge";

const classes = [
  "agent",
  "infrastructure",
  "configuration",
  "cancelled",
] as const satisfies readonly FailureClass[];

export function FailureClassFilter({
  value,
  onChange,
}: {
  value: FailureClass | "";
  onChange: (next: FailureClass | "") => void;
}) {
  return (
    <div className="filter" role="group" aria-label="Failure class">
      <button type="button" aria-pressed={value === ""} onClick={() => onChange("")}>
        All
      </button>
      {classes.map((failureClass) => (
        <button
          key={failureClass}
          type="button"
          aria-pressed={value === failureClass}
          onClick={() => onChange(value === failureClass ? "" : failureClass)}
        >
          <StatusBadge failureClass={failureClass} />
        </button>
      ))}
    </div>
  );
}
