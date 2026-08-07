/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
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
import { type GrantSpec, meFixture } from "../app/mefixture";
import { pickOption } from "../design-system/select-testing";
import { LocaleProvider } from "../i18n";
import { companyContextCapabilitiesQueryKey } from "./company-context";
import { AuditLogCard, PipelinesCard, SettingsScreen } from "./settings";

// The Organization tab group is composed from its MEMBERS: each tab opens on
// the write grant its own cards ask for, and the three objectless ones (users,
// privacy, audit) on the role. So a fixture that wants the nav has to name
// both — a role alone no longer buys the group, and a grant alone no longer
// buys the tabs the server gates on the role.
const PIPELINE_ADMIN: GrantSpec = { pipeline: ["create", "update"] };
const ORG_ADMIN: GrantSpec = {
  ...PIPELINE_ADMIN,
  custom_field: ["create", "update"],
};

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
      const me = meFixture({
        roles: ["admin", "field_marketing"],
        allow: ORG_ADMIN,
      });
      return jsonResponse({
        ...me,
        user: { ...me.user, email: "ada@acme.test" },
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

// The client comes back with the render so a test can read a query's settled
// state, not just the DOM: "the answer is in the cache" is the fact a nav
// assertion about an absent tab has to stand on.
const render = (ui: ReactNode) => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return {
    ...rtlRender(
      <QueryClientProvider client={client}>
        <LocaleProvider initial="en">{ui}</LocaleProvider>
      </QueryClientProvider>,
    ),
    client,
  };
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

  it("hides the admin-only AI usage & call-trace cards from a non-admin on the AI tab", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input instanceof Request ? input.url : input);
        if (url.endsWith("/v1/me")) {
          return jsonResponse({
            user: { email: "rep@acme.test" },
            roles: ["rep"],
            teams: [],
          });
        }
        return jsonResponse({
          data: [],
          page: { next_cursor: null, has_more: false },
        });
      }),
    );
    render(<SettingsScreen tab="ai" />);
    // A card the whole tab shares still renders for a rep...
    await waitFor(() =>
      expect(screen.getByText("Autonomy tiers")).toBeTruthy(),
    );
    // ...but the two cards whose endpoints require the automation grant are
    // absent, so a rep never hits a 403 error box (GET /ai/usage, /ai/calls).
    expect(screen.queryByText("AI usage & budget")).toBeNull();
    expect(screen.queryByText("AI call trace")).toBeNull();
  });
});

// AS-2: the per-row Revoke kill-switch. A dedicated backend so the DELETE
// call can be asserted precisely, and a second passport is served already
// revoked to prove the button never shows on a row that's already dead.
function passportsBackend(opts: { onDelete?: (id: string) => void }) {
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

// The governed tool console (IT-1): the /agent-tools inventory renders
// alongside an empty /passports list, so no row is dimmed and the
// egress badge shows only on the tool that reaches outside the workspace.
function agentToolsBackend() {
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input instanceof Request ? input.url : input);
    if (url.endsWith("/v1/me")) {
      return jsonResponse({
        user: { email: "ada@acme.test" },
        roles: ["admin"],
        teams: [],
      });
    }
    if (url.includes("/agent-tools")) {
      return jsonResponse({
        data: [
          {
            name: "search_records",
            title: "Search records",
            description:
              'Find people, organizations, deals, leads and projects by name. (Governance: runs immediately; requires passport scope "read".)',
            required_scope: "read",
            tier: "auto_execute",
            egress: false,
          },
          {
            name: "send_email",
            title: "Send an email",
            description:
              'Put a mail on the wire to a real recipient, exactly as it is given. (Governance: a person approves every call before it runs; requires passport scope "send".)',
            required_scope: "send",
            tier: "confirmation_required",
            egress: true,
          },
        ],
      });
    }
    return jsonResponse({
      data: [],
      page: { next_cursor: null, has_more: false },
    });
  });
}

describe("AgentToolsCard (IT-1)", () => {
  it("renders the governed tool inventory with the egress badge on send_email", async () => {
    vi.stubGlobal("fetch", agentToolsBackend());
    render(<SettingsScreen tab="ai" />);

    await waitFor(() =>
      expect(screen.getAllByText("search_records").length).toBe(1),
    );
    expect(screen.getAllByText("send_email").length).toBe(1);

    const searchRow = document.querySelector('[data-tool="search_records"]');
    const sendRow = document.querySelector('[data-tool="send_email"]');
    expect(searchRow).toBeTruthy();
    expect(sendRow).toBeTruthy();
    // The egress "reaches out" badge shows only on the tool that reaches
    // outside the workspace (send_email), never on the pure-read tool.
    expect(
      sendRow && within(sendRow as HTMLElement).getByText("reaches out"),
    ).toBeTruthy();
    expect(
      searchRow && within(searchRow as HTMLElement).queryByText("reaches out"),
    ).toBeNull();
  });

  // The console's own promise is that it shows the surface an MCP client sees,
  // and a verb with an autonomy dot beside it is not that: what an agent
  // selects on is the written description the server serves, so the row has to
  // show it rather than leave an operator to guess what their agents are told.
  it("shows each tool's written display name and the text an agent selects it by", async () => {
    vi.stubGlobal("fetch", agentToolsBackend());
    render(<SettingsScreen tab="ai" />);

    await waitFor(() =>
      expect(screen.getAllByText("search_records").length).toBe(1),
    );
    const searchRow = document.querySelector(
      '[data-tool="search_records"]',
    ) as HTMLElement | null;
    expect(searchRow).toBeTruthy();
    expect(
      searchRow && within(searchRow).getByText("Search records"),
    ).toBeTruthy();
    expect(
      searchRow &&
        within(searchRow).getByText(/Find people, organizations, deals/),
    ).toBeTruthy();
    // Governance travels with it, because the server appends it to the same
    // string — the console must not show a shortened reading of what an agent
    // was actually told.
    expect(
      searchRow && within(searchRow).getByText(/Governance: runs immediately/),
    ).toBeTruthy();

    // The confirm-first row too, and not only the 🟢 one: a regression that
    // dropped the title or the description from the row an operator most needs
    // to read — the one that leaves the workspace — would otherwise pass here.
    const sendRow = document.querySelector(
      '[data-tool="send_email"]',
    ) as HTMLElement | null;
    expect(sendRow).toBeTruthy();
    expect(sendRow && within(sendRow).getByText("Send an email")).toBeTruthy();
    expect(
      sendRow && within(sendRow).getByText(/Put a mail on the wire/),
    ).toBeTruthy();
    expect(
      sendRow &&
        within(sendRow).getByText(/Governance: a person approves every call/),
    ).toBeTruthy();
    expect(sendRow && within(sendRow).getByText("send")).toBeTruthy();
  });
});

