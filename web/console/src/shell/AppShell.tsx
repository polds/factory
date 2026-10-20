import { Link } from "@tanstack/react-router";
import { Moon, Sun } from "lucide-react";
import type { ReactNode } from "react";
import { useTheme } from "../lib/theme";
import type { Factory } from "../api/types";

const sections = [
  { label: "Dashboard", to: "/", needsFactory: false },
  { label: "Work items", to: "/f/$factory/work-items", needsFactory: true },
  { label: "Runs", to: "/runs", needsFactory: false },
  { label: "Inbox", to: "/f/$factory/inbox", needsFactory: true },
  { label: "Definition", to: "/f/$factory/definition", needsFactory: true },
  { label: "Automations", to: "/f/$factory/automations", needsFactory: true },
  { label: "Webhooks", to: "/f/$factory/webhooks", needsFactory: true },
  { label: "Deliveries", to: "/f/$factory/deliveries", needsFactory: true },
  { label: "Workers", to: "/workers", needsFactory: false },
  { label: "Secrets", to: "/f/$factory/secrets", needsFactory: true },
] as const;

export function AppShell({
  factories,
  factoryId,
  onFactoryChange,
  factoriesState,
  children,
}: {
  factories: readonly Factory[];
  factoryId: string | null;
  onFactoryChange: (id: string) => void;
  factoriesState: "loading" | "ready" | "error";
  children: ReactNode;
}) {
  const { theme, toggle } = useTheme();
  const themeLabel = theme === "dark" ? "Switch to light theme" : "Switch to dark theme";
  const ThemeIcon = theme === "dark" ? Sun : Moon;
  return (
    <div className="shell">
      <a className="skip-link" href="#content">
        Skip to content
      </a>
      <aside className="sidebar">
        <h1 className="wordmark">
          Factory <small>console</small>
        </h1>
        <nav aria-label="Factories">
          <h2 className="rail-label">Factories</h2>
          {factoriesState === "loading" ? <p>Loading factories</p> : null}
          {factoriesState === "error" ? <p>Factory list unavailable</p> : null}
          {factoriesState === "ready" && factories.length === 0 ? <p>No factories</p> : null}
          <ul>
            {factories.map((factory) => {
              const selected = factory.id === factoryId;
              return (
                <li key={factory.id}>
                  <button
                    type="button"
                    aria-current={selected ? "true" : undefined}
                    onClick={() => onFactoryChange(factory.id)}
                  >
                    <span>{factory.name}</span>
                    {factory.alias ? <span className="factory-alias">{factory.alias}</span> : null}
                  </button>
                </li>
              );
            })}
          </ul>
        </nav>
        <nav aria-label="Sections">
          <h2 className="rail-label">Sections</h2>
          <ul>
            {sections.map((section) => (
              <li key={section.label}>
                {section.needsFactory && !factoryId ? (
                  <span aria-disabled="true" title="Choose a factory first">
                    {section.label}
                  </span>
                ) : (
                  <Link
                    to={section.to}
                    params={section.needsFactory && factoryId ? { factory: factoryId } : undefined}
                    activeOptions={{ exact: section.to === "/" }}
                    activeProps={{ "aria-current": "page" as const }}
                  >
                    {section.label}
                  </Link>
                )}
              </li>
            ))}
          </ul>
        </nav>
        <div className="rail-foot">
          <button type="button" className="button" onClick={toggle} aria-label={themeLabel}>
            <ThemeIcon aria-hidden="true" size={14} />
            <span>{theme === "dark" ? "Light" : "Dark"}</span>
          </button>
        </div>
      </aside>
      <div className="workspace">
        <main id="content">{children}</main>
      </div>
    </div>
  );
}
