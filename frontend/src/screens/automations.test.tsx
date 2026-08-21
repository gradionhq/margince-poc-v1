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
import type { components } from "../api/schema";
import { type GrantSpec, meFixture } from "../app/mefixture";
import { pickOption } from "../design-system/select-testing";
import { LocaleProvider } from "../i18n";
import { AutomationRow, AutomationsAdmin, paramFields } from "./automations";

// B-EP09.15 acceptance: the editor is catalog-driven end to end — the
// anti-DSL guard (no free-form rule body, no user-defined trigger; form
// fields derive only from params_schema + name), the autonomy tier badge
// on every row, create-arrives-paused with the deliberate enable step
// (PATCH + If-Match), and authorship-blind rendering (the row is a pure
// function of the Automation wire schema).

beforeEach(() => {
  // the surface sits behind the auth gate in the app; the useMe probe needs a
  // resolved workspace before it will ask /v1/me
  globalThis.localStorage.setItem("margince.workspaceSlug", "acme");
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  globalThis.localStorage.clear();
  window.location.hash = "";
});

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
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

type CatalogEntry = components["schemas"]["AutomationCatalogEntry"];
type Automation = components["schemas"]["Automation"];

const dueInDaysSchema = (fallback: number) => ({
  type: "object",
  properties: {
    due_in_days: {
      type: "integer",
      minimum: 1,
      maximum: 30,
      default: fallback,
    },
  },
  required: ["due_in_days"],
});

const catalog: CatalogEntry[] = [
  {
    key: "stalled_deal_nudge",
    name: "Stalled-deal nudge",
    description: "Stages a follow-up when a deal stalls.",
    trigger: "deal.stalled",
    action: "send_email",
    tier: "confirmation_required",
    params_schema: dueInDaysSchema(3),
  },
  {
    key: "task_on_stage_entry",
    name: "Task on stage entry",
    trigger: "deal.stage_changed",
    action: "create_task",
    tier: "auto_execute",
    params_schema: dueInDaysSchema(7),
  },
];

type Recorded = {
  method: string;
  url: string;
  body: unknown;
  ifMatch: string | null;
};

// The surface asks four separate questions of the automation object, so the
// default fixture answers all four. `read` is not decoration: AutomationStore's
// List gates on it, so the instances query is disabled without it and an
// operator fixture missing it would prove the list renders for a seat the
// server would refuse.
const AUTOMATION_OPERATOR: GrantSpec = {
  automation: ["create", "read", "update", "delete"],
};

// The verbs a row folds behind its overflow control. Opening it is a step the
// reader takes deliberately, so a test asserting on Edit / Runs / Preview /
// Delete takes it too.
async function openRowMenu(name = "Nudge stalled fleet deals") {
  await userEvent.click(
    screen.getByRole("button", { name: `Actions for ${name}` }),
  );
}

// Authoring is a name plus every parameter the schema declares, submitted
// together, so it lives in a dialog behind the library's verb. A test whose
// subject is one of those fields opens the dialog first and scopes its queries
// to it — the card behind it still carries the library the verb came from.
async function openCreateDialog(index = 0): Promise<HTMLElement> {
  await userEvent.click(
    screen.getAllByRole("button", { name: "Use template" })[index],
  );
  return screen.getByRole("dialog");
}

// The same, for a configured automation's own definition.
async function openRowEditor(name = "Nudge stalled fleet deals") {
  await openRowMenu(name);
  await userEvent.click(screen.getByRole("button", { name: "Edit" }));
  return screen.getByRole("dialog");
}

// React mints an id per render tree, and a second tree in the same document
// gets different ones. They say nothing about the row's CONTENT, which is what
// the authorship-blindness check is about, so the comparison drops them.
function withoutGeneratedIds(html: string): string {
  return html.replaceAll(/_r_[0-9a-z]+_/g, "_rid_");
}

