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
import { SettingsRail } from "../app/shell";
import { pickOption } from "../design-system/select-testing";
import { LocaleProvider } from "../i18n";
import { companyContextCapabilitiesQueryKey } from "./company-context";
import { AuditLogCard, PipelinesCard, SettingsScreen } from "./settings";

// The Organization tab group is composed from its MEMBERS: each entry opens on
// the write grant its own cards ask for, and the objectless ones (people,
// privacy, maintenance) on the role. So a fixture that wants the nav has to name
// both — a role alone no longer buys the group, and a grant alone no longer
// buys the entries the server gates on the role.
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

// A settings route renders in two halves, and the sidebar owns one of them: the
// tabs are the shell's SECOND NAVIGATION LEVEL, fed by the section this screen
// publishes (useSettingsSection). So a claim about which tabs a principal is
// offered renders the real rail — the production wiring, not a copy of it — and
// a claim about a tab's content renders the screen.
const railFor = (tab?: string) => (
  <SettingsRail
    route={{ screen: "settings", id: tab }}
    onOpenSearch={() => undefined}
  />
);

const renderNav = (tab?: string) => render(railFor(tab));

// Both halves, for a claim that spans them: the tab is in the nav AND its cards
// are on the page.
const renderSettings = (tab?: string) =>
  render(
    <>
      {railFor(tab)}
      <SettingsScreen tab={tab} />
    </>,
  );

