/** @vitest-environment jsdom */
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Database, ScrollText, UserRound } from "lucide-react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import { Button } from "../design-system/atoms";
import { LocaleProvider } from "../i18n";
import type { NavSection } from "./nav";
import {
  clearPendingAuthorize,
  readPendingAuthorize,
  stashPendingAuthorize,
} from "./pendingauthorize";
import { navigate } from "./router";
import { PageHead, Shell, WorkspaceRail } from "./shell";

function memoryStorage(): Storage {
  const map = new Map<string, string>();
  return {
    getItem: (key) => (map.has(key) ? (map.get(key) as string) : null),
    setItem: (key, value) => {
      map.set(key, String(value));
    },
    removeItem: (key) => {
      map.delete(key);
    },
    clear: () => map.clear(),
    key: (index) => Array.from(map.keys())[index] ?? null,
    get length() {
      return map.size;
    },
  };
}

// B-EP09.4 acceptance, for what the SHELL itself owns: the canonical 10-item
// nav in order (AC-shell-1b — A72 promoted Automations to primary nav), at most
// one active item tracking the route (AC-shell-2), badges only on the attention
// screens and only from live counts (AC-shell-1e), 44x44 collapsed targets with
// a dismissible tooltip (AC-shell-1c/1d), the sidebar's search row (AC-shell-7),
// the page head that names the screen, and the rail-less exceptions.
//
// The account block and the agent dock are components of their own now, and
// their behaviour is proved where they live (account.test.tsx,
// agentdock.test.tsx). What is asserted here is that the shell MOUNTS them in
// the places it promises, and feeds them the counts it already has: the account
// block at the sidebar foot, the agent beside the page title.

// Only what a level hides needs the shell's real stylesheet in the document
// (see mountShellStyles); it outlives cleanup(), so it is taken down here.
let shellStyles: HTMLStyleElement | undefined;

afterEach(() => {
  cleanup();
  shellStyles?.remove();
  shellStyles = undefined;
  window.location.hash = "";
  vi.unstubAllGlobals();
  clearPendingAuthorize();
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

// The rail cannot render without a way to open the palette. Cases that are not
// about search pass a handler that records nothing; the search cases pass a spy.
const ignoreSearch = () => undefined;
// The collapse control is minted only when the shell hands the rail a toggle, so
// a case that needs it on screen supplies one that records nothing.
const ignoreToggle = () => undefined;

// Phone width, for the chrome that has to KNOW it rather than merely be laid out
// by it (app/viewport.ts). jsdom's own window answers every media query with
// false, so the wide arrangement is what every other case here renders — which is
// the honest default and exactly what the desktop assertions rely on.
//
// Only the QUERY the app asks for matches: a stub that answered true to
// everything would also tell the theme the reader prefers a dark one.
function stubPhoneViewport(): void {
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: query === "(max-width: 700px)",
    media: query,
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
  }));
}

// The route id never changes with a label: `deals` presents as Pipeline and
// `inbox` as Approvals, which names a governance surface rather than a mailbox.
const CANONICAL_ORDER = [
  "Home",
  "Contacts",
  "Companies",
  "Leads",
  "Pipeline",
  "Tasks",
  "Approvals",
  "Reports",
  "Automations",
  "Ask Margince",
];

