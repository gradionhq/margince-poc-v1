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
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { AutomationRow, AutomationsScreen, paramFields } from "./automations";

// B-EP09.15 acceptance: the editor is catalog-driven end to end — the
// anti-DSL guard (no free-form rule body, no user-defined trigger; form
// fields derive only from params_schema + name), the autonomy tier badge
// on every row, create-arrives-paused with the deliberate enable step
// (PATCH + If-Match), and authorship-blind rendering (the row is a pure
// function of the Automation wire schema).

beforeEach(() => {
  // the screen sits behind the auth gate in the app; the useMe probe needs a
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
    tier: "yellow",
    params_schema: dueInDaysSchema(3),
  },
  {
    key: "task_on_stage_entry",
    name: "Task on stage entry",
    trigger: "deal.stage_changed",
    action: "create_task",
    tier: "green",
    params_schema: dueInDaysSchema(7),
  },
];

type Recorded = {
  method: string;
  url: string;
  body: unknown;
  ifMatch: string | null;
};

function automationsBackend(
  automations: Automation[],
  calls: Recorded[],
  roles: string[] = ["admin"],
) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const request = input instanceof Request ? input : null;
    const url = String(request ? request.url : input);
    const method = request ? request.method : (init?.method ?? "GET");
    if (url.endsWith("/v1/me")) {
      return jsonResponse({ user: {}, roles, teams: [] });
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

describe("AutomationsScreen (B-EP09.15)", () => {
  it("anti-DSL guard: form fields derive only from params_schema + name — no rule body, no trigger input", async () => {
    vi.stubGlobal("fetch", automationsBackend([], []));
    const { container } = render(<AutomationsScreen />);
    await waitFor(() =>
      expect(screen.getByText("Stalled-deal nudge")).toBeTruthy(),
    );
    await userEvent.click(
      screen.getAllByRole("button", { name: "Use template" })[0],
    );
    // exactly the name field + the one schema-derived integer parameter
    expect(screen.getAllByRole("textbox")).toHaveLength(1);
    expect(screen.getAllByRole("spinbutton")).toHaveLength(1);
    expect(
      screen.getByRole("spinbutton", { name: "due_in_days" }),
    ).toBeTruthy();
    // no free-form rule body, no user-defined trigger, anywhere
    expect(container.querySelectorAll("textarea")).toHaveLength(0);
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
    render(<AutomationsScreen />);
    await waitFor(() =>
      expect(screen.getByText("Stalled-deal nudge")).toBeTruthy(),
    );
    await userEvent.click(
      screen.getAllByRole("button", { name: "Use template" })[0],
    );
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
      expect(within(row).getByText("paused")).toBeTruthy();
    }
    await userEvent.click(screen.getByRole("button", { name: "Enable" }));
    await waitFor(() => expect(calls).toHaveLength(2));
    expect(calls[1].body).toMatchObject({ status: "enabled" });
    expect(calls[1].ifMatch).toBe("1");
  });

  it("each row wears its catalog autonomy tier through AutonomyDot", async () => {
    const automations = [
      instance({ id: "au-1", key: "stalled_deal_nudge", name: "Yellow one" }),
      instance({
        id: "au-2",
        key: "task_on_stage_entry",
        name: "Green one",
        params: { due_in_days: 7 },
      }),
    ];
    vi.stubGlobal("fetch", automationsBackend(automations, []));
    render(<AutomationsScreen />);
    await waitFor(() => expect(screen.getByText("Yellow one")).toBeTruthy());
    const yellow = screen.getByText("Yellow one").closest("li");
    const green = screen.getByText("Green one").closest("li");
    expect(yellow).not.toBeNull();
    expect(green).not.toBeNull();
    if (yellow && green) {
      expect(
        within(yellow).getByRole("img", { name: "confirm-first" }),
      ).toBeTruthy();
      expect(
        within(green).getByRole("img", { name: "auto-execute" }),
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
          canConfigure
        />
      </ul>,
    );
    const firstHtml = first.container.innerHTML;
    cleanup();
    const second = render(
      <ul>
        <AutomationRow
          automation={{ ...fields }}
          entry={catalog[0]}
          canConfigure
        />
      </ul>,
    );
    expect(second.container.innerHTML).toBe(firstHtml);
  });

  it("a role without the automation config grant gets the honest read-only editor", async () => {
    // manager/rep hold read-only automation grants: the
    // screen still shows catalog + instances, but no affordance that could
    // only 403 — and it says WHY instead of silently thinning out.
    const automations = [instance({})];
    vi.stubGlobal("fetch", automationsBackend(automations, [], ["rep"]));
    render(<AutomationsScreen />);
    await waitFor(() =>
      expect(screen.getByText("Nudge stalled fleet deals")).toBeTruthy(),
    );
    expect(
      screen.getByText(
        "Read-only view — automation settings are managed by the admin and ops roles.",
      ),
    ).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Use template" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Enable" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Edit" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Delete" })).toBeNull();
  });

  it("the config affordances stay for admin", async () => {
    const automations = [instance({})];
    vi.stubGlobal("fetch", automationsBackend(automations, []));
    render(<AutomationsScreen />);
    await waitFor(() =>
      expect(screen.getByText("Nudge stalled fleet deals")).toBeTruthy(),
    );
    expect(
      screen.queryByText(
        "Read-only view — automation settings are managed by the admin and ops roles.",
      ),
    ).toBeNull();
    await waitFor(() =>
      expect(
        screen.getAllByRole("button", { name: "Use template" }).length,
      ).toBeGreaterThan(0),
    );
    expect(screen.getByRole("button", { name: "Enable" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Delete" })).toBeTruthy();
  });
});