describe("SettingsScreen RBAC surfaces", () => {
  it("renders the session roles as localized badges on the default Account tab; a custom key stays its raw self", async () => {
    render(<SettingsScreen />);
    await waitFor(() => expect(screen.getByText("ada@acme.test")).toBeTruthy());
    expect(screen.getByText("Admin")).toBeTruthy();
    expect(screen.getByText("field_marketing")).toBeTruthy();
    // the seeded key never leaks raw once a label exists
    expect(screen.queryByText("admin")).toBeNull();
  });

  // Theme and language are this person's own preferences, so the Account tab is
  // where they are offered — the sidebar's account menu carries destinations.
  // The theme choice has to reach the document AND storage: on the document
  // because that is what repaints, in storage because that is what survives a
  // reload.
  it("offers the theme on the Account tab, and a choice reaches the document and storage", async () => {
    const user = userEvent.setup();
    render(<SettingsScreen />);
    await waitFor(() => expect(screen.getByText("ada@acme.test")).toBeTruthy());

    const dark = screen.getByRole("button", { name: "Dark" });
    await user.click(dark);
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(globalThis.localStorage.getItem("margince.theme")).toBe("dark");
    expect(dark.getAttribute("aria-pressed")).toBe("true");

    // Put it back. The theme is document-wide state held in theme.ts's own
    // store, which neither cleanup() nor the localStorage clear reaches, so
    // leaving it flipped would hand every later test a theme that depends on
    // the order the file happened to run in.
    await user.click(screen.getByRole("button", { name: "Light" }));
    expect(document.documentElement.dataset.theme).toBe("light");
  });

  it("switches the language from the Account tab, through the design-system select", async () => {
    const user = userEvent.setup();
    render(<SettingsScreen />);
    await waitFor(() => expect(screen.getByText("ada@acme.test")).toBeTruthy());

    await pickOption(
      user,
      screen.getByRole("combobox", { name: "Language" }),
      "Deutsch",
    );
    // The choice reaches the chrome around the control, not just the control's
    // own face — which is the whole point of changing a language here.
    expect(screen.getByRole("combobox", { name: "Sprache" })).toBeTruthy();
    expect(screen.getByText("Voreinstellungen")).toBeTruthy();
  });

  it("the passport row's token reads as withheld — masked, never re-disclosed — on the Your agents tab", async () => {
    render(<SettingsScreen tab="agents" />);
    await waitFor(() => expect(screen.getByText("Scout")).toBeTruthy());
    expect(screen.getByRole("img", { name: "Masked value" })).toBeTruthy();
    expect(screen.queryByText(/mgp_/)).toBeNull();
  });

  // The spend cards follow `automation:update` rather than any AI-named object,
  // so the principal here holds the model-price grant that opens the AI entry
  // and nothing else: the tab is reachable, and the two cards whose endpoints
  // would 403 stay off it.
  it("hides the AI usage & call-trace cards from a principal without the automation grant", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input instanceof Request ? input.url : input);
        if (url.endsWith("/v1/me")) {
          return jsonResponse(
            meFixture({
              roles: ["rep"],
              allow: { ai_model_rate: ["update"] },
            }),
          );
        }
        return jsonResponse({
          data: [],
          page: { next_cursor: null, has_more: false },
        });
      }),
    );
    render(<SettingsScreen tab="ai" />);
    // The model prices this grant authors are on screen, so the tab rendered...
    await waitFor(() =>
      expect(screen.getByText("AI model costs")).toBeTruthy(),
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
    render(<SettingsScreen tab="agents" />);

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
    render(<SettingsScreen tab="agents" />);

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
    render(<SettingsScreen tab="agents" />);
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
    render(<SettingsScreen tab="agents" />);
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
    render(<SettingsScreen tab="agents" />);
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
    render(<SettingsScreen tab="agents" />);
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

  it("groups the nav into personal and organization entries, Account current by default", async () => {
    renderNav();
    // ONE navigation landmark in the chrome: the level names itself with a
    // heading rather than opening a second `nav` beside the sidebar's own.
    const nav = screen.getByRole("navigation", { name: /primary navigation/i });
    expect(
      within(nav).getByRole("heading", { level: 2, name: "Settings" }),
    ).toBeTruthy();
    // The organization entries appear once the /me role probe resolves to admin.
    await waitFor(() =>
      expect(screen.getByRole("link", { name: "Data model" })).toBeTruthy(),
    );
    // The two groups the level carries, under its own title rather than beside
    // it — the outline reads Settings → You / Organization.
    expect(
      within(nav)
        .getAllByRole("heading", { level: 3 })
        .map((heading) => heading.textContent),
    ).toEqual(["You", "Organization"]);
    for (const label of [
      "Account",
      "Voice",
      "Your agents",
      "People & access",
      "Connections",
      "Data model",
      "Privacy & audit",
      "Maintenance",
    ]) {
      expect(screen.getByRole("link", { name: label })).toBeTruthy();
    }
    const account = screen.getByRole("link", { name: "Account" });
    expect(account.getAttribute("aria-current")).toBe("page");
    expect(
      screen.getByRole("link", { name: "Data model" }).getAttribute("href"),
    ).toBe("#/settings/data-model");
  });

  it("renders only the active entry's cards — the passport is off the Account tab", async () => {
    render(<SettingsScreen />);
    await waitFor(() => expect(screen.getByText("ada@acme.test")).toBeTruthy());
    // Scout lives on Your agents; the default Account tab must not render it.
    expect(screen.queryByText("Scout")).toBeNull();
  });

  it("renders the custom-field editor itself on the Data model tab, never a door to it", async () => {
    render(<SettingsScreen tab="data-model" />);
    // Org entry: visible once /me resolves the custom_field write grant.
    expect(
      await screen.findByRole("heading", { name: "Custom fields" }),
    ).toBeTruthy();
    // The editor IS the content now, so nothing on the page navigates to it.
    expect(screen.queryByRole("link", { name: /custom fields/i })).toBeNull();
  });

  it("renders the pipeline, product and offer-template surfaces on the Data model tab, never doors to them", async () => {
    render(<SettingsScreen tab="data-model" />);
    expect(
      await screen.findByRole("heading", { name: "Products" }),
    ).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Pipelines" })).toBeTruthy();
    expect(
      screen.getByRole("heading", { name: "Offer templates" }),
    ).toBeTruthy();
    // Three former standalone screens are inline content: the door-cards that
    // stood in for them are gone rather than relabelled.
    const hrefs = screen
      .queryAllByRole("link")
      .map((link) => link.getAttribute("href"));
    expect(hrefs).not.toContain("#/products");
    expect(hrefs).not.toContain("#/offer-templates");
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

// The settings entries currently in the nav, in render order — personal group
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

// The entries under ONE group heading. Each group renders its heading and its
// own links inside a single container, so the heading's parent is what says
// which entries belong to which group — the flat list above cannot tell a
// mis-grouped entry from a correctly grouped one.
function navGroupTabs(heading: HTMLElement): string[] {
  const container = heading.parentElement;
  if (!container) {
    throw new Error(`the group heading "${heading.textContent}" stands alone`);
  }
  return within(container)
    .getAllByRole("link")
    .map((link) => link.textContent ?? "");
}

// The personal group, which no grant gates — every case below shows exactly
// these three, and the assertions differ only in what follows them. `agents` is
// among them on purpose: the passports it carries are the PERSON's to lend, so
// gating it would regress passport minting for every seat that is not an admin.
const PERSONAL_TABS = ["Account", "Voice", "Your agents"];

// What an admin holding no object grant at all is offered. Three of the four
// organization entries here have no RBAC object for a grant to name, and
// Connections is ungated for everybody.
const ADMIN_ORG_TABS = [
  ...PERSONAL_TABS,
  "People & access",
  "Connections",
  "Privacy & audit",
  "Maintenance",
];

// Every entry open at once: the three role-gated ones from `admin`, and one
// grant apiece for the four that follow an object.
const EVERY_TAB_GRANTED: GrantSpec = {
  installation_settings: ["update"],
  capture_settings: ["create"],
  custom_field: ["update"],
  automation: ["update"],
};

describe("SettingsScreen Organization group", () => {
  // The group is composed from its members: an entry appears when the principal
  // holds a write grant on what its cards author, or — for the surfaces with no
  // RBAC object — when they hold the role the server gates them on.

  it("renders the eleven entries in their declared order, split across the two groups", async () => {
    vi.stubGlobal(
      "fetch",
      orgNavBackend({ roles: ["admin"], allow: EVERY_TAB_GRANTED }),
    );
    renderNav();
    const ORG_TABS = [
      "General",
      "People & access",
      "Connections",
      "Capture",
      "Data model",
      "AI",
      "Privacy & audit",
      "Maintenance",
    ];
    await waitFor(() =>
      expect(navTabs()).toEqual([...PERSONAL_TABS, ...ORG_TABS]),
    );
    // And each half is under the heading that claims it: the flat order above
    // would read the same if an entry were declared in the wrong group.
    const nav = screen.getByRole("navigation", { name: /primary navigation/i });
    const headings = within(nav).getAllByRole("heading", { level: 3 });
    // Asserted before either heading is read, so a level that lost a group
    // fails on the missing heading rather than on a lookup inside it.
    expect(headings.map((heading) => heading.textContent)).toEqual([
      "You",
      "Organization",
    ]);
    const [you, org] = headings;
    expect(navGroupTabs(you)).toEqual(PERSONAL_TABS);
    expect(navGroupTabs(org)).toEqual(ORG_TABS);
  });

  it("collapses to Connections alone for a principal holding neither an org grant nor an admin role", async () => {
    // Connections is the group's one unconditional member — connecting your own
    // mailbox is per-seat work, and the topbar's system-of-record chip points
    // every seat at it — so the heading survives while every gated member is
    // gone. This is the group hiding, as far as the group can hide.
    vi.stubGlobal("fetch", orgNavBackend({ roles: ["rep"] }));
    renderNav();
    // /me has to have SETTLED before an emptiness claim means anything: a nav
    // read mid-flight is empty for every principal.
    await screen.findByText("test@example.test");
    expect(navTabs()).toEqual([...PERSONAL_TABS, "Connections"]);
  });

  it("renders the group for a single visible member — a lone custom_field write opens Data model", async () => {
    vi.stubGlobal(
      "fetch",
      orgNavBackend({ roles: ["rep"], allow: { custom_field: ["update"] } }),
    );
    renderNav();
    await waitFor(() =>
      expect(navTabs()).toEqual([
        ...PERSONAL_TABS,
        "Connections",
        "Data model",
      ]),
    );
  });

  it("opens Maintenance for a lone embedding_reindex write, with Data model absent", async () => {
    // The reindex moved to Maintenance and kept its grant: taking the entry away
    // from a principal who could reach the verb before would be a regression
    // dressed as a tidy-up. Granting it alone is also what separates the
    // predicate from its neighbour — a Maintenance wired to the data-model
    // objects, or a Data model wired to this one, shows up as an entry the
    // whole-list assertion does not expect.
    vi.stubGlobal(
      "fetch",
      orgNavBackend({
        roles: ["rep"],
        allow: { embedding_reindex: ["update"] },
      }),
    );
    renderNav();
    await waitFor(() =>
      expect(navTabs()).toEqual([
        ...PERSONAL_TABS,
        "Connections",
        "Maintenance",
      ]),
    );
  });

  it("opens General for a lone fx_rate write, with Data model absent", async () => {
    // The currency table joined the base currency it converts to, so fx_rate is
    // one of the three terms General's predicate unions — this grant alone has
    // to open it, and the neighbouring entries have to stay shut.
    vi.stubGlobal(
      "fetch",
      orgNavBackend({ roles: ["rep"], allow: { fx_rate: ["create"] } }),
    );
    renderNav();
    await waitFor(() =>
      expect(navTabs()).toEqual([...PERSONAL_TABS, "General", "Connections"]),
    );
  });

  it("opens AI for a lone ai_model_rate write", async () => {
    // Model prices joined the AI runtime they price, and either term of that
    // entry's predicate opens it on its own — so the union has to be read as a
    // union and not as one object with a decorative second term.
    vi.stubGlobal(
      "fetch",
      orgNavBackend({ roles: ["rep"], allow: { ai_model_rate: ["update"] } }),
    );
    renderNav();
    await waitFor(() =>
      expect(navTabs()).toEqual([...PERSONAL_TABS, "Connections", "AI"]),
    );
  });

  it("opens Data model for a lone offer_template write", async () => {
    // Data model's fourth member, held here without custom_field, pipeline or
    // product, so the entry can only have come from the offer_template term.
    vi.stubGlobal(
      "fetch",
      orgNavBackend({
        roles: ["rep"],
        allow: { offer_template: ["create", "update"] },
      }),
    );
    renderNav();
    await waitFor(() =>
      expect(navTabs()).toEqual([
        ...PERSONAL_TABS,
        "Connections",
        "Data model",
      ]),
    );
  });

  it("opens Data model for a manager on product writes alone, with no pipeline grant", async () => {
    // The seeded manager holds pipeline read-only and product create/update/
    // delete, so this is the case the role check used to hide: the cards on
    // the entry would serve them, the nav would not.
    vi.stubGlobal(
      "fetch",
      orgNavBackend({
        roles: ["manager"],
        allow: { pipeline: ["read"], product: ["create", "update", "delete"] },
      }),
    );
    renderNav();
    await waitFor(() =>
      expect(navTabs()).toEqual([
        ...PERSONAL_TABS,
        "Connections",
        "Data model",
      ]),
    );
  });

  it("opens General and Data model for a rep holding the writes the seeded matrix gives them", async () => {
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
    renderNav();
    await waitFor(() =>
      expect(navTabs()).toEqual([
        ...PERSONAL_TABS,
        "General",
        "Connections",
        "Data model",
      ]),
    );
  });

  it("keeps People & access and Privacy & audit on the role — every object write in the matrix does not buy them", async () => {
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
    renderNav();
    await waitFor(() =>
      expect(navTabs()).toEqual([
        ...PERSONAL_TABS,
        "General",
        "Connections",
        "Data model",
        "AI",
        "Maintenance",
      ]),
    );
  });

  it("gives ops only the objectless entry its role actually holds", async () => {
    // Those surfaces have no RBAC object for a grant to name, so the role is
    // what the server checks and what the nav must check — but the roles are
    // not interchangeable. User administration, the extension inventory and
    // the compliance audit read are admin-ONLY: the server refuses ops on all
    // three, so offering them rendered entries that dead-ended on a refusal
    // state and one that handed over a read the governance matrix reserves.
    // Consent configuration is the genuine Admin/Ops surface, so Privacy is
    // the one that survives — carrying the audit trail, which gates itself
    // inside against exactly this principal.
    //
    // General is deliberately absent too: the installation carries its own
    // `installation_settings` object (ADR-0090/A135), so it follows the grant
    // like the data model does, and an ops principal holding no object grant
    // does not get it.
    vi.stubGlobal("fetch", orgNavBackend({ roles: ["ops"] }));
    renderNav();
    await waitFor(() =>
      expect(navTabs()).toEqual([
        ...PERSONAL_TABS,
        "Connections",
        "Privacy & audit",
      ]),
    );
  });

  it("keeps user administration and the maintenance verbs for the admin alone", async () => {
    // The other half of the split above, asserted from the admin's side so a
    // future widening of the predicate fails here rather than in production.
    vi.stubGlobal("fetch", orgNavBackend({ roles: ["admin"] }));
    renderNav();
    await waitFor(() => expect(navTabs()).toEqual(ADMIN_ORG_TABS));
  });

  it("shows General to an admin holding organization writes once the company rollout flag is on", async () => {
    vi.stubGlobal(
      "fetch",
      orgNavBackend({
        roles: ["admin"],
        allow: { organization: ["create", "update"] },
        companyReadEnabled: true,
      }),
    );
    renderNav();
    expect(await screen.findByRole("link", { name: "General" })).toBeTruthy();
  });

  it("withholds General from the same admin while the rollout flag is off — before the flag answers and after", async () => {
    // The flag is a deployment posture, not a permission, so it ANDs with the
    // grant: the company profile may simply not exist on this installation, and
    // the organization write is the only term of General's predicate this admin
    // holds. An unknown flag therefore reads as "off" — an entry that appears
    // while the answer is in flight and then vanishes has already offered a
    // surface this installation may not have.
    const { fetchMock, answerCapabilities } = orgNavBackendHoldingCapabilities({
      roles: ["admin"],
      allow: { organization: ["create", "update"] },
    });
    vi.stubGlobal("fetch", fetchMock);
    const { client } = renderNav();

    // Moment one: the nav is fully composed from /me — its role-gated entries
    // are on screen — while the flag is still unanswered, because this test
    // holds the answer.
    await screen.findByRole("link", { name: "Maintenance" });
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

// The whole of Overlay — connect, sync/budget health, user mapping — sits on
// Connections beside the connectors, because both halves answer "which outside
// system is talking to us". `system_of_record` is stubbed explicitly per test:
// the entry must stay reachable in native mode (a workspace is native until an
// overlay is connected, so gating it on overlay mode would hide the only place
// to connect one), and the retired Integrations id must carry none of it.
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

describe("SettingsScreen connections tab", () => {
  it("carries the overlay on Connections, reachable before any overlay is connected", async () => {
    vi.stubGlobal(
      "fetch",
      overlaySettingsBackend({ roles: ["admin"], sorMode: "native" }),
    );
    renderSettings("connections");
    await waitFor(() =>
      expect(
        screen
          .getByRole("link", { name: "Connections" })
          .getAttribute("aria-current"),
      ).toBe("page"),
    );
    // OverlayCard's own heading — proof its connect flow rendered on this
    // entry even though the workspace has never connected an overlay.
    expect(
      await screen.findByRole("heading", { name: "HubSpot mirror" }),
    ).toBeTruthy();
  });

  // A retired id is what a bookmark still carries: Integrations was split into
  // Connections and Capture, so the route names nothing. It has to land on the
  // first entry this principal can see rather than on a blank screen, and it
  // must bring none of the split surface's content with it.
  it("falls back to Account when the route names a retired entry", async () => {
    vi.stubGlobal(
      "fetch",
      overlaySettingsBackend({ roles: ["admin"], sorMode: "overlay" }),
    );
    renderSettings("integrations");
    await waitFor(() =>
      expect(
        screen
          .getByRole("link", { name: "Account" })
          .getAttribute("aria-current"),
      ).toBe("page"),
    );
    // The Account tab's own content, not merely its nav entry: the fallback has
    // to render a page, and the sidebar carries the viewer's email either way.
    expect(
      await screen.findByRole("heading", { name: "Preferences" }),
    ).toBeTruthy();
    expect(
      screen.queryByRole("heading", { name: "HubSpot mirror" }),
    ).toBeNull();
  });

  // The system-of-record chip is shown to every seat and links here, so a
  // rep who follows it must land on the entry, not on the Account fallback —
  // which is why Connections carries no predicate at all. Hiding it would buy
  // no confidentiality either: the mapping card's reads are admin/ops-only on
  // the server, and the card keeps them unsent for anyone else — so a rep sees
  // the connection card's read-only state and the mapping card's admin-only
  // notice, never the directory.
  it("shows Connections to a non-admin rep with both overlay cards in their read-only state", async () => {
    const fetchMock = overlaySettingsBackend({
      roles: ["rep"],
      sorMode: "native",
    });
    vi.stubGlobal("fetch", fetchMock);
    renderSettings("connections");
    await waitFor(() =>
      expect(
        screen
          .getByRole("link", { name: "Connections" })
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
            name: "Sales",
            is_default: true,
            position: 0,
            stages: [
              {
                id: "s1",
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

// A background system with nothing queued and nothing failed — GET
// /admin/job-health's honest quiet answer.
const IDLE_JOB_HEALTH = {
  generated_at: "2026-08-13T09:30:00Z",
  kinds: [],
  recent_failures: [],
};

// The danger-zone Reset data action: server-driven, gated on the literal admin
// role AND me.non_production. A dedicated backend per test so the role/posture
// combination is explicit rather than layered on the shared settingsBackend
// default. `allow` defaults to the reindex write that opens the Maintenance
// entry the card lives on — a test about the card should not also have to argue
// its way onto the entry, and one that wants it CLOSED says so with `{}`.
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
        allow: opts.allow ?? { embedding_reindex: ["read", "update"] },
      });
      return jsonResponse({
        ...me,
        user: { ...me.user, email: "ada@acme.test" },
        workspace_name: "Acme Inc",
        non_production: opts.nonProduction,
      });
    }
    // The job report is the danger zone's neighbour on this entry, and an admin
    // fetches it on arrival — so it answers with the shape the endpoint serves.
    // A generic `{data: []}` here would crash the card that reads it, and every
    // assertion below would fail describing the wrong thing.
    if (url.includes("/admin/job-health")) {
      return jsonResponse(IDLE_JOB_HEALTH);
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
    render(<SettingsScreen tab="maintenance" />);
    expect(await screen.findByText(/reset data/i)).toBeTruthy();
  });

  it("hides Reset data for an admin in a production posture", async () => {
    vi.stubGlobal(
      "fetch",
      resetDataBackend({ roles: ["admin"], nonProduction: false }),
    );
    render(<SettingsScreen tab="maintenance" />);
    // The job report is the entry's own card, so its heading proves Maintenance
    // rendered — the danger zone below it is what has to stay away.
    await waitFor(() =>
      expect(
        screen.getByRole("heading", { name: "Background jobs" }),
      ).toBeTruthy(),
    );
    expect(screen.queryByText(/reset data/i)).toBeNull();
  });

  it("hides Reset data from a rep even in a non-production posture", async () => {
    vi.stubGlobal(
      "fetch",
      // A rep is no admin and holds no embedding_reindex grant, so Maintenance
      // is not theirs to reach in the first place.
      resetDataBackend({ roles: ["rep"], nonProduction: true, allow: {} }),
    );
    render(<SettingsScreen tab="maintenance" />);
    // With no member grant, the rep falls back to Account — proven here by
    // the identity card rendering instead of anything maintenance-shaped.
    await waitFor(() => expect(screen.getByText("ada@acme.test")).toBeTruthy());
    expect(screen.queryByText(/reset data/i)).toBeNull();
  });

  // The card is admin-ONLY, narrower than the Maintenance entry that hosts it:
  // the server's auth.RequireAdmin on /admin/reset-data admits only the literal
  // "admin" role, so an ops user — who reaches the entry on the reindex grant
  // and uses its other cards — must never see a Reset-data button that could
  // only 403 on confirm.
  it("reaches Maintenance as ops but never sees Reset data", async () => {
    vi.stubGlobal(
      "fetch",
      resetDataBackend({ roles: ["ops"], nonProduction: true }),
    );
    render(<SettingsScreen tab="maintenance" />);
    await waitFor(() =>
      expect(
        screen.getByRole("heading", { name: "Search index" }),
      ).toBeTruthy(),
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
    render(<SettingsScreen tab="maintenance" />);
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
    render(<SettingsScreen tab="maintenance" />);
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
  // on the Maintenance entry that hosts the card.
  function renderSettingsAsAdmin(opts: { resetResponse: unknown }) {
    vi.stubGlobal(
      "fetch",
      resetDataBackend({
        roles: ["admin"],
        nonProduction: true,
        resetBody: opts.resetResponse,
      }),
    );
    return render(<SettingsScreen tab="maintenance" />);
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
            allow: { embedding_reindex: ["read", "update"] },
          });
          return jsonResponse({
            ...me,
            workspace_name: "Acme Inc",
            non_production: true,
          });
        }
        if (url.includes("/admin/job-health")) {
          return jsonResponse(IDLE_JOB_HEALTH);
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
    render(<SettingsScreen tab="maintenance" />);

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

// A reindex marker with work waiting, so the search-index card has a state to
// report rather than a shapeless payload to guess at.
const REINDEX_STATUS = {
  configured_identity: "anthropic/voyage-3@1024",
  populated_identity: "anthropic/voyage-2@1024",
  status: "idle",
  updated_at: "2026-07-21T12:00:00Z",
  reindex_needed: true,
  entities_pending: 42,
  per_workspace: [{ entities_pending: 42 }],
};

// Every read the three restructured entries make, answered honestly in one
// place: the passports Your agents lists, the consent registry and audit trail
// Privacy & audit now share, and the two operational reports on Maintenance.
function mergedEntryBackend(opts: {
  roles: string[];
  seat?: "full" | "read";
  allow?: GrantSpec;
  nonProduction?: boolean;
}) {
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input instanceof Request ? input.url : input);
    if (url.endsWith("/v1/me")) {
      const me = meFixture({
        roles: opts.roles,
        seat: opts.seat ?? "full",
        allow: opts.allow ?? {},
      });
      return jsonResponse({
        ...me,
        workspace_name: "Acme Inc",
        non_production: opts.nonProduction ?? false,
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
    if (url.includes("/audit-log")) {
      return jsonResponse({
        data: [auditEntry],
        page: { next_cursor: null, has_more: false },
      });
    }
    if (url.includes("/admin/job-health")) {
      return jsonResponse(IDLE_JOB_HEALTH);
    }
    if (url.includes("/embeddings/reindex/status")) {
      return jsonResponse(REINDEX_STATUS);
    }
    return jsonResponse({
      data: [],
      page: { next_cursor: null, has_more: false },
    });
  });
}

// The entries the restructure created or merged into, read as CONTENT: a merged
// page has to carry the surfaces its parts brought, and the personal one has to
// open for a seat no grant would have admitted.
describe("SettingsScreen restructured entries", () => {
  it("opens Your agents for a read-only seat, passports and all", async () => {
    vi.stubGlobal(
      "fetch",
      mergedEntryBackend({ roles: ["rep"], seat: "read" }),
    );
    renderSettings("agents");
    await waitFor(() =>
      expect(
        screen
          .getByRole("link", { name: "Your agents" })
          .getAttribute("aria-current"),
      ).toBe("page"),
    );
    // A passport is lent by the HUMAN who minted it, so the surface that mints
    // and lists one opens for a seat holding no org grant and no writing
    // licence at all — gating it behind the org group would have meant only
    // admins could lend.
    expect(
      await screen.findByRole("heading", { name: "Agent passports" }),
    ).toBeTruthy();
    expect(screen.getByText("Scout")).toBeTruthy();
    // And the autonomy table the passports sit under, which came off the
    // organization's AI entry with them.
    expect(
      screen.getByRole("heading", { name: "Autonomy tiers" }),
    ).toBeTruthy();
  });

  it("renders the audit trail beside the consent registry on Privacy & audit", async () => {
    vi.stubGlobal("fetch", mergedEntryBackend({ roles: ["admin"] }));
    renderSettings("privacy");
    await waitFor(() =>
      expect(
        screen
          .getByRole("link", { name: "Privacy & audit" })
          .getAttribute("aria-current"),
      ).toBe("page"),
    );
    // The purpose registry the entry has always carried...
    expect(
      await screen.findByRole("heading", { name: "Consent purposes" }),
    ).toBeTruthy();
    // ...and the trail that proves those purposes were honoured, which had a
    // tab of its own before: its filters, and an entry answering them.
    expect(screen.getByRole("heading", { name: "Filters" })).toBeTruthy();
    expect(
      await screen.findByRole("heading", { name: "Audit log" }),
    ).toBeTruthy();
    expect(screen.getByText("update")).toBeTruthy();
  });

  it("renders the reindex on Maintenance for a principal holding only that grant, and no danger zone", async () => {
    vi.stubGlobal(
      "fetch",
      mergedEntryBackend({
        roles: ["rep"],
        allow: { embedding_reindex: ["read", "update"] },
        // The posture the danger zone's second gate asks for, so the ROLE is
        // the only thing left holding it back below.
        nonProduction: true,
      }),
    );
    renderSettings("maintenance");
    await waitFor(() =>
      expect(
        screen
          .getByRole("link", { name: "Maintenance" })
          .getAttribute("aria-current"),
      ).toBe("page"),
    );
    // The verb this grant buys, which used to hide beside the field editor.
    expect(
      await screen.findByRole("heading", { name: "Search index" }),
    ).toBeTruthy();
    // The job report keeps its place and withholds its content: the endpoint is
    // the admin's, and an absent card here would read as "nothing is queued".
    expect(
      screen.getByRole("heading", { name: "Background jobs" }),
    ).toBeTruthy();
    expect(
      screen.getByText(/Only an admin can see background-job health/),
    ).toBeTruthy();
    expect(screen.queryByText(/reset data/i)).toBeNull();
  });
});

// Which /audit-log URLs a backend was actually asked for, newest last — the
// wire is the only honest witness that a typed filter narrowed the question.
function auditLogUrls(backend: ReturnType<typeof auditLogBackend>) {
  return backend.mock.calls
    .map(([input]) => String(input instanceof Request ? input.url : input))
    .filter((url) => url.includes("/audit-log"));
}

describe("AuditLogCard", () => {
  it("puts the filters and the entries in two separate cards", async () => {
    vi.stubGlobal("fetch", auditLogBackend());
    render(<AuditLogCard />);
    await screen.findByText("update");

    const actorFilter = screen.getByLabelText("Actor");
    const entryAction = screen.getByText("update");
    const filterCard = actorFilter.closest("section");
    const entryCard = entryAction.closest("section");
    expect(filterCard).not.toBeNull();
    expect(entryCard).not.toBeNull();
    expect(entryCard).not.toBe(filterCard);
    // Each card carries its own heading, and neither reaches into the other:
    // the six controls stay put while the entries below them scroll.
    expect(filterCard).toContainElement(
      screen.getByRole("heading", { name: "Filters" }),
    );
    expect(entryCard).toContainElement(
      screen.getByRole("heading", { name: "Audit log" }),
    );
    expect(filterCard).not.toContainElement(entryAction);
    expect(entryCard).not.toContainElement(actorFilter);
  });

  it("narrows the request to the filters, keeping the page size and dropping the cursor", async () => {
    const backend = auditLogBackend();
    vi.stubGlobal("fetch", backend);
    render(<AuditLogCard />);
    await screen.findByText("update");
    expect(auditLogUrls(backend)[0]).toContain("limit=20");

    await userEvent.type(screen.getByLabelText("Actor"), "agent:sdr");
    await userEvent.type(screen.getByLabelText("Entity type"), "person");

    await waitFor(() => {
      const latest = auditLogUrls(backend).at(-1) ?? "";
      expect(latest).toContain("actor=agent%3Asdr");
      expect(latest).toContain("entity_type=person");
    });
    const latest = auditLogUrls(backend).at(-1) ?? "";
    expect(latest).toContain("limit=20");
    // A filter change is a new question, so the narrowed request starts the
    // keyset chain over instead of resuming the unfiltered one's cursor.
    expect(latest).not.toContain("cursor=");
  });

  it("says the log is empty rather than showing an empty entries card", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          data: [],
          page: { next_cursor: null, has_more: false },
        }),
      ),
    );
    render(<AuditLogCard />);
    expect(await screen.findByText("Nothing here yet.")).toBeInTheDocument();
  });

  it("offers a retry when the log fails to load", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input instanceof Request ? input.url : input);
        if (url.includes("/audit-log")) {
          return jsonResponse({ title: "Upstream is down" }, 500);
        }
        return jsonResponse({
          data: [],
          page: { next_cursor: null, has_more: false },
        });
      }),
    );
    render(<AuditLogCard />);
    expect(
      await screen.findByRole("button", { name: "Retry" }),
    ).toBeInTheDocument();
    expect(screen.getByText("Couldn't load this view.")).toBeInTheDocument();
    // The filter row survives the failure — a failed page must not take the
    // controls that could ask a different question with it.
    expect(screen.getByLabelText("Actor")).toBeInTheDocument();
  });

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
