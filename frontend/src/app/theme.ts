// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/**
 * The document's theme: resolved in ONE place, applied before React mounts.
 *
 * This exists because the theme used to be owned entirely by `useTheme` inside
 * `TopBar` — the AUTHENTICATED chrome. Every unauthenticated surface therefore
 * rendered with no `data-theme` at all: a reader whose OS is set to dark got the
 * light sign-in page, however carefully the dark tokens were authored. Worse, the
 * effect that set the attribute had no cleanup, so after signing out of a dark
 * session the stale `dark` persisted and the SAME screen rendered dark. One
 * screen, two appearances, neither of them chosen.
 *
 * Applying it at boot fixes both at once, and keeping the resolution here rather
 * than duplicating it means the boot path and the toggle cannot drift on what
 * "dark" means. The toggle still owns CHANGING the theme (see `useTheme`); this
 * module owns deciding and applying it.
 */

export type Theme = "light" | "dark";

export const THEME_KEY = "margince.theme";

/** Storage is unavailable in some embedded contexts; a missing preference is a
 *  default, never an error. */
function readStoredTheme(): string | null {
  try {
    return window.localStorage.getItem(THEME_KEY);
  } catch {
    return null;
  }
}

export function persistTheme(theme: Theme): void {
  try {
    window.localStorage.setItem(THEME_KEY, theme);
  } catch {
    // A browser refusing storage must not break the toggle.
  }
}

/**
 * An explicit choice wins; otherwise follow the operating system.
 *
 * The OS fallback is what makes the unauthenticated surface correct for a reader
 * who has never signed in and so has never had a chance to choose.
 */
export function resolveTheme(): Theme {
  const stored = readStoredTheme();
  if (stored === "light" || stored === "dark") {
    return stored;
  }
  const prefersDark =
    typeof window.matchMedia === "function" &&
    window.matchMedia("(prefers-color-scheme: dark)").matches;
  return prefersDark ? "dark" : "light";
}

export function applyTheme(theme: Theme): void {
  document.documentElement.dataset.theme = theme;
}
