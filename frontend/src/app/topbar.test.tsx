// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ShieldCheck, UserRound } from "lucide-react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import type { NavCounts, NavSection } from "./nav";
import { parseHash, type Route } from "./router";
import { TopBar } from "./topbar";

// The sticky strip above every railed page: what is true of the SESSION rather
// than of the page under it. Left to right — the sidebar's collapse toggle, the
// trail that says where the reader is, the one search affordance, the system-of
// -record chip and the account menu.
//
// Three of those five are proved here, because three of them are the top bar's
// own: the toggle, the trail and the search. The chip and the account menu are
// components with suites of their own (sormodechip.test.tsx, account.test.tsx);
// what this file asserts about them is nothing, and what the SHELL asserts is
// that they are mounted (shell.test.tsx).
//
// The page's own NAME is deliberately absent from this file: it is not in the
// top bar. It stands inside the scroller above the content it names, and its
// rules are PageTitle's, in shell.test.tsx.

// A composed installation, because the vanilla registry is empty by
// construction — a unit's trail could otherwise only ever be exercised on its
// miss path. The descriptor shape is the generator's, the same one
// App.extscreen.test.tsx hands the same lookup.
vi.mock("@composition/extensions", () => ({
  extensions: [
    {
      name: "notes",
      verbs: [
        {
          operationId: "notesList",
          route: "/ext/notes/list",
          method: "POST",
          title: "List demo notes",
          version: "1.0.0",
          rbacObject: "ext_notes_note",
        },
      ],
    },
  ],
}));

afterEach(() => {
  cleanup();
  window.location.hash = "";
  vi.unstubAllGlobals();
});

const newClient = () =>
  new QueryClient({ defaultOptions: { queries: { retry: false } } });

const renderWith = (client: QueryClient, ui: ReactNode) =>
  rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );

const render = (ui: ReactNode) => renderWith(newClient(), ui);

// The bar cannot render without a way to open the palette. Cases that are not
// about search pass a handler that records nothing; the search cases pass a spy.
const ignoreSearch = () => undefined;
// The collapse control is minted only when the bar is handed a toggle, so a case
// that needs it on screen supplies one that records nothing.
const ignoreToggle = () => undefined;

// The trail landmark. Named, because a page carries more than one navigation
// landmark — the sidebar is the other — and two unnamed ones are
// indistinguishable in a screen reader's landmark list.
const trail = () => screen.getByRole("navigation", { name: "Breadcrumb" });

// One stop's words, without the separator the stop carries INSIDE it. The slash
// is an `aria-hidden` span in the same list item on purpose (a separator that is
// a list item of its own makes a three-stop trail announce as five things), so
// reading the item's text means reading the slash with it.
function stopText(item: Element): string {
  return (item.textContent ?? "")
    .replace(/\s+/g, " ")
    .replace(/^\/\s*/, "")
    .trim();
}

const stopTexts = () => within(trail()).getAllByRole("listitem").map(stopText);

// A section with a THIRD level under one of its entries, so nothing here rests
// on Settings happening to be exactly two levels deep. The child level is
// synthetic and borrows labels rather than real sibling entry ids, which would
// name a shape the settings level does not publish.
function fixtureSection(activeId?: string): NavSection {
  return {
    screen: "settings",
    titleKey: "nav.settings",
    activeId,
    groups: [
      {
        headingKey: "settings.group.you",
        items: [
          { id: "account", labelKey: "settings.tab.account", icon: UserRound },
        ],
      },
      {
        headingKey: "settings.group.org",
        items: [
          {
            id: "deep",
            labelKey: "settings.tab.privacy",
            icon: ShieldCheck,
          },
        ],
      },
    ],
  };
}

function renderTopBar(
  route: Route,
  extra: Readonly<{
    section?: NavSection;
    counts?: NavCounts;
    collapsed?: boolean;
    onToggle?: () => void;
    onOpenSearch?: () => void;
  }> = {},
) {
  return render(
    <TopBar
      route={route}
      section={extra.section}
      counts={extra.counts}
      collapsed={extra.collapsed ?? false}
      onToggle={extra.onToggle}
      onOpenSearch={extra.onOpenSearch ?? ignoreSearch}
    />,
  );
}

