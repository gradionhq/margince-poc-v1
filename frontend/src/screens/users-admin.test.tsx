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
import { UsersAdminCard } from "./users-admin";

// The admin member-management card renders the include-inactive roster and drives
// the invite / role / deactivate / reactivate seams; the server stays the RBAC
// authority (this suite asserts the wire calls, not the gate).

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

// `roles` rides the roster only for an admin caller, which this card always is.
// Nora holds none — an unassigned seat is reachable and has no current role to
// show.
const ROSTER = {
  data: [
    {
      id: "u-active",
      workspace_id: "ws-1",
      email: "ada@acme.test",
      display_name: "Ada Active",
      status: "active",
      is_agent: false,
      roles: ["admin"],
    },
    {
      id: "u-off",
      workspace_id: "ws-1",
      email: "otto@acme.test",
      display_name: "Otto Off",
      status: "deactivated",
      is_agent: false,
      roles: ["read_only"],
    },
    {
      id: "u-none",
      workspace_id: "ws-1",
      email: "nora@acme.test",
      display_name: "Nora None",
      status: "active",
      is_agent: false,
      roles: [],
    },
  ],
  page: { next_cursor: null, has_more: false },
};

// Both helpers narrow by instance rather than asserting: a cast would let the
// suite read `.value` off whatever the query happened to return, so a control
// that stopped being a select would surface as a confusing undefined instead of
// a named failure.
function roleSelect(row: HTMLElement, name: string) {
  const control = within(row).getByLabelText(
    new RegExp(`set role for ${name}`, "i"),
  );
  if (!(control instanceof HTMLSelectElement)) {
    throw new Error(`the role control for ${name} is not a select`);
  }
  return control;
}

function rowFor(name: string) {
  const row = screen.getByText(name).closest("li");
  if (!(row instanceof HTMLElement)) {
    throw new Error(`no member row rendered for ${name}`);
  }
  return row;
}

