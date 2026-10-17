import { useVirtualizer, type Virtualizer } from "@tanstack/react-virtual";
import {
  ArrowDown,
  BookOpen,
  Bot,
  Camera,
  Circle,
  CircleHelp,
  Coins,
  FileDiff,
  Flag,
  Hand,
  MessageSquare,
  Radio,
  Shield,
  User,
  Wrench,
  type LucideIcon,
} from "lucide-react";
import { useLayoutEffect, useRef, useState } from "react";
import { formatCount, formatCost } from "./format";
import { formatDuration } from "../../lib/format";
import { ensureAnimationFrame } from "./frame";
import type { TranscriptKind, TranscriptRow } from "./types";
import type { StreamStatus } from "./use-run-events";

const ROW_ESTIMATE = 36;

function iconFor(kind: TranscriptKind, role: string | undefined): LucideIcon {
  switch (kind) {
    case "turn":
      return Flag;
    case "message":
      return role === "user" ? User : Bot;
    case "tool-call":
    case "tool-result":
      return Wrench;
    case "skill":
      return BookOpen;
    case "file":
      return FileDiff;
    case "usage":
      return Coins;
    case "question":
      return CircleHelp;
    case "terminal":
    case "run-terminal":
      return Radio;
    case "run-status":
      return Radio;
    case "capabilities":
      return Shield;
    case "interrupted":
      return Hand;
    case "followup":
      return MessageSquare;
    case "artifact":
      return Camera;
    case "unknown":
      return Circle;
    default: {
      const neverKind: never = kind;
      return neverKind;
    }
  }
}

function RowBody({ row }: { row: TranscriptRow }) {
  switch (row.kind) {
    case "turn":
      return (
        <span>
          Turn {row.turn ?? "—"}
          {row.modelId ? <span className="muted"> {row.modelId}</span> : null}
        </span>
      );
    case "message":
      return <span>{row.text}</span>;
    case "tool-call":
      return (
        <span className="tool-line">
          <span>{row.tool || "tool"}</span>
          {row.redactedInput ? <code className="mono">{row.redactedInput}</code> : null}
        </span>
      );
    case "tool-result":
      return (
        <span className="tool-line">
          <span>{row.tool || "tool"}</span>
          {row.ok === undefined ? null : <span>{row.ok ? "ok" : "error"}</span>}
          {row.durationMs === undefined ? null : <span>{formatDuration(row.durationMs)}</span>}
        </span>
      );
    case "skill":
      return <span className="mono">{row.path || row.text || "skill"}</span>;
    case "file":
      return (
        <span className="file-line">
          <span className="mono">{row.path}</span>
          <span className="add">+{row.additions ?? 0}</span>
          <span className="del">-{row.deletions ?? 0}</span>
        </span>
      );
    case "usage":
      return (
        <span className="mono">
          {row.modelId ? `${row.modelId} ` : ""}
          {row.inputTokens === undefined ? "" : `${formatCount(row.inputTokens)} in `}
          {row.outputTokens === undefined ? "" : `${formatCount(row.outputTokens)} out `}
          {row.costCents === undefined ? "" : formatCost(row.costCents)}
        </span>
      );
    case "question":
      return <span>{row.text}</span>;
    case "terminal":
    case "run-terminal":
      return (
        <span>
          {row.status ?? "ended"}
          {row.summary ? ` · ${row.summary}` : ""}
        </span>
      );
    case "run-status":
      return (
        <span>
          {row.status ?? "status"}
          {row.workerId ? <span className="muted"> · {row.workerId}</span> : null}
        </span>
      );
    case "capabilities":
      return <span>{row.steer ? "steer available" : "steer unavailable"}</span>;
    case "interrupted":
      return <span>{row.steerId ? row.steerId : "Turn interrupted"}</span>;
    case "followup":
      return <span className="mono">{row.steerId}</span>;
    case "artifact":
      return (
        <a href={`#artifact-${encodeURIComponent(row.artifactUid || row.seq)}`}>
          {row.artifactUid || "artifact"}
        </a>
      );
    case "unknown":
      return <span className="muted">Unrecognized event</span>;
    default: {
      const neverKind: never = row.kind;
      return neverKind;
    }
  }
}

