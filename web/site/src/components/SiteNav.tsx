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
  const docsLinks = [
    ["01", "Evidence and provenance", "01-evidence-and-provenance"],
    ["02", "Architecture", "02-architecture"],
    ["03", "Definition schema", "03-definition-schema"],
    ["05", "Harness Contract v1", "05-harness-contract"],
    ["10", "Kubernetes and Helm", "10-kubernetes-and-helm"],
    ["12", "Build sequence", "12-build-sequence"],
  ];
  return (
    <footer className="border-t border-border">
      <div className="mx-auto grid max-w-6xl gap-10 px-4 py-12 sm:px-6 md:grid-cols-[1.4fr_1fr_1fr]">
        <div>
          <p className="font-mono text-sm font-medium">factory</p>
          <p className="mt-3 max-w-sm text-sm text-muted-foreground">
            A self-hostable software factory orchestrator. The control plane, operator, CLI and
            console are this repository&rsquo;s. The worker is oz-agent-worker, MIT, Copyright 2026
            Denver Technologies, Inc., used unmodified.
          </p>
        </div>
        <div>
          <p className="font-mono text-xs text-muted-foreground">design docs</p>
          <ul className="mt-3 space-y-1.5 text-sm">
            {docsLinks.map(([n, label, slug]) => (
              <li key={slug}>
                <Link to={`/docs/${slug}`} className="hover:text-foreground">
                  <span className="font-mono text-muted-foreground">{n}</span> {label}
                </Link>
              </li>
            ))}
            <li>
              <Link to="/docs" className="text-muted-foreground hover:text-foreground">
                all twelve
              </Link>
            </li>
          </ul>
        </div>
        <div>
          <p className="font-mono text-xs text-muted-foreground">project</p>
          <ul className="mt-3 space-y-1.5 text-sm">
            <li>
              <a href={GITHUB} className="hover:text-foreground">
                github.com/polds/factory
              </a>
            </li>
            <li>
              <a
                href={`${GITHUB}/tree/main/examples/acme-factory`}
                className="hover:text-foreground"
              >
                example factory
              </a>
            </li>
            <li>
              <a href={`${GITHUB}/issues`} className="hover:text-foreground">
                issues
              </a>
            </li>
            <li>
              <a href={`${GITHUB}/blob/main/deploy/helm/factory`} className="hover:text-foreground">
                helm chart
              </a>
            </li>
          </ul>
        </div>
      </div>
    </footer>
  );
}
