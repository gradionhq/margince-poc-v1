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
 * "dark" means. Changing it lives here too (`useTheme`), because three surfaces
 * now offer the control — the top bar, the sign-in page and the onboarding rail
 * — and component-local state would let two of them disagree about the theme the
 * document is already showing.
 */

import { useSyncExternalStore } from "react";

export type Theme = "light" | "dark";

/**
 * The offered themes, in the order a chooser shows them.
 *
 * `satisfies` proves every entry is a real theme, so the settings control's
 * option set follows the type rather than being a second hand-written list that
 * can fall behind it.
 */
export const THEMES = ["light", "dark"] as const satisfies readonly Theme[];

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

/** The theme the document is currently showing. Resolved on first read so the
 *  store cannot answer before `resolveTheme` may safely touch the window. */
let liveTheme: Theme | null = null;
const listeners = new Set<() => void>();

function readTheme(): Theme {
  liveTheme ??= resolveTheme();
  return liveTheme;
}

function subscribeToTheme(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/**
 * Choose a theme, persist the choice, and repaint every mounted control.
 *
 * Unconditional even when the theme asked for is the one already showing: an
 * unstored theme is the OPERATING SYSTEM's, so picking the theme a reader can
 * already see is the first time they have said it is theirs rather than their
 * machine's — and swallowing that write would let the next OS change take it
 * away again.
 */
export function setTheme(theme: Theme): void {
  liveTheme = theme;
  persistTheme(theme);
  applyTheme(theme);
  for (const listener of listeners) {
    listener();
  }
}

/** Flip the theme, for the surfaces that offer one control rather than a choice. */
export function toggleTheme(): void {
  setTheme(readTheme() === "light" ? "dark" : "light");
}

export function useTheme(): readonly [Theme, () => void] {
  const theme = useSyncExternalStore(subscribeToTheme, readTheme, readTheme);
  return [theme, toggleTheme] as const;
}
