// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render as rtlRender, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { LocaleProvider } from "../i18n";
import { AccountMenu } from "./account";
import { meFixture } from "./mefixture";

// The account block at the sidebar foot: the trigger prints who is signed in,
// the menu it opens carries this person's surfaces, their preferences and the
// way out. Behaviour only — where it opens is CSS, but WHAT it opens, what it
// keeps open, and what it hands back to a keyboard user are promises.

afterEach(cleanup);

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

// Expanded, the trigger's accessible name carries the visible name as well as
// what the control does, so it is matched by suffix rather than by equality.
const openMenu = async () => {
  const trigger = screen.getByRole("button", { name: /Account$/ });
  await userEvent.click(trigger);
  return trigger;
};

describe("AccountMenu", () => {
  it("prints the signed-in identity on the trigger, name over address", () => {
    const { container } = render(<AccountMenu collapsed={false} />);
    const who = container.querySelector(".acctwho");
    expect(who?.querySelector("b")?.textContent).toBe("Test User");
    expect(who?.querySelector(".acctmail")?.textContent).toBe(
      "test@example.test",
    );
    // Initials, never a fabricated name.
    expect(container.querySelector(".acctavatar")?.textContent).toBe("TU");
  });

  // WCAG 2.5.3: the row prints the person's name, so a voice user who says the
  // word they can read has to reach this control.
  it("names the trigger with the visible name and what it opens", () => {
    render(<AccountMenu collapsed={false} />);
    const trigger = screen.getByRole("button", { name: /Account$/ });
    expect(trigger.getAttribute("aria-label")).toBe("Test User — Account");
  });

  it("opens on the trigger and closes on it again", async () => {
    const { container } = render(<AccountMenu collapsed={false} />);
    expect(container.querySelector(".accountmenu")).toBeNull();

    const trigger = await openMenu();
    expect(container.querySelector(".accountmenu")).not.toBeNull();
    expect(trigger.getAttribute("aria-expanded")).toBe("true");

    await userEvent.click(trigger);
    expect(container.querySelector(".accountmenu")).toBeNull();
    expect(trigger.getAttribute("aria-expanded")).toBe("false");
  });

  // The two destinations are different surfaces: this person's own account, and
  // the installation's settings. A single Settings row would send both readers
  // to the same place.
  it("offers the account surface and the settings surface, in that order", async () => {
    render(<AccountMenu collapsed={false} />);
    await openMenu();
    const links = screen.getAllByRole("link");
    expect(links.map((link) => link.getAttribute("href"))).toEqual([
      "#/settings/account",
      "#/settings",
    ]);
    expect(links[0].textContent).toBe("Account settings");
    expect(links[1].textContent).toBe("Settings");
  });

  it("stays open while a preference is being changed", async () => {
    render(<AccountMenu collapsed={false} />);
    await openMenu();
    const theme = screen.getByRole("button", {
      name: /^Theme: Switch to (dark|light) theme$/,
    });
    const before = theme.getAttribute("aria-label");

    await userEvent.click(theme);
    // The row is still there, now offering the way back: a menu that dismissed
    // itself would take the control out of view together with the change.
    const after = screen.getByRole("button", {
      name: /^Theme: Switch to (dark|light) theme$/,
    });
    expect(after.getAttribute("aria-label")).not.toBe(before);

    // Put the theme back. It is document-wide state that outlives this test —
    // persisted to localStorage and held in theme.ts's own store, neither of
    // which `cleanup()` touches — so leaving it flipped would hand every later
    // test in this file a theme that depends on test order.
    await userEvent.click(after);
    expect(
      screen
        .getByRole("button", {
          name: /^Theme: Switch to (dark|light) theme$/,
        })
        .getAttribute("aria-label"),
    ).toBe(before);
  });

  it("hands focus back to the trigger when Escape closes it", async () => {
    const { container } = render(<AccountMenu collapsed={false} />);
    const trigger = await openMenu();
    // Standing on a row, the way a keyboard user arrives at one.
    const language = screen.getByRole("button", { name: /^Language: / });
    language.focus();
    expect(document.activeElement).toBe(language);

    await userEvent.keyboard("{Escape}");
    expect(container.querySelector(".accountmenu")).toBeNull();
    // Not document.body: dismissing unmounts the focused row, and focus left on
    // the body restarts the next Tab at the top of the page, having lost the
    // sidebar the user was standing in.
    expect(document.activeElement).toBe(trigger);
  });

  // Both dismissals listen on the document. Without an owner for the keystroke
  // one Escape would collapse the language list AND the menu around it, leaving
  // the reader two steps from where they were — and the language list is the one
  // control a reader who cannot read the current locale needs most.
  it("closes one layer per Escape when the language list is open", async () => {
    render(<AccountMenu collapsed={false} />);
    await openMenu();
    await userEvent.click(screen.getByRole("button", { name: /^Language: / }));
    expect(screen.getByRole("menu", { name: "Language" })).toBeTruthy();

    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("menu", { name: "Language" })).toBeNull();
    expect(screen.getByRole("button", { name: /^Language: / })).toBeTruthy();

    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("button", { name: /^Language: / })).toBeNull();
  });

  it("keeps the way out reachable from the menu", async () => {
    render(<AccountMenu collapsed={false} />);
    await openMenu();
    const signOut = screen.getByRole("button", { name: "Sign out" });
    expect(signOut.hasAttribute("disabled")).toBe(false);
  });

  // Collapsed there is no room to print who is signed in, and an avatar alone
  // tells a screen reader nothing. The sentence is carried as clipped text and
  // wired as the trigger's description — the `title` is the pointer's copy of it
  // and is never the accessible name.
  it("carries the identity for a screen reader when collapsed, without printing it", () => {
    const { container } = render(<AccountMenu collapsed />);
    const trigger = screen.getByRole("button", { name: "Account" });
    expect(container.querySelector(".acctwho")).toBeNull();

    const describedBy = trigger.getAttribute("aria-describedby") ?? "";
    const spoken = document.getElementById(describedBy);
    expect(spoken?.className).toContain("sr-only");
    expect(spoken?.textContent).toContain("Test User");
    expect(spoken?.textContent).toContain("test@example.test");
    expect(trigger.getAttribute("title")).toBe(spoken?.textContent);
  });

  it("opens the same menu from the collapsed trigger", async () => {
    const { container } = render(<AccountMenu collapsed />);
    await openMenu();
    expect(container.querySelector(".accountmenu")).not.toBeNull();
    expect(screen.getByRole("button", { name: "Sign out" })).toBeTruthy();
  });
});
