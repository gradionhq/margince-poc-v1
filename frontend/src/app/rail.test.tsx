/** @vitest-environment jsdom */
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import {
  cleanup,
  fireEvent,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { navigate, type Route } from "./router";
import { Shell, WorkspaceRail } from "./shell";
import {
  fixtureSection,
  ignoreSearch,
  render,
  stubPhoneViewport,
} from "./testing/shellharness";

// A composed installation, because the vanilla registry is empty by
// construction: the case below that stands on a composed unit's screen is
// asserting what the rail does when the DESCRIPTOR resolves, and against an
// empty registry it would silently be the uncomposed case instead. The
// descriptor shape is the generator's, the same one App.extscreen.test.tsx hands
// the same lookup.
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

// B-EP09.4 acceptance, for the SIDEBAR — the left-hand panel and nothing else.
//
// It is destinations only: the canonical 12-item nav in order (AC-shell-1b —
// Automations left it for Settings → AI while the dedupe queue and the filter
// builder took rows, which is a UI divergence on the founder's back-fill list),
// at most one active
// item tracking the route (AC-shell-2), badges only on the attention screens and
// only from live counts (AC-shell-1e), collapsed rows on the expanded rows' own
// geometry with a dismissible tooltip (AC-shell-1d), and the phone bar with its
// More sheet. It carries no search row, no collapse control, no Settings door
// and no account block any more — each of those moved to the top bar
// (topbar.test.tsx).
//
// The second suite is the panel's own DEPTH: a section route replaces the
// destinations with that section's entries, one level at a time, with the way
// back up in the panel. A few of those cases drive the whole Shell, because
// where the reader walked down FROM is remembered above the rail and a rail on
// its own always answers "home".
//
// What the shell composes around this panel — the page title, the top bar's
// mounting, sign-out — is shell.test.tsx's.

// Only what a level hides needs the shell's real stylesheet in the document
// (see mountShellStyles); it outlives cleanup(), so it is taken down here.
let shellStyles: HTMLStyleElement | undefined;

afterEach(() => {
  cleanup();
  shellStyles?.remove();
  shellStyles = undefined;
  window.location.hash = "";
  vi.unstubAllGlobals();
  // The sidebar remembers its width in localStorage, which jsdom keeps for the
  // whole file: without this a case that collapses the sidebar hands the next
  // one a collapsed shell, and the next one's failure names a control that is
  // simply in its other state. Cleared after the unstub, so it clears the real
  // storage rather than a stub that is about to be thrown away.
  window.localStorage.clear();
});

// The route id never changes with a label: `deals` presents as Pipeline and
// `inbox` as Approvals, which names a governance surface rather than a mailbox.
const CANONICAL_ORDER = [
  "Home",
  "Contacts",
  "Companies",
  "Leads",
  "Duplicates",
  "Filters & views",
  "Pipeline",
  "Projects",
  "Tasks",
  "Approvals",
  "Reports",
  "Ask Margince",
];

// The rows of whatever level the panel is showing — the destinations, or a
// section's entries. Scoped to `.navlevel` rather than to the whole nav, because
// the brand above the level and the More control below it are not places the
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

describe("WorkspaceRail (AC-shell-1/2)", () => {
  it("renders the canonical 12 items in order, logomark → home", () => {
    render(<WorkspaceRail route={{ screen: "deals" }} />);
    const brand = within(
      screen.getByRole("navigation", { name: "Primary navigation" }),
    ).getByRole("link", {
      name: "Margince",
    });
    expect(brand.getAttribute("href")).toBe("#/home");
    // The DESTINATIONS are the level's own rows, so they are counted there: the
    // brand above it and the More control below it are neither of them, and each
    // is asserted where it belongs.
    expect(levelLabels()).toEqual(CANONICAL_ORDER);
    // The mark leads them, which is what "logomark → home" means.
    const home = screen.getByRole("link", { name: "Home" });
    expect(
      brand.compareDocumentPosition(home) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeGreaterThan(0);
  });

  it("groups the items under Records / Work / Intelligence when expanded", () => {
    render(<WorkspaceRail route={{ screen: "home" }} />);
    const headings = screen
      .getAllByRole("heading", { level: 2 })
      .map((heading) => heading.textContent);
    expect(headings).toEqual(["Records", "Work", "Intelligence"]);
  });

  it("marks exactly one item active, matching the route", () => {
    render(<WorkspaceRail route={{ screen: "deals" }} />);
    const active = screen
      .getAllByRole("link")
      .filter((link) => link.getAttribute("aria-current") === "page");
    expect(active).toHaveLength(1);
    expect(active[0].getAttribute("aria-label")).toBe("Pipeline");
  });

  // Settings is not a destination and no longer has a door here, so a route the
  // destinations do not hold — the settings screen itself, a composed unit's screen —
  // leaves every row quiet rather than lighting one that does not lead there. It
  // is the top bar's trail that says where the reader is on those routes
  // (topbar.test.tsx); what the rail promises is that it never claims a page it
  // is not showing. A RENDER assertion on purpose: the route→activeId half is a
  // string that resolves to a rendered element or to nothing at all, and a
  // data-level test reads identically either way.
  it.each([
    ["a settings route", { screen: "settings" }],
    ["a composed unit's screen", { screen: "ext", id: "notes" }],
  ] satisfies readonly (readonly [string, Route])[])(
    "claims no destination on %s",
    (_case, route) => {
      render(<WorkspaceRail route={route} />);
      expect(levelLabels()).toEqual(CANONICAL_ORDER);
      expect(document.querySelectorAll('[aria-current="page"]')).toHaveLength(
        0,
      );
    },
  );

  it("renders count badges only for provided positive counts", () => {
    const { container } = render(
      <WorkspaceRail
        route={{ screen: "home" }}
        counts={{ tasks: 4, inbox: 0 }}
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
      />,
    );
    expect(container.querySelectorAll(".count")).toHaveLength(0);
  });

  // AC-shell-1c/1d: collapsed items are icon-only, so the label must reach a
  // screen reader via aria-label in BOTH states, and the visible tooltip must
  // appear on keyboard focus (not hover alone) and be dismissible with Escape.
  it("keeps the accessible name when collapsed and shows a dismissible tooltip on focus", async () => {
    const user = userEvent.setup();
    render(<WorkspaceRail route={{ screen: "home" }} collapsed />);
    const pipeline = screen.getByRole("link", { name: "Pipeline" });
    expect(screen.queryByRole("tooltip")).toBeNull();

    pipeline.focus();
    const tip = await screen.findByRole("tooltip");
    expect(tip.textContent).toBe("Pipeline");

    await user.keyboard("{Escape}");
    expect(screen.queryByRole("tooltip")).toBeNull();
    // Escape dismisses the tooltip without moving focus (WCAG 1.4.13).
    expect(document.activeElement).toBe(pipeline);
  });

  // WCAG 1.4.13 also requires the tooltip be HOVERABLE: reaching for it must not
  // dismiss it. What makes that true is CONTAINMENT — the tooltip is a descendant
  // of the row it belongs to, so a pointer moving onto it never leaves the row —
  // and containment is what this asserts. A sibling tooltip would vanish under
  // the cursor and no assertion on its text would notice.
  //
  // The hover itself is deliberately NOT simulated: user-event dispatches
  // `mouseleave` on the row when the pointer moves to a child of it, which a
  // browser does not do, so a pass there would measure the simulator and a
  // failure would report a defect that is not in the product.
  it("nests the collapsed tooltip inside its own row so hovering it cannot dismiss it", async () => {
    const user = userEvent.setup();
    render(<WorkspaceRail route={{ screen: "home" }} collapsed />);
    const pipeline = screen.getByRole("link", { name: "Pipeline" });

    await user.hover(pipeline);
    const tip = await screen.findByRole("tooltip");
    expect(pipeline.contains(tip)).toBe(true);
    expect(tip.parentElement).toBe(pipeline);
  });

  // On a phone the four bar tabs are the only rows rendered, so a route living
  // in the More sheet has nothing to carry the current-destination state. More
  // carries it instead, or the bar shows no active tab at all.
  it("marks More as the active tab for a destination the phone bar hides", () => {
    const sheeted = render(<WorkspaceRail route={{ screen: "reports" }} />);
    const more = sheeted.container.querySelector(".railmore.active");
    expect(more).not.toBeNull();
    // Announced, not merely tinted: the hidden route's own link is out of the
    // accessibility tree at phone width, so More has to report the current page.
    expect(more?.getAttribute("aria-current")).toBe("page");
    cleanup();

    const onBar = render(<WorkspaceRail route={{ screen: "contacts" }} />);
    const inactive = onBar.container.querySelector(".railmore");
    expect(inactive?.className).not.toContain("active");
    expect(inactive?.getAttribute("aria-current")).toBeNull();
  });

  // Open, the sheet renders the real row for that route, which carries
  // aria-current itself. Two elements claiming the current page is worse than
  // the visual-only state this replaced.
  it("hands the current-page claim back to the real row once the sheet is open", async () => {
    const user = userEvent.setup();
    // The sheet exists only at phone width — the control that opens it is not
    // rendered above the breakpoint, and the rail closes any sheet it finds
    // itself holding there.
    stubPhoneViewport();
    const { container } = render(
      <WorkspaceRail route={{ screen: "reports" }} />,
    );
    await user.click(screen.getByRole("button", { name: "More" }));
    expect(
      container.querySelector(".railmore")?.getAttribute("aria-current"),
    ).toBeNull();
    expect(container.querySelectorAll('[aria-current="page"]')).toHaveLength(1);
  });

  // The sheet is the phone's whole sidebar, and it is navigation and nothing
  // else: the account menu lives in the top bar, which is on screen at this
  // width too. A second copy of it down here would be two account affordances
  // one scroll apart, which is what the sidebar's own foot used to be.
  it("opens a sheet of destinations and no second account affordance", async () => {
    const user = userEvent.setup();
    stubPhoneViewport();
    const { container } = render(<WorkspaceRail route={{ screen: "home" }} />);

    await user.click(screen.getByRole("button", { name: "More" }));
    expect(levelLabels()).toEqual(CANONICAL_ORDER);
    expect(container.querySelector(".accountrows")).toBeNull();
    expect(screen.queryByRole("button", { name: /Account$/ })).toBeNull();
    expect(screen.queryByRole("button", { name: "Sign out" })).toBeNull();
  });

  // The sheet takes focus when it opens, so dismissing it from INSIDE (Escape, a
  // click outside) leaves focus on a row that is about to be gone. It goes back
  // to the control that opened it rather than onto <body> — and only then: a rail
  // that merely mounts with a row focused must keep that focus where it is.
  it("hands focus back to More when the sheet is dismissed from inside it", async () => {
    const user = userEvent.setup();
    stubPhoneViewport();
    render(<WorkspaceRail route={{ screen: "home" }} />);
    await user.click(screen.getByRole("button", { name: "More" }));
    expect(document.activeElement).toBe(
      screen.getByRole("link", { name: "Home" }),
    );

    await user.keyboard("{Escape}");
    expect(document.activeElement).toBe(
      screen.getByRole("button", { name: "More" }),
    );
  });
});

// The sidebar's SECOND level (and its third). A section route replaces the
// destinations with the section's entries rather than hanging them off it: one
// level at a time, with the way back up in the panel. The depth is read off the
// DATA, which is what the synthetic third level in `fixtureSection` is for —
// settings, the only real section the app ships, is two levels deep.
describe("Rail levels (a section's entries as the second level)", () => {
  it("replaces the destinations with the section's entries, one current", () => {
    render(
      <WorkspaceRail
        route={{ screen: "settings", id: "account" }}
        section={fixtureSection("account")}
      />,
    );
    // The destinations are GONE, not pushed below a second list: 64px cannot
    // carry two levels and 252px carrying both is a list of twenty places to go.
    expect(screen.queryByRole("link", { name: "Pipeline" })).toBeNull();
    expect(levelLabels()).toEqual(["Account", "Privacy & audit"]);
    expect(
      screen
        .getByRole("link", { name: "Privacy & audit" })
        .getAttribute("href"),
    ).toBe("#/settings/deep");
    // Exactly one row claims the current page, and it is the entry the SECTION
    // resolved — the screen owns that answer, fallbacks included.
    const current = document.querySelectorAll('[aria-current="page"]');
    expect(current).toHaveLength(1);
    expect(current[0].getAttribute("aria-label")).toBe("Account");
  });

  // The level names itself at heading level 2, so its group labels move down to
  // 3 — the outline reads Settings → You / Admin settings, and the rail's
  // own destinations keep level 2 for their groups on every other route.
  it("names the level at heading level 2 and its groups at 3", () => {
    render(
      <WorkspaceRail
        route={{ screen: "settings", id: "account" }}
        section={fixtureSection("account")}
      />,
    );
    expect(
      screen.getAllByRole("heading", { level: 2 }).map((h) => h.textContent),
    ).toEqual(["Settings"]);
    expect(
      screen.getAllByRole("heading", { level: 3 }).map((h) => h.textContent),
    ).toEqual(["You", "Admin settings"]);
  });

  // A section belongs to ONE screen. Without this the fixture's entries would
  // leak onto every route, which is exactly what the canonical-order assertions
  // above would then be lying about.
  it("ignores a section that belongs to another screen", () => {
    render(
      <WorkspaceRail
        route={{ screen: "home" }}
        section={fixtureSection("deep")}
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
    const user = userEvent.setup();
    window.location.hash = "#/reports";
    render(<Shell onOpenSearch={ignoreSearch}>{null}</Shell>);
    expect(screen.getByRole("link", { name: "Reports" })).toBeTruthy();

    navigate({ screen: "settings", id: "account" });
    await user.click(
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

  // The walk asks for focus at the address it is GOING to, and the route it is
  // going to arrives on a hashchange that lands a task later. Anything that
  // re-renders the section's own rail in that gap — a query settling, a popover
  // closing — must not be handed the focus meant for the destinations: it would
  // spend it on a row it is about to unmount, and the document would end on
  // <body> with the sidebar lost. The re-render below is that gap, made
  // deterministic.
  it("keeps the arriving level's focus when the section re-renders mid-walk", async () => {
    window.location.hash = "#/reports";
    render(<Shell onOpenSearch={ignoreSearch}>{null}</Shell>);
    navigate({ screen: "settings", id: "account" });
    const back = await screen.findByRole("button", {
      name: "Back to Destinations",
    });

    back.click();
    // The gap: the address has changed but the hashchange has not been delivered,
    // and the section's rail re-renders for a reason of its own (a pointer landing
    // on a row is the cheapest one to stage).
    fireEvent.mouseEnter(screen.getByRole("link", { name: "Account" }));
    // Still inside the section: its own first row must not have taken the focus.
    expect(document.activeElement).not.toBe(
      screen.getByRole("link", { name: "Account" }),
    );

    await waitFor(() => expect(levelLabels()).toEqual(CANONICAL_ORDER));
    expect(document.activeElement).toBe(
      screen.getByRole("link", { name: "Home" }),
    );
  });

  // A reader who typed the address, or followed a link into it, walked down from
  // nowhere — there is no origin to return them to, and home is the one place
  // the app can honestly send them.
  it("falls back home when the reader deep-linked into the section", async () => {
    const user = userEvent.setup();
    window.location.hash = "#/settings/account";
    render(<Shell onOpenSearch={ignoreSearch}>{null}</Shell>);
    await user.click(
      await screen.findByRole("button", { name: "Back to Destinations" }),
    );
    expect(window.location.hash).toBe("#/home");
  });

  // The level's rows take the brand's WORDS — the mark alone stands for the
  // product here — and nothing else. The mark stays, and with it the job it
  // holds: the link home. A head reduced to a dead box would take that with it.
  it("hides the brand words but keeps the mark while a level is shown", () => {
    shellStyles = mountShellStyles();
    const { container } = render(
      <WorkspaceRail
        route={{ screen: "settings", id: "account" }}
        section={fixtureSection("account")}
      />,
    );
    expect(railDisplay(container, ".ws-name")).toBe("none");
    expect(railDisplay(container, ".ws-chip")).not.toBe("none");
    expect(
      screen.getByRole("link", { name: "Margince" }).getAttribute("href"),
    ).toBe("#/home");
  });

  // The other half of the same rule: outside a level the head is the head. The
  // brand assertion alone is satisfied by a rail that hides the words everywhere,
  // or by one that hides them nowhere.
  it("keeps the brand words on a route with no level", () => {
    shellStyles = mountShellStyles();
    const { container } = render(<WorkspaceRail route={{ screen: "home" }} />);
    expect(railDisplay(container, ".ws-name")).not.toBe("none");
    expect(railDisplay(container, ".ws-chip")).not.toBe("none");
  });

  // An entry that HAS children opens them: standing on it, the panel shows the
  // level it leads to rather than the list it came from. Nothing carries the
  // current page there until a child is addressed — the same as any list a
  // reader has just been handed.
  it("opens an entry's children as soon as the route stands on that entry", () => {
    render(
      <WorkspaceRail
        route={{ screen: "settings", id: "deep" }}
        section={fixtureSection("deep")}
      />,
    );
    expect(levelLabels()).toEqual(["Data model"]);
    expect(document.querySelectorAll('[aria-current="page"]')).toHaveLength(0);
    expect(
      screen.getAllByRole("heading", { level: 2 }).map((h) => h.textContent),
    ).toEqual(["Privacy & audit"]);
  });

  it("renders a third level from the data, addressed under the entry that opens it", () => {
    render(
      <WorkspaceRail
        route={{ screen: "settings", id: "deep", id2: "deeper" }}
        section={fixtureSection("deep")}
      />,
    );
    // The child level: only the entry's children, addressed under it.
    expect(levelLabels()).toEqual(["Data model"]);
    expect(
      screen.getByRole("link", { name: "Data model" }).getAttribute("href"),
    ).toBe("#/settings/deep/deeper");
    expect(
      screen.getAllByRole("heading", { level: 2 }).map((h) => h.textContent),
    ).toEqual(["Privacy & audit"]);
  });

  // One step at a time, and the step is an ADDRESS: below the section's own
  // level the way back leads to the entry the reader drilled through, whose own
  // address is what names the level above. It is also what the control is named
  // for — the section's list, not the entry whose children are on screen.
  it("lands on the parent entry's own address from a level below the section", async () => {
    const user = userEvent.setup();
    render(
      <WorkspaceRail
        route={{ screen: "settings", id: "deep", id2: "deeper" }}
        section={fixtureSection("deep")}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Back to Settings" }));
    expect(window.location.hash).toBe("#/settings/deep");
  });

  // AC-shell-1d holds at every depth, and there is ONE tooltip in the sidebar:
  // moving between two entries of a level must not leave the first one open.
  it("shows one tooltip at a time on the collapsed level", async () => {
    const user = userEvent.setup();
    render(
      <WorkspaceRail
        route={{ screen: "settings", id: "account" }}
        section={fixtureSection("account")}
        collapsed
      />,
    );
    const account = screen.getByRole("link", { name: "Account" });
    const privacyEntry = screen.getByRole("link", { name: "Privacy & audit" });
    expect(screen.queryByRole("tooltip")).toBeNull();

    account.focus();
    // waitFor on the TEXT, not findAllByRole on the role: a tooltip left over
    // from the previous row satisfies the role query on its first poll, so a
    // "one tooltip" assertion could pass while showing the wrong one.
    await waitFor(() =>
      expect(screen.getByRole("tooltip").textContent).toBe("Account"),
    );

    privacyEntry.focus();
    await waitFor(() =>
      expect(screen.getByRole("tooltip").textContent).toBe("Privacy & audit"),
    );
    expect(screen.getAllByRole("tooltip")).toHaveLength(1);

    await user.keyboard("{Escape}");
    expect(screen.queryByRole("tooltip")).toBeNull();
    // Escape dismisses the tooltip without moving focus (WCAG 1.4.13).
    expect(document.activeElement).toBe(privacyEntry);
  });

  // At phone width the panel is a bar of four destinations, and it keeps them on
  // a section route: handing the bar to a section lost every destination, made
  // switching entries More → scroll → tap, and left a bar holding two controls.
  // The section is reached from the page title's own switcher there instead.
  it("keeps the destinations on the phone bar, even on a section route", async () => {
    const user = userEvent.setup();
    stubPhoneViewport();
    render(
      <WorkspaceRail
        route={{ screen: "settings", id: "account" }}
        section={fixtureSection("account")}
      />,
    );
    expect(levelLabels()).toEqual(CANONICAL_ORDER);
    // No level at all: no entries, no way back up, and no `leveled` arrangement
    // for the bar to be rearranged by.
    expect(screen.queryByRole("link", { name: "Privacy & audit" })).toBeNull();
    expect(screen.queryByRole("button", { name: /^Back/ })).toBeNull();
    expect(
      screen.getByRole("navigation", { name: "Primary navigation" }).className,
    ).not.toContain("leveled");

    // And the sheet is what it was before levels existed: every destination, and
    // still no entry of the section the reader is standing in.
    await user.click(screen.getByRole("button", { name: "More" }));
    expect(levelLabels()).toEqual(CANONICAL_ORDER);
    expect(screen.queryByRole("link", { name: "Privacy & audit" })).toBeNull();
  });

  // The other half: above the breakpoint the level is exactly what it was. Either
  // assertion alone is satisfied by a rail that ignores every section, or by one
  // that ignores the width.
  it("still walks into the section above the phone breakpoint", () => {
    render(
      <WorkspaceRail
        route={{ screen: "settings", id: "account" }}
        section={fixtureSection("account")}
      />,
    );
    expect(levelLabels()).toEqual(["Account", "Privacy & audit"]);
    expect(
      screen.getByRole("navigation", { name: "Primary navigation" }).className,
    ).toContain("leveled");
  });
});
