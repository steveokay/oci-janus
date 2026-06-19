import { useEffect, useState } from "react";

// Beacon — theme. Persisted to localStorage so the user's choice survives
// reloads. `system` follows the OS pref via `matchMedia`. We touch the DOM
// class on <html> directly rather than syncing through React state because
// downstream CSS reacts purely to `.dark`.

export type Theme = "light" | "dark" | "system";

const STORAGE_KEY = "beacon:theme";

function applyTheme(theme: Theme): void {
  const root = document.documentElement;
  const resolved =
    theme === "system"
      ? window.matchMedia("(prefers-color-scheme: dark)").matches
        ? "dark"
        : "light"
      : theme;
  root.classList.toggle("dark", resolved === "dark");
}

export function initTheme(): void {
  const stored = (localStorage.getItem(STORAGE_KEY) as Theme | null) ?? "system";
  applyTheme(stored);
  // Keep system-mode users in sync if they flip OS theme mid-session.
  if (stored === "system") {
    window
      .matchMedia("(prefers-color-scheme: dark)")
      .addEventListener("change", () => applyTheme("system"));
  }
}

export function useTheme(): {
  theme: Theme;
  setTheme: (t: Theme) => void;
} {
  const [theme, setTheme] = useState<Theme>(
    () => (localStorage.getItem(STORAGE_KEY) as Theme | null) ?? "system",
  );
  useEffect(() => {
    localStorage.setItem(STORAGE_KEY, theme);
    applyTheme(theme);
  }, [theme]);
  return { theme, setTheme };
}
