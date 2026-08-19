// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";

import { act, cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider, translate } from "../i18n";
import {
  setThemeChoice,
  THEME_CHOICES,
  THEME_KEY,
  type Theme,
  type ThemeChoice,
} from "./theme";
import { resetTheme } from "./theme-reset";
import { ThemeToggle } from "./theme-toggle";

// The theme has toggles on two surfaces (sign-in and the onboarding rail) and a
// three-way chooser in the account menu. Only one of them is ever mounted today,
// but the state that answers them is one store rather than one `useState` per
// component precisely so a second one cannot show the opposite of what the
// document is displaying.

/**
 * A `prefers-color-scheme` the case drives.
 *
 * jsdom's own `matchMedia` answers `false` and has no way to change its mind,
 * which is the one thing every claim about "system" needs. This stands in for
 * the machine: `set()` moves the preference and dispatches to the listeners the
 * theme store subscribed with, synchronously — the media query is this suite's
 * clock, so no case waits on a real one.
 */
function stubSystemPreference(prefersDark: boolean) {
  const listeners = new Set<() => void>();
  let dark = prefersDark;
  vi.stubGlobal("matchMedia", (query: string) => ({
    // Only the query the theme asks for: a stub answering true to everything
    // would also tell the viewport hook the reader is on a phone.
    get matches() {
      return dark && query === "(prefers-color-scheme: dark)";
    },
    media: query,
    addEventListener: (_type: string, listener: () => void) =>
      listeners.add(listener),
    removeEventListener: (_type: string, listener: () => void) =>
      listeners.delete(listener),
  }));
  return {
    set(next: boolean) {
      dark = next;
      act(() => {
        for (const listener of [...listeners]) {
          // No event argument, and none asserted into being: `followSystem`
          // takes none and reads `systemPrefersDark()` itself, so a hand-built
          // object standing in for a `MediaQueryListEvent` would be describing a
          // contract the store does not have.
          listener();
        }
      });
    },
    /** How many listeners the store is holding — the claim that it lets go. */
    get listenerCount() {
      return listeners.size;
    },
  };
}

