import { useState } from "react";

export function CopyButton({ text }: { text: string }) {
  const [state, setState] = useState<"idle" | "copied" | "failed">("idle");
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
      setState("copied");
    } catch {
      setState("failed");
    }
    window.setTimeout(() => setState("idle"), 1500);
  };
  return (
    <button
      type="button"
      onClick={() => void copy()}
      className="font-mono text-xs text-muted-foreground hover:text-foreground"
      aria-live="polite"
    >
      {state === "copied" ? "copied" : state === "failed" ? "select and copy" : "copy"}
    </button>
  );
}