function scrollTranscript(
  offset: number,
  _options: { behavior?: ScrollBehavior },
  instance: Virtualizer<HTMLDivElement, Element>,
) {
  const element = instance.scrollElement;
  if (!element) return;
  element.scrollTop = offset;
  ensureAnimationFrame();
  window.requestAnimationFrame(() => {
    element.dispatchEvent(new Event("scroll"));
  });
}

export function Transcript({
  rows,
  eventCount,
  status,
  latestSeq,
  resumedFrom,
}: {
  rows: readonly TranscriptRow[];
  eventCount: number;
  status: StreamStatus;
  latestSeq: string | null;
  resumedFrom: string | null;
}) {
  ensureAnimationFrame();
  const parentRef = useRef<HTMLDivElement>(null);
  const pinnedRef = useRef(true);
  const seenCount = useRef(0);
  const [pinned, setPinned] = useState(true);
  const [unseen, setUnseen] = useState(0);

  // useVirtualizer returns functions React Compiler cannot memoize.
  // eslint-disable-next-line react-hooks/incompatible-library -- TanStack Virtual documents this skip
  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => parentRef.current,
    estimateSize: () => ROW_ESTIMATE,
    overscan: 8,
    initialRect: { width: 640, height: 320 },
    getItemKey: (index) => rows[index]?.key ?? String(index),
    scrollToFn: scrollTranscript,
  });
  const virtualizerRef = useRef(virtualizer);
  virtualizerRef.current = virtualizer;

  useLayoutEffect(() => {
    const added = rows.length - seenCount.current;
    seenCount.current = rows.length;
    if (added <= 0 || rows.length === 0) return;
    if (pinnedRef.current) {
      virtualizerRef.current.scrollToIndex(rows.length - 1, { align: "end" });
    } else {
      setUnseen((count) => count + added);
    }
  }, [rows.length]);

  function onScroll() {
    const element = parentRef.current;
    if (!element) return;
    const gap = element.scrollHeight - element.scrollTop - element.clientHeight;
    const atBottom = gap <= 48;
    pinnedRef.current = atBottom;
    setPinned(atBottom);
    if (atBottom) setUnseen(0);
  }

  function jump() {
    pinnedRef.current = true;
    setPinned(true);
    setUnseen(0);
    if (rows.length > 0) virtualizerRef.current.scrollToIndex(rows.length - 1, { align: "end" });
  }

  const headline =
    status === "closed"
      ? "Stream closed"
      : status === "reconnecting"
        ? "Reconnecting…"
        : `Live · streaming from seq ${latestSeq ?? "—"}`;
  const rendered = virtualizer.getVirtualItems().length;

  return (
    <div className="transcript">
      <div className="transcript-strip">
        <span>{headline}</span>
        {resumedFrom ? <span>Resumed from seq {resumedFrom}</span> : null}
        <span className="transcript-count">
          {formatCount(eventCount)} events, rendering {rendered}
        </span>
      </div>
      <div className="transcript-frame">
        <div
          ref={parentRef}
          className="transcript-log"
          role="log"
          aria-label="Run transcript"
          tabIndex={0}
          onScroll={onScroll}
        >
          <div className="transcript-inner" style={{ height: virtualizer.getTotalSize() }}>
            {virtualizer.getVirtualItems().map((item) => {
              const row = rows[item.index];
              if (!row) return null;
              const Icon = iconFor(row.kind, row.role);
              return (
                <div
                  key={row.key}
                  className="transcript-row"
                  data-index={item.index}
                  style={{ transform: `translateY(${item.start}px)` }}
                >
                  <span className="seq-gutter">
                    {row.seqs.map((seq) => (
                      <span key={seq} data-seq={seq}>
                        {seq === row.seqs[0] ? row.seq : ""}
                      </span>
                    ))}
                  </span>
                  <span className="transcript-label">
                    <Icon aria-hidden="true" size={14} />
                    {row.label}
                  </span>
                  <span className="transcript-body">
                    <RowBody row={row} />
                  </span>
                </div>
              );
            })}
          </div>
        </div>
        {!pinned && unseen > 0 ? (
          <button type="button" className="jump-latest" onClick={jump}>
            <ArrowDown aria-hidden="true" size={16} /> Jump to latest ({unseen} new)
          </button>
        ) : null}
      </div>
    </div>
  );
}