function backend(calls: { method: string; url: string; body?: unknown }[]) {
  // openapi-fetch calls fetch(request) with a Request object, so read the
  // method + body off it rather than a separate init.
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const req =
      input instanceof Request ? input : new Request(String(input), init);
    if (req.url.endsWith("/v1/me")) {
      return jsonResponse({
        user: { email: "admin@acme.test" },
        roles: ["admin"],
        teams: [],
      });
    }
    if (req.url.includes("/users") && req.method === "GET") {
      return jsonResponse(ROSTER);
    }
    let body: unknown;
    try {
      body = await req.clone().json();
    } catch {
      body = undefined;
    }
    calls.push({ method: req.method, url: req.url, body });
    return jsonResponse({ ...ROSTER.data[0], id: "u-new" }, 201);
  });
}

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

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("UsersAdminCard", () => {
  it("shows an admin-only notice and no roster to a non-admin", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const req =
          input instanceof Request ? input : new Request(String(input), init);
        if (req.url.endsWith("/v1/me")) {
          return jsonResponse({
            user: { email: "rep@acme.test" },
            roles: ["rep"],
            teams: [],
          });
        }
        // A non-admin must never reach the roster — any other request is a
        // regression, so fail loudly rather than serving fixture data.
        throw new Error(`unexpected request: ${req.method} ${req.url}`);
      }),
    );
    render(<UsersAdminCard />);
    // The notice renders only after /me resolves (the card gates on that query),
    // so this cannot pass on the loading render.
    await waitFor(() => expect(screen.getByText(/admins only/i)).toBeTruthy());
    expect(screen.queryByText("Ada Active")).toBeNull();
  });

  it("renders the include-inactive roster with per-status actions", async () => {
    vi.stubGlobal("fetch", backend([]));
    render(<UsersAdminCard />);

    await waitFor(() => expect(screen.getByText("Ada Active")).toBeTruthy());
    expect(screen.getByText("Otto Off")).toBeTruthy();
    // The roster request opts into the inactive members.
    // (asserted indirectly: the deactivated member is present at all.)
    const active = screen.getByText("Ada Active").closest("li") as HTMLElement;
    const off = screen.getByText("Otto Off").closest("li") as HTMLElement;
    expect(within(active).getByText("Deactivate")).toBeTruthy();
    expect(within(off).getByText("Reactivate")).toBeTruthy();
  });

  it("invites a member with the entered email, name, and role", async () => {
    const calls: { method: string; url: string; body?: unknown }[] = [];
    vi.stubGlobal("fetch", backend(calls));
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Ada Active")).toBeTruthy());

    await userEvent.type(
      screen.getByPlaceholderText("name@company.com"),
      "new@acme.test",
    );
    await userEvent.type(
      screen.getByPlaceholderText("Full name"),
      "New Person",
    );
    await userEvent.click(screen.getByRole("button", { name: /invite/i }));

    await waitFor(() => {
      const post = calls.find(
        (c) => c.method === "POST" && c.url.endsWith("/users"),
      );
      expect(post).toBeTruthy();
      expect(post?.body).toEqual({
        email: "new@acme.test",
        display_name: "New Person",
        role: "rep",
      });
    });
  });

  it("deactivates an active member through the deactivate seam", async () => {
    const calls: { method: string; url: string; body?: unknown }[] = [];
    vi.stubGlobal("fetch", backend(calls));
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Ada Active")).toBeTruthy());

    const active = screen.getByText("Ada Active").closest("li") as HTMLElement;
    await userEvent.click(within(active).getByText("Deactivate"));
    // Deactivation is destructive (revokes sessions/passports): confirm first.
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(
      within(dialog).getByRole("button", { name: /deactivate/i }),
    );

    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "POST" && c.url.includes("/users/u-active/deactivate"),
        ),
      ).toBe(true),
    );
  });

  it("reads back each member's current role", async () => {
    vi.stubGlobal("fetch", backend([]));
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Ada Active")).toBeTruthy());

    expect(roleSelect(rowFor("Ada Active"), "ada active").value).toBe("admin");
    expect(roleSelect(rowFor("Otto Off"), "otto off").value).toBe("read_only");
  });

  it("offers the placeholder to a member holding no role", async () => {
    vi.stubGlobal("fetch", backend([]));
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Nora None")).toBeTruthy());

    const select = roleSelect(rowFor("Nora None"), "nora none");
    expect(select.value).toBe("");
    expect(within(select).getByText("Set role…")).toBeTruthy();
    // A member who already has a role gets no such option: it would be
    // selectable and do nothing.
    expect(
      within(roleSelect(rowFor("Ada Active"), "ada active")).queryByText(
        "Set role…",
      ),
    ).toBeNull();
  });

  // Any choice replaces the whole set, so a member holding several roles must
  // not read as a blank "Set role…" — that would let an admin strip privileges
  // they were never shown.
  it("names the roles a multi-role member holds", async () => {
    const twoRoles = {
      ...ROSTER,
      data: ROSTER.data.map((u) =>
        u.id === "u-none" ? { ...u, roles: ["manager", "ops"] } : u,
      ),
    };
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const req =
          input instanceof Request ? input : new Request(String(input), init);
        if (req.url.endsWith("/v1/me")) {
          return jsonResponse({
            user: { email: "admin@acme.test" },
            roles: ["admin"],
            teams: [],
          });
        }
        return jsonResponse(twoRoles);
      }),
    );
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Nora None")).toBeTruthy());

    const select = roleSelect(rowFor("Nora None"), "nora none");
    expect(select.value).toBe("");
    // Both held roles are named, under their display labels, and the copy says
    // what picking one does.
    const shown = within(select).getByText(/holds/i).textContent ?? "";
    expect(shown).toContain("Manager");
    expect(shown).toContain("Ops");
    expect(shown).toMatch(/replaces them all/i);
  });

  it("sets a member's role through the role seam", async () => {
    const calls: { method: string; url: string; body?: unknown }[] = [];
    vi.stubGlobal("fetch", backend(calls));
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Ada Active")).toBeTruthy());

    const active = screen.getByText("Ada Active").closest("li") as HTMLElement;
    await userEvent.selectOptions(
      within(active).getByLabelText(/set role for ada active/i),
      "manager",
    );

    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "PATCH" &&
            c.url.includes("/users/u-active/role") &&
            (c.body as { role?: string })?.role === "manager",
        ),
      ).toBe(true),
    );
  });

  it("reactivates a deactivated member", async () => {
    const calls: { method: string; url: string; body?: unknown }[] = [];
    vi.stubGlobal("fetch", backend(calls));
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Otto Off")).toBeTruthy());

    const off = screen.getByText("Otto Off").closest("li") as HTMLElement;
    await userEvent.click(within(off).getByText("Reactivate"));

    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "POST" && c.url.includes("/users/u-off/reactivate"),
        ),
      ).toBe(true),
    );
  });

  it("surfaces a failed member action as an inline alert on the row", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const req =
          input instanceof Request ? input : new Request(String(input), init);
        if (req.url.endsWith("/v1/me")) {
          return jsonResponse({
            user: { email: "admin@acme.test" },
            roles: ["admin"],
            teams: [],
          });
        }
        if (req.url.includes("/users") && req.method === "GET") {
          return jsonResponse(ROSTER);
        }
        return jsonResponse(
          { title: "Conflict", detail: "That would leave no admin." },
          409,
        );
      }),
    );
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Ada Active")).toBeTruthy());

    const active = rowFor("Ada Active");
    await userEvent.selectOptions(roleSelect(active, "ada active"), "rep");

    await waitFor(() => expect(within(active).getByRole("alert")).toBeTruthy());
    expect(screen.getByText(/leave no admin/i)).toBeTruthy();
    // The refused change left the role untouched, so the select must read the
    // role the member still holds — anything else would claim a change the
    // server rejected.
    expect(roleSelect(active, "ada active").value).toBe("admin");
  });

  // The whole reason the select returns to the held role after a refusal: a
  // select left showing the refused target would make re-picking it a no-op,
  // and the operator's retry would silently never reach the server.
  it("lets the same role be re-picked after a refusal", async () => {
    const patches: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const req =
          input instanceof Request ? input : new Request(String(input), init);
        if (req.url.endsWith("/v1/me")) {
          return jsonResponse({
            user: { email: "admin@acme.test" },
            roles: ["admin"],
            teams: [],
          });
        }
        if (req.url.includes("/users") && req.method === "GET") {
          return jsonResponse(ROSTER);
        }
        patches.push(req.url);
        return jsonResponse({ title: "Conflict", detail: "Try again." }, 409);
      }),
    );
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Ada Active")).toBeTruthy());

    const active = rowFor("Ada Active");
    await userEvent.selectOptions(roleSelect(active, "ada active"), "rep");
    await waitFor(() => expect(patches).toHaveLength(1));

    // The SAME target again — the retry the operator would make.
    await userEvent.selectOptions(roleSelect(active, "ada active"), "rep");
    await waitFor(() => expect(patches).toHaveLength(2));
  });

  // A settled mutation whose roster refetch is still outstanding would render
  // the member's replaced role from the stale cache — the operator would watch
  // their change appear and then undo itself.
  it("stays pending until the refreshed roster lands", async () => {
    let rosterReads = 0;
    // Built up front rather than captured lazily: the refetch has to be held
    // open from the moment it starts, and a deferred that only exists once the
    // request arrives would race the assertions below.
    let releaseRoster = () => {};
    const rosterHeld = new Promise<void>((resolve) => {
      releaseRoster = resolve;
    });
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const req =
          input instanceof Request ? input : new Request(String(input), init);
        if (req.url.endsWith("/v1/me")) {
          return jsonResponse({
            user: { email: "admin@acme.test" },
            roles: ["admin"],
            teams: [],
          });
        }
        if (req.url.includes("/users") && req.method === "GET") {
          rosterReads += 1;
          if (rosterReads === 1) {
            return jsonResponse(ROSTER);
          }
          // Hold the refetch open so the window between "mutation done" and
          // "new roster in hand" is observable rather than a race.
          await rosterHeld;
          return jsonResponse({
            ...ROSTER,
            data: ROSTER.data.map((u) =>
              u.id === "u-active" ? { ...u, roles: ["manager"] } : u,
            ),
          });
        }
        return jsonResponse({}, 200);
      }),
    );
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Ada Active")).toBeTruthy());

    const active = rowFor("Ada Active");
    await userEvent.selectOptions(roleSelect(active, "ada active"), "manager");
    await waitFor(() => expect(rosterReads).toBe(2));

    // Mid-flight: the row reads the role being applied and stays locked. "admin"
    // here would be the stale cache showing through.
    expect(roleSelect(active, "ada active").value).toBe("manager");
    expect(roleSelect(active, "ada active").disabled).toBe(true);

    releaseRoster();
    await waitFor(() =>
      expect(roleSelect(rowFor("Ada Active"), "ada active").disabled).toBe(
        false,
      ),
    );
    expect(roleSelect(rowFor("Ada Active"), "ada active").value).toBe(
      "manager",
    );
  });

  it("surfaces a failed invite as an inline error", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const req =
          input instanceof Request ? input : new Request(String(input), init);
        if (req.url.endsWith("/v1/me")) {
          return jsonResponse({
            user: { email: "admin@acme.test" },
            roles: ["admin"],
            teams: [],
          });
        }
        if (req.url.includes("/users") && req.method === "GET") {
          return jsonResponse(ROSTER);
        }
        return jsonResponse(
          { title: "Conflict", detail: "That email already exists." },
          409,
        );
      }),
    );
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Ada Active")).toBeTruthy());

    await userEvent.type(
      screen.getByPlaceholderText("name@company.com"),
      "dupe@acme.test",
    );
    await userEvent.type(screen.getByPlaceholderText("Full name"), "Dupe");
    await userEvent.click(screen.getByRole("button", { name: /invite/i }));

    await waitFor(() =>
      expect(screen.getByText(/already exists/i)).toBeTruthy(),
    );
  });
});
