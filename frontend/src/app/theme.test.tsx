// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";

import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { LocaleProvider, translate } from "../i18n";
import { THEME_KEY, type Theme } from "./theme";
import { resetTheme } from "./theme-reset";
import { ThemeToggle } from "./theme-toggle";

// The theme now has three toggles on three surfaces (top bar, sign-in,
// onboarding rail). Only one of them is ever mounted today, but the state that
// answers them is one store rather than one `useState` per component precisely
// so a second one cannot show the opposite of what the document is displaying.

// That one store outlives a case, so every case here starts from light.
beforeEach(resetTheme);
afterEach(cleanup);

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
    renderToggle();
    expect(document.documentElement.dataset.theme).toBe("light");

    await userEvent.click(screen.getByRole("button"));

    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(window.localStorage.getItem(THEME_KEY)).toBe("dark");
  });

  it("keeps every mounted toggle showing the same theme", async () => {
    renderTwoToggles();
    const [first, second] = screen.getAllByRole("button");
    expect(first).toHaveAccessibleName(labelForPressTo("dark"));
    expect(second).toHaveAccessibleName(labelForPressTo("dark"));

    await userEvent.click(first);

    // The one that was NOT pressed has to move too — it names the theme the
    // next press would move to, and the document has already moved.
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(first).toHaveAccessibleName(labelForPressTo("light"));
    expect(second).toHaveAccessibleName(labelForPressTo("light"));
  });

  it("names the theme the press moves to, not the one on screen", async () => {
    renderToggle();
    const toggle = screen.getByRole("button");
    expect(toggle).toHaveAccessibleName(labelForPressTo("dark"));

    await userEvent.click(toggle);

    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(toggle).toHaveAccessibleName(labelForPressTo("light"));
  });
});
