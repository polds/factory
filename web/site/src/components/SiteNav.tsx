import { Moon, Sun } from "lucide-react";
import type { ReactNode } from "react";
import { useState } from "react";
import { Link, NavLink } from "react-router";

export const GITHUB = "https://github.com/polds/factory";

export function GithubMark({ size = 14 }: { size?: number }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 16 16"
      fill="currentColor"
      aria-hidden
      focusable="false"
    >
      <path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0 0 16 8c0-4.42-3.58-8-8-8z" />
    </svg>
  );
}

function Logo() {
  return (
    <Link to="/" className="flex items-center gap-2 font-mono text-sm font-semibold tracking-tight">
      <span className="inline-block h-3 w-3 bg-primary" aria-hidden />
      factory
    </Link>
  );
}

function ThemeToggle() {
  const [dark, setDark] = useState(() => document.documentElement.classList.contains("dark"));
  const toggle = () => {
    const next = !dark;
    setDark(next);
    document.documentElement.classList.toggle("dark", next);
    try {
      localStorage.setItem("factory.site.theme", next ? "dark" : "light");
    } catch {
      /* theme preference is optional */
    }
  };
  return (
    <button
      type="button"
      onClick={toggle}
      aria-label={dark ? "Switch to light theme" : "Switch to dark theme"}
      className="flex h-8 w-8 items-center justify-center border border-border text-muted-foreground hover:text-foreground"
    >
      {dark ? <Sun size={14} /> : <Moon size={14} />}
    </button>
  );
}

export function SiteNav({ left }: { left?: ReactNode }) {
  return (
    <header className="sticky top-0 z-40 border-b border-border bg-background/85 backdrop-blur">
      <div className="mx-auto flex h-14 max-w-7xl items-center gap-3 px-4 sm:px-6">
        {left}
        <Logo />
        <nav className="ml-auto flex items-center gap-1 font-mono text-xs sm:gap-4">
          <NavLink
            to="/docs"
            className={({ isActive }) =>
              `px-2 py-1 hover:text-foreground ${isActive ? "text-primary" : "text-muted-foreground"}`
            }
          >
            Docs
          </NavLink>
          <a
            href={GITHUB}
            className="flex items-center gap-1.5 px-2 py-1 text-muted-foreground hover:text-foreground"
          >
            <GithubMark size={14} /> <span className="hidden sm:inline">GitHub</span>
          </a>
          <ThemeToggle />
        </nav>
      </div>
    </header>
  );
}

export function Footer() {
  return (
    <footer className="border-t border-border">
      <div className="mx-auto flex max-w-7xl flex-col gap-4 px-4 py-8 font-mono text-xs text-muted-foreground sm:flex-row sm:items-center sm:px-6">
        <span>factory — self-hostable software factory</span>
        <span className="sm:ml-auto">Data plane: unmodified MIT-licensed oz-agent-worker.</span>
        <div className="flex gap-4">
          <Link to="/docs" className="hover:text-foreground">
            docs
          </Link>
          <a href={GITHUB} className="hover:text-foreground">
            github
          </a>
        </div>
      </div>
    </footer>
  );
}