function automationsBackend(
  automations: Automation[],
  calls: Recorded[],
  allow: GrantSpec = AUTOMATION_OPERATOR,
) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const request = input instanceof Request ? input : null;
    const url = String(request ? request.url : input);
    const method = request ? request.method : (init?.method ?? "GET");
    if (url.endsWith("/v1/me")) {
      return jsonResponse(meFixture({ allow }));
    }
    if (url.includes("/automations/catalog")) {
      return jsonResponse({ data: catalog });
    }
    if (url.includes("/automations") && method === "POST") {
      const body: unknown = request
        ? await request.json()
        : JSON.parse(String(init?.body));
      calls.push({ method, url, body, ifMatch: null });
      const created: Automation = {
        id: `au-${automations.length + 1}`,
        ...(body as {
          key: string;
          name: string;
          params: Automation["params"];
        }),
        status: "paused",
        version: 1,
        created_at: "2026-07-05T08:00:00Z",
      };
      automations.push(created);
      return jsonResponse(created, 201);
    }
    if (/\/automations\/au-\d+$/.test(url) && method === "PATCH") {
      const body: unknown = request
        ? await request.json()
        : JSON.parse(String(init?.body));
      calls.push({
        method,
        url,
        body,
        ifMatch: request?.headers.get("If-Match") ?? null,
      });
      return jsonResponse({ ...automations[0], status: "enabled" });
    }
    if (url.includes("/automations")) {
      return jsonResponse({ data: automations, page: { next_cursor: null } });
    }
    return jsonResponse({ data: [], page: { next_cursor: null } });
  });
}

const instance = (over: Partial<Automation>): Automation => ({
  id: "au-1",
  key: "stalled_deal_nudge",
  name: "Nudge stalled fleet deals",
  status: "paused",
  params: { due_in_days: 3 },
  version: 3,
  created_at: "2026-07-01T08:00:00Z",
  ...over,
});

