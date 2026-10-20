import { Ban, Bot, ServerCrash, Settings2, type LucideIcon } from "lucide-react";
import type { CSSProperties } from "react";
import type { FailureClass } from "../api/types";

const labels = {
  agent: "Agent",
  infrastructure: "Infrastructure",
  configuration: "Configuration",
  cancelled: "Cancelled",
} as const satisfies Record<FailureClass, string>;

const icons = {
  agent: Bot,
  infrastructure: ServerCrash,
  configuration: Settings2,
  cancelled: Ban,
} as const satisfies Record<FailureClass, LucideIcon>;

// Colours live in index.css as --class-<name>-fg/bg so the badge, the
// failure panel, and the dashboard cells read one source and the dark theme
// is one override. The inline style only points at the pair for this class.
export function failureClassStyle(failureClass: FailureClass): CSSProperties {
  return {
    color: `var(--class-${failureClass}-fg)`,
    backgroundColor: `var(--class-${failureClass}-bg)`,
    borderColor: `var(--class-${failureClass}-fg)`,
  };
}

export function StatusBadge({ failureClass }: { failureClass: FailureClass }) {
  const Icon = icons[failureClass];
  return (
    <span
      className="status-badge"
      data-failure-class={failureClass}
      style={failureClassStyle(failureClass)}
    >
      <Icon aria-hidden="true" size={14} />
      <span>{labels[failureClass]}</span>
    </span>
  );
}
