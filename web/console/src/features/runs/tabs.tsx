import { useState, type ReactNode } from "react";

export type RunTab = "transcript" | "timeline" | "usage" | "files" | "tree";

const tabs = [
  { id: "transcript", label: "Transcript" },
  { id: "timeline", label: "Timeline" },
  { id: "usage", label: "Usage" },
  { id: "files", label: "Files & PR" },
  { id: "tree", label: "Tree" },
] as const satisfies readonly { id: RunTab; label: string }[];

function tabOffset(key: string, index: number): number | null {
  if (key === "ArrowRight") return (index + 1) % tabs.length;
  if (key === "ArrowLeft") return (index - 1 + tabs.length) % tabs.length;
  if (key === "Home") return 0;
  if (key === "End") return tabs.length - 1;
  return null;
}

export function RunTabs({ panels }: { panels: Record<RunTab, ReactNode> }) {
  const [tab, setTab] = useState<RunTab>("transcript");

  function move(next: RunTab) {
    setTab(next);
    document.getElementById(`run-tab-${next}`)?.focus();
  }

  function onKeyDown(event: React.KeyboardEvent<HTMLDivElement>) {
    const index = tabs.findIndex((item) => item.id === tab);
    if (index < 0) return;
    const next = tabOffset(event.key, index);
    if (next === null) return;
    event.preventDefault();
    const item = tabs[next];
    if (item) move(item.id);
  }

  return (
    <div className="run-tabs">
      <div role="tablist" aria-label="Run sections" className="tablist" onKeyDown={onKeyDown}>
        {tabs.map((item) => {
          const selected = item.id === tab;
          return (
            <button
              key={item.id}
              id={`run-tab-${item.id}`}
              type="button"
              role="tab"
              aria-selected={selected}
              aria-controls={`run-panel-${item.id}`}
              tabIndex={selected ? 0 : -1}
              onClick={() => setTab(item.id)}
            >
              {item.label}
            </button>
          );
        })}
      </div>
      <div
        role="tabpanel"
        id={`run-panel-${tab}`}
        aria-labelledby={`run-tab-${tab}`}
        className="tabpanel"
      >
        {panels[tab]}
      </div>
    </div>
  );
}
