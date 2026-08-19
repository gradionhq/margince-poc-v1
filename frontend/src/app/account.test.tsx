// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render as rtlRender, screen } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { AccountMenu } from "./account";
import { meFixture } from "./mefixture";
import { setThemeChoice, THEME_KEY } from "./theme";

// The account block: an avatar in the top bar's trail, and the menu it opens is
// the product's ONE door into settings and its ONE appearance control — the
// sidebar's foot carried the first and no longer exists, and the second used to
// be a form two navigations away. Behaviour only: WHERE the panel opens is CSS,
// but what it holds, and what a keyboard reader can do with it, are promises.

// The theme is ONE module-level store shared by every mounted control, so it
// outlives a case. Every case here starts from the unchosen state — following
// the machine, which the suite's matchMedia stub answers with light.
beforeEach(() => {
  setThemeChoice("system");
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.localStorage.removeItem(THEME_KEY);
});

// Seeded through the cache rather than a fetch stub: `useMe` reads ["me"], and
// a fixture written into it is the same snapshot the shell renders from.
const render = (ui: Parameters<typeof rtlRender>[0]) => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  client.setQueryData(["me"], meFixture());
  return rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
};

// The same shell, with this reader carrying a different display name and the
// same address — which is what a rename is.
const renderNamed = (displayName: string) => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const me = meFixture();
  client.setQueryData(["me"], {
    ...me,
    user: { ...me.user, display_name: displayName },
  });
  return rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">
        <AccountMenu />
      </LocaleProvider>
    </QueryClientProvider>,
  );
};

// Expanded, the trigger's accessible name carries the visible name as well as
// what the control does, so it is matched by suffix rather than by equality.
// Avatar-only there is no visible text to carry, and the name is exact.
const railTrigger = () => screen.getByRole("button", { name: /Account$/ });
const avatarTrigger = () => screen.getByRole("button", { name: "Account" });

const openMenu = async (user: UserEvent, trigger: HTMLElement) => {
  await user.click(trigger);
  return trigger;
};

const row = (name: string) => screen.getByRole("menuitem", { name });
const choice = (name: string) => screen.getByRole("menuitemradio", { name });

