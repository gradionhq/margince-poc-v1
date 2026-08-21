/** @vitest-environment jsdom */
import { cleanup, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { pickOption } from "../design-system/select-testing";
import { SettingsScreen, settingsAddress } from "./settings";
import { jsonResponse, render } from "./settings.testkit";

// The Agents entry: the governed tool inventory an MCP client is shown, and the
// passports that scope it. Minting one has a file of its own
// (`settings-passports.test.tsx`); what is held here is the console — which
// tools an operator reads, which credential filters them, and the per-row revoke
// that has to reach BOTH cards, because they share the ["passports"] read.

// No shared fetch stub: the backend a claim needs is installed beside the claim,
// so what answered it is readable where it is asserted.
beforeEach(() => {
  globalThis.localStorage.setItem("margince.workspaceSlug", "acme");
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  globalThis.localStorage.clear();
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
    render(<SettingsScreen route={settingsAddress("agents")} />);

    await waitFor(() =>
      expect(screen.getAllByText("search_records").length).toBe(1),
    );
    expect(screen.getAllByText("send_email").length).toBe(1);

    const searchRow = document.querySelector<HTMLElement>(
      '[data-tool="search_records"]',
    );
    const sendRow = document.querySelector<HTMLElement>(
      '[data-tool="send_email"]',
    );
    expect(searchRow).toBeTruthy();
    expect(sendRow).toBeTruthy();
    // The egress "reaches out" badge shows only on the tool that reaches
    // outside the workspace (send_email), never on the pure-read tool.
    expect(sendRow && within(sendRow).getByText("reaches out")).toBeTruthy();
    expect(
      searchRow && within(searchRow).queryByText("reaches out"),
    ).toBeNull();
  });

  // The console's own promise is that it shows the surface an MCP client sees,
  // and a verb with an autonomy dot beside it is not that: what an agent
  // selects on is the written description the server serves, so the row has to
  // show it rather than leave an operator to guess what their agents are told.
  it("shows each tool's written display name and the text an agent selects it by", async () => {
    vi.stubGlobal("fetch", agentToolsBackend());
    render(<SettingsScreen route={settingsAddress("agents")} />);

    await waitFor(() =>
      expect(screen.getAllByText("search_records").length).toBe(1),
    );
    const searchRow = document.querySelector<HTMLElement>(
      '[data-tool="search_records"]',
    );
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
    const sendRow = document.querySelector<HTMLElement>(
      '[data-tool="send_email"]',
    );
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
    render(<SettingsScreen route={settingsAddress("agents")} />);
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
    render(<SettingsScreen route={settingsAddress("agents")} />);
    await screen.findByText("list_pipelines");

    const select = screen.getByLabelText("All passports");
    await pickOption(user, select, "Reachable by Scout");

    const freeRow = document.querySelector<HTMLElement>(
      '[data-tool="list_pipelines"]',
    );
    expect(freeRow).toBeTruthy();
    expect(
      freeRow && within(freeRow).queryByText("scope not granted"),
    ).toBeNull();

    const scopedRow = document.querySelector<HTMLElement>(
      '[data-tool="send_email"]',
    );
    expect(scopedRow).toBeTruthy();
    expect(
      scopedRow && within(scopedRow).getByText("scope not granted"),
    ).toBeTruthy();
  });

  // A sentence is never a control. The row's right column answers the question
  // the row asks — here the tier dot and the governance badges — and prose that
  // explains the row belongs with the naming on the left. This explanation used
  // to sit among the badges, which left the three tool rows answering at three
  // different widths while the words that matter most to a reader were the ones
  // pushed to the far edge.
  it("explains an unreachable tool beside its name, not in the answer column", async () => {
    const user = userEvent.setup();
    vi.stubGlobal("fetch", agentToolsWithPassportsBackend());
    render(<SettingsScreen route={settingsAddress("agents")} />);
    await screen.findByText("list_pipelines");

    await pickOption(
      user,
      screen.getByLabelText("All passports"),
      "Reachable by Scout",
    );

    const scopedRow = document.querySelector<HTMLElement>(
      '[data-tool="send_email"]',
    );
    const naming = scopedRow?.querySelector<HTMLElement>(".settingrow-naming");
    const answer = scopedRow?.querySelector<HTMLElement>(".settingrow-control");
    expect(naming).toBeTruthy();
    expect(answer).toBeTruthy();
    expect(
      naming && within(naming).getByText("scope not granted"),
    ).toBeTruthy();
    expect(
      answer && within(answer).queryByText("scope not granted"),
    ).toBeNull();
    // And the answer column still carries the governance it is there for.
    expect(answer && within(answer).getByText("send")).toBeTruthy();
    expect(answer && within(answer).getByText("reaches out")).toBeTruthy();
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
    render(<SettingsScreen route={settingsAddress("agents")} />);
    await screen.findByText("send_email");

    const select = screen.getByLabelText("All passports");
    await pickOption(user, select, "Reachable by Scout");
    const scopedRow = document.querySelector<HTMLElement>(
      '[data-tool="send_email"]',
    );
    expect(scopedRow).toBeTruthy();
    // Scout grants "read" only, so the send tool reads as out of scope while
    // Scout is the filter.
    expect(
      scopedRow && within(scopedRow).getByText("scope not granted"),
    ).toBeTruthy();

    const scoutRow = screen
      .getByText("Scout")
      .closest<HTMLElement>("[data-passport]");
    if (scoutRow === null) {
      throw new Error("the live passport row is not rendered");
    }
    await user.click(within(scoutRow).getByRole("button", { name: "Revoke" }));
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
      scopedRow && within(scopedRow).queryByText("scope not granted"),
    ).toBeNull();
  });

  it("revokes a non-revoked passport: click Revoke, confirm, DELETE fires with its id and the list refetches", async () => {
    const user = userEvent.setup();
    const deleted: string[] = [];
    const fetchMock = passportsBackend({ onDelete: (id) => deleted.push(id) });
    vi.stubGlobal("fetch", fetchMock);
    render(<SettingsScreen route={settingsAddress("agents")} />);
    await screen.findByText("Scout");

    // The already-revoked row shows no Revoke control at all.
    const retiredRow = screen.getByText("Retired").closest("[data-passport]");
    expect(retiredRow).toBeTruthy();
    expect(
      retiredRow && Array.from(retiredRow.querySelectorAll("button")).length,
    ).toBe(0);

    const scoutRow = screen
      .getByText("Scout")
      .closest<HTMLElement>("[data-passport]");
    if (scoutRow === null) {
      throw new Error("the live passport row is not rendered");
    }
    await user.click(within(scoutRow).getByRole("button", { name: "Revoke" }));

    const dialog = await screen.findByRole("dialog");
    const confirmButton = within(dialog).getByRole("button", {
      name: "Revoke",
    });
    const callsBeforeConfirm = fetchMock.mock.calls.length;
    await user.click(confirmButton);

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
