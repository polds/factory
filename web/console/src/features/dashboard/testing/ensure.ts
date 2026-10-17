type Install = () => void;

let install: Install | null = null;

if (import.meta.env.MODE === "test") {
  const mod = await import("./install-streams");
  install = mod.installStreamFallback;
}

export function ensureStreamFallback(): void {
  install?.();
}
