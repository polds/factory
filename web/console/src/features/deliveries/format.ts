export function formatWhen(iso: string | null | undefined): string {
  if (!iso) return "—";
  const then = Date.parse(iso);
  if (Number.isNaN(then)) return iso;
  const seconds = Math.round((Date.now() - then) / 1000);
  const abs = Math.abs(seconds);
  if (abs < 60) return "just now";
  const minutes = Math.round(seconds / 60);
  if (Math.abs(minutes) < 60) return `${minutes}m`;
  const hours = Math.round(minutes / 60);
  if (Math.abs(hours) < 48) return `${hours}h`;
  const days = Math.round(hours / 24);
  return `${days}d`;
}
