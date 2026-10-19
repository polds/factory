type Install = () => void;

let install: Install | null = null;

if (import.meta.env.MODE === "test") {
  const mod = await import("./install-streams");
  install = mod.installRunsStreamFallback;
}

export function ensureRunsStreamFallback(): void {
  install?.();
}
