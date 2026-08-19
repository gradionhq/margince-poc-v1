// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { setThemeChoice, THEME_KEY } from "./theme";

/**
 * Put the theme back to what a first-time visitor meets: no choice recorded,
 * the store following the operating system, and the document painted with
 * whatever the suite's `matchMedia` says that is.
 *
 * The theme is deliberately ONE module-level store shared by every mounted
 * control, so a case that presses one moves state its neighbours can see — the
 * store, `localStorage`, and `document.documentElement.dataset.theme`. Any
 * suite that presses a theme control therefore starts from here, or it is
 * asserting against whatever the case before it left behind.
 *
 * Choosing "system" rather than clearing the key alone is what reaches all
 * three at once: it re-resolves the theme, applies it to the document, and
 * re-arms the `prefers-color-scheme` subscription against the `matchMedia` this
 * case has stubbed — a case that restubbed it would otherwise inherit the
 * listener belonging to the previous one. Removing the key afterwards leaves
 * storage as an install that has never chosen, which is what "system" means.
 */
export function resetTheme(): void {
  setThemeChoice("system");
  window.localStorage.removeItem(THEME_KEY);
}
