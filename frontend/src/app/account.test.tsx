// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render as rtlRender, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { AccountMenu, AccountRows } from "./account";
import { meFixture } from "./mefixture";

// The account block at the sidebar foot: the trigger prints who is signed in and
// the menu it opens carries this person's two surfaces and the way out — theme
// and language are preferences and live on Settings → Account instead. Behaviour
// only: where it opens is CSS, but WHAT it opens and what it hands back to a
// keyboard user are promises. `AccountRows` is the same list flat, for the phone
// sheet, where a popover anchored to the rail's foot has nowhere to open.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
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
        <AccountMenu collapsed={false} />
      </LocaleProvider>
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
    // Initials, never a fabricated name — and drawn by the design system's one
    // chip rather than by a hand-rolled span with its own initials rule, which
    // is what gave this reader a different mark here than on their own account
    // settings page.
    expect(container.querySelector(".avatar")?.textContent).toBe("TU");
  });

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

  // ONE destination, and it is this person's own account. A second row into
  // settings generally would land on the same page — settings opens on its first
  // entry, which is Account — while the rail already carries that door.
  it("offers this person's account, and no second door into settings", async () => {
    render(<AccountMenu collapsed={false} />);
    await openMenu();
    const links = screen.getAllByRole("link");
    expect(links.map((link) => link.getAttribute("href"))).toEqual([
      "#/settings/account",
    ]);
    expect(links[0].textContent).toBe("Account");
  });

  // A preference is not a destination: the menu offers the two surfaces and the
  // way out, and nothing that changes a setting in place.
  it("carries no preference controls", async () => {
    render(<AccountMenu collapsed={false} />);
    await openMenu();
    expect(screen.queryByRole("button", { name: /Theme/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /Language/i })).toBeNull();
  });

  it("hands focus back to the trigger when Escape closes it", async () => {
    const { container } = render(<AccountMenu collapsed={false} />);
    const trigger = await openMenu();
    // Standing on a row, the way a keyboard user arrives at one.
    const signOut = screen.getByRole("button", { name: "Sign out" });
    signOut.focus();
    expect(document.activeElement).toBe(signOut);

    await userEvent.keyboard("{Escape}");
    expect(container.querySelector(".accountmenu")).toBeNull();
    // Not document.body: dismissing unmounts the focused row, and focus left on
    // the body restarts the next Tab at the top of the page, having lost the
    // sidebar the user was standing in.
    expect(document.activeElement).toBe(trigger);
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

describe("AccountRows", () => {
  it("offers the same rows, flat, in the menu's order", () => {
    render(<AccountRows />);
    const links = screen.getAllByRole("link");
    expect(links.map((link) => link.getAttribute("href"))).toEqual([
      "#/settings/account",
    ]);
    expect(links[0].textContent).toBe("Account");
    // No trigger to open first, and nothing that could hide the rows behind one:
    // in the sheet the rows ARE the surface.
    expect(screen.queryByRole("button", { name: /Account$/ })).toBeNull();
    expect(screen.getByRole("button", { name: "Sign out" })).toBeTruthy();
  });

  // The sheet is the phone's whole sidebar, and it has no trigger printing the
  // person — so the rows carry that line themselves, or nothing on the surface
  // says whose account it is offering.
  it("prints who is signed in above the rows", () => {
    const { container } = render(<AccountRows />);
    const who = container.querySelector(".acctwho");
    expect(who?.querySelector("b")?.textContent).toBe("Test User");
    expect(who?.querySelector(".acctmail")?.textContent).toBe(
      "test@example.test",
    );
  });

  // The same mutation the menu's row runs, so the same guard against a second
  // POST while the first is in flight. The request is left unresolved on
  // purpose: pending is the state under test, and a stub that answered would
  // race the assertion against the mutation settling.
  it("disables the way out while the sign-out request is in flight", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => new Promise<Response>(() => {})),
    );
    render(<AccountRows />);
    const signOut = screen.getByRole("button", { name: "Sign out" });
    expect(signOut.hasAttribute("disabled")).toBe(false);

    await userEvent.click(signOut);
    expect(
      screen.getByRole("button", { name: "Sign out" }).hasAttribute("disabled"),
    ).toBe(true);
  });
});
