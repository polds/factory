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

export function formatCost(cents: number | undefined): string {
  if (cents === undefined) return "—";
  return `$${(cents / 100).toFixed(2)}`;
}

export function formatCount(value: number): string {
  return new Intl.NumberFormat("en-US").format(value);
}

// Returns a parsed https href, or null for empty, relative, and non-https values.
export function httpsHref(value: string): string | null {
  const text = value.trim();
  if (text === "") return null;
  try {
    const url = new URL(text);
    if (url.protocol !== "https:") return null;
    return url.href;
  } catch {
    return null;
  }
}

export function formatScore(score: number): string {
  if (!Number.isFinite(score)) return "—";
  return String(score);
}