// Both /passports and /agent-tools served together so the passport
// selector's filtering and the reachability computation can be exercised
// against the same fixture: one live passport, one revoked, and one
// scope-free tool alongside a scoped one the live passport doesn't cover.
function agentToolsWithPassportsBackend() {
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input instanceof Request ? input.url : input);
    if (url.endsWith("/v1/me")) {
      return jsonResponse({
        user: { email: "ada@acme.test" },
        roles: ["admin"],
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
    if (url.includes("/agent-tools")) {
      return jsonResponse({
        data: [
          {
            name: "list_pipelines",
            title: "List pipelines and their stages",
            description:
              'Every pipeline with its live stages. (Governance: runs immediately; requires passport scope "read".)',
            required_scope: null,
            tier: "auto_execute",
            egress: false,
          },
          {
            name: "send_email",
            title: "Send an email",
            description:
              'Put a mail on the wire to a real recipient, exactly as it is given. (Governance: a person approves every call before it runs; requires passport scope "send".)',
            required_scope: "send",
            tier: "confirmation_required",
            egress: true,
          },
        ],
      });
    }
    return jsonResponse({
      data: [],
      page: { next_cursor: null, has_more: false },
    });
  });
}

describe("AgentToolsCard passport scoping", () => {
  it("excludes a revoked passport from the selector", async () => {
    const user = userEvent.setup();
    vi.stubGlobal("fetch", agentToolsWithPassportsBackend());
    render(<SettingsScreen tab="ai" />);
    await screen.findByText("list_pipelines");

    // The options only exist while the popup is open — the control renders no
    // listbox when closed — so reading what it offers means opening it first.
    await user.click(screen.getByLabelText("All passports"));
    const optionLabels = screen
      .getAllByRole("option")
      .map((option) => option.textContent);
    expect(optionLabels).toContain("Reachable by Scout");
    expect(optionLabels).not.toContain("Reachable by Retired");
  });

  it("keeps a scope-free tool reachable once a passport is selected", async () => {
    const user = userEvent.setup();
    vi.stubGlobal("fetch", agentToolsWithPassportsBackend());
    render(<SettingsScreen tab="ai" />);
    await screen.findByText("list_pipelines");

    const select = screen.getByLabelText("All passports");
    await pickOption(user, select, "Reachable by Scout");

    const freeRow = document.querySelector('[data-tool="list_pipelines"]');
    expect(freeRow).toBeTruthy();
    expect(
      freeRow &&
        within(freeRow as HTMLElement).queryByText("scope not granted"),
    ).toBeNull();

    const scopedRow = document.querySelector('[data-tool="send_email"]');
    expect(scopedRow).toBeTruthy();
    expect(
      scopedRow &&
        within(scopedRow as HTMLElement).getByText("scope not granted"),
    ).toBeTruthy();
  });
});

// The tool console and the passport list share the ["passports"] read, so a
// revoke on one card refetches the other's options. This backend answers the
// second read honestly: the revoked passport comes back marked revoked.
function revocablePassportsBackend() {
  const revoked = new Set<string>();
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
      revoked.add(url.split("/passports/")[1]);
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
            revoked_at: revoked.has("pp-1") ? "2026-07-03T08:00:00Z" : null,
          },
        ],
        page: { next_cursor: null, has_more: false },
      });
    }
    if (url.includes("/agent-tools")) {
      return jsonResponse({
        data: [
          {
            name: "send_email",
            title: "Send an email",
            description:
              'Put a mail on the wire to a real recipient, exactly as it is given. (Governance: a person approves every call before it runs; requires passport scope "send".)',
            required_scope: "send",
            tier: "confirmation_required",
            egress: true,
          },
        ],
      });
    }
    return jsonResponse({
      data: [],
      page: { next_cursor: null, has_more: false },
    });
  });
}

