/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { formatMoney } from "../format/format";
import { LocaleProvider } from "../i18n";
import { buildColumns, DealScreen, DealsScreen, mapDealUpdate } from "./deals";

// B-EP09.11 acceptance: board renders per-column sub-lines from the fetched
// set, mixed-currency columns refuse a sum, the board↔table control keeps
// the SAME deal set with no reload, terminal drop opens the 🟡 confirm and
// nothing posts until confirmed, and an open-stage drop posts the advance.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.location.hash = "";
  localStorage.clear();
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

type Stage = components["schemas"]["Stage"];
type Deal = components["schemas"]["Deal"];
type Offer = components["schemas"]["Offer"];

const stages: Stage[] = [
  {
    id: "s1",
    workspace_id: "w",
    pipeline_id: "pl",
    name: "Qualify",
    position: 1,
    semantic: "open",
    win_probability: 20,
  },
  {
    id: "s2",
    workspace_id: "w",
    pipeline_id: "pl",
    name: "Proposal",
    position: 2,
    semantic: "open",
    win_probability: 40,
  },
  {
    id: "s3",
    workspace_id: "w",
    pipeline_id: "pl",
    name: "Won",
    position: 3,
    semantic: "won",
    win_probability: 100,
  },
];

function deal(overrides: Partial<Deal>): Deal {
  return {
    id: "d1",
    workspace_id: "w",
    name: "Fleet retrofit",
    amount_minor: 4_800_000,
    currency: "EUR",
    pipeline_id: "pl",
    stage_id: "s1",
    status: "open",
    source: "manual",
    captured_by: "human:u1",
    created_at: "2026-06-01T00:00:00Z",
    updated_at: "2026-06-01T00:00:00Z",
    ...overrides,
  } as Deal;
}

function offer(overrides: Partial<Offer>): Offer {
  return {
    id: "o1",
    workspace_id: "w",
    deal_id: "d1",
    offer_number: "OFF-0001",
    revision: 1,
    status: "draft",
    currency: "EUR",
    net_minor: 100_000,
    tax_minor: 19_000,
    gross_minor: 119_000,
    ai_generated: false,
    line_items: [],
    source: "manual",
    captured_by: "human:u1",
    created_at: "2026-06-01T00:00:00Z",
    updated_at: "2026-06-01T00:00:00Z",
    ...overrides,
  } as Offer;
}

function stubDealBackend(
  onRecord: Deal,
  offers: Offer[],
  onCreateOffer?: (body: unknown) => void,
) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const request = input instanceof Request ? input : null;
    const url = String(request ? request.url : input);
    const method = request ? request.method : (init?.method ?? "GET");
    if (url.includes("/pipelines")) {
      return jsonResponse({ data: [], page: { next_cursor: null } });
    }
    if (url.includes("/context")) {
      return jsonResponse({ anchor: { type: "deal", id: "d1" }, sections: [] });
    }
    if (method === "POST" && url.includes("/offers")) {
      const body = request
        ? await request.json()
        : JSON.parse(String(init?.body));
      onCreateOffer?.(body);
      return jsonResponse(
        offer({ id: "new-offer", currency: body.currency }),
        201,
      );
    }
    if (url.includes("/offers")) {
      return jsonResponse({ data: offers, page: { next_cursor: null } });
    }
    if (url.includes("/history")) {
      return jsonResponse({
        data: [
          {
            id: "h1",
            actor_type: "human",
            actor_id: "u1",
            action: "update",
            occurred_at: "2026-07-13T10:00:00Z",
            summary: "Deal amount changed",
          },
        ],
        page: { next_cursor: null },
      });
    }
    if (url.includes("/stakeholders")) {
      return jsonResponse({ data: [], page: { next_cursor: null } });
    }
    if (url.includes("/approvals")) {
      return jsonResponse({ data: [], page: { next_cursor: null } });
    }
    if (url.includes("/activities")) {
      return jsonResponse({ data: [], page: { next_cursor: null } });
    }
    if (url.includes("/deals/")) {
      return jsonResponse(onRecord);
    }
    return jsonResponse({ data: [], page: { next_cursor: null } });
  });
}

