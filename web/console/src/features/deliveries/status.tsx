import {
  AlertCircle,
  CheckCircle2,
  CircleDashed,
  CircleSlash,
  XCircle,
  type LucideIcon,
} from "lucide-react";

function presentation(status: string): { label: string; icon: LucideIcon } {
  switch (status) {
    case "received":
      return { label: "Received", icon: CircleDashed };
    case "matched":
      return { label: "Matched", icon: CheckCircle2 };
    case "unmatched":
      return { label: "Unmatched", icon: CircleDashed };
    case "ignored":
      return { label: "Ignored", icon: CircleSlash };
    case "failed":
      return { label: "Failed", icon: XCircle };
    default:
      return { label: status || "Unknown", icon: AlertCircle };
  }
}

export function DeliveryStatus({ status }: { status: string }) {
  const { label, icon: Icon } = presentation(status);
  return (
    <span className="inline-flex items-center gap-1 whitespace-nowrap">
      <Icon className="size-4 shrink-0" aria-hidden />
      {label}
    </span>
  );
}