describe("WorkspaceRail (AC-shell-1/2)", () => {
  it("renders the canonical 10 items in order, logomark → home", () => {
    render(
      <WorkspaceRail route={{ screen: "deals" }} onOpenSearch={ignoreSearch} />,
    );
    const brand = within(screen.getByRole("navigation")).getByRole("link", {
      name: "Margince",
    });
    expect(brand.getAttribute("href")).toBe("#/home");
    // The DESTINATIONS are the level's own rows, so they are counted there: the
    // brand above it and the foot's Settings door are neither of them, and each
    // is asserted where it belongs.
    expect(levelLabels()).toEqual(CANONICAL_ORDER);
    // The mark leads them, which is what "logomark → home" means.
    const home = screen.getByRole("link", { name: "Home" });
    expect(
      brand.compareDocumentPosition(home) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeGreaterThan(0);
  });

  it("groups the items under Records / Work / Intelligence when expanded", () => {
    render(
      <WorkspaceRail route={{ screen: "home" }} onOpenSearch={ignoreSearch} />,
    );
    const headings = screen
      .getAllByRole("heading", { level: 2 })
      .map((heading) => heading.textContent);
    expect(headings).toEqual(["Records", "Work", "Intelligence"]);
  });

  it("marks exactly one item active, matching the route", () => {
    render(
      <WorkspaceRail route={{ screen: "deals" }} onOpenSearch={ignoreSearch} />,
    );
    const active = screen
      .getAllByRole("link")
      .filter((link) => link.getAttribute("aria-current") === "page");
    expect(active).toHaveLength(1);
    expect(active[0].getAttribute("aria-label")).toBe("Pipeline");
  });

  it("marks nothing active on a non-rail screen", () => {
    render(
      <WorkspaceRail
        route={{ screen: "settings" }}
        onOpenSearch={ignoreSearch}
      />,
    );
    const active = screen
      .getAllByRole("link")
      .filter((link) => link.getAttribute("aria-current") === "page");
    expect(active).toHaveLength(0);
  });

  it("renders count badges only for provided positive counts", () => {
    const { container } = render(
      <WorkspaceRail
        route={{ screen: "home" }}
        counts={{ tasks: 4, inbox: 0 }}
        onOpenSearch={ignoreSearch}
      />,
    );
    const badges = container.querySelectorAll(".count");
    expect(badges).toHaveLength(1);
    expect(badges[0].textContent).toBe("4");
  });

  // AC-shell-1e: a badge counts only what wants attention. Pipeline and Leads
  // carry no badge even when a count is supplied — this bites if BADGE_SCREENS
  // is dropped and every screen starts rendering ambient totals again.
  it("ignores counts for screens that are not attention surfaces", () => {
    const { container } = render(
      <WorkspaceRail
        route={{ screen: "home" }}
        counts={{ deals: 13, leads: 7, contacts: 248 }}
        onOpenSearch={ignoreSearch}
      />,
    );
    expect(container.querySelectorAll(".count")).toHaveLength(0);
  });

  // AC-shell-1c/1d: collapsed items are icon-only, so the label must reach a
  // screen reader via aria-label in BOTH states, and the visible tooltip must
  // appear on keyboard focus (not hover alone) and be dismissible with Escape.
  it("keeps the accessible name when collapsed and shows a dismissible tooltip on focus", async () => {
    render(
      <WorkspaceRail
        route={{ screen: "home" }}
        collapsed
        onOpenSearch={ignoreSearch}
      />,
    );
    const pipeline = screen.getByRole("link", { name: "Pipeline" });
    expect(screen.queryByRole("tooltip")).toBeNull();

    pipeline.focus();
    const tip = await screen.findByRole("tooltip");
    expect(tip.textContent).toBe("Pipeline");

    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("tooltip")).toBeNull();
    // Escape dismisses the tooltip without moving focus (WCAG 1.4.13).
    expect(document.activeElement).toBe(pipeline);
  });

  // WCAG 1.4.13 also requires the tooltip be HOVERABLE: reaching for it must not
  // dismiss it. The tooltip is a descendant of the row it belongs to, so moving
  // the pointer onto it never fires the row's mouseleave. As a sibling it would
  // vanish under the cursor, and no assertion on its text would notice.
  it("nests the collapsed tooltip inside its own row so hovering it cannot dismiss it", async () => {
    render(
      <WorkspaceRail
        route={{ screen: "home" }}
        collapsed
        onOpenSearch={ignoreSearch}
      />,
    );
    const pipeline = screen.getByRole("link", { name: "Pipeline" });

    await userEvent.hover(pipeline);
    const tip = await screen.findByRole("tooltip");
    expect(pipeline.contains(tip)).toBe(true);

    await userEvent.hover(tip);
    expect(screen.queryByRole("tooltip")).not.toBeNull();
  });

  // On a phone the four bar tabs are the only rows rendered, so a route living
  // in the More sheet has nothing to carry the current-destination state. More
  // carries it instead, or the bar shows no active tab at all.
  it("marks More as the active tab for a destination the phone bar hides", () => {
    const sheeted = render(
      <WorkspaceRail
        route={{ screen: "reports" }}
        onOpenSearch={ignoreSearch}
      />,
    );
    const more = sheeted.container.querySelector(".railmore.active");
    expect(more).not.toBeNull();
    // Announced, not merely tinted: the hidden route's own link is out of the
    // accessibility tree at phone width, so More has to report the current page.
    expect(more?.getAttribute("aria-current")).toBe("page");
    cleanup();

    const onBar = render(
      <WorkspaceRail
        route={{ screen: "contacts" }}
        onOpenSearch={ignoreSearch}
      />,
    );
    const inactive = onBar.container.querySelector(".railmore");
    expect(inactive?.className).not.toContain("active");
    expect(inactive?.getAttribute("aria-current")).toBeNull();
  });

  // Open, the sheet renders the real row for that route, which carries
  // aria-current itself. Two elements claiming the current page is worse than
  // the visual-only state this replaced.
  it("hands the current-page claim back to the real row once the sheet is open", async () => {
    // The sheet exists only at phone width — the control that opens it is not
    // rendered above the breakpoint, and the rail closes any sheet it finds
    // itself holding there.
    stubPhoneViewport();
    const { container } = render(
      <WorkspaceRail
        route={{ screen: "reports" }}
        onOpenSearch={ignoreSearch}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "More" }));
    expect(
      container.querySelector(".railmore")?.getAttribute("aria-current"),
    ).toBeNull();
    expect(container.querySelectorAll('[aria-current="page"]')).toHaveLength(1);
  });

  // The sheet is the phone's whole sidebar, and a popover anchored to the rail's
  // foot has nowhere to open at the bottom of the viewport: the account rows the
  // menu hides were unreachable there. Open, the foot IS the rows.
  it("puts the account rows in the sheet instead of the menu that cannot open", async () => {
    stubPhoneViewport();
    const { container } = render(
      <WorkspaceRail route={{ screen: "home" }} onOpenSearch={ignoreSearch} />,
    );
    expect(screen.getByRole("button", { name: /Account$/ })).toBeTruthy();

    await userEvent.click(screen.getByRole("button", { name: "More" }));
    expect(screen.queryByRole("button", { name: /Account$/ })).toBeNull();
    // Scoped to the rows themselves: the foot's Settings door leads to the same
    // address, and this claim is about what the SHEET offers when the menu that
    // normally hides these rows cannot open.
    const rows = container.querySelector<HTMLElement>(".accountrows");
    if (!rows) {
      throw new Error("the open sheet rendered no account rows");
    }
    // Reachable without opening anything, in the order the menu offers them.
    expect(
      within(rows).getByRole("link", { name: "Account" }).getAttribute("href"),
    ).toBe("#/settings/account");
    expect(
      within(rows).getByRole("link", { name: "Settings" }).getAttribute("href"),
    ).toBe("#/settings");
    expect(within(rows).getByRole("button", { name: "Sign out" })).toBeTruthy();
  });

  // The sheet takes focus when it opens, so dismissing it from INSIDE (Escape, a
  // click outside) leaves focus on a row that is about to be gone. It goes back
  // to the control that opened it rather than onto <body> — and only then: a rail
  // that merely mounts with a row focused must keep that focus where it is.
  it("hands focus back to More when the sheet is dismissed from inside it", async () => {
    stubPhoneViewport();
    render(
      <WorkspaceRail route={{ screen: "home" }} onOpenSearch={ignoreSearch} />,
    );
    await userEvent.click(screen.getByRole("button", { name: "More" }));
    expect(document.activeElement).toBe(
      screen.getByRole("link", { name: "Home" }),
    );

    await userEvent.keyboard("{Escape}");
    expect(document.activeElement).toBe(
      screen.getByRole("button", { name: "More" }),
    );
  });

  it("renders the collapse control with the state it will move to", () => {
    const onToggle = vi.fn();
    render(
      <WorkspaceRail
        route={{ screen: "home" }}
        onToggle={onToggle}
        onOpenSearch={ignoreSearch}
      />,
    );
    const toggle = screen.getByRole("button", { name: "Collapse sidebar" });
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    toggle.click();
    expect(onToggle).toHaveBeenCalled();
  });

  // Who is signed in belongs to the session, not to a screen, so the sidebar
  // carries it — at the foot, below the destinations. The menu's own behaviour
  // is proved in account.test.tsx; what the rail promises is that the block is
  // there and is the ONE account affordance in the chrome.
  it("mounts the account block at the sidebar foot", () => {
    const { container } = render(
      <WorkspaceRail route={{ screen: "home" }} onOpenSearch={ignoreSearch} />,
    );
    const account = screen.getByRole("button", { name: /Account$/ });
    expect(container.querySelector(".railfoot")?.contains(account)).toBe(true);
    expect(screen.getAllByRole("button", { name: /Account$/ })).toHaveLength(1);
  });

  // The way INTO Settings. Without a row here the level could only be entered
  // from inside the account menu's popover, so a reader who walked out of the
  // section had no way back in that did not start with opening a menu.
  it("offers Settings at the foot, above the account block", () => {
    const { container } = render(
      <WorkspaceRail route={{ screen: "home" }} onOpenSearch={ignoreSearch} />,
    );
    const door = screen.getByRole("link", { name: "Settings" });
    expect(door.getAttribute("href")).toBe("#/settings");
    const foot = container.querySelector(".railfoot");
    expect(foot?.contains(door)).toBe(true);
    // Above the person it belongs to, and NOT one of the destinations: the level
    // above still lists exactly the canonical ten.
    const account = screen.getByRole("button", { name: /Account$/ });
    expect(
      door.compareDocumentPosition(account) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeGreaterThan(0);
    expect(levelLabels()).toEqual(CANONICAL_ORDER);
  });

  // The door is a way in, not a place: standing inside the section the level is
  // on screen carrying the reader's own entry, and the document must offer
  // exactly one current page. A door that claimed it would be the second.
  it("never lets the Settings door claim the current page", () => {
    render(
      <WorkspaceRail
        route={{ screen: "settings", id: "account" }}
        section={fixtureSection("account")}
        onOpenSearch={ignoreSearch}
      />,
    );
    const door = screen.getByRole("link", { name: "Settings" });
    expect(door.getAttribute("aria-current")).toBeNull();
    expect(door.className).not.toContain("active");
    const current = document.querySelectorAll('[aria-current="page"]');
    expect(current).toHaveLength(1);
    expect(current[0].getAttribute("aria-label")).toBe("Account");
  });

  // Collapsed the door is a glyph like every other row, so it owes the same
  // tooltip contract — and its own tooltip key, or focusing it would light up
  // whichever destination shared the key. There is never more than one open.
  it("shows the collapsed door a dismissible tooltip of its own", async () => {
    render(
      <WorkspaceRail
        route={{ screen: "home" }}
        collapsed
        onOpenSearch={ignoreSearch}
      />,
    );
    const door = screen.getByRole("link", { name: "Settings" });
    const home = screen.getByRole("link", { name: "Home" });
    expect(screen.queryByRole("tooltip")).toBeNull();

    door.focus();
    await waitFor(() =>
      expect(screen.getByRole("tooltip").textContent).toBe("Settings"),
    );
    expect(door.contains(screen.getByRole("tooltip"))).toBe(true);

    home.focus();
    await waitFor(() =>
      expect(screen.getByRole("tooltip").textContent).toBe("Home"),
    );
    expect(screen.getAllByRole("tooltip")).toHaveLength(1);

    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("tooltip")).toBeNull();
    expect(document.activeElement).toBe(home);
  });
});

