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
import type { RbacObject } from "../app/capability";
import { type GrantSpec, meFixture } from "../app/mefixture";
import { SettingsRail } from "../app/shell";
import { pickOption } from "../design-system/select-testing";
import { LocaleProvider } from "../i18n";
import { companyContextCapabilitiesQueryKey } from "./company-context";
import { AuditLogCard, PipelinesCard, SettingsScreen } from "./settings";

// The Organization tab group is composed from its MEMBERS, and OPENING AN ENTRY
// IS A READ: every predicate asks for a read grant on something the entry shows,
// while the write affordances inside it gate themselves. So a fixture that wants
// an entry in the nav has to name the READ, and one that also wants the authoring
// controls names the write on top — two separate claims, and a grant list holding
// writes alone reaches no entry at all.
const PIPELINE_ADMIN: GrantSpec = { pipeline: ["read", "create", "update"] };
const ORG_ADMIN: GrantSpec = {
  ...PIPELINE_ADMIN,
  custom_field: ["read", "create", "update"],
  // The consent registry's own gate (consent/store.go demands person:read), which
  // every seeded role holds — so a fixture standing in for a real principal has to
  // carry it or Privacy & audit disappears for reasons the test is not about.
  person: ["read"],
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

  it("the passport row's token reads as withheld — masked, never re-disclosed — on the Agents tab", async () => {
    render(<SettingsScreen tab="agents" />);
    await waitFor(() => expect(screen.getByText("Scout")).toBeTruthy());
    expect(screen.getByRole("img", { name: "Masked value" })).toBeTruthy();
    expect(screen.queryByText(/mgp_/)).toBeNull();
  });

  // The spend cards follow `automation:update` rather than any AI-named object,
  // so the principal here holds the model-price grants that open the AI entry and
  // nothing else: the read reaches the page, the write authors the price table on
  // it, and the two cards whose endpoints would 403 stay off it.
  it("withholds the AI spend and call trace from a principal without the automation grant", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input instanceof Request ? input.url : input);
        if (url.endsWith("/v1/me")) {
          return jsonResponse(
            meFixture({
              roles: ["rep"],
              allow: { ai_model_rate: ["read", "update"] },
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
    // ...and the two cards whose endpoints require the automation grant KEEP
    // their place and say they are withheld. Absent, they would claim the
    // installation had spent nothing and made no model calls — a statement about
    // the data, where the truth is only about who may read it. No request is made
    // for either, so a rep never hits a 403 error box (GET /ai/usage, /ai/calls).
    expect(await screen.findByText("AI usage & budget")).toBeTruthy();
    expect(
      await screen.findByText(
        /only an operator can see what the AI runtime spent/i,
      ),
    ).toBeTruthy();
    expect(screen.getByText("AI call trace")).toBeTruthy();
    expect(
      screen.getByText(/only an operator can read the per-call trace/i),
    ).toBeTruthy();
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

    const scoutRow = screen.getByText("Scout").closest("[data-passport]");
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
    const retiredRow = screen.getByText("Retired").closest("[data-passport]");
    expect(retiredRow).toBeTruthy();
    expect(
      retiredRow && Array.from(retiredRow.querySelectorAll("button")).length,
    ).toBe(0);

    const scoutRow = screen.getByText("Scout").closest("[data-passport]");
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
      "Writing voice",
      "Agents",
      "Connections",
      "People & access",
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
    // Scout lives on Agents; the default Account tab must not render it.
    expect(screen.queryByText("Scout")).toBeNull();
  });

  it("renders the custom-field editor itself on the Data model tab, never a door to it", async () => {
    render(<SettingsScreen tab="data-model" />);
    // Org entry: visible once /me resolves the custom_field read grant.
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
  // The licensing seat, which the entry predicates deliberately leave out: a
  // read seat still READS every page behind them, so a case can name the seat
  // and expect the nav not to narrow.
  seat?: "full" | "read";
  companyReadEnabled?: boolean;
}) {
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input instanceof Request ? input.url : input);
    if (url.endsWith("/v1/me")) {
      return jsonResponse(
        meFixture({
          roles: opts.roles,
          seat: opts.seat ?? "full",
          allow: opts.allow ?? {},
        }),
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

// The personal group, which no grant gates — every case below opens with exactly
// these four, and the assertions differ only in what follows them. `agents` is
// among them because the passports it carries are the PERSON's to lend, so gating
// it would regress passport minting for every seat that is not an admin; and
// `connections` because a mailbox and a LinkedIn network nobody else can see are
// that person's, not the installation's configuration.
const PERSONAL_TABS = ["Account", "Writing voice", "Agents", "Connections"];

// The eight Organization entries, in the order they are declared.
const ORG_TABS = [
  "General",
  "People & access",
  "Integrations",
  "Capture",
  "Data model",
  "AI",
  "Privacy & audit",
  "Maintenance",
];

const EVERY_TAB = [...PERSONAL_TABS, ...ORG_TABS];

// Maintenance is declared last, so "every entry but Maintenance" is this list
// without its tail — the eleven a principal holding the seeded reads and no
// admin role reaches.
const EVERY_TAB_BUT_MAINTENANCE = EVERY_TAB.slice(0, -1);

// What mere membership buys: the ONE Organization entry with no grant to ask for.
// No RBAC object describes identity administration and none can, and `GET /users`
// answers 200 to any authenticated principal — so the nav admits everybody, as
// the server does.
//
// Privacy is deliberately NOT here. `consent_config` is absent from the shipped
// vocabulary, but the registry's server gate is not a role either: ListPurposes
// demands `person:read`, so that is what the entry asks for. Every seeded role
// holds it; a principal holding nothing does not.
const MEMBER_TABS = [...PERSONAL_TABS, "People & access"];
const MEMBER_TABS_WITH_PRIVACY = [...MEMBER_TABS, "Privacy & audit"];

// Membership's two entries plus Maintenance, which is what EITHER half of that
// entry's predicate buys on its own — the admin role, or the reindex read an
// edited role can hold without it. Both halves are asserted against this list.
const MEMBER_TABS_WITH_MAINTENANCE = [
  ...MEMBER_TABS_WITH_PRIVACY,
  "Maintenance",
];

// Every entry open at once: the admin role for Maintenance, and one read apiece
// for the five that follow an object.
const EVERY_TAB_GRANTED: GrantSpec = {
  person: ["read"],
  installation_settings: ["read"],
  webhook_subscription: ["read"],
  capture_settings: ["read"],
  custom_field: ["read"],
  automation: ["read"],
};

// The read grant on ONE object, as a GrantSpec.
//
// Built by assignment rather than as a literal, because a computed key whose own
// type is a union widens the object to `{ [x: string]: string[] }` — which does
// not satisfy GrantSpec, and only fails in `tsc -b`, where test files are
// typechecked, rather than under the app project alone.
function readOn(object: RbacObject): GrantSpec {
  // `person:read` rides along because Privacy asks for it, and every seeded role
  // holds it — so a case about ONE object's entry is not also a case about losing
  // the consent registry. Isolating the object under test means holding the floor
  // steady, not stripping it.
  const spec: GrantSpec = { person: ["read"] };
  spec[object] = ["read"];
  return spec;
}

// The four reads Data model unions. Each has to open the page alone: an entry
// wired to one object with three decorative terms passes any fixture that grants
// all four.
const DATA_MODEL_READS = [
  "custom_field",
  "pipeline",
  "product",
  "offer_template",
] as const;

// The seeded grant matrix, READ verbs only — the only verb an entry's predicate
// asks for. manager, read_only and rep hold the identical ten reads and differ
// only in the writes on top, which is exactly why write-shaped predicates hid
// pages the server serves: the differentiation the matrix carries lives in the
// writes, and a write is not what opens a page.
const SEEDED_READS: GrantSpec = {
  automation: ["read"],
  person: ["read"],
  capture_settings: ["read"],
  custom_field: ["read"],
  installation_settings: ["read"],
  offer_template: ["read"],
  organization: ["read"],
  overlay_connection: ["read"],
  pipeline: ["read"],
  product: ["read"],
  webhook_subscription: ["read"],
};

// What the matrix adds for ops, the four objects it shares with admin alone —
// and `embedding_reindex` among them is what opens the twelfth entry.
const SEEDED_OPS_READS: GrantSpec = {
  ...SEEDED_READS,
  ai_model_rate: ["read"],
  embedding_reindex: ["read"],
  fx_rate: ["read"],
  retention_policy: ["read"],
};

describe("SettingsScreen Organization group", () => {
  // The group is composed from its members: an entry appears when the principal
  // may READ some part of it — opening a page is reading it — or, for the two
  // surfaces with no RBAC object, on membership alone. The write affordances
  // inside each entry gate themselves, so no case below needs a write to reach a
  // page and none of them proves anything by granting one.

  it("renders the twelve entries in their declared order, split across the two groups", async () => {
    vi.stubGlobal(
      "fetch",
      orgNavBackend({ roles: ["admin"], allow: EVERY_TAB_GRANTED }),
    );
    renderNav();
    await waitFor(() => expect(navTabs()).toEqual(EVERY_TAB));
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

  it("gives a principal holding no read at all the two entries that ask for none", async () => {
    // People & access and Privacy & audit have no grant to ask for: the member
    // roster answers 200 to any authenticated principal, and `consent_config` is
    // not in the shipped RBAC vocabulary. So they are the floor of this level
    // rather than a case — every gated member is gone here, and those two stay.
    vi.stubGlobal("fetch", orgNavBackend({ roles: ["rep"] }));
    renderNav();
    // /me has to have SETTLED before an emptiness claim means anything: a nav
    // read mid-flight is empty for every principal.
    await screen.findByText("test@example.test");
    expect(navTabs()).toEqual(MEMBER_TABS);
  });

  it.each(DATA_MODEL_READS)(
    "opens Data model for a lone %s read",
    async (object) => {
      const allow = readOn(object);
      vi.stubGlobal("fetch", orgNavBackend({ roles: ["rep"], allow }));
      renderNav();
      await waitFor(() =>
        expect(navTabs()).toEqual([
          ...PERSONAL_TABS,
          "People & access",
          "Data model",
          "Privacy & audit",
        ]),
      );
    },
  );

  it.each(["webhook_subscription", "overlay_connection"] as const)(
    "opens Integrations for a lone %s read",
    async (object) => {
      // The installation's outside wiring was half of the entry Connections used
      // to be, and the system-of-record chip in the topbar points every seat at
      // it — so either read has to open it on its own, or whoever follows that
      // chip lands on the Account fallback.
      const allow = readOn(object);
      vi.stubGlobal("fetch", orgNavBackend({ roles: ["rep"], allow }));
      renderNav();
      await waitFor(() =>
        expect(navTabs()).toEqual([
          ...PERSONAL_TABS,
          "People & access",
          "Integrations",
          "Privacy & audit",
        ]),
      );
    },
  );

  it("opens Capture for a lone capture_settings read", async () => {
    // Two surfaces both called "Capture" became one page, and this is the read
    // the merged page asks for. Granted alone so a Capture wired to a
    // neighbouring object, or a neighbour wired to this one, shows up as an
    // entry the whole-list assertion does not expect.
    vi.stubGlobal(
      "fetch",
      orgNavBackend({ roles: ["rep"], allow: readOn("capture_settings") }),
    );
    renderNav();
    await waitFor(() =>
      expect(navTabs()).toEqual([
        ...PERSONAL_TABS,
        "People & access",
        "Capture",
        "Privacy & audit",
      ]),
    );
  });

  it("opens Maintenance for a lone embedding_reindex read, for a principal who is no admin", async () => {
    // The reindex moved to Maintenance and kept its object: taking the entry away
    // from a principal who could reach the verb before would be a regression
    // dressed as a tidy-up. It is also the term that lets Maintenance open for
    // someone who is not an admin, which is the half of that predicate a role
    // check could never express.
    vi.stubGlobal(
      "fetch",
      orgNavBackend({
        roles: ["rep"],
        allow: readOn("embedding_reindex"),
      }),
    );
    renderNav();
    await waitFor(() =>
      expect(navTabs()).toEqual(MEMBER_TABS_WITH_MAINTENANCE),
    );
  });

  it("opens General for a lone fx_rate read, and no other entry with it", async () => {
    // The currency table joined the base currency it converts to, so fx_rate is
    // one of the three terms General's predicate unions — this read alone has to
    // open it, and the neighbouring entries have to stay shut.
    vi.stubGlobal(
      "fetch",
      orgNavBackend({ roles: ["rep"], allow: readOn("fx_rate") }),
    );
    renderNav();
    await waitFor(() =>
      expect(navTabs()).toEqual([
        ...PERSONAL_TABS,
        "General",
        "People & access",
        "Privacy & audit",
      ]),
    );
  });

  it("opens AI for a lone ai_model_rate read", async () => {
    // Model prices joined the AI runtime they price, and either term of that
    // entry's predicate opens it on its own — so the union has to be read as a
    // union and not as one object with a decorative second term.
    vi.stubGlobal(
      "fetch",
      orgNavBackend({ roles: ["rep"], allow: readOn("ai_model_rate") }),
    );
    renderNav();
    await waitFor(() =>
      expect(navTabs()).toEqual([
        ...PERSONAL_TABS,
        "People & access",
        "AI",
        "Privacy & audit",
      ]),
    );
  });

  // THE REGRESSION THIS RULE EXISTS TO PREVENT. Measured against the live API,
  // the write-shaped predicates hid a read-only seat from eight of the eleven
  // entries the server answers 200 on — three of which (products, offer
  // templates, custom fields) were ungated routes of their own before the merge.
  // A client that hides a page the server serves is not protecting anything; it
  // is disagreeing with the authority.
  //
  // The licensing seat is named here too, and must not narrow the level either: a
  // read seat READS every page behind these entries, and the server clamps it on
  // the write.
  it("reaches every entry but Maintenance for a read_only role on a read seat", async () => {
    vi.stubGlobal(
      "fetch",
      orgNavBackend({
        roles: ["read_only"],
        seat: "read",
        allow: SEEDED_READS,
      }),
    );
    renderNav();
    await waitFor(() => expect(navTabs()).toEqual(EVERY_TAB_BUT_MAINTENANCE));
  });

  it.each(["manager", "rep"] as const)(
    "reaches the same entries for a seeded %s, whose extra writes buy no page",
    async (role) => {
      vi.stubGlobal(
        "fetch",
        orgNavBackend({ roles: [role], allow: SEEDED_READS }),
      );
      renderNav();
      await waitFor(() => expect(navTabs()).toEqual(EVERY_TAB_BUT_MAINTENANCE));
    },
  );

  it("reaches all twelve entries for a seeded ops, whose reindex read opens Maintenance", async () => {
    // Maintenance is the one entry that genuinely narrows, and it narrows to
    // admin/ops rather than to admin: ops holds the reindex read, so the entry
    // opens on the grant and not on a role name — which is what lets an edited
    // role holding the same read reach it too.
    vi.stubGlobal(
      "fetch",
      orgNavBackend({ roles: ["ops"], allow: SEEDED_OPS_READS }),
    );
    renderNav();
    await waitFor(() => expect(navTabs()).toEqual(EVERY_TAB));
  });

  it("adds Maintenance for an admin holding no read at all, and loses Privacy with it", async () => {
    // The role half of Maintenance's predicate, on its own: an admin whose grants
    // were all revoked still administers the installation, and the danger zone
    // inside asks for that same role.
    //
    // Privacy goes, and that is the point of asking for a grant rather than
    // assuming membership: the consent registry's server gate is `person:read`, so
    // an admin stripped of it would reach a page of four refusals. The entry
    // follows the grant, not the role.
    vi.stubGlobal("fetch", orgNavBackend({ roles: ["admin"] }));
    renderNav();
    await waitFor(() =>
      expect(navTabs()).toEqual([...MEMBER_TABS, "Maintenance"]),
    );
  });

  it("shows General to an admin holding the organization read once the company rollout flag is on", async () => {
    vi.stubGlobal(
      "fetch",
      orgNavBackend({
        roles: ["admin"],
        allow: readOn("organization"),
        companyReadEnabled: true,
      }),
    );
    renderNav();
    expect(await screen.findByRole("link", { name: "General" })).toBeTruthy();
  });

  it("withholds General from that same admin while the rollout flag is off — before the flag answers and after", async () => {
    // The flag is a deployment posture, not a permission, so it ANDs with the
    // grant beside it: the company profile may simply not exist on this
    // installation. An unknown flag therefore reads as "off" — an entry that
    // appears while the answer is in flight and then vanishes has already offered
    // a surface this installation may not have.
    //
    // The organization read is the ONLY term of General's predicate this fixture
    // grants, which is what leaves the flag decisive. On a seeded installation
    // every role also holds `installation_settings:read` and General opens on
    // that regardless — so this case is about the flag's contribution to the
    // union, not a claim that General is ever unreachable in practice.
    const { fetchMock, answerCapabilities } = orgNavBackendHoldingCapabilities({
      roles: ["admin"],
      allow: readOn("organization"),
    });
    vi.stubGlobal("fetch", fetchMock);
    const { client } = renderNav();

    // Moment one: the nav is fully composed from /me — its role-gated entries
    // are on screen — while the flag is still unanswered, because this test
    // holds the answer.
    await screen.findByRole("link", { name: "Maintenance" });
    expect(navTabs()).toEqual(MEMBER_TABS_WITH_MAINTENANCE);

    // Moment two: the answer is in the cache, which is the fact the emptiness
    // claim needs — the request having been SENT proves nothing about what the
    // nav has rendered.
    answerCapabilities();
    await waitFor(() =>
      expect(
        client.getQueryState(companyContextCapabilitiesQueryKey)?.status,
      ).toBe("success"),
    );
    expect(navTabs()).toEqual(MEMBER_TABS_WITH_MAINTENANCE);
  });
});

// The whole of Overlay — connect, sync/budget health, user mapping — sits on
// Integrations beside the provider credential and the webhooks, because all of
// them are the INSTALLATION's outside wiring: one shared key, one set of
// subscriptions, one system-of-record flip that re-points every read. The
// personal mailbox and LinkedIn network that used to share the entry are on
// Connections, and neither page carries the other's cards.
//
// `system_of_record` is stubbed explicitly per test: the entry must stay reachable
// in native mode (a workspace is native until an overlay is connected, so gating
// it on overlay mode would hide the only place to connect one), and a retired
// route id must carry none of it.
function overlaySettingsBackend(opts: {
  roles: string[];
  allow?: GrantSpec;
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
      const me = meFixture({ roles: opts.roles, allow: opts.allow ?? {} });
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

// The reads the seeded matrix gives every role on the installation's wiring, and
// the two terms of the Integrations predicate — granted wherever a case needs the
// entry to be OPEN, so an absent card on it can only mean the card is elsewhere.
const WIRING_READS: GrantSpec = {
  overlay_connection: ["read"],
  webhook_subscription: ["read"],
};

describe("SettingsScreen connections and integrations tabs", () => {
  it("carries the overlay on Integrations, reachable before any overlay is connected", async () => {
    vi.stubGlobal(
      "fetch",
      overlaySettingsBackend({
        roles: ["admin"],
        allow: WIRING_READS,
        sorMode: "native",
      }),
    );
    renderSettings("integrations");
    await waitFor(() =>
      expect(
        screen
          .getByRole("link", { name: "Integrations" })
          .getAttribute("aria-current"),
      ).toBe("page"),
    );
    // OverlayCard's own heading — proof its connect flow rendered on this
    // entry even though the workspace has never connected an overlay.
    expect(
      await screen.findByRole("heading", { name: "HubSpot mirror" }),
    ).toBeTruthy();
  });

  // A retired id is what a bookmark still carries: the audit trail was an entry
  // of its own before it moved onto Privacy & audit, so `#/settings/audit` names
  // nothing. It has to land on the first entry this principal can see rather than
  // on a blank screen. The wiring reads are granted so Integrations is genuinely
  // open — a fallback that happens because an entry is hidden proves nothing
  // about a route id that no longer exists.
  it("falls back to Account when the route names a retired entry", async () => {
    vi.stubGlobal(
      "fetch",
      overlaySettingsBackend({
        roles: ["admin"],
        allow: WIRING_READS,
        sorMode: "overlay",
      }),
    );
    renderSettings("audit");
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

  // The overlay is the installation's, so its page is gated — on the read every
  // seeded role holds, which is what keeps the system-of-record chip in the
  // topbar honest: that chip is shown to every seat and links here, so a rep who
  // follows it must land on the entry rather than the Account fallback. Reaching
  // it costs no confidentiality: both cards' write and management reads are
  // admin/ops-only on the server, and each keeps them unsent for anyone else — so
  // a rep sees the connection card's read-only state and the mapping card's
  // admin-only notice, never the directory.
  it("shows Integrations to a non-admin rep with both overlay cards in their read-only state", async () => {
    const fetchMock = overlaySettingsBackend({
      roles: ["rep"],
      allow: WIRING_READS,
      sorMode: "native",
    });
    vi.stubGlobal("fetch", fetchMock);
    renderSettings("integrations");
    await waitFor(() =>
      expect(
        screen
          .getByRole("link", { name: "Integrations" })
          .getAttribute("aria-current"),
      ).toBe("page"),
    );
    expect(
      await screen.findByRole("heading", { name: "HubSpot mirror" }),
    ).toBeTruthy();
    expect(
      await screen.findByText(
        "You do not have permission to change the HubSpot connection.",
      ),
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

  // The split, from both sides. One entry used to hold a rep's own mailbox and
  // the installation's webhooks together, which is why it could carry no honest
  // predicate: any gate on it took a personal task away from whoever it hid it
  // from. The two cases below are what makes the split real rather than a
  // relabelling — each page has to carry its own half and NOT the other's, and
  // the wiring reads are granted in both so an absent card can only mean the card
  // lives on the other entry.
  it("renders the personal connections on Connections and none of the installation's wiring", async () => {
    vi.stubGlobal(
      "fetch",
      overlaySettingsBackend({
        roles: ["admin"],
        allow: WIRING_READS,
        sorMode: "native",
      }),
    );
    renderSettings("connections");
    // Every surface here reads a per-user seam: the connector list is scoped to
    // the calling human server-side, and both LinkedIn cards read /me.
    expect(
      await screen.findByRole("heading", { name: "Connected inboxes" }),
    ).toBeTruthy();
    expect(
      screen.getByRole("heading", { name: "LinkedIn connections" }),
    ).toBeTruthy();
    expect(
      screen.getByRole("heading", { name: "Where your network reaches" }),
    ).toBeTruthy();
    // And nothing workspace-wide: a key everybody spends from, subscriptions
    // everybody's writes fire, the mirror that re-points every read.
    for (const heading of [
      "Contact data",
      "Webhooks",
      "HubSpot mirror",
      "Mirror user mapping",
    ]) {
      expect(screen.queryByRole("heading", { name: heading })).toBeNull();
    }
  });

  it("renders the installation's wiring on Integrations and none of the personal connections", async () => {
    vi.stubGlobal(
      "fetch",
      overlaySettingsBackend({
        roles: ["admin"],
        allow: WIRING_READS,
        sorMode: "native",
      }),
    );
    renderSettings("integrations");
    expect(
      await screen.findByRole("heading", { name: "Contact data" }),
    ).toBeTruthy();
    for (const heading of [
      "Webhooks",
      "HubSpot mirror",
      "Mirror user mapping",
    ]) {
      expect(screen.getByRole("heading", { name: heading })).toBeTruthy();
    }
    for (const heading of [
      "Connected inboxes",
      "LinkedIn connections",
      "Where your network reaches",
    ]) {
      expect(screen.queryByRole("heading", { name: heading })).toBeNull();
    }
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
    // The trail is the admin's alone, so every case below needs a principal who
    // may read it — an anonymous fixture would only ever exercise the withheld
    // rung, which has a case of its own on the Privacy & audit page.
    if (url.endsWith("/v1/me")) {
      return jsonResponse(meFixture({ roles: ["admin"] }));
    }
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
    const card = (
      await screen.findByRole("button", { name: /reset data/i })
    ).closest("section");
    if (!(card instanceof HTMLElement)) {
      throw new Error("the Reset data control renders outside a card");
    }
    // Read that card rather than the page: the summary is a status region inside
    // it, and the two cards above it on Maintenance each render a loading
    // skeleton that is also a status region while its query is in flight — a
    // page-wide query would be answered by whichever of those was still pending.
    expect(within(card).queryByRole("status")).not.toBeInTheDocument();
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
// place: the passports Agents lists, the consent registry and audit trail
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
  it("opens Agents for a read-only seat, passports and all", async () => {
    vi.stubGlobal(
      "fetch",
      mergedEntryBackend({ roles: ["rep"], seat: "read" }),
    );
    renderSettings("agents");
    await waitFor(() =>
      expect(
        screen
          .getByRole("link", { name: "Agents" })
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
    vi.stubGlobal(
      "fetch",
      mergedEntryBackend({ roles: ["admin"], allow: readOn("person") }),
    );
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

  // Before this page absorbed it, the automations editor was a route of its own
  // that nothing gated. Every seeded role holds `automation:read` and the server
  // serves them, so gating the merged entry on the WRITE grant would take a
  // working surface away from manager, rep and read_only — the merge inheriting
  // the spend cards' authority and dropping the door's.
  it("opens AI for a rep on the automations read alone, editor and all", async () => {
    vi.stubGlobal(
      "fetch",
      mergedEntryBackend({ roles: ["rep"], allow: { automation: ["read"] } }),
    );
    renderSettings("ai");
    await waitFor(() =>
      expect(
        screen.getByRole("link", { name: "AI" }).getAttribute("aria-current"),
      ).toBe("page"),
    );
    // The surface they came for.
    expect(
      await screen.findByRole("heading", { name: "Automations" }),
    ).toBeTruthy();
    // The spend card follows the automation WRITE grant, so this seat is not
    // handed the operator's bill — but it is told that, rather than left to read
    // an absent card as "nothing was spent".
    expect(
      screen.getByRole("heading", { name: "AI usage & budget" }),
    ).toBeTruthy();
    expect(
      screen.getByText(/only an operator can see what the AI runtime spent/i),
    ).toBeTruthy();
  });

  // The trail is the admin's alone, and this page opens for OPS — the consent
  // registry above it is theirs. Before the merge the audit log was an entry of
  // its own, gated on the admin role by the nav; merging it onto a page ops
  // reaches moved that gate's job into the card, and nothing was doing it.
  it("withholds the audit trail from an ops seat, and asks the server for nothing", async () => {
    const backend = mergedEntryBackend({
      roles: ["ops"],
      allow: readOn("person"),
    });
    vi.stubGlobal("fetch", backend);
    renderSettings("privacy");
    // Ops reaches the page for the registry, which renders.
    expect(
      await screen.findByRole("heading", { name: "Consent purposes" }),
    ).toBeTruthy();
    // The trail keeps its place and says why it is empty — absent, it would read
    // as "nothing has happened here", a different claim entirely.
    expect(
      await screen.findByRole("heading", { name: "Audit log" }),
    ).toBeTruthy();
    expect(
      screen.getByText(/only an admin can read the full trail/i),
    ).toBeTruthy();
    // Six inputs that narrow a list you cannot see are a control with nothing
    // behind it, so the filter row is absent rather than withheld.
    expect(screen.queryByRole("heading", { name: "Filters" })).toBeNull();
    // And the request is never issued: it could only ever come back 403, and a
    // red failure with a futile Retry is what the withheld body replaces.
    const asked = backend.mock.calls.map((call) =>
      String(call[0] instanceof Request ? call[0].url : call[0]),
    );
    expect(asked.some((url) => url.includes("/audit-log"))).toBe(false);
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
  // The dials and the list they narrow are ONE surface, the way every other
  // filtered list in this product draws them. Two cards made the filter row a
  // subject in the page outline, level with the trail it narrows, and left a
  // reader scanning two boxes to answer one question.
  it("puts the filters inside the log's own card, under the log's own name", async () => {
    vi.stubGlobal("fetch", auditLogBackend());
    render(<AuditLogCard />);
    await screen.findByText("update");

    const actorFilter = screen.getByLabelText("Actor");
    const entryAction = screen.getByText("update");
    const card = actorFilter.closest("section");
    expect(card).not.toBeNull();
    expect(entryAction.closest("section")).toBe(card);
    // One card, named for the log. The filters are labelled INSIDE it, at a
    // level that says they belong to it rather than stand beside it.
    expect(card).toContainElement(
      screen.getByRole("heading", { level: 2, name: "Audit log" }),
    );
    expect(card).toContainElement(
      screen.getByRole("heading", { level: 3, name: "Filters" }),
    );
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
      vi.fn(async (input: RequestInfo | URL) =>
        String(input instanceof Request ? input.url : input).endsWith("/v1/me")
          ? jsonResponse(meFixture({ roles: ["admin"] }))
          : jsonResponse({
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
        if (url.endsWith("/v1/me")) {
          return jsonResponse(meFixture({ roles: ["admin"] }));
        }
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
        if (url.endsWith("/v1/me")) {
          return jsonResponse(meFixture({ roles: ["admin"] }));
        }
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
