import { MessageCircleQuestion, ShieldQuestion } from "lucide-react";

export function GateBadge({ status }: { status: "awaiting_approval" | "awaiting_input" }) {
  const approval = status === "awaiting_approval";
  const Icon = approval ? ShieldQuestion : MessageCircleQuestion;
  return (
    <span
      className="gate-badge"
      data-waiting={status}
      data-icon={approval ? "shield-question" : "message-circle-question"}
      style={{ borderStyle: approval ? "solid" : "dashed" }}
    >
      <Icon aria-hidden="true" size={16} />
      <span>{approval ? "Awaiting approval" : "Awaiting input"}</span>
    </span>
  );
}