describe("buildColumns", () => {
  it("computes same-currency page-local sub-lines and hides mixed-currency sums", () => {
    const columns = buildColumns(stages, [
      deal({ id: "a", stage_id: "s1", amount_minor: 100_000, currency: "EUR" }),
      deal({ id: "b", stage_id: "s1", amount_minor: 200_000, currency: "EUR" }),
      deal({ id: "c", stage_id: "s2", amount_minor: 100_000, currency: "EUR" }),
      deal({ id: "d", stage_id: "s2", amount_minor: 100_000, currency: "USD" }),
    ]);
    expect(columns[0].rawMinor).toBe(300_000);
    expect(columns[0].weightedMinor).toBe(60_000);
    expect((columns[1] as unknown as { sumHidden: boolean }).sumHidden).toBe(
      true,
    );
  });
});

function stubBackend(
  deals: Deal[],
  opts: {
    onAdvance?: (body: unknown) => void;
    single?: Deal;
    onPatch?: (body: unknown, ifMatch: string | null) => void;
    onDelete?: () => void;
    onDealsUrl?: (url: string) => void;
    pipelines?: components["schemas"]["Pipeline"][];
    agentTools?: components["schemas"]["AgentTool"][];
  } = {},
) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const request = input instanceof Request ? input : null;
    const url = String(request ? request.url : input);
    const method = request ? request.method : (init?.method ?? "GET");
    if (url.includes("/agent-tools")) {
      return jsonResponse({
        data: opts.agentTools ?? [],
        page: { next_cursor: null },
      });
    }
    if (url.includes("/context")) {
      return jsonResponse({ anchor: { type: "deal", id: "x" }, sections: [] });
    }
    if (url.includes("/pipelines")) {
      return jsonResponse({
        data: opts.pipelines ?? [
          {
            id: "pl",
            workspace_id: "w",
            name: "Sales",
            is_default: true,
            position: 0,
            stages,
          },
        ],
        page: { next_cursor: null },
      });
    }
    if (method === "POST" && url.includes("/advance")) {
      const body = request
        ? await request.json()
        : JSON.parse(String(init?.body));
      opts.onAdvance?.(body);
      return jsonResponse(deal({ stage_id: body.to_stage_id }));
    }
    if (method === "GET" && /\/deals\/[^/?]+(\?.*)?$/.test(url)) {
      return jsonResponse(opts.single ?? deals[0]);
    }
    if (method === "PATCH") {
      const body = request
        ? await request.json()
        : JSON.parse(String(init?.body));
      const ifMatch = request ? request.headers.get("If-Match") : null;
      opts.onPatch?.(body, ifMatch);
      return jsonResponse(opts.single ?? deals[0]);
    }
    if (method === "DELETE") {
      opts.onDelete?.();
      return jsonResponse(opts.single ?? deals[0]);
    }
    if (url.includes("/me")) {
      return jsonResponse({
        user: {
          id: "u-me",
          email: "me@acme.test",
          display_name: "Me",
          workspace_id: "w",
          timezone: "UTC",
          status: "active",
          is_agent: false,
        },
        roles: ["admin"],
        teams: [],
      });
    }
    if (url.includes("/organizations")) {
      return jsonResponse({
        data: [{ id: "o1", display_name: "Acme" }],
        page: { next_cursor: null },
      });
    }
    if (url.includes("/deals")) {
      opts.onDealsUrl?.(url);
      return jsonResponse({ data: deals, page: { next_cursor: null } });
    }
    return jsonResponse({ data: [], page: { next_cursor: null } });
  });
}

describe("mapDealUpdate", () => {
  it("rebuilds amount_minor from major units and nulls blanks", () => {
    const body = mapDealUpdate({
      name: "Fleet retrofit",
      amount: "2120",
      currency: "EUR",
      organization_id: "",
      owner_id: "u-me",
      partner_org_id: "",
      forecast_category: "commit",
      expected_close_date: "2026-09-01",
      wait_until: "",
    });
    expect(body.name).toBe("Fleet retrofit");
    expect(body.amount_minor).toBe(212_000);
    expect(body.currency).toBe("EUR");
    expect(body.organization_id).toBeNull();
    expect(body.owner_id).toBe("u-me");
    expect(body.partner_org_id).toBeNull();
    expect(body.forecast_category).toBe("commit");
    expect(body.expected_close_date).toBe("2026-09-01");
    expect(body.wait_until).toBeNull();
  });
});