// The sidebar's SECOND level (and its third). A section route replaces the ten
// destinations with the section's entries rather than hanging them off it: one
// level at a time, with the way back up in the panel.
//
// The fixture below is a section with a THIRD level under one of its entries.
// Settings — the only real section the app ships — is two levels deep, so
// nothing in production would prove the renderer takes its depth from the data
// rather than from a hard-coded pair of levels.
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
            id: "audit",
            labelKey: "settings.tab.audit",
            icon: ScrollText,
            children: [
              { id: "data", labelKey: "settings.tab.data", icon: Database },
            ],
          },
        ],
      },
    ],
  };
}

// The rows of whatever level the panel is showing — the ten destinations, or a
// section's entries. Scoped to `.navlevel` rather than to the whole nav, because
// the brand above the level and the Settings door in the foot are not places the
// level leads: a claim about the level's inventory must not move when either of
// them does.
const levelLabels = () => {
  const level = document.querySelector<HTMLElement>(".navlevel");
  if (!level) {
    throw new Error("the rail rendered no navigation level at all");
  }
  return within(level)
    .getAllByRole("link")
    .map((link) => link.getAttribute("aria-label"));
};

// What a level does to the rail's head is CSS, and nothing applies a stylesheet
// in this environment unless it is in the document. It is the SHELL's own
// stylesheet that goes in — a rule copied into the test would prove only that
// the copy says what the test says. The phone block does not apply: these
// queries are widths, and the window here is wider than 700px.
const SHELL_CSS = readFileSync(
  join(dirname(fileURLToPath(import.meta.url)), "shell.css"),
  "utf8",
);

function mountShellStyles(): HTMLStyleElement {
  const style = document.createElement("style");
  style.textContent = SHELL_CSS;
  document.head.append(style);
  return style;
}

// A row that is not in the rail at all is not the same thing as a hidden one,
// and must not read as one: the head keeps its elements and the level takes
// their space.
function railDisplay(container: HTMLElement, selector: string): string {
  const node = container.querySelector(selector);
  if (!node) {
    throw new Error(`${selector} is missing from the rail entirely`);
  }
  return getComputedStyle(node).display;
}