describe("AutomationsAdmin (B-EP09.15)", () => {
  // It renders INSIDE the Settings → AI page, which already owns the `.wrap`
  // reading column and the h1 naming the tab. A nested column double-pads the
  // page and a second h1 gives the document two page titles, so this surface
  // contributes neither.
  it("renders as a settings section: no reading column of its own, no page heading", async () => {
    vi.stubGlobal("fetch", automationsBackend([], []));
    const { container } = render(<AutomationsAdmin />);
    await waitFor(() =>
      expect(screen.getByText("Stalled-deal nudge")).toBeTruthy(),
    );
    expect(container.querySelector(".wrap")).toBeNull();
    expect(screen.queryByRole("heading", { level: 1 })).toBeNull();
    expect(
      screen.getByRole("heading", { level: 2, name: "Automations" }),
    ).toBeTruthy();
  });

  it("anti-DSL guard: form fields derive only from params_schema + name — no rule body, no trigger input", async () => {
    vi.stubGlobal("fetch", automationsBackend([], []));
    render(<AutomationsAdmin />);
    await waitFor(() =>
      expect(screen.getByText("Stalled-deal nudge")).toBeTruthy(),
    );
    const dialog = await openCreateDialog();
    // exactly the name field + the one schema-derived integer parameter
    expect(within(dialog).getAllByRole("textbox")).toHaveLength(1);
    expect(within(dialog).getAllByRole("spinbutton")).toHaveLength(1);
    expect(
      within(dialog).getByRole("spinbutton", { name: "due_in_days" }),
    ).toBeTruthy();
    // No free-form rule body, no user-defined trigger, anywhere on the page —
    // the DOCUMENT, not the render container: the dialog is portalled to the
    // body, so a container-scoped count would pass by looking somewhere the
    // form is not.
    expect(document.querySelectorAll("textarea")).toHaveLength(0);
    expect(screen.queryByRole("textbox", { name: /trigger/i })).toBeNull();
    // the schema bounds reach the input verbatim
    const param = screen.getByRole("spinbutton", { name: "due_in_days" });
    expect(param.getAttribute("min")).toBe("1");
    expect(param.getAttribute("max")).toBe("30");
  });

  it("paramFields reads only typed schema properties", () => {
    expect(paramFields(dueInDaysSchema(3))).toEqual([
      { key: "due_in_days", kind: "integer", min: 1, max: 30, initial: "3" },
    ]);
    expect(paramFields({})).toEqual([]);
  });

  it("create posts key+name+params, arrives paused, and enable is the deliberate If-Match PATCH", async () => {
    const automations: Automation[] = [];
    const calls: Recorded[] = [];
    vi.stubGlobal("fetch", automationsBackend(automations, calls));
    render(<AutomationsAdmin />);
    await waitFor(() =>
      expect(screen.getByText("Stalled-deal nudge")).toBeTruthy(),
    );
    await openCreateDialog();
    await userEvent.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0].body).toMatchObject({
      key: "stalled_deal_nudge",
      name: "Stalled-deal nudge",
      params: { due_in_days: 3 },
    });
    // the honest post-create state: paused until the user enables it
    await waitFor(() =>
      expect(
        screen.getByText("Created paused — nothing runs until you enable it."),
      ).toBeTruthy(),
    );
    const row = document.querySelector('[data-automation="au-1"]');
    expect(row).not.toBeNull();
    if (row instanceof HTMLElement) {
      // The state is the switch's own, announced rather than restated beside it
      // in a second vocabulary: a created automation arrives OFF.
      expect(
        within(row)
          .getByRole("switch", { name: /is enabled$/ })
          .getAttribute("aria-checked"),
      ).toBe("false");
    }
    await userEvent.click(
      screen.getByRole("switch", { name: "Stalled-deal nudge is enabled" }),
    );
    await waitFor(() => expect(calls).toHaveLength(2));
    expect(calls[1].body).toMatchObject({ status: "enabled" });
    expect(calls[1].ifMatch).toBe("1");
  });

  it("each row wears its catalog autonomy tier through AutonomyDot", async () => {
    const automations = [
      instance({
        id: "au-1",
        key: "stalled_deal_nudge",
        name: "Confirmation-required one",
      }),
      instance({
        id: "au-2",
        key: "task_on_stage_entry",
        name: "Auto-execute one",
        params: { due_in_days: 7 },
      }),
    ];
    vi.stubGlobal("fetch", automationsBackend(automations, []));
    render(<AutomationsAdmin />);
    await waitFor(() =>
      expect(screen.getByText("Confirmation-required one")).toBeTruthy(),
    );
    const confirmationRequired = screen
      .getByText("Confirmation-required one")
      .closest("li");
    const autoExecute = screen.getByText("Auto-execute one").closest("li");
    expect(confirmationRequired).not.toBeNull();
    expect(autoExecute).not.toBeNull();
    if (confirmationRequired && autoExecute) {
      expect(
        within(confirmationRequired).getByRole("img", {
          name: "confirm-first",
        }),
      ).toBeTruthy();
      expect(
        within(autoExecute).getByRole("img", { name: "auto-execute" }),
      ).toBeTruthy();
    }
  });

  it("renders an instance from the wire schema alone — authorship cannot change the row", async () => {
    // origin is not on the wire: two instances with identical Automation
    // fields (one imagined agent-authored, one catalog-authored) MUST
    // produce identical markup.
    vi.stubGlobal("fetch", automationsBackend([], []));
    const fields = instance({});
    const first = render(
      <ul>
        <AutomationRow
          automation={{ ...fields }}
          entry={catalog[0]}
          canViewRuns
          canEdit
          canDelete
        />
      </ul>,
    );
    const firstHtml = withoutGeneratedIds(first.container.innerHTML);
    cleanup();
    const second = render(
      <ul>
        <AutomationRow
          automation={{ ...fields }}
          entry={catalog[0]}
          canViewRuns
          canEdit
          canDelete
        />
      </ul>,
    );
    expect(withoutGeneratedIds(second.container.innerHTML)).toBe(firstHtml);
  });

  it("a role without the automation config grant gets the honest read-only editor", async () => {
    // manager/rep hold read-only automation grants: the
    // surface still shows catalog + instances, but no affordance that could
    // only 403 — and it says WHY instead of silently thinning out.
    const automations = [instance({})];
    vi.stubGlobal(
      "fetch",
      automationsBackend(automations, [], { automation: ["read"] }),
    );
    render(<AutomationsAdmin />);
    await waitFor(() =>
      expect(screen.getByText("Nudge stalled fleet deals")).toBeTruthy(),
    );
    expect(
      screen.getByText(
        "Read-only view — you do not have permission to change automations.",
      ),
    ).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Use template" })).toBeNull();
    // No switch to flip, and the badge in its place so the state is still a
    // read this row answers.
    expect(screen.queryByRole("switch")).toBeNull();
    expect(screen.getByText("paused")).toBeTruthy();
    await openRowMenu();
    expect(screen.queryByRole("button", { name: "Edit" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Delete" })).toBeNull();
  });

  // One grant at a time. A fixture holding create+update+delete together
  // cannot tell a correct binding from a transposed one — swapping update and
  // delete in the screen passes such a suite outright. These pin each verb to
  // the control it actually governs.
  it("shows only the affordances the specific grant covers", async () => {
    const automations = [instance({})];
    vi.stubGlobal(
      "fetch",
      automationsBackend(automations, [], { automation: ["read", "update"] }),
    );
    render(<AutomationsAdmin />);
    await waitFor(() =>
      expect(screen.getByText("Nudge stalled fleet deals")).toBeTruthy(),
    );
    // update -> the setting writes when you flip it, so it is a switch
    expect(
      screen.getAllByRole("switch", {
        name: "Nudge stalled fleet deals is enabled",
      }).length,
    ).toBe(1);
    await openRowMenu();
    expect(screen.getByRole("button", { name: "Edit" })).toBeTruthy();
    // no delete grant -> the destructive control is withheld
    expect(screen.queryByRole("button", { name: "Delete" })).toBeNull();
    // no create grant -> the catalog cannot be instantiated
    expect(screen.queryByRole("button", { name: "Use template" })).toBeNull();
  });

  it("offers deletion only with the delete grant, and nothing else with it", async () => {
    const automations = [instance({})];
    vi.stubGlobal(
      "fetch",
      automationsBackend(automations, [], { automation: ["read", "delete"] }),
    );
    render(<AutomationsAdmin />);
    await waitFor(() =>
      expect(screen.getByText("Nudge stalled fleet deals")).toBeTruthy(),
    );
    await openRowMenu();
    expect(screen.getAllByRole("button", { name: "Delete" }).length).toBe(1);
    expect(screen.queryByRole("switch")).toBeNull();
    expect(screen.queryByRole("button", { name: "Edit" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Use template" })).toBeNull();
  });

  it("deleting asks first — the confirm is what writes, not the menu item", async () => {
    const automations = [instance({})];
    const calls: Recorded[] = [];
    vi.stubGlobal(
      "fetch",
      automationsBackend(automations, calls, {
        automation: ["read", "delete"],
      }),
    );
    render(<AutomationsAdmin />);
    await waitFor(() =>
      expect(screen.getByText("Nudge stalled fleet deals")).toBeTruthy(),
    );
    await openRowMenu();
    await userEvent.click(screen.getByRole("button", { name: "Delete" }));
    // The dialog names the automation and says what pausing would do instead;
    // nothing has been written yet.
    expect(
      screen.getByRole("heading", { name: "Delete this automation?" }),
    ).toBeTruthy();
    expect(calls.filter((call) => call.method === "DELETE")).toHaveLength(0);
  });

  // The runs and preview inspectors are READS (automations_runs.go gates on
  // automation:read; Preview resolves through Get). A read-only principal must
  // still reach them.
  it("offers the runs and preview inspectors on the read grant alone", async () => {
    const automations = [instance({})];
    vi.stubGlobal(
      "fetch",
      automationsBackend(automations, [], { automation: ["read"] }),
    );
    render(<AutomationsAdmin />);
    await waitFor(() =>
      expect(screen.getByText("Nudge stalled fleet deals")).toBeTruthy(),
    );
    await openRowMenu();
    expect(screen.getAllByRole("button", { name: "Runs" }).length).toBe(1);
    expect(screen.getAllByRole("button", { name: "Preview" }).length).toBe(1);
    expect(screen.queryByRole("switch")).toBeNull();
    expect(screen.queryByRole("button", { name: "Delete" })).toBeNull();
  });

  // The row language: this card holds two decisions, and each of them IS a list
  // rather than an answer that would fit beside its naming — so each takes the
  // full width below it, and neither carries a heading of its own on top of the
  // panel's.
  it("lays both lists out as stacked settings rows under one heading", async () => {
    const automations = [instance({})];
    vi.stubGlobal("fetch", automationsBackend(automations, []));
    render(<AutomationsAdmin />);
    await waitFor(() =>
      expect(screen.getByText("Nudge stalled fleet deals")).toBeTruthy(),
    );
    for (const label of ["Configured automations", "Starter library"]) {
      const row = screen.getByText(label).closest(".settingrow");
      expect(row).not.toBeNull();
      if (row instanceof HTMLElement) {
        expect(row.className).toContain("settingrow-stack");
      }
    }
    // One heading, the panel's own.
    expect(screen.getAllByRole("heading", { level: 2 })).toHaveLength(1);
    expect(screen.queryByRole("heading", { level: 3 })).toBeNull();
  });

  // Every library entry is one ROW of the same language, so the hairlines do the
  // separating. As a bare `<ul>` an entry ran a name, a sentence and a mono
  // trigger/action pair together with no interval between the lines and no rule
  // between entries, and its verb floated at the right of the first line.
  it("gives every library entry a row, its recipe, and its verb in the answer column", async () => {
    vi.stubGlobal("fetch", automationsBackend([], []));
    render(<AutomationsAdmin />);

    const list = await screen.findByTestId("auto-catalog");
    const rows = list.querySelectorAll(":scope > .settingrow");
    expect(rows).toHaveLength(catalog.length);

    const stalled = screen
      .getByText("Stalled-deal nudge")
      .closest(".settingrow");
    expect(stalled).not.toBeNull();
    if (stalled instanceof HTMLElement) {
      // The recipe is part of the naming, not a third line under the row.
      const recipe = within(stalled).getByText(
        /deal\.stalled\s*->\s*send_email/,
      );
      expect(recipe.closest(".settingrow-naming")).not.toBeNull();
      // The verb sits at the one x every answer on this page sits at.
      expect(
        within(stalled)
          .getByRole("button", { name: "Use template" })
          .closest(".settingrow-control"),
      ).not.toBeNull();
    }
  });

  it("opens a configured automation's definition in a dialog, not under the row", async () => {
    const automations = [instance({})];
    vi.stubGlobal("fetch", automationsBackend(automations, []));
    render(<AutomationsAdmin />);
    await waitFor(() =>
      expect(screen.getByText("Nudge stalled fleet deals")).toBeTruthy(),
    );
    // Nothing to edit with until the verb is used: the row is an answer.
    expect(screen.queryByRole("dialog")).toBeNull();
    const dialog = await openRowEditor();
    // The dialog names WHICH automation is open, because it covers the row that
    // would otherwise have said.
    expect(
      within(dialog).getByRole("heading", {
        name: "Nudge stalled fleet deals",
      }),
    ).toBeTruthy();
    expect(
      within(dialog).getByRole("spinbutton", { name: "due_in_days" }),
    ).toBeTruthy();
    expect(within(dialog).getByRole("button", { name: "Save" })).toBeTruthy();
  });

  it("reports a refused definition edit inside the dialog, never behind it", async () => {
    const automations = [instance({})];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const request = input instanceof Request ? input : null;
        const url = String(request ? request.url : input);
        const method = request ? request.method : (init?.method ?? "GET");
        if (url.endsWith("/v1/me")) {
          return jsonResponse(meFixture({ allow: AUTOMATION_OPERATOR }));
        }
        if (url.includes("/automations/catalog")) {
          return jsonResponse({ data: catalog });
        }
        if (/\/automations\/au-\d+$/.test(url) && method === "PATCH") {
          return jsonResponse(
            {
              title: "Conflict",
              status: 409,
              detail: "somebody else changed this automation",
            },
            409,
          );
        }
        return jsonResponse({
          data: automations,
          page: { next_cursor: null },
        });
      }),
    );
    const { container } = render(<AutomationsAdmin />);
    await waitFor(() =>
      expect(screen.getByText("Nudge stalled fleet deals")).toBeTruthy(),
    );
    const dialog = await openRowEditor();
    await userEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    // Where the reader still is. The card behind the dialog says nothing: a
    // refusal reported there is a refusal reported behind the thing covering
    // it.
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      /somebody else changed this automation/,
    );
    expect(container.querySelector('[role="alert"]')).toBeNull();
  });

  it("the config affordances stay for admin", async () => {
    const automations = [instance({})];
    vi.stubGlobal("fetch", automationsBackend(automations, []));
    render(<AutomationsAdmin />);
    await waitFor(() =>
      expect(screen.getByText("Nudge stalled fleet deals")).toBeTruthy(),
    );
    expect(
      screen.queryByText(
        "Read-only view — you do not have permission to change automations.",
      ),
    ).toBeNull();
    await waitFor(() =>
      expect(
        screen.getAllByRole("button", { name: "Use template" }).length,
      ).toBeGreaterThan(0),
    );
    expect(screen.getByRole("switch", { name: /is enabled$/ })).toBeTruthy();
    await openRowMenu();
    expect(screen.getByRole("button", { name: "Delete" })).toBeTruthy();
  });
});

