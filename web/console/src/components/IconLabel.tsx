import type { LucideIcon } from "lucide-react";

export function IconLabel({ icon: Icon, label }: { icon: LucideIcon; label: string }) {
  return (
    <span className="inline-flex items-center gap-1 whitespace-nowrap">
      <Icon aria-hidden="true" size={16} />
      <span>{label}</span>
    </span>
  );
}