describe("Rail levels (a section's entries as the second level)", () => {
  it("replaces the destinations with the section's entries, one current", () => {
    render(
      <WorkspaceRail
        route={{ screen: "settings", id: "account" }}
        section={fixtureSection("account")}
        onOpenSearch={ignoreSearch}
      />,
    );
    // The destinations are GONE, not pushed below a second list: 64px cannot
    // carry two levels and 252px carrying both is a list of twenty places to go.
    expect(screen.queryByRole("link", { name: "Pipeline" })).toBeNull();
    expect(levelLabels()).toEqual(["Account", "Audit log"]);
    expect(
      screen.getByRole("link", { name: "Audit log" }).getAttribute("href"),
    ).toBe("#/settings/audit");
    // Exactly one row claims the current page, and it is the entry the SECTION
    // resolved — the screen owns that answer, fallbacks included.
    const current = document.querySelectorAll('[aria-current="page"]');
    expect(current).toHaveLength(1);
    expect(current[0].getAttribute("aria-label")).toBe("Account");
  });

  // The level names itself at heading level 2, so its group labels move down to
  // 3 — the outline reads Settings → You / Organization, and the rail's
  // own destinations keep level 2 for their groups on every other route.
  it("names the level at heading level 2 and its groups at 3", () => {
    render(
      <WorkspaceRail
        route={{ screen: "settings", id: "account" }}
        section={fixtureSection("account")}
        onOpenSearch={ignoreSearch}
      />,
    );
    expect(
      screen.getAllByRole("heading", { level: 2 }).map((h) => h.textContent),
    ).toEqual(["Settings"]);
    expect(
      screen.getAllByRole("heading", { level: 3 }).map((h) => h.textContent),
    ).toEqual(["You", "Organization"]);
  });

  // A section belongs to ONE screen. Without this the fixture's entries would
  // leak onto every route, which is exactly what the canonical-ten assertions
  // above would then be lying about.
  it("ignores a section that belongs to another screen", () => {
    render(
      <WorkspaceRail
        route={{ screen: "home" }}
        section={fixtureSection("audit")}
        onOpenSearch={ignoreSearch}
      />,
    );
    expect(levelLabels()).toEqual(CANONICAL_ORDER);
  });

  // The control READS one word at every depth — the reader knows what they
  // walked down from — while its accessible name still says where it leads.
  // WCAG 2.5.3 holds because "Back" is contained in "Back to Destinations".
  it("reads Back and is named for the level it leads to", () => {
    render(
      <WorkspaceRail
        route={{ screen: "settings", id: "account" }}
        section={fixtureSection("account")}
        onOpenSearch={ignoreSearch}
      />,
    );
    const back = screen.getByRole("button", { name: "Back to Destinations" });
    expect(back.querySelector(".navlabel")?.textContent).toBe("Back");
  });

  // Walking back changes the ADDRESS. Climbing in the panel's own state left the
  // reader on `#/settings/<tab>` with the destinations on screen, and the only
  // way back into the section was the address they were already standing on — so
  // nothing re-rendered and the level could not be reached again.
  //
  // Through the whole SHELL, because that is the only thing that can prove it:
  // the rail on a section route is a different component (SettingsRail), mounted
  // on the way into the level and gone again on the way out, so where the reader
  // came from is remembered above it. A rail on its own always answers "home",
  // which is the case below and would hide this one.
  it("walks out of the section to the route the reader came from", async () => {
    window.location.hash = "#/reports";
    render(<Shell onOpenSearch={ignoreSearch}>{null}</Shell>);
    expect(screen.getByRole("link", { name: "Reports" })).toBeTruthy();

    navigate({ screen: "settings", id: "account" });
    await userEvent.click(
      await screen.findByRole("button", { name: "Back to Destinations" }),
    );
    expect(window.location.hash).toBe("#/reports");
    // The panel is derived from the address, so the destinations arrive with it —
    // and they take the focus the level's own rows gave up rather than leaving
    // the document on <body>.
    await waitFor(() => expect(levelLabels()).toEqual(CANONICAL_ORDER));
    expect(document.activeElement).toBe(
      screen.getByRole("link", { name: "Home" }),
    );
  });

  // A reader who typed the address, or followed a link into it, walked down from
  // nowhere — there is no origin to return them to, and home is the one place
  // the app can honestly send them.
  it("falls back home when the reader deep-linked into the section", async () => {
    window.location.hash = "#/settings/account";
    render(<Shell onOpenSearch={ignoreSearch}>{null}</Shell>);
    await userEvent.click(
      await screen.findByRole("button", { name: "Back to Destinations" }),
    );
    expect(window.location.hash).toBe("#/home");
  });

  // The level's rows take the brand's WORDS — the mark alone stands for the
  // product here — and nothing else: the search row STAYS, because ⌘K is
  // invisible to anyone who does not already know it and a level that hid the row
  // had no search affordance in it at all.
  it("hides the brand words but keeps the search row while a level is shown", () => {
    shellStyles = mountShellStyles();
    const { container } = render(
      <WorkspaceRail
        route={{ screen: "settings", id: "account" }}
        section={fixtureSection("account")}
        onToggle={ignoreToggle}
        onOpenSearch={ignoreSearch}
      />,
    );
    expect(railDisplay(container, ".ws-name")).toBe("none");
    expect(railDisplay(container, ".railsearchwrap")).not.toBe("none");
    expect(screen.getByRole("button", { name: "Search" })).toBeTruthy();
    // The mark stays, and with it both jobs it holds — the link home and the
    // sidebar's collapse affordance. A head reduced to a dead box would take
    // them with it.
    expect(railDisplay(container, ".ws-chip")).not.toBe("none");
    expect(screen.getByRole("link", { name: "Margince" })).toBeTruthy();
    expect(
      screen.getByRole("button", { name: "Collapse sidebar" }),
    ).toBeTruthy();
  });

  // The other half of the same rule: outside a level the head is the head. The
  // brand assertion alone is satisfied by a rail that hides the words everywhere,
  // or by one that hides them nowhere.
  it("keeps the brand words and the search row on a route with no level", () => {
    shellStyles = mountShellStyles();
    const { container } = render(
      <WorkspaceRail route={{ screen: "home" }} onOpenSearch={ignoreSearch} />,
    );
    expect(railDisplay(container, ".ws-name")).not.toBe("none");
    expect(railDisplay(container, ".railsearchwrap")).not.toBe("none");
  });

  // An entry that HAS children opens them: standing on it, the panel shows the
  // level it leads to rather than the list it came from. Nothing carries the
  // current page there until a child is addressed — the same as any list a
  // reader has just been handed.
  it("opens an entry's children as soon as the route stands on that entry", () => {
    render(
      <WorkspaceRail
        route={{ screen: "settings", id: "audit" }}
        section={fixtureSection("audit")}
        onOpenSearch={ignoreSearch}
      />,
    );
    expect(levelLabels()).toEqual(["Data model"]);
    expect(document.querySelectorAll('[aria-current="page"]')).toHaveLength(0);
    expect(
      screen.getAllByRole("heading", { level: 2 }).map((h) => h.textContent),
    ).toEqual(["Audit log"]);
  });

  it("renders a third level from the data, addressed under the entry that opens it", () => {
    render(
      <WorkspaceRail
        route={{ screen: "settings", id: "audit", id2: "data" }}
        section={fixtureSection("audit")}
        onOpenSearch={ignoreSearch}
      />,
    );
    // The child level: only the entry's children, addressed under it.
    expect(levelLabels()).toEqual(["Data model"]);
    expect(
      screen.getByRole("link", { name: "Data model" }).getAttribute("href"),
    ).toBe("#/settings/audit/data");
    expect(
      screen.getAllByRole("heading", { level: 2 }).map((h) => h.textContent),
    ).toEqual(["Audit log"]);
  });

  // One step at a time, and the step is an ADDRESS: below the section's own
  // level the way back leads to the entry the reader drilled through, whose own
  // address is what names the level above. It is also what the control is named
  // for — the section's list, not the entry whose children are on screen.
  it("lands on the parent entry's own address from a level below the section", async () => {
    render(
      <WorkspaceRail
        route={{ screen: "settings", id: "audit", id2: "data" }}
        section={fixtureSection("audit")}
        onOpenSearch={ignoreSearch}
      />,
    );
    await userEvent.click(
      screen.getByRole("button", { name: "Back to Settings" }),
    );
    expect(window.location.hash).toBe("#/settings/audit");
  });

  // AC-shell-1d holds at every depth, and there is ONE tooltip in the sidebar:
  // moving between two entries of a level must not leave the first one open.
  it("shows one tooltip at a time on the collapsed level", async () => {
    render(
      <WorkspaceRail
        route={{ screen: "settings", id: "account" }}
        section={fixtureSection("account")}
        collapsed
        onOpenSearch={ignoreSearch}
      />,
    );
    const account = screen.getByRole("link", { name: "Account" });
    const audit = screen.getByRole("link", { name: "Audit log" });
    expect(screen.queryByRole("tooltip")).toBeNull();

    account.focus();
    // waitFor on the TEXT, not findAllByRole on the role: a tooltip left over
    // from the previous row satisfies the role query on its first poll, so a
    // "one tooltip" assertion could pass while showing the wrong one.
    await waitFor(() =>
      expect(screen.getByRole("tooltip").textContent).toBe("Account"),
    );

    audit.focus();
    await waitFor(() =>
      expect(screen.getByRole("tooltip").textContent).toBe("Audit log"),
    );
    expect(screen.getAllByRole("tooltip")).toHaveLength(1);

    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("tooltip")).toBeNull();
    // Escape dismisses the tooltip without moving focus (WCAG 1.4.13).
    expect(document.activeElement).toBe(audit);
  });

  // At phone width the panel is a bar of four destinations, and it keeps them on
  // a section route: handing the bar to a section lost every destination, made
  // switching entries More → scroll → tap, and left a bar holding two controls.
  // The section is reached from the page head there instead.
  it("keeps the destinations on the phone bar, even on a section route", async () => {
    stubPhoneViewport();
    render(
      <WorkspaceRail
        route={{ screen: "settings", id: "account" }}
        section={fixtureSection("account")}
        onOpenSearch={ignoreSearch}
      />,
    );
    expect(levelLabels()).toEqual(CANONICAL_ORDER);
    // No level at all: no entries, no way back up, and no `leveled` arrangement
    // for the bar to be rearranged by.
    expect(screen.queryByRole("link", { name: "Audit log" })).toBeNull();
    expect(screen.queryByRole("button", { name: /^Back/ })).toBeNull();
    expect(screen.getByRole("navigation").className).not.toContain("leveled");

    // And the sheet is what it was before levels existed: the destinations plus
    // the account rows.
    await userEvent.click(screen.getByRole("button", { name: "More" }));
    expect(levelLabels()).toEqual(CANONICAL_ORDER);
    expect(screen.getByRole("button", { name: "Sign out" })).toBeTruthy();
  });

  // The other half: above the breakpoint the level is exactly what it was. Either
  // assertion alone is satisfied by a rail that ignores every section, or by one
  // that ignores the width.
  it("still walks into the section above the phone breakpoint", () => {
    render(
      <WorkspaceRail
        route={{ screen: "settings", id: "account" }}
        section={fixtureSection("account")}
        onOpenSearch={ignoreSearch}
      />,
    );
    expect(levelLabels()).toEqual(["Account", "Audit log"]);
    expect(screen.getByRole("navigation").className).toContain("leveled");
  });
});

