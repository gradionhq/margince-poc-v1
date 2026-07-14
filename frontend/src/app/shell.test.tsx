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
import { LocaleProvider } from "../i18n";
import { Shell, TopBar, WorkspaceRail } from "./shell";

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
    await waitFor(() =>
      expect(client.getQueryData(["me"])).toBeUndefined(),
    );
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
