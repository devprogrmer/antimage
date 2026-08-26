import { createContext, useCallback, useContext, useEffect, useState } from "react";
import type { ReactNode } from "react";

export type Theme = "light" | "dark" | "system";

const STORAGE_KEY = "antimage.theme";

interface ThemeContextValue {
  /** What the operator chose, which may be "system". */
  theme: Theme;
  /** What is actually on screen right now, never "system". */
  resolved: "light" | "dark";
  setTheme: (next: Theme) => void;
}

const ThemeContext = createContext<ThemeContextValue | null>(null);

function systemPrefersDark(): boolean {
  return window.matchMedia?.("(prefers-color-scheme: dark)").matches ?? false;
}

function storedTheme(): Theme {
  const raw = localStorage.getItem(STORAGE_KEY);
  return raw === "light" || raw === "dark" || raw === "system" ? raw : "system";
}

/** apply writes the class the `dark:` variant keys off. */
function apply(resolved: "light" | "dark") {
  document.documentElement.classList.toggle("dark", resolved === "dark");
  // Tells the browser which scrollbars and form controls to render, which is
  // the part a class alone does not cover.
  document.documentElement.style.colorScheme = resolved;
}

/**
 * ThemeProvider resolves the operator's choice and keeps it applied.
 *
 * "system" is resolved HERE rather than left to a CSS media query, because the
 * two cannot coexist: if `dark:` followed prefers-color-scheme there would be
 * no way to express "this operator chose light while their OS is dark". One
 * place decides, and the class on <html> is the answer.
 */
export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setThemeState] = useState<Theme>(() => storedTheme());
  const [resolved, setResolved] = useState<"light" | "dark">(() =>
    storedTheme() === "system"
      ? systemPrefersDark()
        ? "dark"
        : "light"
      : (storedTheme() as "light" | "dark"),
  );

  useEffect(() => {
    const next = theme === "system" ? (systemPrefersDark() ? "dark" : "light") : theme;
    setResolved(next);
    apply(next);
  }, [theme]);

  // Follow the OS while, and only while, the operator has chosen to.
  useEffect(() => {
    if (theme !== "system") return;
    const media = window.matchMedia("(prefers-color-scheme: dark)");
    const onChange = () => {
      const next = media.matches ? "dark" : "light";
      setResolved(next);
      apply(next);
    };
    media.addEventListener("change", onChange);
    return () => media.removeEventListener("change", onChange);
  }, [theme]);

  const setTheme = useCallback((next: Theme) => {
    localStorage.setItem(STORAGE_KEY, next);
    setThemeState(next);
  }, []);

  return (
    <ThemeContext.Provider value={{ theme, resolved, setTheme }}>
      {children}
    </ThemeContext.Provider>
  );
}

export function useTheme(): ThemeContextValue {
  const ctx = useContext(ThemeContext);
  if (!ctx) {
    // A component reading the theme outside the provider would silently render
    // in whatever the defaults happen to be, which is the kind of bug that only
    // shows up as "the dialog is the wrong colour on one screen".
    throw new Error("useTheme must be used within a ThemeProvider");
  }
  return ctx;
}
