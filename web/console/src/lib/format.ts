export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return "unknown";
  const totalSeconds = Math.round(ms / 1000);
  if (totalSeconds < 1) return `${Math.round(ms)}ms`;
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  if (hours > 0) return minutes > 0 ? `${hours}h ${minutes}m` : `${hours}h`;
  if (minutes > 0) return seconds > 0 ? `${minutes}m ${seconds}s` : `${minutes}m`;
  return `${seconds}s`;
}

export function durationDateTime(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return "PT0S";
  return `PT${Math.round(ms / 1000)}S`;
}

// rate is a fraction (0.5 → "50%"): pass_rate, CI bounds, and T65's autonomy share.
export function formatPercent(rate: number): string {
  if (!Number.isFinite(rate)) return "—";
  const percent = Math.round(rate * 1000) / 10;
  const text = Number.isInteger(percent) ? String(percent) : percent.toFixed(1);
  return `${text}%`;
}