describe("PassportCard revoke (AS-2)", () => {
  // Revoking the passport the console was filtered by leaves the selector
  // showing "All passports". The inventory has to say the same thing: a row
  // dimmed by a credential the human can no longer choose is a filter with no
  // control, and no way to undo it.
  it("stops scoping the tool console to a passport revoked while it was selected", async () => {
    const user = userEvent.setup();
    vi.stubGlobal("fetch", revocablePassportsBackend());
    render(<SettingsScreen tab="ai" />);
    await screen.findByText("send_email");

    const select = screen.getByLabelText("All passports");
    await pickOption(user, select, "Reachable by Scout");
    const scopedRow = document.querySelector('[data-tool="send_email"]');
    expect(scopedRow).toBeTruthy();
    // Scout grants "read" only, so the send tool reads as out of scope while
    // Scout is the filter.
    expect(
      scopedRow &&
        within(scopedRow as HTMLElement).getByText("scope not granted"),
    ).toBeTruthy();

    const scoutRow = screen.getByText("Scout").closest("li");
    const revokeButton = scoutRow?.querySelector("button");
    if (!(revokeButton instanceof HTMLButtonElement)) {
      throw new Error("the live passport row offers no revoke control");
    }
    await user.click(revokeButton);
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "Revoke" }));

    // The revoked passport leaves the selector, which is left offering the
    // "all passports" choice and nothing else. That list only exists while the
    // popup is open, so reopen it: it stays mounted and re-renders as the
    // refetched passports arrive, which is what this waitFor waits for...
    await user.click(select);
    await waitFor(() =>
      expect(
        screen.getAllByRole("option").map((option) => option.textContent),
      ).toEqual(["All passports"]),
    );
    // ...and the inventory reads unfiltered again, matching what it shows.
    expect(
      scopedRow &&
        within(scopedRow as HTMLElement).queryByText("scope not granted"),
    ).toBeNull();
  });

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
  // These layout assertions run as an admin holding the org grants, so every
  // tab under test is present. Which principal sees which tab is the
  // Organization-group suite's subject, not this one's.
  beforeEach(() => {
    vi.stubGlobal("fetch", settingsBackend());
  });

  it("groups the nav into personal and organization tabs, Account current by default", async () => {
    render(<SettingsScreen />);
    const nav = screen.getByRole("navigation", { name: /settings sections/i });
    expect(nav).toBeTruthy();
    // The organization tabs appear once the /me role probe resolves to admin.
    await waitFor(() =>
      expect(screen.getByRole("link", { name: /Data model/i })).toBeTruthy(),
    );
    for (const label of [
      "Account",
      "Voice DNA",
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

  it("surfaces the custom-fields door on the Data model tab", async () => {
    render(<SettingsScreen tab="data" />);
    // Org tab: visible once /me resolves the custom_field write grant.
    await waitFor(() =>
      expect(screen.getByRole("link", { name: /custom fields/i })).toBeTruthy(),
    );
  });

  it("surfaces the Products and Offer-templates doors on the Catalog tab", async () => {
    render(<SettingsScreen tab="catalog" />);
    await waitFor(() =>
      expect(screen.getByRole("link", { name: /products/i })).toBeTruthy(),
    );
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

// The nav, driven by exactly the two things the Organization group composes:
// the grant map /me carries and the company-context rollout flag. Every other
// endpoint answers empty, so a failure here can only be about visibility.
function orgNavBackend(opts: {
  roles: string[];
  allow?: GrantSpec;
  companyReadEnabled?: boolean;
}) {
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input instanceof Request ? input.url : input);
    if (url.endsWith("/v1/me")) {
      return jsonResponse(
        meFixture({ roles: opts.roles, allow: opts.allow ?? {} }),
      );
    }
    if (url.includes("/company/context/capabilities")) {
      const enabled = opts.companyReadEnabled ?? false;
      return jsonResponse({
        rollout: enabled ? "read" : "off",
        read_enabled: enabled,
        tasks_enabled: false,
        onboarding_enabled: false,
      });
    }
    return jsonResponse({
      data: [],
      page: { next_cursor: null, has_more: false },
    });
  });
}

// The same backend with the rollout answer on a valve the test opens. The nav
// can then be read at two named moments — flag unanswered, flag answered "off"
// — instead of whichever of the two the event loop happens to serve first.
function orgNavBackendHoldingCapabilities(opts: {
  roles: string[];
  allow?: GrantSpec;
}) {
  const answer = orgNavBackend({ ...opts, companyReadEnabled: false });
  let release: (() => void) | undefined;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input instanceof Request ? input.url : input);
    if (url.includes("/company/context/capabilities")) {
      await held;
    }
    return answer(input);
  });
  return { fetchMock, answerCapabilities: () => release?.() };
}

// The settings tabs currently in the nav, in render order — personal group
// first, then the organization group. Asserting the WHOLE list rather than one
// membership is the point: a predicate wired to the wrong object shows up as
// an extra or a missing entry, where a single getBy would pass regardless.
function navTabs(): string[] {
  return screen
    .getAllByRole("link")
    .filter((link) =>
      (link.getAttribute("href") ?? "").startsWith("#/settings/"),
    )
    .map((link) => link.textContent ?? "");
}

// The personal group, which no grant gates — every case below shows exactly
// these four, and the assertions differ only in what follows them.
const PERSONAL_TABS = ["Account", "Voice DNA", "AI & autonomy", "Integrations"];

