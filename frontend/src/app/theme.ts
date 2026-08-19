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
 * "dark" means. Changing it lives here too, because several surfaces offer the
 * control — the account menu, the sign-in page and the onboarding rail — and
 * component-local state would let two of them disagree about the theme the
 * document is already showing.
 *
 * Two values, not one, and the distinction is the whole point of "system":
 * a CHOICE is what the person picked (`ThemeChoice`, including "system"), a
 * THEME is what the document is painted in (`Theme`, only ever light or dark).
 * "system" resolves to a theme and keeps resolving: while it is the choice this
 * module follows `prefers-color-scheme` live, so an OS switch reaches an open
 * tab instead of waiting for a reload.
 */

import { useSyncExternalStore } from "react";

export type Theme = "light" | "dark";

/** What a person can pick. "system" is a standing instruction to follow the
 *  operating system rather than a third appearance. */
export type ThemeChoice = Theme | "system";

/**
 * The offered choices, in the order a chooser shows them.
 *
 * Annotated rather than asserted: the type is what proves every entry is a real
 * choice, so a chooser's option set follows `ThemeChoice` instead of being a
 * second hand-written list that can fall behind it.
 */
export const THEME_CHOICES: readonly ThemeChoice[] = [
  "light",
  "dark",
  "system",
];

export const THEME_KEY = "margince.theme";

const PREFERS_DARK = "(prefers-color-scheme: dark)";

/** Storage is unavailable in some embedded contexts; a missing preference is a
 *  default, never an error. */
function readStoredValue(): string | null {
  try {
    return window.localStorage.getItem(THEME_KEY);
  } catch {
    return null;
  }
}

/**
 * The stored choice, or "system".
 *
 * An ABSENT key is what every install had before "system" was a word this
 * module knew, and following the OS is exactly what those installs already did
 * — so the default is the upgrade path, not a new appearance. An unrecognised
 * value gets the same answer: storage is shared with browser extensions and
 * older builds, and a string this build cannot name is not a reason to paint
 * the page nothing at all.
 */
function readStoredChoice(): ThemeChoice {
  const stored = readStoredValue();
  return stored === "light" || stored === "dark" || stored === "system"
    ? stored
    : "system";
}

function persistChoice(choice: ThemeChoice): void {
  try {
    window.localStorage.setItem(THEME_KEY, choice);
  } catch {
    // A browser refusing storage must not break the chooser.
  }
}

/** `matchMedia` is absent in some embedded contexts, and a missing media query
 *  is an unstated preference — which is light. */
function systemPrefersDark(): boolean {
  return (
    typeof window.matchMedia === "function" &&
    window.matchMedia(PREFERS_DARK).matches
  );
}

function themeFor(choice: ThemeChoice): Theme {
  if (choice === "light" || choice === "dark") {
    return choice;
  }
  return systemPrefersDark() ? "dark" : "light";
}

/**
 * An explicit choice wins; otherwise follow the operating system.
 *
 * The OS fallback is what makes the unauthenticated surface correct for a reader
 * who has never signed in and so has never had a chance to choose.
 */
export function resolveTheme(): Theme {
  return themeFor(readStoredChoice());
}

function applyTheme(theme: Theme): void {
  document.documentElement.dataset.theme = theme;
}

/** What the document is currently showing, and why. Resolved on first read so
 *  the store cannot answer before `resolveTheme` may safely touch the window. */
let current: { choice: ThemeChoice; theme: Theme } | null = null;
const listeners = new Set<() => void>();

function announce(): void {
  for (const listener of listeners) {
    listener();
  }
}

/**
 * The media query this module is listening to, held rather than re-derived
 * because `matchMedia` mints a fresh object per call and a listener can only be
 * removed from the object it was added to.
 */
let watchedMedia: MediaQueryList | null = null;

/** Repaint on an OS change. Only ever attached while the choice is "system", so
 *  it never has to ask again whether it is allowed to move the theme. */
function followSystem(): void {
  const state = ensureLoaded();
  const next = systemPrefersDark() ? "dark" : "light";
  if (next === state.theme) {
    return;
  }
  state.theme = next;
  applyTheme(next);
  announce();
}

function stopFollowingSystem(): void {
  watchedMedia?.removeEventListener("change", followSystem);
  watchedMedia = null;
}

/**
 * Subscribe to the OS preference, from scratch each time.
 *
 * Dropping any previous subscription first is what keeps this correct across a
 * re-entry: `matchMedia` may answer with a different object than it did before
 * (a test that restubs it, a document that was replaced), and holding the stale
 * one would leave a listener nothing can remove.
 */
function startFollowingSystem(): void {
  stopFollowingSystem();
  if (typeof window.matchMedia !== "function") {
    return;
  }
  const media = window.matchMedia(PREFERS_DARK);
  // Safari before 14 offers only the deprecated `addListener`. Following the OS
  // is an enhancement there, not the feature — the resolved theme is still
  // correct at boot.
  if (typeof media.addEventListener !== "function") {
    return;
  }
  media.addEventListener("change", followSystem);
  watchedMedia = media;
}

function ensureLoaded(): { choice: ThemeChoice; theme: Theme } {
  if (current) {
    return current;
  }
  const choice = readStoredChoice();
  current = { choice, theme: themeFor(choice) };
  if (choice === "system") {
    startFollowingSystem();
  }
  return current;
}

function readTheme(): Theme {
  return ensureLoaded().theme;
}

function readChoice(): ThemeChoice {
  return ensureLoaded().choice;
}

function subscribeToTheme(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/**
 * Pick a theme, persist the choice, and repaint every mounted control.
 *
 * Unconditional even when the resolved theme does not move: picking "light" on
 * a machine that is already light is the first time the reader has said the
 * appearance is theirs rather than their machine's, and swallowing that write
 * would let the next OS change take it away again. The mirror holds for
 * "system", which is a person handing that authority BACK — so the subscription
 * is re-armed here and dropped the moment the choice becomes explicit again.
 */
export function setThemeChoice(choice: ThemeChoice): void {
  const theme = themeFor(choice);
  current = { choice, theme };
  persistChoice(choice);
  applyTheme(theme);
  if (choice === "system") {
    startFollowingSystem();
  } else {
    stopFollowingSystem();
  }
  announce();
}

/**
 * Flip the theme, for the surfaces that offer one control rather than a choice.
 *
 * A flip from "system" lands on an EXPLICIT theme: the reader pressed a button
 * that names one appearance, so answering with a standing instruction to follow
 * the machine would be a different thing from what the label promised.
 */
export function toggleTheme(): void {
  setThemeChoice(readTheme() === "light" ? "dark" : "light");
}

/**
 * Resolve, apply and start following, before the first render.
 *
 * The apply is what avoids the light-to-dark flash on reload. The follow has to
 * begin here too rather than with the first mounted chooser: the account menu
 * renders its theme control only while it is OPEN, so a store that woke up on
 * its first reader would leave a tab nobody had opened the menu in deaf to the
 * OS switch this module exists to hear.
 */
export function startTheme(): void {
  ensureLoaded();
  applyTheme(readTheme());
}

/** The theme the document is showing. */
export function useTheme(): Theme {
  return useSyncExternalStore(subscribeToTheme, readTheme, readTheme);
}

/** The choice the reader made — "system" until they make one. */
export function useThemeChoice(): ThemeChoice {
  return useSyncExternalStore(subscribeToTheme, readChoice, readChoice);
}
