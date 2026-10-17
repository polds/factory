import { durationDateTime, formatDuration } from "../lib/format";

export function DurationPair({ queueWaitMs, execMs }: { queueWaitMs: number; execMs: number }) {
  return (
    <p className="duration-pair">
      <span>
        Queue <time dateTime={durationDateTime(queueWaitMs)}>{formatDuration(queueWaitMs)}</time>
      </span>
      <span aria-hidden="true"> | </span>
      <span>
        Execution <time dateTime={durationDateTime(execMs)}>{formatDuration(execMs)}</time>
      </span>
    </p>
  );
}