// AC-shell-7: ONE search affordance, and it is the sidebar's first row. It is a
// button, not a field — it opens the palette and never accepts inline typing.
describe("Rail search (AC-shell-7)", () => {
  it("opens the palette and never takes the query itself", async () => {
    const onOpenSearch = vi.fn();
    const { container } = render(
      <WorkspaceRail route={{ screen: "home" }} onOpenSearch={onOpenSearch} />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Search" }));
    expect(onOpenSearch).toHaveBeenCalledTimes(1);
    // A field here would be a second search that answers to nothing: the
    // palette owns the query, the row only opens it.
    expect(within(container).queryByRole("textbox")).toBeNull();
  });

  it("leads the destinations rather than joining them", () => {
    const { container } = render(
      <WorkspaceRail route={{ screen: "home" }} onOpenSearch={ignoreSearch} />,
    );
    const search = screen.getByRole("button", { name: "Search" });
    const home = screen.getByRole("link", { name: "Home" });
    // First in the reading order, and not an eleventh place to go: the level
    // under it still lists exactly the canonical ten.
    expect(
      search.compareDocumentPosition(home) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeGreaterThan(0);
    expect(levelLabels()).toHaveLength(CANONICAL_ORDER.length);
    expect(container.querySelector(".railsearch")?.tagName).toBe("BUTTON");
  });

  // The shortcut cap is a hint about how else to get here, not a second name:
  // a reader who says "Search" must reach it, and none of them should be made
  // to spell out ⌘K. `name: "Search"` is an exact match on the computed name,
  // so a kbd that leaked into it would fail this.
  it("is named for what it does, with the shortcut kept out of that name", () => {
    const { container } = render(
      <WorkspaceRail route={{ screen: "home" }} onOpenSearch={ignoreSearch} />,
    );
    expect(screen.getByRole("button", { name: "Search" })).toBeTruthy();
    expect(
      container.querySelector(".railsearch kbd")?.getAttribute("aria-hidden"),
    ).toBe("true");
  });

  // Collapsed the row is a glyph, so it needs the same tooltip contract the
  // destinations have — and its own tooltip key, or focusing search would light
  // up whichever destination shared the key.
  it("keeps its name and shows a dismissible tooltip on the collapsed rail", async () => {
    render(
      <WorkspaceRail
        route={{ screen: "home" }}
        collapsed
        onOpenSearch={ignoreSearch}
      />,
    );
    const search = screen.getByRole("button", { name: "Search" });
    expect(screen.queryByRole("tooltip")).toBeNull();

    search.focus();
    const tips = await screen.findAllByRole("tooltip");
    expect(tips).toHaveLength(1);
    expect(tips[0].textContent).toBe("Search");
    expect(search.contains(tips[0])).toBe(true);

    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("tooltip")).toBeNull();
    expect(document.activeElement).toBe(search);
  });
});

// The page head replaced the top bar: the screen's own name as a real heading,
// and beside it only what is true of the whole product.
describe("PageHead", () => {
  it("names the screen in a level-1 heading", () => {
    render(<PageHead route={{ screen: "deals" }} />);
    expect(
      screen.getByRole("heading", { level: 1, name: "Pipeline" }),
    ).toBeTruthy();
    // No record, so nothing to go back to — the heading is the whole title.
    expect(screen.queryByRole("link", { name: "Pipeline" })).toBeNull();
  });

  // AC-shell-1k: every authenticated route resolves to real copy. This bites on
  // a new off-rail route landing in the router without a title key — the old
  // fallback rendered the raw screen slug.
  it("resolves a title for off-rail routes instead of the raw slug", () => {
    render(<PageHead route={{ screen: "dedupe" }} />);
    expect(
      screen.getByRole("heading", { level: 1, name: "Duplicates" }),
    ).toBeTruthy();
  });

  // The same rule where there is no route at all. A hash nobody answers used to
  // put whatever the reader typed in the page's heading, which reads as a page
  // by that name existing.
  it("names an unknown route rather than echoing the hash", () => {
    render(<PageHead route={{ screen: "nope" }} />);
    expect(
      screen.getByRole("heading", { level: 1, name: "Not found" }),
    ).toBeTruthy();
    expect(document.body.textContent).not.toContain("nope");
  });

  // An extension route the installation does NOT answer keeps the unknown-page
  // heading, and that is the deliberate half of the head's yield to a unit: the
  // yield is conditioned on the descriptor resolving, so a hand-typed
  // `#/ext/<anything>` is an unknown page here exactly as it is under the head,
  // where the screen says so in words. This is the vanilla registry, where EVERY
  // unit route is unknown — the composed half is pinned in App.extscreen.test.tsx.
  it("names an extension route this installation did not compose", () => {
    render(<PageHead route={{ screen: "ext", id: "notes" }} />);
    expect(
      screen.getByRole("heading", { level: 1, name: "Not found" }),
    ).toBeTruthy();
    expect(document.body.textContent).not.toContain("notes");
  });

  // A record names itself: its surface prints the identity block, and that is
  // the page's one h1. The head yields — it prints the trail that leads here
  // and nothing at heading level, or the document would offer two page titles
  // for the same record.
  it("yields the heading to the record and prints the trail that leads there", () => {
    const client = newClient();
    client.setQueryData(["person", "ref", "p-anna"], "Anna Weber");
    const { container } = renderWith(
      client,
      <PageHead route={{ screen: "contacts", id: "p-anna" }} />,
    );

    expect(screen.queryAllByRole("heading", { level: 1 })).toHaveLength(0);
    const crumb = container.querySelector(".pagecrumb");
    expect(crumb?.textContent).toContain("Anna Weber");
    // The section is the way BACK to the list, which is the only navigation a
    // reader standing on the record still needs.
    const back = screen.getByRole("link", { name: "Contacts" });
    expect(crumb?.contains(back)).toBe(true);
    expect(back.getAttribute("href")).toBe("#/contacts");
  });

  // Loading, or a record this principal cannot read: the id is not a name, but
  // it is true, and it is what the reader can quote. A blank trail is not.
  it("falls back to the record id when the name cannot be resolved", () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(null, { status: 404 })),
    );
    const { container } = render(
      <PageHead route={{ screen: "contacts", id: "p-anna" }} />,
    );
    // In mono, so the reader can see it is an identifier and not somebody's
    // name — and carrying the whole id in `title` when the line has to clip.
    const raw = container.querySelector(".pagecrumb .t-mono");
    expect(raw?.textContent).toBe("p-anna");
    expect(raw?.getAttribute("title")).toBe("p-anna");
  });

  // An id segment that names no record is the screen's own state, not a record:
  // #/settings/privacy is still the Settings page. Treating the segment as a
  // record gave Settings a heading that read "privacy" — a raw slug, shown to a
  // reader as the name of the page they are on.
  it("keeps a screen's own id segment out of the page's name", () => {
    const { container } = render(
      <PageHead route={{ screen: "settings", id: "privacy" }} />,
    );
    expect(
      screen.getByRole("heading", { level: 1, name: "Settings" }),
    ).toBeTruthy();
    expect(container.querySelector(".pagecrumb")).toBeNull();
    expect(container.textContent).not.toContain("privacy");
  });

  // Nothing but the title, the SoR chip (silent in native mode) and the agent
  // dock. The dock's own trigger is the ONE control the head mints for itself;
  // any other button appearing here without a caller asking for it is chrome
  // creeping back into the space the top bar used to occupy.
  it("carries no control the screen did not ask for", () => {
    render(<PageHead route={{ screen: "deals" }} />);
    const buttons = screen.getAllByRole("button");
    expect(buttons).toHaveLength(1);
    expect(buttons[0]).toBe(
      screen.getByRole("button", { name: /^Margince AI/ }),
    );
  });

  it("renders the screen actions it is given, beside the title", () => {
    const { container } = render(
      <PageHead
        route={{ screen: "deals" }}
        // The design system's control, not a hand-rolled button: what a screen
        // actually passes here comes from there, and a test that supplies its
        // own version of production proves nothing about production.
        actions={<Button variant="primary">New deal</Button>}
      />,
    );
    const action = screen.getByRole("button", { name: "New deal" });
    expect(container.querySelector(".pageaside")?.contains(action)).toBe(true);
  });

  // The agent is true of the product, not of the screen, so it rides beside the
  // title rather than in the sidebar. Its own claims are proved in
  // agentdock.test.tsx; that it is mounted here is the shell's promise.
  it("carries the agent beside the title", () => {
    const { container } = render(<PageHead route={{ screen: "deals" }} />);
    const dock = screen.getByRole("button", { name: /^Margince AI/ });
    expect(container.querySelector(".pageaside")?.contains(dock)).toBe(true);
  });

  // The counts the head is given are the rail's counts, and the dock reads the
  // approvals one out of them. Without the pass-through the agent is silent
  // about work that the sidebar is already badging two columns away.
  it("hands the approvals count on to the agent", () => {
    const { container } = render(
      <PageHead route={{ screen: "deals" }} counts={{ tasks: 9, inbox: 5 }} />,
    );
    // The inbox count specifically — tasks sits first in the same record and
    // carries a different number, so a head reading any count would show 9.
    expect(container.querySelector(".agentwait")?.textContent).toBe(
      "5 Approvals waiting",
    );
  });
});