// The two ops behind these toggles are human-only and gated on the same
// automation:update grant as pause and edit; the panels mount lazily and
// independently (opening one never closes the other).
describe("AutomationRow — Runs/Preview toggles", () => {
  // A benign stub for the lazily-mounted panels' first fetch: the toggle
  // tests care about mount/independence, not panel contents, so runs answer
  // an empty page and preview a zero-radius result.
  function panelBackend() {
    return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : null;
      const url = String(request ? request.url : input);
      const method = request ? request.method : (init?.method ?? "GET");
      if (url.includes("/preview") && method === "POST") {
        return jsonResponse({
          matches_now: 0,
          would_have_fired: 0,
          window_days: 30,
        });
      }
      return jsonResponse({ data: [], page: { next_cursor: null } });
    });
  }

  it("shows the Runs/Preview toggles on the READ grant, not the write one", async () => {
    vi.stubGlobal("fetch", panelBackend());
    const view = render(
      <ul>
        <AutomationRow
          automation={instance({})}
          entry={catalog[0]}
          canViewRuns
          canEdit
          canDelete
        />
      </ul>,
    );
    await openRowMenu();
    expect(screen.getByRole("button", { name: "Runs" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Preview" })).toBeTruthy();
    view.unmount();

    // Read but no write: the inspectors stay, because the server gates them on
    // automation:read. Hiding them behind the write grant — as the old role
    // proxy did — withheld a surface a reader is entitled to.
    const readOnly = render(
      <ul>
        <AutomationRow
          automation={instance({})}
          entry={catalog[0]}
          canViewRuns
          canEdit={false}
          canDelete={false}
        />
      </ul>,
    );
    await openRowMenu();
    expect(screen.getByRole("button", { name: "Runs" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Preview" })).toBeTruthy();
    readOnly.unmount();

    // No read grant: gone. The menu is still there — Edit and Delete are the
    // caller's other two grants — so the assertion is about the items, not
    // about the control that holds them.
    render(
      <ul>
        <AutomationRow
          automation={instance({})}
          entry={catalog[0]}
          canViewRuns={false}
          canEdit
          canDelete
        />
      </ul>,
    );
    await openRowMenu();
    expect(screen.queryByRole("button", { name: "Runs" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Preview" })).toBeNull();
  });

  it("mounts the runs panel on click without mounting preview", async () => {
    vi.stubGlobal("fetch", panelBackend());
    render(
      <ul>
        <AutomationRow
          automation={instance({})}
          entry={catalog[0]}
          canViewRuns
          canEdit
          canDelete
        />
      </ul>,
    );
    expect(screen.queryByTestId("automation-runs")).toBeNull();
    await openRowMenu();
    await userEvent.click(screen.getByRole("button", { name: "Runs" }));
    expect(screen.getByTestId("automation-runs")).toBeTruthy();
    expect(screen.queryByTestId("automation-preview")).toBeNull();
  });

  it("keeps both panels open independently", async () => {
    vi.stubGlobal("fetch", panelBackend());
    render(
      <ul>
        <AutomationRow
          automation={instance({})}
          entry={catalog[0]}
          canViewRuns
          canEdit
          canDelete
        />
      </ul>,
    );
    await openRowMenu();
    await userEvent.click(screen.getByRole("button", { name: "Runs" }));
    await userEvent.click(screen.getByRole("button", { name: "Preview" }));
    expect(screen.getByTestId("automation-runs")).toBeTruthy();
    expect(screen.getByTestId("automation-preview")).toBeTruthy();
  });
});

// GH-706: renewal_reminder's real catalog schema (automations_catalog.go's
// renewalReminderSchema) — days_before stays the one integer param; object
// and date_field name the workspace's own cf_* column to watch; recurs_yearly
// opts a stored value into yearly re-arming. This is the boolean case and
// the date_field picker, proven against the CLOSED catalog-driven renderer
// (paramKind/paramFields/paramsFromValues) rather than a renewal_reminder
// special case in the component.
type CustomField = components["schemas"]["CustomField"];

const renewalReminderSchema = {
  type: "object",
  properties: {
    date_field: {
      type: "string",
      description: "The workspace's own custom date-field name to watch.",
    },
    days_before: {
      type: "integer",
      minimum: 1,
      maximum: 365,
      default: 30,
    },
    object: {
      type: "string",
      enum: ["person", "organization", "deal", "lead", "project"],
      description: "Which record type owns the watched date field.",
    },
    recurs_yearly: {
      type: "boolean",
      default: false,
      description: "Whether the watched date recurs every year.",
    },
  },
};

const renewalCatalogEntry: CatalogEntry = {
  key: "renewal_reminder",
  name: "Renewal reminder",
  trigger: "clock.daily",
  action: "create_task",
  tier: "auto_execute",
  params_schema: renewalReminderSchema,
};

function customField(overrides: Partial<CustomField>): CustomField {
  return {
    id: "cf-1",
    object: "person",
    label: "Field",
    slug: "field",
    type: "text",
    status: "active",
    column_name: "cf_field",
    created_by: "u1",
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

const PERSON_DATE_FIELDS: CustomField[] = [
  customField({
    id: "cf-birthday",
    object: "person",
    label: "Birthday",
    slug: "birthday",
    type: "date",
    column_name: "cf_birthday",
  }),
  // Filtered out on two different grounds — retired, and the wrong type —
  // proving the picker's client-side filter reads both, not just one.
  customField({
    id: "cf-old",
    object: "person",
    label: "Old renewal date",
    slug: "old-renewal-date",
    type: "date",
    status: "retired",
    column_name: "cf_old_renewal_date",
  }),
  customField({
    id: "cf-name",
    object: "person",
    label: "Nickname",
    slug: "nickname",
    type: "text",
    column_name: "cf_nickname",
  }),
];

function renewalBackend(calls: Recorded[]) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const request = input instanceof Request ? input : null;
    const url = String(request ? request.url : input);
    const method = request ? request.method : (init?.method ?? "GET");
    if (url.endsWith("/v1/me")) {
      return jsonResponse(meFixture({ allow: AUTOMATION_OPERATOR }));
    }
    if (url.includes("/automations/catalog")) {
      return jsonResponse({ data: [renewalCatalogEntry] });
    }
    if (url.includes("/custom-fields")) {
      const object = new URL(url).searchParams.get("object");
      return jsonResponse({
        data: PERSON_DATE_FIELDS.filter((field) => field.object === object),
        page: { next_cursor: null },
      });
    }
    if (url.includes("/automations") && method === "POST") {
      const body: unknown = request
        ? await request.json()
        : JSON.parse(String(init?.body));
      calls.push({ method, url, body, ifMatch: null });
      return jsonResponse(
        {
          id: "au-1",
          ...(body as { key: string; name: string; params: unknown }),
          status: "paused",
          version: 1,
          created_at: "2026-08-13T08:00:00Z",
        },
        201,
      );
    }
    return jsonResponse({ data: [], page: { next_cursor: null } });
  });
}

describe("renewal_reminder's schema-driven params (GH-706)", () => {
  it("paramFields reads days_before/object/recurs_yearly, gives date_field its own picker kind, and object its enum options", () => {
    expect(paramFields(renewalReminderSchema)).toEqual([
      { key: "date_field", kind: "date_field", initial: "" },
      { key: "days_before", kind: "integer", min: 1, max: 365, initial: "30" },
      {
        key: "object",
        kind: "enum",
        initial: "",
        options: ["person", "organization", "deal", "lead", "project"],
      },
      { key: "recurs_yearly", kind: "boolean", initial: "false" },
    ]);
  });

  it("the boolean param renders as a checkbox, and object/date_field render as selects rather than free text", async () => {
    vi.stubGlobal("fetch", renewalBackend([]));
    render(<AutomationsAdmin />);
    await waitFor(() =>
      expect(screen.getByText("Renewal reminder")).toBeTruthy(),
    );
    await openCreateDialog();

    // recurs_yearly: a checkbox, not a spinbutton or free-text box, and it
    // starts at the schema's own default (false, i.e. unchecked).
    const recurring = screen.getByRole("checkbox", { name: "recurs_yearly" });
    expect(recurring).not.toBeChecked();

    // object: a combobox sourced from the schema's own enum, and date_field:
    // a combobox (the Select control) too — neither a native <select> nor a
    // plain textbox a typo could break. days_before stays an ordinary
    // spinbutton.
    expect(screen.getByRole("combobox", { name: "object" })).toBeTruthy();
    expect(screen.getByRole("combobox", { name: "date_field" })).toBeTruthy();
    expect(
      screen.getByRole("spinbutton", { name: "days_before" }),
    ).toBeTruthy();
  });

  it("the date_field picker is disabled with a hint until an object is chosen, then queries /custom-fields for it", async () => {
    vi.stubGlobal("fetch", renewalBackend([]));
    render(<AutomationsAdmin />);
    await waitFor(() =>
      expect(screen.getByText("Renewal reminder")).toBeTruthy(),
    );
    await openCreateDialog();

    // No object chosen yet: disabled, with the reason stated rather than a
    // silently broken empty control.
    expect(screen.getByRole("combobox", { name: "date_field" })).toBeDisabled();
    expect(
      screen.getByText("Choose an object first to list its date fields."),
    ).toBeTruthy();

    await pickOption(
      userEvent.setup(),
      screen.getByRole("combobox", { name: "object" }),
      "person",
    );

    const picker = screen.getByRole("combobox", { name: "date_field" });
    await waitFor(() => expect(picker).not.toBeDisabled());

    // Only the active, date-typed field reaches the list — the retired one
    // and the text-typed one are both filtered out, on two different
    // grounds, so this proves the picker reads both checks.
    await userEvent.click(picker);
    const listbox = screen.getByRole("listbox");
    expect(
      within(listbox).getByRole("option", { name: "Birthday" }),
    ).toBeTruthy();
    expect(
      within(listbox).queryByRole("option", { name: "Old renewal date" }),
    ).toBeNull();
    expect(
      within(listbox).queryByRole("option", { name: "Nickname" }),
    ).toBeNull();
  });

  it("disables the date_field picker with a load-error hint when /custom-fields fails, rather than an empty enabled control", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const request = input instanceof Request ? input : null;
        const url = String(request ? request.url : input);
        if (url.endsWith("/v1/me")) {
          return jsonResponse(meFixture({ allow: AUTOMATION_OPERATOR }));
        }
        if (url.includes("/automations/catalog")) {
          return jsonResponse({ data: [renewalCatalogEntry] });
        }
        if (url.includes("/custom-fields")) {
          return jsonResponse(
            { code: "internal", message: "custom field lookup failed" },
            500,
          );
        }
        return jsonResponse({ data: [], page: { next_cursor: null } });
      }),
    );
    render(<AutomationsAdmin />);
    await waitFor(() =>
      expect(screen.getByText("Renewal reminder")).toBeTruthy(),
    );
    await openCreateDialog();

    await pickOption(
      userEvent.setup(),
      screen.getByRole("combobox", { name: "object" }),
      "person",
    );

    const picker = screen.getByRole("combobox", { name: "date_field" });
    await waitFor(() => expect(picker).toBeDisabled());
    expect(
      screen.getByText("Couldn't load this object's date fields. Try again."),
    ).toBeTruthy();
  });

  it("round-trips the boolean and the picked date field through paramsFromValues into the create request", async () => {
    const calls: Recorded[] = [];
    vi.stubGlobal("fetch", renewalBackend(calls));
    render(<AutomationsAdmin />);
    await waitFor(() =>
      expect(screen.getByText("Renewal reminder")).toBeTruthy(),
    );
    await openCreateDialog();

    await pickOption(
      userEvent.setup(),
      screen.getByRole("combobox", { name: "object" }),
      "person",
    );
    await waitFor(() =>
      expect(
        screen.getByRole("combobox", { name: "date_field" }),
      ).not.toBeDisabled(),
    );
    await pickOption(
      userEvent.setup(),
      screen.getByRole("combobox", { name: "date_field" }),
      "Birthday",
    );
    await userEvent.click(
      screen.getByRole("checkbox", { name: "recurs_yearly" }),
    );
    await userEvent.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0].body).toMatchObject({
      key: "renewal_reminder",
      params: {
        object: "person",
        date_field: "cf_birthday",
        recurs_yearly: true,
        days_before: 30,
      },
    });
  });
});
