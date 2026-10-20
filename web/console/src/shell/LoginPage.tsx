import { useNavigate } from "@tanstack/react-router";
import { Moon, Sun } from "lucide-react";
import { useState, type FormEvent } from "react";
import { client, UnauthorizedError } from "../api/client";
import { setAPIKey } from "../api/session";
import { Button } from "../components/Button";
import { useTheme } from "../lib/theme";

export function LoginPage() {
  const navigate = useNavigate();
  const { theme, toggle } = useTheme();
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);
  const themeLabel = theme === "dark" ? "Switch to light theme" : "Switch to dark theme";
  const ThemeIcon = theme === "dark" ? Sun : Moon;

  async function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    const key = String(data.get("api-key") ?? "").trim();
    if (!key) {
      setError("Enter an API key.");
      return;
    }
    setPending(true);
    setError(null);
    try {
      setAPIKey(key);
      await client.listFactories();
      await navigate({ to: "/" });
    } catch (err) {
      setError(
        err instanceof UnauthorizedError
          ? "That API key was rejected."
          : "Could not reach the factory.",
      );
    } finally {
      setPending(false);
    }
  }

  return (
    <main className="login">
      <h1>Factory</h1>
      <p>Paste the factory API key. It stays in this tab and is cleared when the tab closes.</p>
      <form onSubmit={onSubmit}>
        <label htmlFor="api-key">API key</label>
        <input
          id="api-key"
          name="api-key"
          type="password"
          autoComplete="off"
          spellCheck={false}
          required
        />
        <Button type="submit" className="button-primary" disabled={pending}>
          {pending ? "Signing in" : "Sign in"}
        </Button>
        {error ? <p role="alert">{error}</p> : null}
      </form>
      <button type="button" onClick={toggle} aria-label={themeLabel}>
        <ThemeIcon aria-hidden="true" size={14} />
        <span>{theme === "dark" ? "Light theme" : "Dark theme"}</span>
      </button>
    </main>
  );
}