// AC-shell-7: ONE search affordance in the product, and it is this one. It is a
// button, not a field — it opens the palette and never accepts inline typing.
describe("Top bar search (AC-shell-7)", () => {
  it("opens the palette and never takes the query itself", async () => {
    const user = userEvent.setup();
    const onOpenSearch = vi.fn();
    const { container } = renderTopBar(
      { screen: "home" },
      { onOpenSearch, onToggle: ignoreToggle },
    );

    await user.click(
      screen.getByRole("button", { name: "Search everything…" }),
    );
    expect(onOpenSearch).toHaveBeenCalledTimes(1);
    // A field here would be a second search that answers to nothing: the
    // palette owns the query, the bar only opens it.
    expect(within(container).queryByRole("textbox")).toBeNull();
  });

  it("is a button rather than the field it is styled as", () => {
    const { container } = renderTopBar(
      { screen: "home" },
      { onToggle: ignoreToggle },
    );
    expect(container.querySelector(".topbar-search")?.tagName).toBe("BUTTON");
  });

  // The shortcut cap is a hint about how else to get here, not a second name: a
  // reader who says "Search everything" must reach it, and none of them should
  // be made to spell out ⌘K. `name` is an exact match on the computed name, so
  // a kbd that leaked into it fails this.
  it("is named for what it does, with the shortcut kept out of that name", () => {
    const { container } = renderTopBar(
      { screen: "home" },
      { onToggle: ignoreToggle },
    );
    expect(
      screen.getByRole("button", { name: "Search everything…" }),
    ).toBeTruthy();
    // One cap per key, and the GROUP is what is hidden — a per-cap attribute
    // would leave the group announcing itself as the caps' container.
    const keys = container.querySelector(".topbar-keys");
    expect(keys?.getAttribute("aria-hidden")).toBe("true");
    // Two caps, whichever platform the test host claims to be: the chord reads
    // "⌘ then K" on a Mac and "Ctrl then K" elsewhere, and the split is what
    // keeps those one source rather than two spellings.
    const caps = [...(keys?.querySelectorAll("kbd") ?? [])].map(
      (cap) => cap.textContent,
    );
    expect(caps).toHaveLength(2);
    expect(caps[1]).toBe("K");
    expect(caps[0]).toMatch(/^(⌘|Ctrl)$/);
  });
});

// The sidebar's own control, moved out of the sidebar: a panel that can collapse
// to 64px cannot hold the affordance that brings it back at a width where its
// labels are gone.
describe("Top bar sidebar toggle", () => {
  it("reports the sidebar expanded and calls the handler on click", async () => {
    const user = userEvent.setup();
    const onToggle = vi.fn();
    renderTopBar({ screen: "home" }, { onToggle });

    const toggle = screen.getByRole("button", { name: "Collapse sidebar" });
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    await user.click(toggle);
    expect(onToggle).toHaveBeenCalledTimes(1);
  });

  // The other half, and the one a hard-coded attribute breaks silently: the
  // control names the state it will MOVE to while `aria-expanded` reports the
  // state the sidebar is IN. Either alone passes against a control that never
  // changes.
  it("reports the sidebar collapsed and names the state it will move to", () => {
    renderTopBar(
      { screen: "home" },
      { collapsed: true, onToggle: ignoreToggle },
    );
    const toggle = screen.getByRole("button", { name: "Expand sidebar" });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
  });

  // A bar with no sidebar behind it mints no control for one. The toggle is
  // conditioned on the handler because the handler is the only evidence the bar
  // has that a sidebar exists at all.
  it("mints no control when it is handed no toggle", () => {
    const { container } = renderTopBar({ screen: "home" });
    expect(container.querySelector(".topbar-toggle")).toBeNull();
    expect(screen.queryByRole("button", { name: /sidebar$/ })).toBeNull();
  });
});

// Where the reader is, ending in the page itself. The last stop is the page and
// is never a link — a link to the page you are already on is a control that does
// nothing — and it is the ONE element in the bar that claims the current page.
describe("Top bar approvals", () => {
  // Silent at zero. A bell that is always on screen with nothing behind it
  // teaches a reader to stop looking at it, which is the one thing a queue that
  // blocks other people cannot afford.
  it("names the queue and shows no count when nothing is waiting", () => {
    renderTopBar({ screen: "home" });
    const bell = screen.getByRole("link", { name: "Approvals" });
    expect(bell.getAttribute("href")).toBe("#/inbox");
    expect(bell.querySelector(".topbar-bellcount")).toBeNull();
  });

  // The number is in the accessible NAME, and the chip that draws it is
  // aria-hidden: read out, the pair would say the count twice, the second time
  // without the word for what is counted.
  it("carries the waiting count in its name, with the chip decorative", () => {
    renderTopBar({ screen: "home" }, { counts: { inbox: 7, tasks: 9 } });
    // The inbox count specifically — tasks sits in the same record and carries a
    // different number, so a bell reading any count would show 9.
    const bell = screen.getByRole("link", { name: "Approvals — 7 waiting" });
    const chip = bell.querySelector(".topbar-bellcount");
    expect(chip?.textContent).toBe("7");
    expect(chip?.getAttribute("aria-hidden")).toBe("true");
  });
});

