// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { THEME_KEY, toggleTheme } from "./theme";

/**
 * Put the theme back to what a first-time visitor meets: store, document and
 * storage all agreeing on light, with no choice recorded.
 *
 * The theme is deliberately ONE module-level store shared by every mounted
 * toggle, so a case that presses one moves state its neighbours can see — the
 * store, `localStorage`, and `document.documentElement.dataset.theme`. Any
 * suite that presses a toggle therefore starts from here, or it is asserting
 * against whatever the case before it left behind.
 *
 * `toggleTheme` is that store's only writer and it applies what it wrote to the
 * document, so one press reveals the store's value and a second returns any
 * value to light. Clearing the key afterwards leaves no explicit preference,
 * which is what `resolveTheme` reads as "follow the operating system" — and the
 * suites' matchMedia stub answers that with light.
 */
export function resetTheme(): void {
  toggleTheme();
  if (document.documentElement.dataset.theme !== "light") {
    toggleTheme();
  }
  window.localStorage.removeItem(THEME_KEY);
}
