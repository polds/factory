import {
  Ban,
  CheckCircle2,
  Circle,
  Clock,
  Loader,
  PauseCircle,
  UserRound,
  XCircle,
  type LucideIcon,
} from "lucide-react";

const known = {
  queued: { icon: Clock, label: "Queued" },
  assigned: { icon: UserRound, label: "Assigned" },
  running: { icon: Loader, label: "Running" },
  idle: { icon: PauseCircle, label: "Idle" },
  succeeded: { icon: CheckCircle2, label: "Succeeded" },
  failed: { icon: XCircle, label: "Failed" },
  cancelled: { icon: Ban, label: "Cancelled" },
} as const satisfies Record<string, { icon: LucideIcon; label: string }>;

type KnownStatus = keyof typeof known;

function isKnownStatus(status: string): status is KnownStatus {
  return Object.prototype.hasOwnProperty.call(known, status);
}

export function RunStatusBadge({ status }: { status: string }) {
  const iconName = isKnownStatus(status) ? status : "other";
  const Icon = isKnownStatus(status) ? known[status].icon : Circle;
  const label = isKnownStatus(status) ? known[status].label : status;
  return (
    <span className={`stat-label run-status run-status-${iconName}`} data-status={status}>
      <Icon aria-hidden="true" size={16} data-testid={`run-status-icon-${iconName}`} />
      <span>{label}</span>
    </span>
  );
}