// The page head's half of the phone model: the sidebar shows the destinations
// there, so a section's own entries are reached from here.
describe("Section switcher (the page head at phone width)", () => {
  const auditRoute = { screen: "settings", id: "audit" };

  // Above the breakpoint the sidebar's level carries the section, so the head
  // names the ENTRY and mints no control at all — a switcher there would be a
  // second copy of the navigation already on screen beside it.
  it("renders no switcher above the phone breakpoint", () => {
    render(<PageHead route={auditRoute} section={fixtureSection("audit")} />);
    expect(
      screen.getByRole("heading", { level: 1, name: "Audit log" }),
    ).toBeTruthy();
    expect(screen.queryByRole("button", { name: /change section/ })).toBeNull();
  });

  // At phone width the pair swaps: the heading names the section — nothing else
  // on screen does — and the switcher names the entry and opens the others.
  it("names the section and hands the entry to the switcher at phone width", () => {
    stubPhoneViewport();
    render(<PageHead route={auditRoute} section={fixtureSection("audit")} />);
    expect(
      screen.getByRole("heading", { level: 1, name: "Settings" }),
    ).toBeTruthy();
    const switcher = screen.getByRole("button", {
      name: "Audit log — change section",
    });
    // The visible word is the entry, and it is part of the name (WCAG 2.5.3), so
    // a reader driving the app by voice says what they can see.
    expect(switcher.textContent).toContain("Audit log");
    expect(switcher.getAttribute("aria-expanded")).toBe("false");
    // Under the head, not inside it: the head is the page's title and the two
    // things true of the whole product, and a control that changes which page you
    // are on is none of those — it belongs to the column it switches.
    expect(document.querySelector(".pagehead")?.contains(switcher)).toBe(false);
    // Closed, it claims nothing: it is a control that opens a list, not a page.
    expect(document.querySelectorAll('[aria-current="page"]')).toHaveLength(0);
  });

  it("opens the section's entries with the current one marked", async () => {
    stubPhoneViewport();
    render(<PageHead route={auditRoute} section={fixtureSection("audit")} />);
    await userEvent.click(
      screen.getByRole("button", { name: "Audit log — change section" }),
    );
    const dialog = screen.getByRole("dialog");
    // Named by the section, with its groups and every entry it publishes.
    expect(
      within(dialog).getByRole("heading", { level: 2, name: "Settings" }),
    ).toBeTruthy();
    expect(
      within(dialog)
        .getAllByRole("heading", { level: 3 })
        .map((heading) => heading.textContent),
    ).toEqual(["You", "Organization"]);
    expect(
      within(dialog)
        .getAllByRole("link")
        .map((link) => link.getAttribute("href")),
    ).toEqual(["#/settings/account", "#/settings/audit"]);
    // The current entry is claimed inside the LIST — the switcher that opened it
    // still claims nothing, so the document offers exactly one current page.
    const current = document.querySelectorAll('[aria-current="page"]');
    expect(current).toHaveLength(1);
    expect(current[0].getAttribute("href")).toBe("#/settings/audit");
  });

  it("navigates and closes itself when an entry is picked", async () => {
    stubPhoneViewport();
    render(<PageHead route={auditRoute} section={fixtureSection("audit")} />);
    await userEvent.click(
      screen.getByRole("button", { name: "Audit log — change section" }),
    );
    await userEvent.click(
      within(screen.getByRole("dialog")).getByRole("link", { name: "Account" }),
    );
    // The sheet covers the page it just navigated to, so it goes with the tap.
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(window.location.hash).toBe("#/settings/account");
  });

  // A full-screen sheet has no backdrop left to click, and a touch reader has no
  // Escape: the way out has to be a control inside it.
  it("closes from a control in the sheet", async () => {
    stubPhoneViewport();
    render(<PageHead route={auditRoute} section={fixtureSection("audit")} />);
    await userEvent.click(
      screen.getByRole("button", { name: "Audit log — change section" }),
    );
    await userEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: "Close" }),
    );
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  // A section that belongs to another screen contributes nothing — the same rule
  // the sidebar's level follows, or the switcher would offer Settings' tabs from
  // the middle of the pipeline.
  it("ignores a section that belongs to another screen", () => {
    stubPhoneViewport();
    render(
      <PageHead
        route={{ screen: "deals" }}
        section={fixtureSection("audit")}
      />,
    );
    expect(
      screen.getByRole("heading", { level: 1, name: "Pipeline" }),
    ).toBeTruthy();
    expect(screen.queryByRole("button", { name: /change section/ })).toBeNull();
  });
});

