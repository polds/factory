import type { DayPassRate } from "../api/types";
import { formatPercent } from "../lib/format";

function formatDayCount(value: number): string {
  if (!Number.isFinite(value)) return "—";
  return new Intl.NumberFormat("en-US").format(value);
}

function sparkPoints(days: readonly DayPassRate[], width: number, height: number): string[] {
  const pad = 2;
  const innerW = width - pad * 2;
  const innerH = height - pad * 2;
  return days.map((day, index) => {
    const x = days.length === 1 ? width / 2 : pad + (index / (days.length - 1)) * innerW;
    const rate = Number.isFinite(day.pass_rate) ? Math.min(1, Math.max(0, day.pass_rate)) : 0;
    const y = pad + (1 - rate) * innerH;
    return `${x},${y}`;
  });
}

function Sparkline({ days }: { days: readonly DayPassRate[] }) {
  const width = 160;
  const height = 36;
  const points = sparkPoints(days, width, height);
  return (
    <svg
      aria-hidden="true"
      width={width}
      height={height}
      viewBox={`0 0 ${width} ${height}`}
      className="text-foreground"
    >
      {points.length > 1 ? (
        <polyline fill="none" stroke="currentColor" strokeWidth="1.5" points={points.join(" ")} />
      ) : null}
      {points.map((point, index) => {
        const [x, y] = point.split(",");
        return <circle key={index} cx={x} cy={y} r="2" fill="currentColor" />;
      })}
    </svg>
  );
}

export function PassRateByDay({ days }: { days: readonly DayPassRate[] }) {
  if (days.length === 0) {
    return <p className="muted">No pass rate yet</p>;
  }
  return (
    <div className="min-w-0">
      <Sparkline days={days} />
      <div className="table-scroll" tabIndex={0}>
        <table>
          <caption className="sr-only">Pass rate by day</caption>
          <thead className="data-head">
            <tr>
              <th scope="col">Day</th>
              <th scope="col">Pass rate</th>
              <th scope="col">n</th>
            </tr>
          </thead>
          <tbody>
            {days.map((day, index) => (
              <tr key={`${day.day}-${index}`}>
                <td>{day.day}</td>
                <td className="mono">{formatPercent(day.pass_rate)}</td>
                <td className="mono">{formatDayCount(day.n)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