describe("AccountMenu", () => {
  // The rail's chip and the settings page's chip are the SAME person, so they
  // are the same colour — and they stay that colour when the display name
  // changes, because the tint is keyed on the address rather than on the name.
  it("keys the chip's tone on the address, not the display name", () => {
    const toneOf = (root: HTMLElement) =>
      [...(root.querySelector(".avatar")?.classList ?? [])].find((cls) =>
        cls.startsWith("avatar-t"),
      );
    const { container: named } = renderNamed("Test User");
    const tone = toneOf(named);
    expect(tone).toBeTruthy();
    cleanup();
    const { container: renamed } = renderNamed("Renamed Person");
    expect(toneOf(renamed)).toBe(tone);
  });

  // WCAG 2.5.3: the row prints the person's name, so a voice user who says the
  // word they can read has to reach this control.
  it("opens on the trigger and closes on it again", async () => {
    const user = userEvent.setup();
    const { container } = render(<AccountMenu />);
    expect(container.querySelector(".accountmenu")).toBeNull();

    const trigger = await openMenu(user, railTrigger());
    expect(screen.getByRole("menu", { name: /Account/ })).toBeTruthy();
    expect(trigger.getAttribute("aria-haspopup")).toBe("menu");
    expect(trigger.getAttribute("aria-expanded")).toBe("true");

    await user.click(trigger);
    expect(container.querySelector(".accountmenu")).toBeNull();
    expect(trigger.getAttribute("aria-expanded")).toBe("false");
  });

  // ONE door into settings, and it is this menu. The sidebar's foot used to
  // carry the other one; it is gone, so a reader who cannot open this menu
  // cannot reach the settings level at all — which is why the door is asserted
  // here rather than merely permitted.
  it("holds the product's one settings door", async () => {
    const user = userEvent.setup();
    render(<AccountMenu />);
    await openMenu(user, railTrigger());

    expect(row("Settings").getAttribute("href")).toBe("#/settings");
    // Exactly one: a second row a click apart landing on the same level is the
    // shape this menu replaced.
    expect(
      screen
        .getAllByRole("menuitem")
        .filter((item) => item.getAttribute("href") === "#/settings"),
    ).toHaveLength(1);
  });

  // The appearance choice IS a destination now: it lives where the reader can
  // see the document repaint under the open panel, rather than on a form two
  // navigations away that they then have to navigate back out of.
  it("owns the appearance choice, behind a submenu of its own", async () => {
    const user = userEvent.setup();
    render(<AccountMenu />);
    await openMenu(user, railTrigger());

    const theme = row("Theme");
    expect(theme.getAttribute("aria-haspopup")).toBe("menu");
    expect(theme.getAttribute("aria-expanded")).toBe("false");
    // Closed, the choices are not merely hidden — they are not rendered, so
    // there is nothing behind the row for a Tab or a screen reader to find.
    expect(screen.queryAllByRole("menuitemradio")).toHaveLength(0);
  });

  it("opens the theme submenu from the pointer", async () => {
    const user = userEvent.setup();
    render(<AccountMenu />);
    await openMenu(user, railTrigger());

    await user.click(row("Theme"));
    expect(row("Theme").getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByRole("menu", { name: "Theme" })).toBeTruthy();
    expect(
      screen.getAllByRole("menuitemradio").map((item) => item.textContent),
    ).toEqual(["Light", "Dark", "System"]);
  });

  // Right opens it and lands the reader on the choice they already have, so the
  // submenu is usable by somebody who never touches a pointer — which is the
  // whole reason this panel became a real menu.
  it("opens the theme submenu from the keyboard, on the standing choice", async () => {
    const user = userEvent.setup();
    render(<AccountMenu />);
    await openMenu(user, railTrigger());

    await user.keyboard("{ArrowDown}");
    expect(document.activeElement).toBe(row("Theme"));

    await user.keyboard("{ArrowRight}");
    expect(row("Theme").getAttribute("aria-expanded")).toBe("true");
    expect(document.activeElement).toBe(choice("System"));
  });

  // Through to the real store, which is the only thing that can prove the tick
  // reports the document rather than decorating the row.
  it("writes the picked appearance through the theme store", async () => {
    const user = userEvent.setup();
    render(<AccountMenu />);
    await openMenu(user, railTrigger());
    await user.click(row("Theme"));

    const system = choice("System");
    const dark = choice("Dark");
    expect(system.getAttribute("aria-checked")).toBe("true");
    expect(dark.getAttribute("aria-checked")).toBe("false");

    await user.click(dark);

    expect(dark.getAttribute("aria-checked")).toBe("true");
    expect(system.getAttribute("aria-checked")).toBe("false");
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(window.localStorage.getItem(THEME_KEY)).toBe("dark");
    // The menu stays open on a pick: the whole of the feedback is the document
    // repainting under it with the tick on the row that did it.
    expect(screen.getByRole("menu", { name: "Theme" })).toBeTruthy();
  });

  // One keystroke, one layer. The dismissal both layers share stands the outer
  // one down while the row that opened the inner one still reads expanded
  // (app/popover.ts), so a reader is never taken two steps back by one press.
  it("closes the submenu on Escape, then the menu, returning focus each time", async () => {
    const user = userEvent.setup();
    const { container } = render(<AccountMenu />);
    const trigger = await openMenu(user, railTrigger());
    await user.click(row("Theme"));
    expect(document.activeElement).toBe(choice("System"));

    await user.keyboard("{Escape}");
    expect(screen.queryByRole("menu", { name: "Theme" })).toBeNull();
    expect(container.querySelector(".accountmenu")).not.toBeNull();
    expect(document.activeElement).toBe(row("Theme"));
    expect(row("Theme").getAttribute("aria-expanded")).toBe("false");

    await user.keyboard("{Escape}");
    expect(container.querySelector(".accountmenu")).toBeNull();
    expect(document.activeElement).toBe(trigger);
  });

  // Left is the other way back out, and it is the one a reader who walked in
  // with Right reaches for.
  it("walks back out of the submenu with Left", async () => {
    const user = userEvent.setup();
    render(<AccountMenu />);
    await openMenu(user, railTrigger());
    await user.click(row("Theme"));

    await user.keyboard("{ArrowLeft}");
    expect(screen.queryByRole("menu", { name: "Theme" })).toBeNull();
    expect(document.activeElement).toBe(row("Theme"));
  });

  it("walks its rows with Up and Down, and its ends with Home and End", async () => {
    const user = userEvent.setup();
    render(<AccountMenu />);
    await openMenu(user, railTrigger());
    // The menu takes focus when it opens, or a keyboard reader has to walk into
    // it with a key nothing told them about.
    expect(document.activeElement).toBe(row("Settings"));

    await user.keyboard("{ArrowDown}");
    expect(document.activeElement).toBe(row("Theme"));
    await user.keyboard("{ArrowDown}");
    expect(document.activeElement).toBe(row("Sign out"));
    // Wrapping, so the walk has no dead end.
    await user.keyboard("{ArrowDown}");
    expect(document.activeElement).toBe(row("Settings"));

    await user.keyboard("{ArrowUp}");
    expect(document.activeElement).toBe(row("Sign out"));
    await user.keyboard("{Home}");
    expect(document.activeElement).toBe(row("Settings"));
    await user.keyboard("{End}");
    expect(document.activeElement).toBe(row("Sign out"));
  });

  // A roving tabstop: exactly one row is in the tab order at a time, so Tab
  // leaves the menu rather than walking it.
  it("keeps one tabstop among the rows, on whichever holds focus", async () => {
    const user = userEvent.setup();
    render(<AccountMenu />);
    await openMenu(user, railTrigger());

    const stops = () =>
      screen
        .getAllByRole("menuitem")
        .filter((item) => item.getAttribute("tabindex") === "0");
    expect(stops()).toEqual([row("Settings")]);

    await user.keyboard("{ArrowDown}");
    expect(stops()).toEqual([row("Theme")]);
  });

  it("hands focus back to the trigger when Escape closes it", async () => {
    const user = userEvent.setup();
    const { container } = render(<AccountMenu />);
    const trigger = await openMenu(user, railTrigger());
    // Standing on a row, the way a keyboard user arrives at one.
    const signOut = row("Sign out");
    signOut.focus();
    expect(document.activeElement).toBe(signOut);

    await user.keyboard("{Escape}");
    expect(container.querySelector(".accountmenu")).toBeNull();
    // Not document.body: dismissing unmounts the focused row, and focus left on
    // the body restarts the next Tab at the top of the page, having lost the
    // chrome the user was standing in.
    expect(document.activeElement).toBe(trigger);
  });

  it("keeps the way out reachable from the menu", async () => {
    const user = userEvent.setup();
    render(<AccountMenu />);
    await openMenu(user, railTrigger());
    expect(row("Sign out").hasAttribute("disabled")).toBe(false);
  });

  // Closed, the panel is not rendered at all — so there is nothing behind the
  // trigger for Tab to land on, and nothing for a screen reader to read out of
  // a menu that is not open.
  it("puts nothing in the tab order while it is closed", () => {
    const { container } = render(<AccountMenu />);
    expect(container.querySelectorAll("a, button")).toHaveLength(1);
    expect(screen.queryByRole("menu")).toBeNull();
  });

  // The trigger prints nothing, and an avatar alone tells a screen reader
  // nothing. The sentence is carried as clipped text and wired as the trigger's
  // description — the `title` is the pointer's copy of it and is never the
  // accessible name.
  it("carries the identity for a screen reader without printing it", () => {
    const { container } = render(<AccountMenu />);
    const trigger = avatarTrigger();
    expect(container.querySelector(".acctwho")).toBeNull();

    const describedBy = trigger.getAttribute("aria-describedby") ?? "";
    const spoken = document.getElementById(describedBy);
    expect(spoken?.className).toContain("sr-only");
    expect(spoken?.textContent).toContain("Test User");
    expect(spoken?.textContent).toContain("test@example.test");
    expect(trigger.getAttribute("title")).toBe(spoken?.textContent);
  });

  // The trigger is the avatar and nothing else, so the name it carries is the
  // only thing that identifies it — and the panel is the only place the reader
  // can confirm WHOSE account they are about to act on.
  it("names the top bar's avatar-only trigger, and prints the identity in the panel", async () => {
    const user = userEvent.setup();
    const { container } = render(<AccountMenu />);
    const trigger = avatarTrigger();
    expect(container.querySelector(".acctwho")).toBeNull();
    expect(trigger.getAttribute("aria-label")).toBe("Account");

    await openMenu(user, trigger);
    const who = container.querySelector(".acctmenuwho");
    expect(who?.querySelector("b")?.textContent).toBe("Test User");
    expect(who?.querySelector(".acctmail")?.textContent).toBe(
      "test@example.test",
    );
    // And in the menu's own name, so a reader entering it in menu mode is told
    // whose account it is without having to leave to find out.
    expect(screen.getByRole("menu", { name: /Test User/ })).toBe(
      container.querySelector(".accountmenu"),
    );
  });
});
