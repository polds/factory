// jsdom has no animation frames. Browsers do. Batching uses one frame either way.
export function ensureAnimationFrame(): void {
  if (typeof window === "undefined" || typeof window.requestAnimationFrame === "function") return;
  window.requestAnimationFrame = (callback: FrameRequestCallback): number =>
    window.setTimeout(() => callback(performance.now()), 0);
  window.cancelAnimationFrame = (handle: number): void => {
    window.clearTimeout(handle);
  };
}

export function scheduleFrame(callback: () => void): () => void {
  ensureAnimationFrame();
  const id = window.requestAnimationFrame(() => callback());
  return () => window.cancelAnimationFrame(id);
}
