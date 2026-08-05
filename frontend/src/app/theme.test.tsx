// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";

import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { LocaleProvider } from "../i18n";
import { ThemeToggle } from "./theme-toggle";

// The theme now has three toggles on three surfaces (top bar, sign-in,
// onboarding rail). Only one of them is ever mounted today, but the state that
// answers them is one store rather than one `useState` per component precisely
// so a second one cannot show the opposite of what the document is displaying.

afterEach(cleanup);

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
    const toggle = screen.getByRole("button");
    const before = document.documentElement.dataset.theme;

    await userEvent.click(toggle);

    const after = document.documentElement.dataset.theme;
    expect(after).toMatch(/^(light|dark)$/);
    expect(after).not.toBe(before);
    expect(window.localStorage.getItem("margince.theme")).toBe(after);
  });

  it("keeps every mounted toggle showing the same theme", async () => {
    renderTwoToggles();
    const [first, second] = screen.getAllByRole("button");
    expect(second.getAttribute("aria-label")).toBe(
      first.getAttribute("aria-label"),
    );

    await userEvent.click(first);

    // The one that was NOT pressed has to move too — it names the theme the
    // next press would move to, and the document has already moved.
    expect(second.getAttribute("aria-label")).toBe(
      first.getAttribute("aria-label"),
    );
    expect(second.getAttribute("aria-label")).toBe(
      document.documentElement.dataset.theme === "light"
        ? "Dark theme"
        : "Light theme",
    );
  });

  it("names the theme the press moves to, not the one on screen", async () => {
    renderToggle();
    const toggle = screen.getByRole("button");

    await userEvent.click(toggle);

    expect(toggle.getAttribute("aria-label")).toBe(
      document.documentElement.dataset.theme === "dark"
        ? "Light theme"
        : "Dark theme",
    );
  });
});
