export const SESSION_KEY = "factory.console.apiKey";

const SESSION_EVENT = "factory:session";

export function getAPIKey(): string | null {
  try {
    return sessionStorage.getItem(SESSION_KEY);
  } catch {
    return null;
  }
}

export function getAPIKeyServer(): null {
  return null;
}

export function setAPIKey(key: string): void {
  sessionStorage.setItem(SESSION_KEY, key);
  emit();
}

export function clearAPIKey(): void {
  sessionStorage.removeItem(SESSION_KEY);
  emit();
}

export function subscribeSession(onChange: () => void): () => void {
  window.addEventListener(SESSION_EVENT, onChange);
  return () => window.removeEventListener(SESSION_EVENT, onChange);
}

function emit(): void {
  window.dispatchEvent(new Event(SESSION_EVENT));
}
