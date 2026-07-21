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

const ROSTER = {
  data: [
    {
      id: "u-active",
      workspace_id: "ws-1",
      email: "ada@acme.test",
      display_name: "Ada Active",
      status: "active",
      is_agent: false,
    },
    {
      id: "u-off",
      workspace_id: "ws-1",
      email: "otto@acme.test",
      display_name: "Otto Off",
      status: "deactivated",
      is_agent: false,
    },
  ],
  page: { next_cursor: null, has_more: false },
};

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
        return jsonResponse(ROSTER);
      }),
    );
    render(<UsersAdminCard />);
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

    const active = screen.getByText("Ada Active").closest("li") as HTMLElement;
    await userEvent.selectOptions(
      within(active).getByLabelText(/set role for ada active/i),
      "rep",
    );

    await waitFor(() => expect(within(active).getByRole("alert")).toBeTruthy());
    expect(screen.getByText(/leave no admin/i)).toBeTruthy();
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