describe("SettingsScreen Organization group", () => {
  // The group is composed from its members: a tab appears when the principal
  // holds a write grant on what its cards author, or — for the three surfaces
  // with no RBAC object — when they hold the role the server gates them on.

  it("collapses to Overlay alone for a principal holding neither an org grant nor an admin role", async () => {
    // Overlay is the group's one unconditional member (the topbar's
    // system-of-record chip points every seat at it), so the heading survives
    // while every gated member is gone — this is the group hiding, as far as
    // the group can hide.
    vi.stubGlobal("fetch", orgNavBackend({ roles: ["rep"] }));
    render(<SettingsScreen />);
    // /me has to have SETTLED before an emptiness claim means anything: a nav
    // read mid-flight is empty for every principal.
    await screen.findByText("test@example.test");
    expect(navTabs()).toEqual([...PERSONAL_TABS, "Overlay"]);
  });

  it("renders the group for a single visible member — a lone custom_field write opens Data model", async () => {
    vi.stubGlobal(
      "fetch",
      orgNavBackend({ roles: ["rep"], allow: { custom_field: ["update"] } }),
    );
    render(<SettingsScreen />);
    await waitFor(() =>
      expect(navTabs()).toEqual([...PERSONAL_TABS, "Data model", "Overlay"]),
    );
  });

  it("renders the group for a single visible member — a lone embedding_reindex write opens Data model", async () => {
    // The Data model tab's other half. Granting it alone is what separates the
    // predicate from its sibling: a `data` wired to the catalog objects, or a
    // `rates` wired to this one, shows up as a tab the whole-list assertion
    // does not expect.
    vi.stubGlobal(
      "fetch",
      orgNavBackend({
        roles: ["rep"],
        allow: { embedding_reindex: ["update"] },
      }),
    );
    render(<SettingsScreen />);
    await waitFor(() =>
      expect(navTabs()).toEqual([...PERSONAL_TABS, "Data model", "Overlay"]),
    );
  });

  it("opens Rates & costs for a lone fx_rate write, with Catalog and Data model absent", async () => {
    // The rate sheets are the only cards on the tab, and fx_rate is one of the
    // two objects they author — so this grant alone has to open it, and the
    // neighbouring tabs have to stay shut.
    vi.stubGlobal(
      "fetch",
      orgNavBackend({ roles: ["rep"], allow: { fx_rate: ["create"] } }),
    );
    render(<SettingsScreen />);
    await waitFor(() =>
      expect(navTabs()).toEqual([...PERSONAL_TABS, "Rates & costs", "Overlay"]),
    );
  });

  it("opens Rates & costs for a lone ai_model_rate write", async () => {
    // The other half of the same predicate: either object on its own opens the
    // tab, so the union has to be read as a union and not as one object with a
    // decorative second term.
    vi.stubGlobal(
      "fetch",
      orgNavBackend({ roles: ["rep"], allow: { ai_model_rate: ["update"] } }),
    );
    render(<SettingsScreen />);
    await waitFor(() =>
      expect(navTabs()).toEqual([...PERSONAL_TABS, "Rates & costs", "Overlay"]),
    );
  });

  it("opens Catalog for a lone offer_template write", async () => {
    // Catalog's third member, held here without pipeline or product, so the
    // tab can only have come from the offer_template term.
    vi.stubGlobal(
      "fetch",
      orgNavBackend({
        roles: ["rep"],
        allow: { offer_template: ["create", "update"] },
      }),
    );
    render(<SettingsScreen />);
    await waitFor(() =>
      expect(navTabs()).toEqual([...PERSONAL_TABS, "Catalog", "Overlay"]),
    );
  });

  it("opens Catalog for a manager on product writes alone, with no pipeline grant", async () => {
    // The seeded manager holds pipeline read-only and product create/update/
    // delete, so this is the case the role check used to hide: the cards on
    // the tab would serve them, the nav would not.
    vi.stubGlobal(
      "fetch",
      orgNavBackend({
        roles: ["manager"],
        allow: { pipeline: ["read"], product: ["create", "update", "delete"] },
      }),
    );
    render(<SettingsScreen />);
    await waitFor(() =>
      expect(navTabs()).toEqual([...PERSONAL_TABS, "Catalog", "Overlay"]),
    );
  });

  it("opens Catalog and Company for a rep holding the writes the seeded matrix gives them", async () => {
    // A rep creates and updates products, offer templates and organizations,
    // and deletes none of them — so the predicate has to accept any write verb
    // rather than insist on one.
    vi.stubGlobal(
      "fetch",
      orgNavBackend({
        roles: ["rep"],
        allow: {
          pipeline: ["read"],
          product: ["create", "read", "update"],
          offer_template: ["create", "read", "update"],
          organization: ["create", "read", "update"],
          custom_field: ["read"],
        },
        companyReadEnabled: true,
      }),
    );
    render(<SettingsScreen />);
    await waitFor(() =>
      expect(navTabs()).toEqual([
        ...PERSONAL_TABS,
        "Company context",
        "Catalog",
        "Overlay",
      ]),
    );
  });

  it("keeps users, privacy and audit on the role — every object write in the matrix does not buy them", async () => {
    vi.stubGlobal(
      "fetch",
      orgNavBackend({
        roles: ["manager"],
        allow: {
          pipeline: ["create", "update", "delete"],
          product: ["create", "update", "delete"],
          offer_template: ["create", "update", "delete"],
          fx_rate: ["create", "update"],
          ai_model_rate: ["create", "update"],
          custom_field: ["create", "update", "delete"],
          embedding_reindex: ["update"],
          organization: ["create", "update", "delete"],
        },
        companyReadEnabled: true,
      }),
    );
    render(<SettingsScreen />);
    await waitFor(() =>
      expect(navTabs()).toEqual([
        ...PERSONAL_TABS,
        "Company context",
        "Data model",
        "Catalog",
        "Rates & costs",
        "Overlay",
      ]),
    );
  });

  it("gives ops the three objectless tabs while it holds no object grant at all", async () => {
    // The mirror of the case above: those three surfaces have no RBAC object
    // for a grant to name, so the role is what the server checks and what the
    // nav must check. Installation is deliberately NOT among them: it carries
    // its own `installation_settings` object (ADR-0090/A135), so it follows
    // the grant like catalog and rates do, and an ops principal holding no
    // object grant does not get it.
    vi.stubGlobal("fetch", orgNavBackend({ roles: ["ops"] }));
    render(<SettingsScreen />);
    await waitFor(() =>
      expect(navTabs()).toEqual([
        ...PERSONAL_TABS,
        "Users & roles",
        "Privacy & consent",
        "Audit log",
        "Overlay",
      ]),
    );
  });

  it("shows Company to an admin holding organization writes once the rollout flag is on", async () => {
    vi.stubGlobal(
      "fetch",
      orgNavBackend({
        roles: ["admin"],
        allow: { organization: ["create", "update"] },
        companyReadEnabled: true,
      }),
    );
    render(<SettingsScreen />);
    expect(
      await screen.findByRole("link", { name: "Company context" }),
    ).toBeTruthy();
  });

  it("withholds Company from the same admin while the rollout flag is off — before the flag answers and after", async () => {
    // The flag is a deployment posture, not a permission, so it ANDs with the
    // grant: the surface may simply not exist on this installation. An unknown
    // flag therefore reads as "off" — a tab that appears while the answer is in
    // flight and then vanishes has already offered a surface this installation
    // may not have.
    const { fetchMock, answerCapabilities } = orgNavBackendHoldingCapabilities({
      roles: ["admin"],
      allow: { organization: ["create", "update"] },
    });
    vi.stubGlobal("fetch", fetchMock);
    const { client } = render(<SettingsScreen />);
    const ADMIN_ORG_TABS = [
      ...PERSONAL_TABS,
      "Users & roles",
      "Privacy & consent",
      "Audit log",
      "Overlay",
    ];

    // Moment one: the nav is fully composed from /me — its three role-gated
    // tabs are on screen — while the flag is still unanswered, because this
    // test holds the answer.
    await screen.findByRole("link", { name: "Audit log" });
    expect(navTabs()).toEqual(ADMIN_ORG_TABS);

    // Moment two: the answer is in the cache, which is the fact the emptiness
    // claim needs — the request having been SENT proves nothing about what the
    // nav has rendered.
    answerCapabilities();
    await waitFor(() =>
      expect(
        client.getQueryState(companyContextCapabilitiesQueryKey)?.status,
      ).toBe("success"),
    );
    expect(navTabs()).toEqual(ADMIN_ORG_TABS);
  });
});