describe("Top bar trail", () => {
  it("names the page once on a list route, with nothing to lead back to", () => {
    renderTopBar({ screen: "deals" }, { onToggle: ignoreToggle });
    expect(stopTexts()).toEqual(["Pipeline"]);
    // A one-stop trail is the page and nothing else: no link, and no separator
    // leading a stop that is not there.
    expect(within(trail()).queryByRole("link")).toBeNull();
    expect(trail().querySelectorAll('[aria-current="page"]')).toHaveLength(1);
  });

  it("leads a record back to the list it was opened from", () => {
    const client = newClient();
    client.setQueryData(["person", "ref", "p-anna"], "Anna Weber");
    renderWith(
      client,
      <TopBar
        route={{ screen: "contacts", id: "p-anna" }}
        collapsed={false}
        onToggle={ignoreToggle}
        onOpenSearch={ignoreSearch}
      />,
    );

    expect(stopTexts()).toEqual(["Contacts", "Anna Weber"]);
    const back = within(trail()).getByRole("link", { name: "Contacts" });
    expect(back.getAttribute("href")).toBe("#/contacts");
    // The record itself is the page: not a link, and the one current claim.
    const stops = within(trail()).getAllByRole("listitem");
    const last = stops[stops.length - 1];
    expect(within(last).queryByRole("link")).toBeNull();
    expect(last.querySelector('[aria-current="page"]')?.textContent).toContain(
      "Anna Weber",
    );
    expect(trail().querySelectorAll('[aria-current="page"]')).toHaveLength(1);
  });

  // Loading, or a record this principal cannot read: the id is not a name, but
  // it is true, and it is what the reader can quote. A blank stop is not.
  it("falls back to the record id when the name cannot be resolved", () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(null, { status: 404 })),
    );
    renderTopBar(
      { screen: "contacts", id: "p-anna" },
      { onToggle: ignoreToggle },
    );
    expect(stopTexts()).toEqual(["Contacts", "p-anna"]);
  });

  it("leads a section entry back to the section it belongs to", () => {
    renderTopBar(
      { screen: "settings", id: "account" },
      { section: fixtureSection("account"), onToggle: ignoreToggle },
    );
    expect(stopTexts()).toEqual(["Settings", "Account"]);
    expect(
      within(trail())
        .getByRole("link", { name: "Settings" })
        .getAttribute("href"),
    ).toBe("#/settings");
    expect(within(trail()).queryByRole("link", { name: "Account" })).toBeNull();
  });

  // An id segment that names no record is the SCREEN's own state, not a record:
  // `#/settings/deep` is still the Settings page. Treating the segment as a
  // record put the raw slug in the trail as though it were somebody's name — and
  // with no section resolved there is no entry to name either, so the trail is
  // the screen alone.
  it("keeps a screen's own id segment out of the trail", () => {
    renderTopBar(
      { screen: "settings", id: "deep" },
      { onToggle: ignoreToggle },
    );
    expect(stopTexts()).toEqual(["Settings"]);
    expect(trail().textContent).not.toContain("deep");
  });

  // A composed unit is offered from Settings and has no index of its own, so
  // Settings is what its trail leads back to. The unit is named by the
  // descriptor, which is the only thing that knows what an installation composed.
  it("leads a composed unit back to Settings", () => {
    renderTopBar({ screen: "ext", id: "notes" }, { onToggle: ignoreToggle });
    expect(stopTexts()).toEqual(["Settings", "notes"]);
    expect(
      within(trail())
        .getByRole("link", { name: "Settings" })
        .getAttribute("href"),
    ).toBe("#/settings");
  });

  // AC-shell-1k: every route resolves to real copy. A hash nobody answers used
  // to put whatever the reader typed on screen, which reads as a page by that
  // name existing.
  it("names an unknown route rather than echoing the hash", () => {
    renderTopBar(parseHash("#/nope"), { onToggle: ignoreToggle });
    expect(stopTexts()).toEqual(["Not found"]);
    expect(trail().textContent).not.toContain("nope");
  });

  // The deliberate half of the unit case: a unit this installation did NOT
  // compose is genuinely an unknown page, and the trail says so rather than
  // printing the segment the reader typed.
  it("names an uncomposed unit route as the unknown page it is", () => {
    renderTopBar({ screen: "ext", id: "ghost" }, { onToggle: ignoreToggle });
    expect(stopTexts()).toEqual(["Not found"]);
    expect(trail().textContent).not.toContain("ghost");
  });
});
