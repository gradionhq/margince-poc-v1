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
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { PipelinesCard, SettingsScreen } from "./settings";

// The settings identity + passport surfaces through the RBAC primitives:
// roles render as localized RoleBadges (a workspace-defined key stays raw),
// and the passport list's token slot reads as WITHHELD (FieldGuard mask) —
// the wire schema carries no token, and the row says so instead of omitting
// the field as if none existed.

beforeEach(() => {
  globalThis.localStorage.setItem("margince.workspaceSlug", "acme");
  vi.stubGlobal("fetch", settingsBackend());
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  globalThis.localStorage.clear();
});

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

// Routed by URL so every card on the screen gets an honest per-endpoint
// answer; the cards not under test render their empty states.
function settingsBackend() {
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input instanceof Request ? input.url : input);
    if (url.endsWith("/v1/me")) {
      return jsonResponse({
        user: { email: "ada@acme.test" },
        roles: ["admin", "field_marketing"],
        teams: [],
      });
    }
    if (url.includes("/passports")) {
      return jsonResponse({
        data: [
          {
            id: "pp-1",
            label: "Scout",
            scopes: ["read"],
            created_at: "2026-07-01T08:00:00Z",
            expires_at: null,
            revoked_at: null,
          },
        ],
        page: { next_cursor: null, has_more: false },
      });
    }
    return jsonResponse({
      data: [],
      page: { next_cursor: null, has_more: false },
    });
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

describe("SettingsScreen RBAC surfaces", () => {
  it("renders the session roles as localized badges on the default Account tab; a custom key stays its raw self", async () => {
    render(<SettingsScreen />);
    await waitFor(() => expect(screen.getByText("ada@acme.test")).toBeTruthy());
    expect(screen.getByText("Admin")).toBeTruthy();
    expect(screen.getByText("field_marketing")).toBeTruthy();
    // the seeded key never leaks raw once a label exists
    expect(screen.queryByText("admin")).toBeNull();
  });

  it("the passport row's token reads as withheld — masked, never re-disclosed — on the AI tab", async () => {
    render(<SettingsScreen tab="ai" />);
    await waitFor(() => expect(screen.getByText("Scout")).toBeTruthy());
    expect(screen.getByRole("img", { name: "Masked value" })).toBeTruthy();
    expect(screen.queryByText(/mgp_/)).toBeNull();
  });
});

// AS-2: the per-row Revoke kill-switch. A dedicated backend so the DELETE
// call can be asserted precisely, and a second passport is served already
// revoked to prove the button never shows on a row that's already dead.
function passportsBackend(opts: {
  onDelete?: (id: string) => void;
}) {
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input instanceof Request ? input.url : input);
    const method = input instanceof Request ? input.method : "GET";
    if (url.endsWith("/v1/me")) {
      return jsonResponse({
        user: { email: "ada@acme.test" },
        roles: ["admin"],
        teams: [],
      });
    }
    if (/\/passports\/[^/]+$/.test(url) && method === "DELETE") {
      const id = url.split("/passports/")[1];
      opts.onDelete?.(id);
      return new Response(null, { status: 204 });
    }
    if (url.includes("/passports")) {
      return jsonResponse({
        data: [
          {
            id: "pp-1",
            label: "Scout",
            scopes: ["read"],
            created_at: "2026-07-01T08:00:00Z",
            expires_at: null,
            revoked_at: null,
          },
          {
            id: "pp-2",
            label: "Retired",
            scopes: ["read"],
            created_at: "2026-06-01T08:00:00Z",
            expires_at: null,
            revoked_at: "2026-07-02T08:00:00Z",
          },
        ],
        page: { next_cursor: null, has_more: false },
      });
    }
    return jsonResponse({
      data: [],
      page: { next_cursor: null, has_more: false },
    });
  });
}

describe("PassportCard revoke (AS-2)", () => {
  it("revokes a non-revoked passport: click Revoke, confirm, DELETE fires with its id and the list refetches", async () => {
    const deleted: string[] = [];
    const fetchMock = passportsBackend({ onDelete: (id) => deleted.push(id) });
    vi.stubGlobal("fetch", fetchMock);
    render(<SettingsScreen tab="ai" />);
    await screen.findByText("Scout");

    // The already-revoked row shows no Revoke control at all.
    const retiredRow = screen.getByText("Retired").closest("li");
    expect(retiredRow).toBeTruthy();
    expect(
      retiredRow && Array.from(retiredRow.querySelectorAll("button")).length,
    ).toBe(0);

    const scoutRow = screen.getByText("Scout").closest("li");
    expect(scoutRow).toBeTruthy();
    const revokeButton = scoutRow?.querySelector("button");
    expect(revokeButton).toBeTruthy();
    await userEvent.click(revokeButton as HTMLButtonElement);

    const dialog = await screen.findByRole("dialog");
    const confirmButton = within(dialog).getByRole("button", {
      name: "Revoke",
    });
    const callsBeforeConfirm = fetchMock.mock.calls.length;
    await userEvent.click(confirmButton);

    await waitFor(() => expect(deleted).toEqual(["pp-1"]));
    // The list refetches after a successful revoke — more fetch calls landed
    // after confirm than just the single DELETE (the refetch GET /passports).
    await waitFor(() =>
      expect(fetchMock.mock.calls.length).toBeGreaterThan(
        callsBeforeConfirm + 1,
      ),
    );
  });
});