// Overlay had outgrown one card in Integrations (connect + sync/budget
// health + user mapping) — it now gets its own org tab. `system_of_record`
// is stubbed explicitly per test: the tab must stay reachable in native
// mode (a workspace is native until an overlay is connected, so gating the
// tab on overlay mode would hide the only place to connect one), and the
// card must be entirely gone from Integrations regardless of mode.
function overlaySettingsBackend(opts: {
  roles: string[];
  sorMode: "native" | "overlay";
}) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input instanceof Request ? input.url : input);
    // The two overlay reads below answer GET only. A mock that answers any verb
    // hands a successful payload to a request the real endpoint would reject,
    // so a client sending the wrong one would still pass here.
    const method = (
      input instanceof Request ? input.method : (init?.method ?? "GET")
    ).toUpperCase();
    if (url.endsWith("/v1/me")) {
      const me = meFixture({ roles: opts.roles });
      return jsonResponse({
        ...me,
        user: { ...me.user, email: "ada@acme.test" },
        system_of_record: { mode: opts.sorMode },
      });
    }
    if (url.includes("/overlay/connection")) {
      return jsonResponse({ detail: "not found" }, 404);
    }
    if (url.includes("/overlay/user-map") && method === "GET") {
      return jsonResponse({
        incumbent: "hubspot",
        entries: [],
        next_cursor: null,
      });
    }
    if (url.includes("/overlay/owners") && method === "GET") {
      return jsonResponse({
        incumbent: "hubspot",
        owners: [],
        truncated: false,
      });
    }
    return jsonResponse({
      data: [],
      page: { next_cursor: null, has_more: false },
    });
  });
}