describe("DealsScreen", () => {
  it("board↔table swaps views over the same fetched set without a reload", async () => {
    const fetchMock = stubBackend([deal({})]);
    vi.stubGlobal("fetch", fetchMock);
    render(<DealsScreen />);
    await waitFor(() =>
      expect(screen.getByText("Fleet retrofit")).toBeTruthy(),
    );
    const dealFetches = () =>
      fetchMock.mock.calls.filter((call) =>
        String(
          call[0] && (call[0] as Request).url
            ? (call[0] as Request).url
            : call[0],
        ).includes("/deals"),
      ).length;
    const before = dealFetches();
    await userEvent.click(screen.getByRole("button", { name: "Table" }));
    expect(screen.getByText("Fleet retrofit")).toBeTruthy(); // same set, table view
    expect(dealFetches()).toBe(before); // no reload
  });

  it("a terminal-stage advance opens the 🟡 confirm and posts only after confirming", async () => {
    const advances: unknown[] = [];
    vi.stubGlobal(
      "fetch",
      stubBackend([deal({})], { onAdvance: (body) => advances.push(body) }),
    );
    render(<DealsScreen />);
    await waitFor(() =>
      expect(screen.getByText("Fleet retrofit")).toBeTruthy(),
    );

    // simulate the drop on the Won column via the drop handler path
    const wonColumn = document.querySelector(
      '[data-stage="s3"]',
    ) as HTMLElement;
    const dataTransfer = { getData: () => "d1", setData: () => {} };
    const dropEvent = new Event("drop", { bubbles: true }) as unknown as {
      dataTransfer: typeof dataTransfer;
    };
    Object.assign(dropEvent, { dataTransfer });
    wonColumn.dispatchEvent(dropEvent as unknown as Event);

    await waitFor(() => expect(screen.getByText("Move to Won?")).toBeTruthy());
    expect(advances).toHaveLength(0); // nothing posted yet — confirm-first
    await userEvent.click(screen.getByRole("button", { name: "Confirm" }));
    await waitFor(() => expect(advances).toHaveLength(1));
    expect(advances[0]).toMatchObject({ to_stage_id: "s3", status: "won" });
  });

  it("the advance-confirm dot reads the live catalog tier, not a hardcode", async () => {
    vi.stubGlobal(
      "fetch",
      stubBackend([deal({})], {
        agentTools: [
          {
            name: "progress_deal",
            required_scope: "write",
            tier: "green",
            egress: false,
          },
        ],
      }),
    );
    render(<DealsScreen />);
    await waitFor(() =>
      expect(screen.getByText("Fleet retrofit")).toBeTruthy(),
    );

    const wonColumn = document.querySelector(
      '[data-stage="s3"]',
    ) as HTMLElement;
    const dataTransfer = { getData: () => "d1", setData: () => {} };
    const dropEvent = new Event("drop", { bubbles: true }) as unknown as {
      dataTransfer: typeof dataTransfer;
    };
    Object.assign(dropEvent, { dataTransfer });
    wonColumn.dispatchEvent(dropEvent as unknown as Event);

    await waitFor(() => expect(screen.getByText("Move to Won?")).toBeTruthy());
    // progress_deal is catalogued "green" (auto-execute) — a hardcoded
    // "confirm" dot would render "confirm-first" here instead.
    await waitFor(() =>
      expect(screen.getByLabelText("auto-execute")).toBeTruthy(),
    );
  });

  it("an open-stage drop advances without a confirm", async () => {
    const advances: unknown[] = [];
    vi.stubGlobal(
      "fetch",
      stubBackend([deal({})], { onAdvance: (body) => advances.push(body) }),
    );
    render(<DealsScreen />);
    await waitFor(() =>
      expect(screen.getByText("Fleet retrofit")).toBeTruthy(),
    );

    const proposalColumn = document.querySelector(
      '[data-stage="s2"]',
    ) as HTMLElement;
    const dropEvent = new Event("drop", { bubbles: true });
    Object.assign(dropEvent, {
      dataTransfer: { getData: () => "d1", setData: () => {} },
    });
    proposalColumn.dispatchEvent(dropEvent);

    await waitFor(() => expect(advances).toHaveLength(1));
    expect(advances[0]).toMatchObject({ to_stage_id: "s2" });
    expect((advances[0] as Record<string, unknown>).status).toBeUndefined();
    await waitFor(() =>
      expect(screen.getByText("Moved to Proposal")).toBeTruthy(),
    );
  });

  it("overlay mode paginates the flat mirror table through the keyset cursor", async () => {
    const dealsCalls: string[] = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input instanceof Request ? input.url : input);
      if (url.includes("/me")) {
        return jsonResponse({
          user: {
            id: "u-me",
            email: "me@acme.test",
            display_name: "Me",
            workspace_id: "w",
            timezone: "UTC",
            status: "active",
            is_agent: false,
          },
          roles: ["admin"],
          teams: [],
          system_of_record: { mode: "overlay" },
        });
      }
      if (url.includes("/deals")) {
        dealsCalls.push(url);
        if (new URL(url, "http://t").searchParams.get("cursor")) {
          return jsonResponse({
            data: [deal({ id: "d2", name: "Second page deal" })],
            page: { next_cursor: null, has_more: false },
          });
        }
        return jsonResponse({
          data: [deal({ id: "d1", name: "First page deal" })],
          page: { next_cursor: "cursor-2", has_more: true },
        });
      }
      // pipelines / agent-tools / organizations / context — all empty here.
      return jsonResponse({ data: [], page: { next_cursor: null } });
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<DealsScreen />);

    // Page one renders in the forced flat table, with the Load-more affordance.
    expect(await screen.findByText("First page deal")).toBeTruthy();
    const loadMore = await screen.findByRole("button", { name: /load more/i });

    // Loading the next page appends it and carries the cursor from page one.
    await userEvent.click(loadMore);
    expect(await screen.findByText("Second page deal")).toBeTruthy();
    expect(screen.getByText("First page deal")).toBeTruthy();
    expect(dealsCalls.some((u) => u.includes("cursor=cursor-2"))).toBe(true);
  });

  it("overlay mode keeps the loaded rows when a Load-more page fails", async () => {
    const dealsCalls: string[] = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input instanceof Request ? input.url : input);
      if (url.includes("/me")) {
        return jsonResponse({
          user: {
            id: "u-me",
            email: "me@acme.test",
            display_name: "Me",
            workspace_id: "w",
            timezone: "UTC",
            status: "active",
            is_agent: false,
          },
          roles: ["admin"],
          teams: [],
          system_of_record: { mode: "overlay" },
        });
      }
      if (url.includes("/deals")) {
        dealsCalls.push(url);
        if (new URL(url, "http://t").searchParams.get("cursor")) {
          return jsonResponse({ title: "boom" }, 500); // the next page fails
        }
        return jsonResponse({
          data: [deal({ id: "d1", name: "First page deal" })],
          page: { next_cursor: "cursor-2", has_more: true },
        });
      }
      return jsonResponse({ data: [], page: { next_cursor: null } });
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<DealsScreen />);

    expect(await screen.findByText("First page deal")).toBeTruthy();
    await userEvent.click(
      await screen.findByRole("button", { name: /load more/i }),
    );
    // The next page errored, but the already-loaded page-one rows must
    // survive — a transient later-page failure never discards usable results.
    await waitFor(() =>
      expect(dealsCalls.some((u) => u.includes("cursor=cursor-2"))).toBe(true),
    );
    expect(screen.getByText("First page deal")).toBeTruthy();
  });
});