describe("SettingsScreen tab layout", () => {
  it("shows a settings-sections nav with the six tabs, Account current by default", () => {
    render(<SettingsScreen />);
    const nav = screen.getByRole("navigation", { name: /settings sections/i });
    expect(nav).toBeTruthy();
    for (const label of [
      "Account",
      "AI & autonomy",
      "Data model",
      "Catalog",
      "Privacy & consent",
      "Audit log",
    ]) {
      expect(
        screen.getByRole("link", { name: new RegExp(label, "i") }),
      ).toBeTruthy();
    }
    const account = screen.getByRole("link", { name: /Account/i });
    expect(account.getAttribute("aria-current")).toBe("page");
    expect(
      screen.getByRole("link", { name: /Data model/i }).getAttribute("href"),
    ).toBe("#/settings/data");
  });

  it("renders only the active tab's cards — the passport is off the Account tab", async () => {
    render(<SettingsScreen />);
    await waitFor(() => expect(screen.getByText("ada@acme.test")).toBeTruthy());
    // Scout lives on the AI tab; the default Account tab must not render it.
    expect(screen.queryByText("Scout")).toBeNull();
  });

  it("surfaces the custom-fields door on the Data model tab", () => {
    render(<SettingsScreen tab="data" />);
    expect(screen.getByRole("link", { name: /custom fields/i })).toBeTruthy();
  });

  it("surfaces the Products and Offer-templates doors on the Catalog tab", () => {
    render(<SettingsScreen tab="catalog" />);
    expect(
      screen.getByRole("link", { name: /products/i }).getAttribute("href"),
    ).toBe("#/products");
    expect(
      screen
        .getByRole("link", { name: /offer templates/i })
        .getAttribute("href"),
    ).toBe("#/offer-templates");
  });
});

// Routed by URL, with the pipelines list stubbed to the D-8 shape (an array
// with embedded stages) and a POST /stages hook so a test can inspect the exact
// body shipped.
function settingsStub(opts: {
  roles: string[];
  onStagePost?: (body: unknown) => void;
}) {
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input instanceof Request ? input.url : input);
    const method = input instanceof Request ? input.method : "GET";
    if (url.endsWith("/v1/me")) {
      return jsonResponse({
        user: { id: "u1", email: "a@acme.test", display_name: "A" },
        roles: opts.roles,
        teams: [],
      });
    }
    if (url.includes("/pipelines")) {
      return jsonResponse({
        data: [
          {
            id: "pl",
            workspace_id: "w",
            name: "Sales",
            is_default: true,
            position: 0,
            stages: [
              {
                id: "s1",
                workspace_id: "w",
                pipeline_id: "pl",
                name: "Qualify",
                position: 1,
                semantic: "open",
                win_probability: 20,
              },
            ],
          },
        ],
        page: { next_cursor: null },
      });
    }
    if (url.includes("/stages") && method === "POST") {
      const raw = input instanceof Request ? await input.clone().text() : "";
      const body = raw ? JSON.parse(raw) : {};
      opts.onStagePost?.(body);
      return jsonResponse(body);
    }
    return jsonResponse({ data: [], page: { next_cursor: null } });
  });
}

describe("PipelinesCard", () => {
  it("shows create controls for an admin", async () => {
    vi.stubGlobal("fetch", settingsStub({ roles: ["admin"] }));
    render(<PipelinesCard />);
    expect(await screen.findByText("New pipeline")).toBeTruthy();
  });
  it("hides create controls for a rep", async () => {
    vi.stubGlobal("fetch", settingsStub({ roles: ["rep"] }));
    render(<PipelinesCard />);
    await screen.findByText("Sales");
    expect(screen.queryByText("New pipeline")).toBeNull();
  });
  it("create stage posts the pipeline_id + semantic + win_probability", async () => {
    const posts: unknown[] = [];
    vi.stubGlobal(
      "fetch",
      settingsStub({ roles: ["admin"], onStagePost: (b) => posts.push(b) }),
    );
    render(<PipelinesCard />);
    await userEvent.click(await screen.findByTestId("new-stage-pl"));
    await userEvent.type(screen.getByLabelText(/Name/), "Discovery");
    await userEvent.type(screen.getByLabelText(/Win probability/), "15");
    await userEvent.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() =>
      expect(posts[0]).toMatchObject({
        pipeline_id: "pl",
        semantic: "open",
        win_probability: 15,
      }),
    );
  });
});
