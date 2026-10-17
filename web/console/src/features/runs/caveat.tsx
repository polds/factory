import { Info } from "lucide-react";
import type { ReactNode } from "react";

export function Caveat({ children }: { children: ReactNode }) {
  return (
    <p className="caveat">
      <Info aria-hidden="true" size={14} />
      <span>{children}</span>
    </p>
  );
}