// The store outlives a case, so every case here starts from a first-time
// visitor: no stored choice, and a machine that prefers light.
beforeEach(() => {
  stubSystemPreference(false);
  resetTheme();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

/**
 * The label a toggle carries when a press would take the document to `next`.
 *
 * Read from the catalog rather than spelled out, so the assertion pins WHICH
 * message the control names its destination with — reworded copy, or a locale
 * that is not English, is then not a failure of the toggle.
 */
const labelForPressTo = (next: Theme) =>
  translate("en", next === "dark" ? "theme.toDark" : "theme.toLight");

const renderToggle = () =>
  render(
    <LocaleProvider initial="en">
      <ThemeToggle />
    </LocaleProvider>,
  );

// Two of them, which is the arrangement no surface ships today and every one of
// them would hit the moment a second toggle went up.
const renderTwoToggles = () =>
  render(
    <LocaleProvider initial="en">
      <ThemeToggle />
      <ThemeToggle />
    </LocaleProvider>,
  );

describe("the theme control", () => {
  it("applies the change to the document and remembers it", async () => {
    const user = userEvent.setup();
    renderToggle();
    expect(document.documentElement.dataset.theme).toBe("light");

    await user.click(screen.getByRole("button"));

    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(window.localStorage.getItem(THEME_KEY)).toBe("dark");
  });

  it("keeps every mounted toggle showing the same theme", async () => {
    const user = userEvent.setup();
    renderTwoToggles();
    const [first, second] = screen.getAllByRole("button");
    expect(first).toHaveAccessibleName(labelForPressTo("dark"));
    expect(second).toHaveAccessibleName(labelForPressTo("dark"));

    await user.click(first);

    // The one that was NOT pressed has to move too — it names the theme the
    // next press would move to, and the document has already moved.
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(first).toHaveAccessibleName(labelForPressTo("light"));
    expect(second).toHaveAccessibleName(labelForPressTo("light"));
  });

  it("names the theme the press moves to, not the one on screen", async () => {
    const user = userEvent.setup();
    renderToggle();
    const toggle = screen.getByRole("button");
    expect(toggle).toHaveAccessibleName(labelForPressTo("dark"));

    await user.click(toggle);

    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(toggle).toHaveAccessibleName(labelForPressTo("light"));
  });

  // A toggle has one label, so it can only ever name an appearance. Pressing it
  // while the machine is deciding is the reader taking that decision back —
  // answering with "system" again would leave the button promising a theme it
  // did not deliver.
  it("turns a press from the system default into an explicit choice", async () => {
    const user = userEvent.setup();
    const machine = stubSystemPreference(true);
    setThemeChoice("system");
    expect(document.documentElement.dataset.theme).toBe("dark");

    renderToggle();
    await user.click(screen.getByRole("button"));

    expect(window.localStorage.getItem(THEME_KEY)).toBe("light");
    expect(document.documentElement.dataset.theme).toBe("light");

    // And the machine no longer has a say: the store let its subscription go.
    machine.set(false);
    machine.set(true);
    expect(document.documentElement.dataset.theme).toBe("light");
  });
});

describe("the system choice", () => {
  it("stores the choice itself, and resolves it from the machine", () => {
    stubSystemPreference(true);
    setThemeChoice("system");

    // "system" is what is REMEMBERED — the resolved theme is not, or a reader
    // who picked it would come back to a fixed appearance.
    expect(window.localStorage.getItem(THEME_KEY)).toBe("system");
    expect(document.documentElement.dataset.theme).toBe("dark");
  });

  it("repaints an open tab when the machine changes its mind", () => {
    const machine = stubSystemPreference(false);
    setThemeChoice("system");
    expect(document.documentElement.dataset.theme).toBe("light");

    machine.set(true);
    expect(document.documentElement.dataset.theme).toBe("dark");

    machine.set(false);
    expect(document.documentElement.dataset.theme).toBe("light");
  });

  it("moves every mounted control with it, not just the document", () => {
    const machine = stubSystemPreference(false);
    setThemeChoice("system");
    renderTwoToggles();
    const [first, second] = screen.getAllByRole("button");
    expect(first).toHaveAccessibleName(labelForPressTo("dark"));

    machine.set(true);

    expect(first).toHaveAccessibleName(labelForPressTo("light"));
    expect(second).toHaveAccessibleName(labelForPressTo("light"));
  });

  // An explicit choice is a standing instruction, and a machine that changes
  // under it must not overrule it — the reader would watch a theme they chose
  // undo itself for reasons the page never showed them.
  it("leaves an explicit choice alone when the machine changes", () => {
    const machine = stubSystemPreference(false);
    setThemeChoice("dark");

    machine.set(true);
    expect(document.documentElement.dataset.theme).toBe("dark");
    machine.set(false);
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(window.localStorage.getItem(THEME_KEY)).toBe("dark");
  });

  // The subscription is state too, and the kind that survives being replaced:
  // a listener nobody removed keeps repainting after the reader has taken the
  // decision away from the machine.
  it("holds one subscription while it is following, and none after", () => {
    const machine = stubSystemPreference(false);
    setThemeChoice("system");
    expect(machine.listenerCount).toBe(1);

    setThemeChoice("system");
    expect(machine.listenerCount).toBe(1);

    setThemeChoice("light");
    expect(machine.listenerCount).toBe(0);
  });
});

describe("a stored value this build cannot name", () => {
  // Storage is shared with older builds and with everything else on the origin.
  // The honest answer to a string we do not recognise is the default, which is
  // "system" — not a crash, and not a document left with no theme at all.
  //
  // A fresh module is what makes this a claim about BOOT: the store resolves
  // once and remembers, so a case that only wrote to storage would be asserting
  // against the choice the previous case left in memory.
  it.each(["", "sepia", "SYSTEM", "  dark  "])(
    "resolves %o by following the machine",
    async (stored) => {
      window.localStorage.setItem(THEME_KEY, stored);
      const machine = stubSystemPreference(true);

      vi.resetModules();
      const booted = await import("./theme");
      booted.startTheme();

      expect(booted.resolveTheme()).toBe("dark");
      expect(document.documentElement.dataset.theme).toBe("dark");

      // And it is FOLLOWING, not merely defaulted once.
      machine.set(false);
      expect(document.documentElement.dataset.theme).toBe("light");
    },
  );

  // The same claim for an ABSENT key, which is what every install that predates
  // "system" has. Following the machine is what those installs already did, so
  // nobody's appearance moves on upgrade.
  it("resolves an install that has never chosen by following the machine", async () => {
    window.localStorage.removeItem(THEME_KEY);
    const machine = stubSystemPreference(true);

    vi.resetModules();
    const booted = await import("./theme");
    booted.startTheme();

    expect(document.documentElement.dataset.theme).toBe("dark");
    machine.set(false);
    expect(document.documentElement.dataset.theme).toBe("light");
  });
});

describe("the offered choices", () => {
  // Pinned so a fourth choice cannot reach a chooser without a catalog entry to
  // name it, and so the order a menu shows them in is a decision rather than a
  // side effect of how the type happened to be written.
  it("are light, dark and system, in that order", () => {
    expect([...THEME_CHOICES]).toEqual<ThemeChoice[]>([
      "light",
      "dark",
      "system",
    ]);
  });
});
