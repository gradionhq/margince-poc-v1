/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import { LocaleProvider } from "../i18n";
import {
  clearPendingAuthorize,
  readPendingAuthorize,
  stashPendingAuthorize,
} from "./pendingauthorize";
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
// The account block and the agent strip are components of their own now, and
// their behaviour is proved where they live (account.test.tsx,
// agentpanel.test.tsx). What is asserted here is that the shell MOUNTS them in
// the places it promises: the account block at the sidebar foot, the agent
// beside the page title.

afterEach(() => {
  cleanup();
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
    const rail = screen.getByRole("navigation");
    const links = within(rail).getAllByRole("link");
    expect(links[0].getAttribute("aria-label")).toBe("Margince");
    expect(links[0].getAttribute("href")).toBe("#/home");
    const navLabels = links
      .slice(1)
      .map((link) => link.getAttribute("aria-label"));
    expect(navLabels).toEqual(CANONICAL_ORDER);
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
    // First in the reading order, and not an eleventh place to go: the ten
    // destinations are still exactly the ten links under the logomark.
    expect(
      search.compareDocumentPosition(home) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeGreaterThan(0);
    const links = within(screen.getByRole("navigation")).getAllByRole("link");
    expect(links).toHaveLength(CANONICAL_ORDER.length + 1);
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
  // strip. A control appearing here without a caller asking for it is chrome
  // creeping back into the space the top bar used to occupy.
  it("carries no control the screen did not ask for", () => {
    render(<PageHead route={{ screen: "deals" }} />);
    expect(screen.queryAllByRole("button")).toHaveLength(0);
  });

  it("renders the screen actions it is given, beside the title", () => {
    const { container } = render(
      <PageHead
        route={{ screen: "deals" }}
        actions={
          <button type="button" className="btn">
            New deal
          </button>
        }
      />,
    );
    const action = screen.getByRole("button", { name: "New deal" });
    expect(container.querySelector(".pageaside")?.contains(action)).toBe(true);
  });

  // The agent is true of the product, not of the screen, so it rides beside the
  // title rather than in the sidebar. Its own claims are proved in
  // agentpanel.test.tsx; that it is mounted here is the shell's promise.
  it("carries the agent beside the title", () => {
    const { container } = render(<PageHead route={{ screen: "deals" }} />);
    const strip = screen.getByRole("region", { name: "Margince AI status" });
    expect(container.querySelector(".pageaside")?.contains(strip)).toBe(true);
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