describe("Shell", () => {
  it("stamps body[data-screen] from the route", () => {
    window.location.hash = "#/reports";
    render(<Shell onOpenSearch={ignoreSearch}>{null}</Shell>);
    expect(document.body.dataset.screen).toBe("reports");
  });

  // The a11y hole this restructure closes: the page's name used to be a span in
  // the top bar, so a railed route had no level-1 heading to jump to at all.
  // One h1 per railed page, and exactly one — on a list, a report, a settings
  // surface, the shell mints it, so a screen that also prints its own title at
  // heading level is printing a duplicate rather than filling a gap.
  it("mints the page-level heading on a route that names no record", () => {
    window.location.hash = "#/contacts";
    render(<Shell onOpenSearch={ignoreSearch}>{null}</Shell>);
    const headings = screen.getAllByRole("heading", { level: 1 });
    expect(headings).toHaveLength(1);
    expect(headings[0].textContent).toBe("Contacts");
  });

  // The other half of the same rule: a record surface prints the identity block
  // that names the page, so the shell contributes NO heading there. The two
  // halves have to be asserted together — either one alone is satisfied by a
  // shell that mints a heading everywhere, or by one that mints it nowhere.
  it("contributes no heading on a record route, leaving the page's name to the record", () => {
    window.location.hash = "#/contacts/p-anna";
    const { container } = render(
      <Shell onOpenSearch={ignoreSearch}>{null}</Shell>,
    );
    expect(screen.queryAllByRole("heading", { level: 1 })).toHaveLength(0);
    // What it shows instead: the trail back to the list this record is in.
    const back = container.querySelector(".pagecrumb .pageback");
    expect(back?.getAttribute("href")).toBe("#/contacts");
  });

  it("renders rail-less for the documented exceptions (AC-shell layout exception)", () => {
    window.location.hash = "#/book";
    render(<Shell onOpenSearch={ignoreSearch}>{null}</Shell>);
    expect(screen.queryByRole("navigation")).toBeNull();
  });

  // A rail-less surface carries its own chrome, so the shell contributes no
  // heading of its own there — the screen's h1 is the only one.
  it("contributes no page head to a rail-less surface", () => {
    window.location.hash = "#/book";
    const { container } = render(
      <Shell onOpenSearch={ignoreSearch}>{null}</Shell>,
    );
    expect(container.querySelector(".pagehead")).toBeNull();
  });

  // The consent screen is where a human hands an agent their own authority —
  // it must never be framed inside the app's own nav, which is what
  // RAIL_LESS_SCREENS carrying "oauth-consent" is for.
  it("renders rail-less for the OAuth consent screen", () => {
    window.location.hash = "#/oauth-consent";
    render(<Shell onOpenSearch={ignoreSearch}>{null}</Shell>);
    expect(screen.queryByRole("navigation")).toBeNull();
  });

  // One number, two surfaces: the rail badges what is waiting and the agent
  // beside the title reports the same thing. They read the counts the shell was
  // given, so a head that is not handed them leaves the two disagreeing.
  it("gives the page head the counts the rail badges", () => {
    window.location.hash = "#/contacts";
    const { container } = render(
      <Shell counts={{ inbox: 7 }} onOpenSearch={ignoreSearch}>
        {null}
      </Shell>,
    );
    expect(container.querySelector(".rail .count")?.textContent).toBe("7");
    expect(container.querySelector(".agentwait")?.textContent).toBe(
      "7 Approvals waiting",
    );
  });

  it("renders the rail on core screens", () => {
    window.location.hash = "#/contacts";
    render(<Shell onOpenSearch={ignoreSearch}>{null}</Shell>);
    expect(screen.getByRole("navigation")).toBeTruthy();
  });
});

