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
  // NOTE: no matchMedia listener here. The old one-shot subscription was
  // bound to the value stored at page load and never removed, so switching
  // to Light/Dark mid-session still got overridden by an OS theme flip, and
  // switching TO System mid-session didn't track until reload. The OS-pref
  // subscription now lives in useTheme's effect, keyed to the CURRENT theme.
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
    // Follow OS theme flips only while the CURRENT selection is "system".
    // Subscribing inside the effect (and cleaning up on theme change /
    // unmount) means picking Light/Dark mid-session detaches the listener,
    // and picking System mid-session attaches it — both without a reload.
    if (theme !== "system") return;
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    const onOSThemeChange = (): void => applyTheme("system");
    mq.addEventListener("change", onOSThemeChange);
    return () => mq.removeEventListener("change", onOSThemeChange);
  }, [theme]);
  return { theme, setTheme };
}