describe("SettingsScreen overlay tab", () => {
  it("gives overlay its own org tab, reachable before any overlay is connected", async () => {
    vi.stubGlobal(
      "fetch",
      overlaySettingsBackend({ roles: ["admin"], sorMode: "native" }),
    );
    render(<SettingsScreen tab="overlay" />);
    await waitFor(() =>
      expect(
        screen
          .getByRole("link", { name: /overlay/i })
          .getAttribute("aria-current"),
      ).toBe("page"),
    );
    // OverlayCard's own heading — proof its connect flow rendered on this
    // tab even though the workspace has never connected an overlay.
    expect(
      await screen.findByRole("heading", { name: "HubSpot mirror" }),
    ).toBeTruthy();
  });

  it("no longer renders the overlay card under Integrations", async () => {
    vi.stubGlobal(
      "fetch",
      overlaySettingsBackend({ roles: ["admin"], sorMode: "overlay" }),
    );
    render(<SettingsScreen tab="integrations" />);
    await waitFor(() =>
      expect(screen.getByRole("link", { name: /integrations/i })).toBeTruthy(),
    );
    expect(
      screen.queryByRole("heading", { name: "HubSpot mirror" }),
    ).toBeNull();
  });

  // The system-of-record chip is shown to every seat and links here, so a
  // rep who follows it must land on the tab, not on the Account fallback.
  // Tab-level hiding would buy no confidentiality either: the mapping
  // card's reads are admin/ops-only on the server, and the card keeps them
  // unsent for anyone else — so a rep sees the connection card's read-only
  // state and the mapping card's admin-only notice, never the directory.
  it("shows the Overlay tab to a non-admin rep with both cards in their read-only state", async () => {
    const fetchMock = overlaySettingsBackend({
      roles: ["rep"],
      sorMode: "native",
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<SettingsScreen tab="overlay" />);
    await waitFor(() =>
      expect(
        screen
          .getByRole("link", { name: /overlay/i })
          .getAttribute("aria-current"),
      ).toBe("page"),
    );
    expect(
      await screen.findByRole("heading", { name: "HubSpot mirror" }),
    ).toBeTruthy();
    expect(
      await screen.findByText("You do not have permission to connect HubSpot."),
    ).toBeTruthy();
    expect(
      await screen.findByText(
        "You do not have permission to review who is mapped.",
      ),
    ).toBeTruthy();
    // No mapping table, no grouping toggle, and — the point of the card's
    // own gate — no request that could only have come back 403.
    expect(screen.queryByRole("group", { name: "Grouping" })).toBeNull();
    expect(screen.queryByRole("button", { name: "By user" })).toBeNull();
    const requested = fetchMock.mock.calls.map(([input]) => String(input));
    expect(
      requested.some((url) => url.includes("/overlay/user-map")),
    ).toBeFalsy();
    expect(
      requested.some((url) => url.includes("/overlay/owners")),
    ).toBeFalsy();
  });
});

// Routed by URL, with the pipelines list stubbed to the D-8 shape (an array
// with embedded stages) and a POST /stages hook so a test can inspect the exact
// body shipped.
function settingsStub(opts: {
  roles: string[];
  allow?: GrantSpec;
  onStagePost?: (body: unknown) => void;
}) {
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input instanceof Request ? input.url : input);
    const method = input instanceof Request ? input.method : "GET";
    if (url.endsWith("/v1/me")) {
      return jsonResponse(
        meFixture({ roles: opts.roles, allow: opts.allow ?? PIPELINE_ADMIN }),
      );
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
    vi.stubGlobal("fetch", settingsStub({ roles: ["rep"], allow: {} }));
    render(<PipelinesCard />);
    await screen.findByText("Sales");
    expect(screen.queryByText("New pipeline")).toBeNull();
  });
  // One grant at a time: create and update govern different controls, and a
  // fixture holding both cannot tell a correct binding from a transposed one.
  it("offers stage editing on update alone, without the create affordance", async () => {
    vi.stubGlobal(
      "fetch",
      settingsStub({ roles: ["admin"], allow: { pipeline: ["update"] } }),
    );
    render(<PipelinesCard />);
    await screen.findByText("Sales");
    expect(screen.getByTestId("new-stage-pl")).toBeTruthy();
    expect(screen.queryByText("New pipeline")).toBeNull();
  });

  it("offers the create affordance on create alone, without stage editing", async () => {
    vi.stubGlobal(
      "fetch",
      settingsStub({ roles: ["admin"], allow: { pipeline: ["create"] } }),
    );
    render(<PipelinesCard />);
    expect(await screen.findByText("New pipeline")).toBeTruthy();
    expect(screen.queryByTestId("new-stage-pl")).toBeNull();
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

// One audit-log entry carrying a full attribution trail (before/after diff,
// agent passport, on-behalf-of human, authorization rule, and evidence) so
// the expand panel has every field to render honestly.
const auditEntry = {
  id: "al-1",
  workspace_id: "w",
  actor_type: "agent",
  actor_id: "agent:sdr",
  passport_id: "pp-9",
  on_behalf_of: "u-1",
  action: "update",
  entity_type: "person",
  entity_id: "p-1",
  before: { stage: "new" },
  after: { stage: "qualified" },
  authorization_rule: "role:admin",
  evidence: { snippet: "Reply confirmed budget", source: "email:msg-1" },
  occurred_at: "2026-07-10T09:00:00Z",
};

function auditLogBackend() {
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input instanceof Request ? input.url : input);
    if (url.includes("/audit-log")) {
      return jsonResponse({
        data: [auditEntry],
        page: { next_cursor: null, has_more: false },
      });
    }
    return jsonResponse({
      data: [],
      page: { next_cursor: null, has_more: false },
    });
  });
}

// The danger-zone Reset data action: server-driven, gated on the literal admin
// role AND me.non_production. A dedicated backend per test so the role/posture
// combination is explicit rather than layered on the shared settingsBackend
// default. `allow` defaults to the custom_field writes that open the Data tab
// the card lives on — a test about the card should not also have to argue its
// way onto the tab, and one that wants the tab CLOSED says so with `{}`.
function resetDataBackend(opts: {
  roles: string[];
  nonProduction: boolean;
  allow?: GrantSpec;
  onReset?: (body: unknown) => void;
  resetStatus?: number;
  resetBody?: unknown;
}) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input instanceof Request ? input.url : input);
    const method = (
      input instanceof Request ? input.method : (init?.method ?? "GET")
    ).toUpperCase();
    if (url.endsWith("/v1/me")) {
      const me = meFixture({
        roles: opts.roles,
        allow: opts.allow ?? { custom_field: ["create", "update"] },
      });
      return jsonResponse({
        ...me,
        user: { ...me.user, email: "ada@acme.test" },
        workspace_name: "Acme Inc",
        non_production: opts.nonProduction,
      });
    }
    if (url.includes("/admin/reset-data") && method === "POST") {
      const raw = input instanceof Request ? await input.clone().text() : "";
      const body = raw ? JSON.parse(raw) : {};
      opts.onReset?.(body);
      if (opts.resetStatus && opts.resetStatus !== 200) {
        return jsonResponse(
          opts.resetBody ?? { detail: "confirmation mismatch" },
          opts.resetStatus,
        );
      }
      return jsonResponse(
        opts.resetBody ?? { status: "reset", tables_cleared: 3 },
      );
    }
    return jsonResponse({
      data: [],
      page: { next_cursor: null, has_more: false },
    });
  });
}