// Sign-out is reached from the account menu at the sidebar foot. What the menu
// does with focus and layers is account.test.tsx's; what is proved here is that
// the shell's copy of it actually ends the session — the mutation, the cache,
// and the gate that follows them.
describe("Sign-out (AS-1)", () => {
  it("posts /auth/logout and clears the query cache on click", async () => {
    let loggedOut = false;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input instanceof Request ? input.url : input);
        const method = input instanceof Request ? input.method : "GET";
        if (url.endsWith("/v1/auth/logout") && method === "POST") {
          loggedOut = true;
          return new Response(null, { status: 204 });
        }
        if (url.endsWith("/v1/me")) {
          return new Response(null, { status: loggedOut ? 401 : 200 });
        }
        return new Response(null, { status: 404 });
      }),
    );
    // Seed the ["me"] cache so we can observe the mutation clearing it — the
    // gate re-probe hangs off this exact entry going away (queryClient.clear()).
    const client = newClient();
    client.setQueryData(["me"], { user: { id: "u1", email: "ada@acme.test" } });
    renderWith(
      client,
      <WorkspaceRail route={{ screen: "deals" }} onOpenSearch={ignoreSearch} />,
    );
    expect(client.getQueryData(["me"])).toBeTruthy();
    // Sign-out lives inside the account menu, so it takes opening first.
    await userEvent.click(screen.getByRole("button", { name: /Account$/ }));
    await userEvent.click(screen.getByRole("button", { name: "Sign out" }));
    // POST fired AND the whole cache was cleared — the ["me"] entry is gone,
    // so the auth gate re-probes → 401 → login. This assertion bites: it fails
    // if `onSuccess: () => queryClient.clear()` is removed from useLogout.
    await waitFor(() => expect(loggedOut).toBe(true));
    await waitFor(() => expect(client.getQueryData(["me"])).toBeUndefined());
  });

  // A pending OAuth authorization lives in sessionStorage, which a sign-out
  // that leaves the tab open does not touch. It is this human's request: the
  // next human to sign in here must not be offered a connection they never
  // started, with their own passports on the consent screen.
  it("discards a pending connection so the next sign-in is not offered it", async () => {
    let loggedOut = false;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input instanceof Request ? input.url : input);
        const method = input instanceof Request ? input.method : "GET";
        if (url.endsWith("/v1/auth/logout") && method === "POST") {
          loggedOut = true;
          return new Response(null, { status: 204 });
        }
        if (url.endsWith("/v1/me")) {
          return new Response(null, { status: loggedOut ? 401 : 200 });
        }
        return new Response(null, { status: 404 });
      }),
    );
    stashPendingAuthorize({
      url: "/oauth/authorize?client_id=night&scope=read",
      clientName: "Claude Code",
    });
    renderWith(
      newClient(),
      <WorkspaceRail route={{ screen: "deals" }} onOpenSearch={ignoreSearch} />,
    );
    await userEvent.click(screen.getByRole("button", { name: /Account$/ }));
    await userEvent.click(screen.getByRole("button", { name: "Sign out" }));
    await waitFor(() => expect(loggedOut).toBe(true));
    await waitFor(() => expect(readPendingAuthorize()).toBeNull());
  });

  // queryClient.clear() alone empties the cache but does NOT force a mounted
  // ["me"] observer to refetch — a component still watching it can keep
  // rendering its last (stale, authenticated) snapshot. Render THROUGH the real
  // AuthGate (App, not just the rail in isolation) and prove sign-out actually
  // lands the user back on the login screen, driven by a real /v1/me re-probe —
  // not merely that the cache entry disappeared.
  it("drives the AuthGate back to the login screen after sign-out (bites on stale-cache regressions)", async () => {
    let loggedOut = false;
    let meCalls = 0;
    vi.stubGlobal("localStorage", memoryStorage());
    globalThis.localStorage.setItem("margince.workspaceSlug", "acme");
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input instanceof Request ? input.url : input);
        const method = input instanceof Request ? input.method : "GET";
        if (url.endsWith("/v1/auth/logout") && method === "POST") {
          loggedOut = true;
          return new Response(null, { status: 204 });
        }
        if (url.endsWith("/v1/me")) {
          meCalls += 1;
          if (loggedOut) {
            return new Response(JSON.stringify({ code: "unauthenticated" }), {
              status: 401,
              headers: { "Content-Type": "application/problem+json" },
            });
          }
          return new Response(
            JSON.stringify({ user: { id: "u1" }, roles: [], teams: [] }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          );
        }
        return new Response(JSON.stringify({ code: "unavailable" }), {
          status: 503,
          headers: { "Content-Type": "application/problem+json" },
        });
      }),
    );
    renderWith(newClient(), <App />);

    // Authenticated: the chrome (and its account menu) is on screen.
    const account = await screen.findByRole("button", { name: /Account$/ });
    expect(meCalls).toBe(1);

    await userEvent.click(account);
    await userEvent.click(screen.getByRole("button", { name: "Sign out" }));

    // The gate must re-probe /v1/me (not just drop the cache entry) and,
    // seeing 401, render the auth (signup/login) screen — the rail must be
    // gone. AuthScreen defaults to its signup mode, so assert on that
    // heading rather than assuming "Sign in" is the first thing shown.
    await screen.findByRole("heading", { name: "Sign in to Margince" });
    expect(screen.queryByRole("navigation")).toBeNull();
    expect(loggedOut).toBe(true);
    expect(meCalls).toBeGreaterThanOrEqual(2);
  });
});
