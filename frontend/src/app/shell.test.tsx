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

// B-EP09.4 acceptance: the canonical 9-item rail in order (AC-shell-1), at
// most one active item tracking the route (AC-shell-2), badges only from live
// counts, the contextual top bar, and the documented rail-less exceptions.

afterEach(() => {
  cleanup();
  window.location.hash = "";
  vi.unstubAllGlobals();
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

const CANONICAL_ORDER = [
  "Home",
  "Contacts",
  "Companies",
  "Leads",
  "Deals",
  "Tasks",
  "Inbox",
  "Reports",
  "Ask AI",
];

describe("WorkspaceRail (AC-shell-1/2)", () => {
  it("renders the canonical 9 items in order, logomark → home, avatar → settings", () => {
    render(<WorkspaceRail route={{ screen: "deals" }} />);
    const rail = screen.getByRole("navigation");
    const links = within(rail).getAllByRole("link");
    expect(links[0].getAttribute("aria-label")).toBe("Margince");
    expect(links[0].getAttribute("href")).toBe("#/home");
    const navLabels = links
      .slice(1, 10)
      .map((link) => link.getAttribute("aria-label"));
    expect(navLabels).toEqual(CANONICAL_ORDER);
    expect(links[10].getAttribute("href")).toBe("#/settings");
  });

  it("marks exactly one item active, matching the route", () => {
    render(<WorkspaceRail route={{ screen: "deals" }} />);
    const active = screen
      .getAllByRole("link")
      .filter((link) => link.getAttribute("aria-current") === "page");
    expect(active).toHaveLength(1);
    expect(active[0].getAttribute("aria-label")).toBe("Deals");
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
});

describe("WorkspaceRail sign-out (AS-1)", () => {
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
          <WorkspaceRail route={{ screen: "deals" }} />
        </LocaleProvider>
      </QueryClientProvider>,
    );
    expect(client.getQueryData(["me"])).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Sign out" }));
    // POST fired AND the whole cache was cleared — the ["me"] entry is gone,
    // so the auth gate re-probes → 401 → login. This assertion bites: it fails
    // if `onSuccess: () => queryClient.clear()` is removed from useLogout.
    await waitFor(() => expect(loggedOut).toBe(true));
    await waitFor(() => expect(client.getQueryData(["me"])).toBeUndefined());
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

    // Authenticated: the rail (and its sign-out control) is on screen.
    const signOut = await screen.findByRole("button", { name: "Sign out" });
    expect(meCalls).toBe(1);

    await userEvent.click(signOut);

    // The gate must re-probe /v1/me (not just drop the cache entry) and,
    // seeing 401, render the auth (signup/login) screen — the rail must be
    // gone. AuthScreen defaults to its signup mode, so assert on that
    // heading rather than assuming "Sign in" is the first thing shown.
    await screen.findByRole("heading", { name: "Create your workspace" });
    expect(screen.queryByRole("navigation")).toBeNull();
    expect(loggedOut).toBe(true);
    expect(meCalls).toBeGreaterThanOrEqual(2);
  });
});

describe("TopBar (§2b contextual truth)", () => {
  it("shows the screen title and no actions that were not provided", () => {
    render(<TopBar route={{ screen: "deals" }} onOpenSearch={() => {}} />);
    expect(screen.getByText("Deals")).toBeTruthy();
    // exactly the three always-true controls: search, locale, theme
    expect(screen.getAllByRole("button")).toHaveLength(3);
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

  it("renders the rail on core screens", () => {
    window.location.hash = "#/contacts";
    render(<Shell onOpenSearch={() => {}}>{null}</Shell>);
    expect(screen.getByRole("navigation")).toBeTruthy();
  });
});