describe("DealsScreen filters", () => {
  it("switching pipeline scopes the deals fetch to that pipeline_id", async () => {
    const urls: string[] = [];
    vi.stubGlobal(
      "fetch",
      stubBackend([deal({})], {
        onDealsUrl: (u) => urls.push(u),
        pipelines: [
          {
            id: "pl",
            workspace_id: "w",
            name: "Sales",
            is_default: true,
            position: 0,
            stages,
          },
          {
            id: "pl2",
            workspace_id: "w",
            name: "Renewals",
            is_default: false,
            position: 1,
            stages,
          },
        ],
      }),
    );
    render(<DealsScreen />);
    await screen.findByText("Fleet retrofit");
    await userEvent.selectOptions(screen.getByLabelText("Pipeline"), "pl2");
    await waitFor(() =>
      expect(urls.some((u) => u.includes("pipeline_id=pl2"))).toBe(true),
    );
  });

  it("the stalled filter adds stalled=true to the deals query", async () => {
    const urls: string[] = [];
    vi.stubGlobal(
      "fetch",
      stubBackend([deal({})], { onDealsUrl: (u) => urls.push(u) }),
    );
    render(<DealsScreen />);
    await screen.findByText("Fleet retrofit");
    await userEvent.selectOptions(
      screen.getByLabelText("Stalled only"),
      "true",
    );
    await waitFor(() =>
      expect(urls.some((u) => u.includes("stalled=true"))).toBe(true),
    );
  });
});

