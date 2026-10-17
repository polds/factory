import { Ban, Bot, ServerCrash, Settings2, type LucideIcon } from "lucide-react";
import type { CSSProperties } from "react";
import { useTheme, type ThemeName } from "../lib/theme";
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

const palette: Record<ThemeName, Record<FailureClass, CSSProperties>> = {
  light: {
    agent: { color: "#9f1239", backgroundColor: "#ffe4e6" },
    infrastructure: { color: "#1e3a8a", backgroundColor: "#dbeafe" },
    configuration: { color: "#78350f", backgroundColor: "#fef3c7" },
    cancelled: { color: "#292524", backgroundColor: "#e7e5e4" },
  },
  dark: {
    agent: { color: "#fecdd3", backgroundColor: "#4c0519" },
    infrastructure: { color: "#dbeafe", backgroundColor: "#172554" },
    configuration: { color: "#fde68a", backgroundColor: "#451a03" },
    cancelled: { color: "#f5f5f4", backgroundColor: "#292524" },
  },
};

export function StatusBadge({ failureClass }: { failureClass: FailureClass }) {
  const { theme } = useTheme();
  const Icon = icons[failureClass];
  const colors = palette[theme][failureClass];
  return (
    <span
      className="status-badge"
      data-failure-class={failureClass}
      style={{ ...colors, borderColor: colors.color }}
    >
      <Icon aria-hidden="true" size={16} />
      <span>{labels[failureClass]}</span>
    </span>
  );
}
