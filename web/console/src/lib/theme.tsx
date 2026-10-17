import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from "react";

export type ThemeName = "light" | "dark";

const THEME_KEY = "factory.console.theme";

type ThemeContextValue = {
  theme: ThemeName;
  toggle: () => void;
};

const ThemeContext = createContext<ThemeContextValue | null>(null);

function initialTheme(): ThemeName {
  if (document.documentElement.dataset.theme === "dark") return "dark";
  try {
    const stored = localStorage.getItem(THEME_KEY);
    if (stored === "dark" || stored === "light") return stored;
  } catch {
    // Preference storage is optional.
  }
  return "light";
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setTheme] = useState<ThemeName>(initialTheme);

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    try {
      localStorage.setItem(THEME_KEY, theme);
    } catch {
      // Preference storage is optional.
    }
  }, [theme]);

  const toggle = useCallback(() => {
    setTheme((current) => (current === "dark" ? "light" : "dark"));
  }, []);

  return <ThemeContext.Provider value={{ theme, toggle }}>{children}</ThemeContext.Provider>;
}

export function useTheme(): ThemeContextValue {
  const value = useContext(ThemeContext);
  if (!value) {
    throw new Error("useTheme must be used within ThemeProvider");
  }
  return value;
}