describe("ResetDataCard (danger zone)", () => {
  it("shows the Reset data control for an admin in a non-production posture", async () => {
    vi.stubGlobal(
      "fetch",
      resetDataBackend({ roles: ["admin"], nonProduction: true }),
    );
    render(<SettingsScreen tab="data" />);
    expect(await screen.findByText(/reset data/i)).toBeTruthy();
  });

  it("hides Reset data for an admin in a production posture", async () => {
    vi.stubGlobal(
      "fetch",
      resetDataBackend({ roles: ["admin"], nonProduction: false }),
    );
    render(<SettingsScreen tab="data" />);
    await waitFor(() =>
      expect(screen.getByRole("link", { name: /custom fields/i })).toBeTruthy(),
    );
    expect(screen.queryByText(/reset data/i)).toBeNull();
  });

  it("hides Reset data from a rep even in a non-production posture", async () => {
    vi.stubGlobal(
      "fetch",
      // A rep holds custom_field read-only and no embedding_reindex at all, so
      // the Data tab is not theirs to reach in the first place.
      resetDataBackend({ roles: ["rep"], nonProduction: true, allow: {} }),
    );
    render(<SettingsScreen tab="data" />);
    // With no member grant, the rep falls back to Account — proven here by
    // the identity card rendering instead of anything data-tab-shaped.
    await waitFor(() => expect(screen.getByText("ada@acme.test")).toBeTruthy());
    expect(screen.queryByText(/reset data/i)).toBeNull();
  });

  // The card is admin-ONLY, narrower than the "data" tab that hosts it: the
  // server's auth.RequireAdmin on /admin/reset-data admits only the literal
  // "admin" role, so an ops user — who legitimately reaches the data tab and
  // its other cards — must never see a Reset-data button that could only 403
  // on confirm.
  it("reaches the data tab as ops but never sees Reset data", async () => {
    vi.stubGlobal(
      "fetch",
      resetDataBackend({ roles: ["ops"], nonProduction: true }),
    );
    render(<SettingsScreen tab="data" />);
    await waitFor(() =>
      expect(screen.getByRole("link", { name: /custom fields/i })).toBeTruthy(),
    );
    expect(screen.queryByText(/reset data/i)).toBeNull();
  });

  it("enables the confirm button once the input is non-empty and POSTs the typed confirmation", async () => {
    const posted: unknown[] = [];
    vi.stubGlobal(
      "fetch",
      resetDataBackend({
        roles: ["admin"],
        nonProduction: true,
        onReset: (body) => posted.push(body),
      }),
    );
    render(<SettingsScreen tab="data" />);
    await userEvent.click(
      await screen.findByRole("button", { name: /reset data/i }),
    );

    const dialog = await screen.findByRole("dialog");
    // The org name is shown so the admin can copy it into the input.
    expect(within(dialog).getByText("Acme Inc")).toBeTruthy();
    const confirmButton = within(dialog).getByRole("button", {
      name: /reset data/i,
    });
    expect(confirmButton).toHaveProperty("disabled", true);

    const input = within(dialog).getByRole("textbox");
    await userEvent.type(input, "Acme Inc");
    expect(confirmButton).toHaveProperty("disabled", false);

    await userEvent.click(confirmButton);

    await waitFor(() => expect(posted).toEqual([{ confirmation: "Acme Inc" }]));
    // The dialog closes and the input clears on success.
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("surfaces the server's confirmation-mismatch message on a 422", async () => {
    vi.stubGlobal(
      "fetch",
      resetDataBackend({
        roles: ["admin"],
        nonProduction: true,
        resetStatus: 422,
        resetBody: {
          detail:
            "The typed confirmation does not match the organization name.",
        },
      }),
    );
    render(<SettingsScreen tab="data" />);
    await userEvent.click(
      await screen.findByRole("button", { name: /reset data/i }),
    );
    const dialog = await screen.findByRole("dialog");
    const input = within(dialog).getByRole("textbox");
    await userEvent.type(input, "Wrong Name");
    await userEvent.click(
      within(dialog).getByRole("button", { name: /reset data/i }),
    );
    expect(
      await screen.findByText(
        "The typed confirmation does not match the organization name.",
      ),
    ).toBeTruthy();
  });

  // The full response (Task 8's five extra counters) — an admin who triggers a
  // reset that now spans tables, jobs, streams, cache keys and blob storage
  // learns what actually happened, not just that the button worked.
  const fullResetBody = {
    status: "reset",
    tables_cleared: 84,
    jobs_deleted: 12,
    streams_purged: 12,
    cache_keys_deleted: 341,
    objects_deleted: 7,
    drain_timed_out: false,
  };

  // Fixed precondition for every summary test below: admin + non_production,
  // on the Data tab that hosts the card.
  function renderSettingsAsAdmin(opts: { resetResponse: unknown }) {
    vi.stubGlobal(
      "fetch",
      resetDataBackend({
        roles: ["admin"],
        nonProduction: true,
        resetBody: opts.resetResponse,
      }),
    );
    return render(<SettingsScreen tab="data" />);
  }

  // Opens the confirm dialog, types the confirmation, and submits — the same
  // three steps every summary test needs before it can see a result.
  async function confirmReset(orgName: string) {
    await userEvent.click(
      await screen.findByRole("button", { name: /reset data/i }),
    );
    const dialog = await screen.findByRole("dialog");
    const input = within(dialog).getByRole("textbox");
    await userEvent.type(input, orgName);
    await userEvent.click(
      within(dialog).getByRole("button", { name: /reset data/i }),
    );
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  }

  it("reports what the reset cleared", async () => {
    renderSettingsAsAdmin({ resetResponse: fullResetBody });
    await confirmReset("Acme Inc");

    expect(
      await screen.findByText(
        // The whole line, not a prefix: dropping the trailing counters is
        // exactly the regression this guards, and a prefix match would pass.
        "Cleared 84 tables, 12 job rows, 12 event streams, 341 cache keys and 7 stored files.",
      ),
    ).toBeInTheDocument();
  });

  it("warns when a job was still running at drain time", async () => {
    renderSettingsAsAdmin({
      resetResponse: { ...fullResetBody, drain_timed_out: true },
    });
    await confirmReset("Acme Inc");

    expect(await screen.findByRole("alert")).toHaveTextContent(
      /background job was still running/,
    );
  });

  it("shows no summary before a reset has run", async () => {
    renderSettingsAsAdmin({ resetResponse: fullResetBody });
    // Wait for the card itself: until /v1/me resolves ResetDataCard renders
    // null, and an assertion made before that passes against an empty screen
    // rather than against a card that is deliberately quiet.
    await screen.findByRole("button", { name: /reset data/i });
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  it("clears a prior success summary once a retry fails, rather than showing both", async () => {
    // The first POST to /admin/reset-data succeeds; the second (a retry, e.g.
    // after a typo) 422s. A dedicated fetch mock rather than resetDataBackend
    // because that helper's resetStatus is fixed for every call — this test
    // needs the response to change between the two attempts.
    let resetCalls = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input instanceof Request ? input.url : input);
        const method = (
          input instanceof Request ? input.method : (init?.method ?? "GET")
        ).toUpperCase();
        if (url.endsWith("/v1/me")) {
          const me = meFixture({
            roles: ["admin"],
            allow: { custom_field: ["create", "update"] },
          });
          return jsonResponse({
            ...me,
            workspace_name: "Acme Inc",
            non_production: true,
          });
        }
        if (url.includes("/admin/reset-data") && method === "POST") {
          resetCalls += 1;
          if (resetCalls === 1) {
            return jsonResponse(fullResetBody);
          }
          return jsonResponse(
            { detail: "The typed confirmation does not match." },
            422,
          );
        }
        return jsonResponse({
          data: [],
          page: { next_cursor: null, has_more: false },
        });
      }),
    );
    render(<SettingsScreen tab="data" />);

    await confirmReset("Acme Inc");
    expect(
      await screen.findByText(/Cleared 84 tables, 12 job rows/),
    ).toBeInTheDocument();

    // Retry: the dialog stays open on error, so the summary from the first
    // attempt must not still be sitting behind it.
    await userEvent.click(
      await screen.findByRole("button", { name: /reset data/i }),
    );
    const dialog = await screen.findByRole("dialog");
    const input = within(dialog).getByRole("textbox");
    await userEvent.type(input, "Acme Inc");
    await userEvent.click(
      within(dialog).getByRole("button", { name: /reset data/i }),
    );

    expect(
      await screen.findByText("The typed confirmation does not match."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });
});

describe("AuditLogCard", () => {
  it("keeps the before/after diff hidden until the row is expanded", async () => {
    vi.stubGlobal("fetch", auditLogBackend());
    render(<AuditLogCard />);
    await screen.findByText("update");
    // Hidden by default — the diff values never render before the toggle.
    expect(screen.queryByText("new")).toBeNull();
    expect(screen.queryByText("qualified")).toBeNull();
    expect(screen.queryByText("pp-9")).toBeNull();

    await userEvent.click(
      screen.getByRole("button", { name: "Show change detail" }),
    );

    expect(await screen.findByText("new")).toBeTruthy();
    expect(screen.getByText("qualified")).toBeTruthy();
    expect(screen.getByText("pp-9")).toBeTruthy();
  });

  it("renders from/to date filters alongside the existing text filters", async () => {
    vi.stubGlobal("fetch", auditLogBackend());
    render(<AuditLogCard />);
    await screen.findByText("update");
    const from = screen.getByLabelText("From") as HTMLInputElement;
    const to = screen.getByLabelText("To") as HTMLInputElement;
    expect(from.type).toBe("date");
    expect(to.type).toBe("date");
  });

  it("renders a non-scalar before/after value as its JSON string, not [object Object]", async () => {
    const objectValuedEntry = {
      ...auditEntry,
      id: "al-2",
      before: { address: { city: "Berlin" } },
      after: { address: { city: "Munich" } },
    };
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input instanceof Request ? input.url : input);
        if (url.includes("/audit-log")) {
          return jsonResponse({
            data: [objectValuedEntry],
            page: { next_cursor: null, has_more: false },
          });
        }
        return jsonResponse({
          data: [],
          page: { next_cursor: null, has_more: false },
        });
      }),
    );
    render(<AuditLogCard />);
    await screen.findByText("update");

    await userEvent.click(
      screen.getByRole("button", { name: "Show change detail" }),
    );

    expect(await screen.findByText('{"city":"Berlin"}')).toBeTruthy();
    expect(screen.getByText('{"city":"Munich"}')).toBeTruthy();
    expect(screen.queryByText("[object Object]")).toBeNull();
  });
});