describe("DealScreen — edit, archive, FX line (A3)", () => {
  beforeEach(() => localStorage.setItem("margince.workspaceSlug", "acme"));

  it("edit prefills and PATCHes with If-Match", async () => {
    const patches: { body: unknown; ifMatch: string | null }[] = [];
    const d = deal({ id: "x", version: 4, owner_id: null });
    vi.stubGlobal(
      "fetch",
      stubBackend([d], {
        single: d,
        onPatch: (b, h) => patches.push({ body: b, ifMatch: h }),
      }),
    );
    render(<DealScreen id="x" />);
    await userEvent.click(await screen.findByTestId("edit-record"));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(patches.length).toBe(1));
    expect(patches[0].ifMatch).toBe("4");
  });

  it("shows the FX base line only when fx_rate_to_base is set", async () => {
    const d = deal({
      id: "x",
      amount_minor: 100_000,
      currency: "USD",
      fx_rate_to_base: "0.92",
      fx_rate_date: "2026-07-01",
    });
    vi.stubGlobal("fetch", stubBackend([d], { single: d }));
    render(<DealScreen id="x" />);
    await waitFor(() => expect(screen.getByText(/rate 0.92/)).toBeTruthy());
  });

  it("archive confirms then DELETEs", async () => {
    let deleted = false;
    const d = deal({ id: "x", version: 1 });
    vi.stubGlobal(
      "fetch",
      stubBackend([d], {
        single: d,
        onDelete: () => {
          deleted = true;
        },
      }),
    );
    render(<DealScreen id="x" />);
    await userEvent.click(await screen.findByTestId("archive-record"));
    await userEvent.click(screen.getByTestId("archive-confirm"));
    await waitFor(() => expect(deleted).toBe(true));
  });
});

describe("DealScreen reopen", () => {
  beforeEach(() => localStorage.setItem("margince.workspaceSlug", "acme"));

  it("reopen is shown only for won/lost and advances to an open stage with status open", async () => {
    const advances: unknown[] = [];
    const d = deal({ id: "x", status: "won", stage_id: "s3" });
    vi.stubGlobal(
      "fetch",
      stubBackend([d], { single: d, onAdvance: (b) => advances.push(b) }),
    );
    render(<DealScreen id="x" />);
    await userEvent.click(await screen.findByTestId("reopen-open"));
    await userEvent.click(screen.getByTestId("reopen-stage-s1"));
    await userEvent.click(screen.getByTestId("reopen-confirm"));
    await waitFor(() => expect(advances.length).toBe(1));
    expect(advances[0]).toMatchObject({ to_stage_id: "s1", status: "open" });
  });

  it("reopen is not offered for an open deal", async () => {
    const d = deal({ id: "y", status: "open" });
    vi.stubGlobal("fetch", stubBackend([d], { single: d }));
    render(<DealScreen id="y" />);
    await screen.findByTestId("edit-record"); // 360 rendered
    expect(screen.queryByTestId("reopen-open")).toBeNull();
  });
});

describe("DealScreen offers panel", () => {
  it("lists a deal's offers with status badge and formatted money", async () => {
    vi.stubGlobal(
      "fetch",
      stubDealBackend(deal({}), [
        offer({
          id: "o1",
          offer_number: "OFF-0001",
          revision: 1,
          status: "sent",
          gross_minor: 119_000,
          currency: "EUR",
        }),
      ]),
    );
    render(<DealScreen id="d1" />);
    await waitFor(() => expect(screen.getByText("OFF-0001")).toBeTruthy());
    expect(screen.getByText("sent")).toBeTruthy();
    expect(screen.getByText(formatMoney(119_000, "EUR", "en"))).toBeTruthy();
  });

  it("creating a new offer posts a draft and navigates to it", async () => {
    const creates: unknown[] = [];
    vi.stubGlobal(
      "fetch",
      stubDealBackend(deal({ currency: "EUR" }), [], (body) =>
        creates.push(body),
      ),
    );
    render(<DealScreen id="d1" />);
    await waitFor(() =>
      expect(screen.getByText("Fleet retrofit")).toBeTruthy(),
    );
    await userEvent.click(screen.getByRole("button", { name: "New offer" }));
    await waitFor(() => expect(creates).toHaveLength(1));
    expect(creates[0]).toMatchObject({ currency: "EUR", source: "manual" });
    await waitFor(() =>
      expect(window.location.hash).toBe("#/offers/new-offer"),
    );
  });
});

describe("DealScreen — History tab", () => {
  it("shows a History tab that lists record changes", async () => {
    vi.stubGlobal("fetch", stubDealBackend(deal({}), []));
    render(<DealScreen id="d1" />);

    await waitFor(() =>
      expect(screen.getByRole("button", { name: /history/i })).toBeTruthy(),
    );
    await userEvent.click(screen.getByRole("button", { name: /history/i }));

    await waitFor(() =>
      expect(screen.getByText("Deal amount changed")).toBeTruthy(),
    );
  });
});
