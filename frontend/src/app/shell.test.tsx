/** @vitest-environment jsdom */
import { cleanup, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import { memoryStorage } from "../testing/appharness";
import {
  clearPendingAuthorize,
  readPendingAuthorize,
  stashPendingAuthorize,
} from "./pendingauthorize";
import { parseHash, type Route } from "./router";
import { PageTitle, Shell } from "./shell";
import {
  fixtureSection,
  ignoreSearch,
  newClient,
  render,
  renderWith,
  stubPhoneViewport,
} from "./testing/shellharness";

// A composed installation, because the vanilla registry is empty by
// construction — the two branches that YIELD to a surface naming itself (a
// record's, a unit's) could otherwise only ever be exercised on one of them.
// The descriptor shape is the generator's, the same one App.extscreen.test.tsx
// hands the same lookup.
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

// B-EP09.4 acceptance, for what the SHELL composes — the page's own name, and
// the chrome the shell mounts around whatever screen is on.
//
// PageTitle is the third piece of the chrome: a heading inside the scroller
// above the content it names, the one line under it that some screens need,
// whatever the screen itself asked to put beside them, and at phone width the
// switcher that reaches a section's entries (the sidebar shows the destinations
// there instead). It yields whole on a surface that names itself — a record, a
// composed unit's screen — or the document would offer two page titles.
//
// What the SHELL itself owes: body[data-screen], exactly one element claiming
// the page on every route, the reading-column policy, the top bar mounted above
// the scroller with the page's name inside it, ⌘B reaching the state behind the
// bar's toggle, the agent dock once at the foot of the content column, and no
// chrome at all on the rail-less surfaces. Sign-out closes the file, driven
// through the whole shell because the account menu is only reachable through the
// chrome that mounts it.
//
// The SIDEBAR — the destinations, the badges, the collapsed tooltips, the phone
// bar and a section's entries as a second level — is rail.test.tsx's. The TOP
// BAR's own trail, search affordance and collapse toggle are topbar.test.tsx's,
// and the account menu and the agent dock are proved where they live
// (account.test.tsx, agentrail.test.tsx). What is asserted here is that the
// shell MOUNTS them in the places it promises, and feeds them the counts it
// already has.

afterEach(() => {
  cleanup();
  window.location.hash = "";
  vi.unstubAllGlobals();
  // The sidebar remembers its width in localStorage, which jsdom keeps for the
  // whole file: without this a case that collapses the sidebar hands the next
  // one a collapsed shell, and the next one's failure names a control that is
  // simply in its other state. Cleared after the unstub, so it clears the real
  // storage rather than a stub that is about to be thrown away.
  window.localStorage.clear();
  clearPendingAuthorize();
});

// The page's own name, standing INSIDE the scroller above the content it names.
// It is what is left of the old page head once the trail, the system-of-record
// chip and the agent moved out of it: a heading, the one line under it that some
// screens need, and whatever the screen itself asked to put beside them.
describe("PageTitle", () => {
  it("names the screen in a level-1 heading", () => {
    render(<PageTitle route={{ screen: "deals" }} />);
    expect(
      screen.getByRole("heading", { level: 1, name: "Pipeline" }),
    ).toBeTruthy();
    // The name, not a way anywhere: the trail is the top bar's and is the only
    // thing here that links.
    expect(screen.queryByRole("link", { name: "Pipeline" })).toBeNull();
  });

  // AC-shell-1k: every authenticated route resolves to real copy. This bites on
  // a new off-rail route landing in the router without a title key — the old
  // fallback rendered the raw screen slug.
  it("resolves a title for off-rail routes instead of the raw slug", () => {
    render(<PageTitle route={{ screen: "partners" }} />);
    expect(
      screen.getByRole("heading", { level: 1, name: "Partners" }),
    ).toBeTruthy();
  });

  // The same rule where there is no route at all. A hash nobody answers used to
  // put whatever the reader typed in the page's heading, which reads as a page
  // by that name existing.
  it("names an unknown route rather than echoing the hash", () => {
    render(<PageTitle route={parseHash("#/nope")} />);
    expect(
      screen.getByRole("heading", { level: 1, name: "Not found" }),
    ).toBeTruthy();
    expect(document.body.textContent).not.toContain("nope");
  });

  // An extension route the installation does NOT answer keeps the unknown-page
  // heading, and that is the deliberate half of the yield to a unit below: the
  // yield is conditioned on the DESCRIPTOR resolving, so a hand-typed
  // `#/ext/<anything>` is an unknown page here exactly as it is on the screen
  // under it, which says so in words. A surface that will not name itself must
  // not be left with no name at all.
  it("names an extension route this installation did not compose", () => {
    render(<PageTitle route={{ screen: "ext", id: "ghost" }} />);
    expect(
      screen.getByRole("heading", { level: 1, name: "Not found" }),
    ).toBeTruthy();
    expect(document.body.textContent).not.toContain("ghost");
  });

  // A page whose name alone does not say what it is for carries one quiet line
  // under the heading, and this is where it belongs: a screen that printed its
  // own subtitle had to print its own title above it to hang it on, and the
  // shell was already printing that title.
  it("prints the page's subtitle under the heading", () => {
    const { container } = render(<PageTitle route={{ screen: "inbox" }} />);
    const heading = screen.getByRole("heading", {
      level: 1,
      name: "Approvals",
    });
    const sub = container.querySelector(".pagesub");
    expect(sub?.textContent).toBe(
      "everything staged, waiting on your call — nothing runs without it",
    );
    // Directly under the name it explains, inside the title's own text column —
    // not beside the actions, where it would read as product chrome.
    expect(heading.nextElementSibling).toBe(sub);
    expect(container.querySelector(".pagetitle-text")?.contains(sub)).toBe(
      true,
    );
  });

  // Only the screens PAGE_SUB_KEYS names. Most headings say what the page is for
  // on their own, and a subtitle there is a line of copy nobody needed to read.
  it("prints no subtitle on a screen the map does not name", () => {
    const { container } = render(<PageTitle route={{ screen: "deals" }} />);
    expect(
      screen.getByRole("heading", { level: 1, name: "Pipeline" }),
    ).toBeTruthy();
    expect(container.querySelector(".pagesub")).toBeNull();
  });

  // A record names itself: its surface prints the identity block, and that is
  // the page's one h1. This yields whole — no heading, no subtitle, no element
  // at all — or the document would offer two page titles for the same record.
  // Where the reader came from is the top bar's trail (topbar.test.tsx).
  //
  // No record screen carries a subtitle key today (the map names `inbox`, `ai`
  // and `dedupe`, and none has a record segment), so what the subtitle half pins
  // is the structure. Give a record screen a subtitle and this is the case that
  // says where it may not appear.
  it("renders nothing at all on a record route", () => {
    const client = newClient();
    client.setQueryData(["person", "ref", "p-anna"], "Anna Weber");
    const { container } = renderWith(
      client,
      <PageTitle route={{ screen: "contacts", id: "p-anna" }} />,
    );
    expect(container.querySelector(".pagetitle")).toBeNull();
    expect(screen.queryAllByRole("heading", { level: 1 })).toHaveLength(0);
    expect(container.querySelector(".pagesub")).toBeNull();
  });

  // The other surface that names itself: a composed unit's screen. It yields on
  // the DESCRIPTOR resolving, which is what separates this from the uncomposed
  // route above — the two cases have to be asserted together, or a yield that
  // fires on the screen slug alone passes one of them.
  it("renders nothing at all on a composed unit's route", () => {
    const { container } = render(
      <PageTitle route={{ screen: "ext", id: "notes" }} />,
    );
    expect(container.querySelector(".pagetitle")).toBeNull();
    expect(screen.queryAllByRole("heading", { level: 1 })).toHaveLength(0);
  });

  // An id segment that names no record is the screen's own state, not a record:
  // #/settings/privacy is still the Settings page. Treating the segment as a
  // record gave Settings a heading that read "privacy" — a raw slug, shown to a
  // reader as the name of the page they are on.
  it("keeps a screen's own id segment out of the page's name", () => {
    const { container } = render(
      <PageTitle route={{ screen: "settings", id: "deep" }} />,
    );
    expect(
      screen.getByRole("heading", { level: 1, name: "Settings" }),
    ).toBeTruthy();
    expect(container.textContent).not.toContain("deep");
  });

  // The title is a NAME and nothing else. Every control the old page head minted
  // for itself — the search, the collapse toggle, the agent's trigger — belongs
  // to the top bar or to the dock now, and a button appearing here without a
  // caller asking for it is chrome creeping back into the content column.
  // The heading block is a NAME, not a toolbar. A screen's own verbs stand in
  // its `.list-head`, where the list they act on is; the shell used to thread a
  // `pageActions` slot down to here and no screen ever filled it.
  it("carries no control at all", () => {
    render(<PageTitle route={{ screen: "deals" }} />);
    expect(screen.queryAllByRole("button")).toHaveLength(0);
    expect(screen.queryAllByRole("link")).toHaveLength(0);
  });
});

// The page title's half of the phone model: the sidebar shows the destinations
// there, so a section's own entries are reached from here — a control under the
// page's name, never a second name for it.
describe("Section switcher (the page title at phone width)", () => {
  const deepRoute: Route = { screen: "settings", id: "deep" };

  // Above the breakpoint the sidebar's level carries the section, so the title
  // names the ENTRY and mints no control at all — a switcher there would be a
  // second copy of the navigation already on screen beside it.
  it("renders no switcher above the phone breakpoint", () => {
    render(<PageTitle route={deepRoute} section={fixtureSection("deep")} />);
    expect(
      screen.getByRole("heading", { level: 1, name: "Privacy & audit" }),
    ).toBeTruthy();
    expect(screen.queryByRole("button", { name: /change section/ })).toBeNull();
  });

  // At phone width the switcher IS the heading: the control names the page and
  // opens the others, so nothing above it repeats that name. The page is still
  // named exactly once at heading level, and the trail in the top bar is the only
  // other place it appears — the same arithmetic as every other route.
  it("stands as the page's heading at phone width", () => {
    stubPhoneViewport();
    render(<PageTitle route={deepRoute} section={fixtureSection("deep")} />);
    const heading = screen.getByRole("heading", { level: 1 });
    const switcher = screen.getByRole("button", {
      name: "Privacy & audit — change section",
    });
    expect(heading.contains(switcher)).toBe(true);
    // The visible word is the entry, and it is part of the name (WCAG 2.5.3), so
    // a reader driving the app by voice says what they can see.
    expect(switcher.textContent).toContain("Privacy & audit");
    expect(switcher.getAttribute("aria-expanded")).toBe("false");
    // One heading, and the entry's name is in it once — not once in a heading
    // and again in a control under it.
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
    // Closed, it claims nothing: it is a control that opens a list, not a page.
    expect(document.querySelectorAll('[aria-current="page"]')).toHaveLength(0);
  });

  it("opens the section's entries with the current one marked", async () => {
    const user = userEvent.setup();
    stubPhoneViewport();
    render(<PageTitle route={deepRoute} section={fixtureSection("deep")} />);
    await user.click(
      screen.getByRole("button", { name: "Privacy & audit — change section" }),
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
    ).toEqual(["You", "Admin settings"]);
    expect(
      within(dialog)
        .getAllByRole("link")
        .map((link) => link.getAttribute("href")),
    ).toEqual(["#/settings/account", "#/settings/deep"]);
    // The current entry is claimed inside the LIST — the switcher that opened it
    // still claims nothing, so the document offers exactly one current page.
    const current = document.querySelectorAll('[aria-current="page"]');
    expect(current).toHaveLength(1);
    expect(current[0].getAttribute("href")).toBe("#/settings/deep");
  });

  it("navigates and closes itself when an entry is picked", async () => {
    const user = userEvent.setup();
    stubPhoneViewport();
    render(<PageTitle route={deepRoute} section={fixtureSection("deep")} />);
    await user.click(
      screen.getByRole("button", { name: "Privacy & audit — change section" }),
    );
    await user.click(
      within(screen.getByRole("dialog")).getByRole("link", { name: "Account" }),
    );
    // The sheet covers the page it just navigated to, so it goes with the tap.
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(window.location.hash).toBe("#/settings/account");
  });

  // A full-screen sheet has no backdrop left to click, and a touch reader has no
  // Escape: the way out has to be a control inside it.
  it("closes from a control in the sheet", async () => {
    const user = userEvent.setup();
    stubPhoneViewport();
    render(<PageTitle route={deepRoute} section={fixtureSection("deep")} />);
    await user.click(
      screen.getByRole("button", { name: "Privacy & audit — change section" }),
    );
    await user.click(
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
      <PageTitle
        route={{ screen: "deals" }}
        section={fixtureSection("deep")}
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

  // ONE element claims the page, on every route — and where two elements could
  // claim it, they must be claiming the SAME page. On a record route they would
  // not: the sidebar's row marks the list the record was opened from, and a list
  // is not the page. So the row yields to `aria-current="true"` there (current in
  // this set) and the trail's last stop keeps `page`.
  it("claims the page exactly once on a record, with the sidebar's row yielding", () => {
    window.location.hash = "#/contacts/p-anna";
    const client = newClient();
    client.setQueryData(["person", "ref", "p-anna"], "Anna Weber");
    const { container } = renderWith(
      client,
      <Shell onOpenSearch={ignoreSearch}>{null}</Shell>,
    );
    const claims = container.querySelectorAll('[aria-current="page"]');
    expect(claims).toHaveLength(1);
    expect(claims[0].textContent).toBe("Anna Weber");
    const row = container.querySelector("nav.rail a.navitem.active");
    expect(row?.textContent).toBe("Contacts");
    expect(row?.getAttribute("aria-current")).toBe("true");
  });

  // On the list itself the row IS the page, so it keeps the stronger claim and
  // agrees with the trail beside it. Two elements naming one page is what a
  // breadcrumb and a navigation row are for; two naming different pages is the
  // case above.
  it("lets the sidebar's row claim the page on the list it names", () => {
    window.location.hash = "#/contacts";
    const { container } = render(
      <Shell onOpenSearch={ignoreSearch}>{null}</Shell>,
    );
    const claims = [...container.querySelectorAll('[aria-current="page"]')];
    expect(claims.map((claim) => claim.textContent)).toEqual([
      "Contacts",
      "Contacts",
    ]);
    expect(
      container.querySelector("nav.rail a.navitem.active")?.textContent,
    ).toBe("Contacts");
  });

  // The a11y hole this restructure closes: the page's name used to be a span in
  // the top bar, so a railed route had no level-1 heading to jump to at all.
  // One h1 per railed page, and exactly one — on a list, a report, a settings
  // surface, the shell mints it, so a screen that also prints its own title at
  // heading level is printing a duplicate rather than filling a gap.
  // Which pages keep the reading column, and the one distinction that is easy to
  // get wrong: a company RECORD is capped, the company LIST is not, and the only
  // thing separating them is the id. The marker is what the stylesheet keys the
  // cap on, so a route landing in the wrong family is a layout regression that
  // nothing else would catch. The set itself is GRIDDED_RECORD_SCREENS.
  it.each([
    ["#/settings/account", true],
    ["#/companies/o-1", true],
    ["#/contacts/p-1", true],
    ["#/companies", false],
    ["#/contacts", false],
    ["#/deals", false],
    ["#/reports", false],
    // A composed unit's page keeps the settings LEVEL (below) but not the
    // reading column: the column is a claim about the page's own content, and a
    // unit lays its own surface out.
    ["#/ext/notes", false],
  ])("reads the column policy off the route: %s", (hash, capped) => {
    window.location.hash = String(hash);
    const { container } = render(
      <Shell onOpenSearch={ignoreSearch}>{null}</Shell>,
    );
    const main = container.querySelector("main");
    expect(main?.className.includes("main-gridded")).toBe(capped);
  });

  // A unit's page is REACHED from settings and its trail says so
  // (`Settings / notes`), so the sidebar keeps the settings level on it. It did
  // not: the rail fell back to the ten destinations, and following "Open" from a
  // settings card swapped the whole sidebar out from under a reader whose URL
  // and breadcrumb still read Settings.
  //
  // Asserted on what the rail SHOWS rather than on a class name, because the
  // class is a rendering detail and the level is the claim.
  it("keeps the settings level in the sidebar on a composed unit's page", async () => {
    window.location.hash = "#/ext/notes";
    render(<Shell onOpenSearch={ignoreSearch}>{null}</Shell>);

    const rail = await screen.findByRole("navigation", {
      name: "Primary navigation",
    });
    expect(
      await within(rail).findByRole("link", { name: "Account" }),
    ).toBeTruthy();
    // And no settings tab claims the page: the reader is on the unit, which has
    // no row of its own (app/nav.ts, activeRowFor).
    expect(
      within(rail)
        .getAllByRole("link")
        .some((link) => link.getAttribute("aria-current") === "page"),
    ).toBe(false);
  });

  it("mints the page-level heading on a route that names no record", () => {
    window.location.hash = "#/contacts";
    render(<Shell onOpenSearch={ignoreSearch}>{null}</Shell>);
    const headings = screen.getAllByRole("heading", { level: 1 });
    expect(headings).toHaveLength(1);
    expect(headings[0].textContent).toBe("Contacts");
  });

  // The same rule for the line under that heading: the screens whose subtitle
  // was lost when they stopped printing their own title get it from the shell,
  // and it arrives with exactly one h1 above it rather than a second title to
  // hang it on. Asserted through the real shell because the head is what mounts
  // it — the subtitle is only as reachable as the route that carries it.
  it("mints the page's subtitle beneath that one heading", () => {
    window.location.hash = "#/ai";
    const { container } = render(
      <Shell onOpenSearch={ignoreSearch}>{null}</Shell>,
    );
    const headings = screen.getAllByRole("heading", { level: 1 });
    expect(headings).toHaveLength(1);
    expect(headings[0].textContent).toBe("Ask Margince");
    expect(container.querySelector(".pagesub")?.textContent).toBe(
      "bring your own agent — governed by the two-tier contract",
    );
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
    expect(container.querySelector(".pagetitle")).toBeNull();
    // What says where the reader is instead: the top bar's trail, leading back
    // to the list this record was opened from. Its own rules are in
    // topbar.test.tsx; that the shell still shows one here is the shell's.
    const trail = screen.getByRole("navigation", { name: "Breadcrumb" });
    expect(
      within(trail)
        .getByRole("link", { name: "Contacts" })
        .getAttribute("href"),
    ).toBe("#/contacts");
  });

  // The three pieces in the order the page is read: the bar is the first row of
  // <main> and stays put, the name is inside the scroller and goes with the
  // page. A title that drifted into the bar would scroll away with nothing; a
  // bar inside the scroller would take the trail off screen on a long page.
  it("mounts the top bar above the scroller and the page's name inside it", () => {
    window.location.hash = "#/contacts";
    const { container } = render(
      <Shell onOpenSearch={ignoreSearch}>{null}</Shell>,
    );
    const main = container.querySelector("main");
    const bar = container.querySelector<HTMLElement>(".topbar");
    const scroller = container.querySelector(".scroll");
    expect(main?.firstElementChild).toBe(bar);
    expect(scroller?.contains(bar)).toBe(false);
    const title = container.querySelector(".pagetitle");
    expect(scroller?.contains(title ?? null)).toBe(true);
    // And the bar's own passengers are mounted, which is all the shell owes
    // them: their behaviour is account.test.tsx's and sormodechip.test.tsx's.
    expect(
      within(bar ?? container).getByRole("button", { name: "Account" }),
    ).toBeTruthy();
  });

  // ⌘B / Ctrl B, the chord the toggle's tooltip has advertised since the sidebar
  // had a toggle. It moves the SAME state the toggle reports, which is why the
  // assertion is on the control's own name rather than on a class: a shortcut
  // wired to a second copy of the state would flip the panel and leave the
  // control lying about it.
  it("toggles the sidebar from the keyboard", async () => {
    const user = userEvent.setup();
    window.location.hash = "#/contacts";
    render(<Shell onOpenSearch={ignoreSearch}>{null}</Shell>);
    expect(
      screen.getByRole("button", { name: "Collapse sidebar" }),
    ).toBeTruthy();

    await user.keyboard("{Meta>}b{/Meta}");
    expect(
      await screen.findByRole("button", { name: "Expand sidebar" }),
    ).toBeTruthy();
  });

  // The same chord is bold in every editor on the platform, and a composer is
  // exactly where a reader reaches for it. Typed into a field, it belongs to the
  // field — the sidebar must not move under someone writing a sentence.
  it("leaves the sidebar alone while a text field has focus", async () => {
    const user = userEvent.setup();
    window.location.hash = "#/contacts";
    render(
      <Shell onOpenSearch={ignoreSearch}>
        <input aria-label="Note" />
      </Shell>,
    );

    await user.click(screen.getByRole("textbox", { name: "Note" }));
    await user.keyboard("{Meta>}b{/Meta}");
    expect(
      screen.getByRole("button", { name: "Collapse sidebar" }),
    ).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Expand sidebar" })).toBeNull();
  });

  // The agent lives in the CHROME, at the foot of the rail, on a screen the rail
  // reaches from its top level. Once, and inside the nav rather than the content
  // column: a second one anywhere is two things claiming to be the same agent.
  // Its own claims are agentrail.test.tsx's; that it is mounted here, once, and
  // where, is the shell's.
  it("mounts the agent once, at the foot of the rail", () => {
    window.location.hash = "#/contacts";
    const { container } = render(
      <Shell onOpenSearch={ignoreSearch}>{null}</Shell>,
    );
    const agents = container.querySelectorAll(".arblock");
    expect(agents).toHaveLength(1);
    expect(
      container.querySelector(".rail .railfoot")?.contains(agents[0]),
    ).toBe(true);
    expect(
      container.querySelector("main")?.querySelector(".arblock"),
    ).toBeNull();
  });

  // A sidebar showing a section's entries is navigation inside ONE destination,
  // and the agent belongs to the whole session — so it is absent there rather
  // than re-parented under a sub-level. The foot goes with it: an empty box would
  // leave the band and the rule that divide a reading from the rows above it.
  it("mounts no agent while the rail shows a section's own entries", async () => {
    window.location.hash = "#/settings/account";
    const { container } = render(
      <Shell onOpenSearch={ignoreSearch}>{null}</Shell>,
    );

    // Waited on the LEVEL arriving, which is what the rule keys on: the settings
    // entries come from a capability read, so a rail asserted before it answers
    // is still the top-level one and would pass whatever the rule did.
    const rail = await screen.findByRole("navigation", {
      name: "Primary navigation",
    });
    expect(
      await within(rail).findByRole("link", { name: "Account" }),
    ).toBeTruthy();
    expect(container.querySelector(".arblock")).toBeNull();
    expect(container.querySelector(".railfoot")).toBeNull();
  });

  it("renders rail-less for the documented exceptions (AC-shell layout exception)", () => {
    window.location.hash = "#/book";
    render(<Shell onOpenSearch={ignoreSearch}>{null}</Shell>);
    expect(screen.queryByRole("navigation")).toBeNull();
  });

  // A rail-less surface carries its own chrome, so the shell contributes none of
  // its own there: no bar, no heading, and no agent either — the agent on the
  // consent screen would be the app reaching into a page a human is reading
  // apart from the app.
  it("contributes no chrome at all to a rail-less surface", () => {
    window.location.hash = "#/book";
    const { container } = render(
      <Shell onOpenSearch={ignoreSearch}>{null}</Shell>,
    );
    expect(container.querySelector(".topbar")).toBeNull();
    expect(container.querySelector(".pagetitle")).toBeNull();
    expect(container.querySelector(".arblock")).toBeNull();
  });

  // The consent screen is where a human hands an agent their own authority —
  // it must never be framed inside the app's own nav, which is what
  // RAIL_LESS_SCREENS carrying "oauth-consent" is for.
  it("renders rail-less for the OAuth consent screen", () => {
    window.location.hash = "#/oauth-consent";
    render(<Shell onOpenSearch={ignoreSearch}>{null}</Shell>);
    expect(screen.queryByRole("navigation")).toBeNull();
  });

  // The agent reads its OWN counts and takes none from the shell. That is what
  // lets it be absent rather than zero while a read has not answered
  // (agentrail.test.tsx), and a prop handed down from here would take that back:
  // the shell's number is always present, so the section would print one before
  // anybody had counted. Handing the shell counts must therefore put no number
  // in the agent at all, however loudly the rail badges them.
  it("hands the agent no counts, so it reports only what it read itself", () => {
    window.location.hash = "#/contacts";
    const { container } = render(
      <Shell counts={{ tasks: 9, inbox: 7 }} onOpenSearch={ignoreSearch}>
        {null}
      </Shell>,
    );
    expect(
      Array.from(container.querySelectorAll(".rail .count")).map(
        (badge) => badge.textContent,
      ),
    ).toEqual(["9", "7"]);
    const agent = container.querySelector(".arblock");
    expect(agent?.textContent).not.toContain("7");
    expect(agent?.textContent).not.toContain("9");
  });

  it("renders the rail on core screens", () => {
    window.location.hash = "#/contacts";
    render(<Shell onOpenSearch={ignoreSearch}>{null}</Shell>);
    expect(
      screen.getByRole("navigation", { name: "Primary navigation" }),
    ).toBeTruthy();
  });
});

// Sign-out is reached from the account menu in the top bar. What the menu does
// with focus and layers is account.test.tsx's; what is proved here is that the
// shell's copy of it actually ends the session — the mutation, the cache, and
// the gate that follows them. Driven through the whole SHELL because the menu is
// only reachable through the chrome that mounts it: a rail rendered on its own
// has carried no account affordance since the sidebar became destinations only.
describe("Sign-out (AS-1)", () => {
  it("posts /auth/logout and clears the query cache on click", async () => {
    const user = userEvent.setup();
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
    window.location.hash = "#/deals";
    renderWith(client, <Shell onOpenSearch={ignoreSearch}>{null}</Shell>);
    expect(client.getQueryData(["me"])).toBeTruthy();
    // Sign-out lives inside the account menu, so it takes opening first.
    await user.click(screen.getByRole("button", { name: /Account$/ }));
    await user.click(screen.getByText("Sign out"));
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
    const user = userEvent.setup();
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
    window.location.hash = "#/deals";
    renderWith(newClient(), <Shell onOpenSearch={ignoreSearch}>{null}</Shell>);
    await user.click(screen.getByRole("button", { name: /Account$/ }));
    await user.click(screen.getByText("Sign out"));
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
    const user = userEvent.setup();
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

    await user.click(account);
    await user.click(screen.getByText("Sign out"));

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
