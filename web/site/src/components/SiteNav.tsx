import type { ReactNode } from "react";
import { useState } from "react";
import { Link, NavLink } from "react-router";

export const GITHUB = "https://github.com/polds/factory";

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
      className="text-muted-foreground hover:text-foreground"
      aria-label={dark ? "Switch to light theme" : "Switch to dark theme"}
    >
      {dark ? "light" : "dark"}
    </button>
  );
}

export function SiteNav({ left }: { left?: ReactNode }) {
  return (
    <header className="sticky top-0 z-40 border-b border-border bg-background">
      <div className="mx-auto flex h-12 max-w-6xl items-center gap-4 px-4 font-mono text-sm sm:px-6">
        {left}
        <Link to="/" className="font-medium">
          factory
        </Link>
        <nav className="ml-auto flex items-center gap-4">
          <NavLink
            to="/docs"
            className={({ isActive }) =>
              isActive ? "text-foreground" : "text-muted-foreground hover:text-foreground"
            }
          >
            docs
          </NavLink>
          <a href={GITHUB} className="text-muted-foreground hover:text-foreground">
            github
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
      <div className="mx-auto flex max-w-6xl flex-col gap-2 px-4 py-6 font-mono text-xs text-muted-foreground sm:flex-row sm:gap-6 sm:px-6">
        <span>
          Worker: oz-agent-worker, MIT, Copyright 2026 Denver Technologies, Inc. Everything else in
          this repository is ours.
        </span>
        <span className="sm:ml-auto">
          <a href={GITHUB} className="hover:text-foreground">
            github.com/polds/factory
          </a>
        </span>
      </div>
    </footer>
  );
}
