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
import { Shell, TopBar, WorkspaceRail } from "./shell";

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

// B-EP09.4 acceptance: the canonical 10-item nav in order (AC-shell-1b — A72
// promoted Automations to primary nav), at most one active item tracking the
// route (AC-shell-2), badges only on the attention screens and only from live
// counts (AC-shell-1e), 44x44 collapsed targets with a dismissible tooltip
// (AC-shell-1c/1d), the contextual top bar, and the rail-less exceptions.

afterEach(() => {
  cleanup();
  window.location.hash = "";
  vi.unstubAllGlobals();
  clearPendingAuthorize();
});

const render = (ui: ReactNode) => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
};

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
    render(<WorkspaceRail route={{ screen: "deals" }} />);
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

  it("marks nothing active on a non-rail screen", () => {
    render(<WorkspaceRail route={{ screen: "settings" }} />);
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
    render(<WorkspaceRail route={{ screen: "home" }} collapsed />);
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
    render(<WorkspaceRail route={{ screen: "home" }} collapsed />);
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
    const { container } = render(
      <WorkspaceRail route={{ screen: "reports" }} />,
    );
    await userEvent.click(screen.getByRole("button", { name: "More" }));
    expect(
      container.querySelector(".railmore")?.getAttribute("aria-current"),
    ).toBeNull();
    expect(container.querySelectorAll('[aria-current="page"]')).toHaveLength(1);
  });

  it("renders the collapse control with the state it will move to", () => {
    const onToggle = vi.fn();
    render(<WorkspaceRail route={{ screen: "home" }} onToggle={onToggle} />);
    const toggle = screen.getByRole("button", { name: "Collapse sidebar" });
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    toggle.click();
    expect(onToggle).toHaveBeenCalled();
  });
});

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
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    client.setQueryData(["me"], { user: { id: "u1", email: "ada@acme.test" } });
    rtlRender(
      <QueryClientProvider client={client}>
        <LocaleProvider initial="en">
          {/* Sign-out lives in the top bar beside the account link; the
              sidebar foot carries the agent panel. */}
          <TopBar route={{ screen: "deals" }} onOpenSearch={() => {}} />
        </LocaleProvider>
      </QueryClientProvider>,
    );
    expect(client.getQueryData(["me"])).toBeTruthy();
    // Sign-out lives inside the account menu, so it takes opening first.
    await userEvent.click(screen.getByRole("button", { name: "Account" }));
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
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    rtlRender(
      <QueryClientProvider client={client}>
        <LocaleProvider initial="en">
          <TopBar route={{ screen: "deals" }} onOpenSearch={() => {}} />
        </LocaleProvider>
      </QueryClientProvider>,
    );
    await userEvent.click(screen.getByRole("button", { name: "Account" }));
    await userEvent.click(screen.getByRole("button", { name: "Sign out" }));
    await waitFor(() => expect(loggedOut).toBe(true));
    await waitFor(() => expect(readPendingAuthorize()).toBeNull());
  });

  // CodeRabbit [9]: queryClient.clear() alone empties the cache but does NOT
  // force a mounted ["me"] observer to refetch — a component still watching
  // it can keep rendering its last (stale, authenticated) snapshot. Render
  // THROUGH the real AuthGate (App, not just the rail in isolation) and prove
  // sign-out actually lands the user back on the login screen, driven by a
  // real /v1/me re-probe — not merely that the cache entry disappeared.
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
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    rtlRender(
      <QueryClientProvider client={client}>
        <LocaleProvider initial="en">
          <App />
        </LocaleProvider>
      </QueryClientProvider>,
    );

    // Authenticated: the chrome (and its account menu) is on screen.
    const account = await screen.findByRole("button", { name: "Account" });
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

describe("TopBar (§2b contextual truth)", () => {
  it("shows the screen title and no actions that were not provided", () => {
    render(<TopBar route={{ screen: "deals" }} onOpenSearch={() => {}} />);
    expect(screen.getByText("Pipeline")).toBeTruthy();
    // exactly the four always-true controls: search, locale, theme, sign out
    expect(screen.getAllByRole("button")).toHaveLength(4);
  });

  // AC-shell-1k: every authenticated route resolves to real copy. This bites on
  // a new off-rail route landing in the router without a title key — the old
  // fallback rendered the raw screen slug.
  it("resolves a title for off-rail routes instead of the raw slug", () => {
    render(<TopBar route={{ screen: "dedupe" }} onOpenSearch={() => {}} />);
    expect(screen.getByText("Duplicates")).toBeTruthy();
  });

  it("opens search from the searchbar affordance (AC-shell-7 seam)", () => {
    const onOpenSearch = vi.fn();
    render(<TopBar route={{ screen: "home" }} onOpenSearch={onOpenSearch} />);
    screen.getByRole("button", { name: "Search" }).click();
    expect(onOpenSearch).toHaveBeenCalled();
  });
});

describe("Shell", () => {
  it("stamps body[data-screen] from the route", () => {
    window.location.hash = "#/reports";
    render(<Shell onOpenSearch={() => {}}>{null}</Shell>);
    expect(document.body.dataset.screen).toBe("reports");
  });

  it("renders rail-less for the documented exceptions (AC-shell layout exception)", () => {
    window.location.hash = "#/book";
    render(<Shell onOpenSearch={() => {}}>{null}</Shell>);
    expect(screen.queryByRole("navigation")).toBeNull();
  });

  // The consent screen is where a human hands an agent their own authority —
  // it must never be framed inside the app's own nav, which is what
  // RAIL_LESS_SCREENS carrying "oauth-consent" is for.
  it("renders rail-less for the OAuth consent screen", () => {
    window.location.hash = "#/oauth-consent";
    render(<Shell onOpenSearch={() => {}}>{null}</Shell>);
    expect(screen.queryByRole("navigation")).toBeNull();
  });

  it("renders the rail on core screens", () => {
    window.location.hash = "#/contacts";
    render(<Shell onOpenSearch={() => {}}>{null}</Shell>);
    expect(screen.getByRole("navigation")).toBeTruthy();
  });
});
