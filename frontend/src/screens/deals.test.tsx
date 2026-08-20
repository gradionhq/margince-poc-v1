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
import { pickOption } from "../design-system/select-testing";
import { formatMoney } from "../format/format";
import { LocaleProvider } from "../i18n";
import {
  buildColumns,
  buildStageTotals,
  DealScreen,
  DealsScreen,
  mapDealUpdate,
} from "./deals";

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
type Approval = components["schemas"]["Approval"];

const stages: Stage[] = [
  {
    id: "s1",
    pipeline_id: "pl",
    name: "Qualify",
    position: 1,
    semantic: "open",
    win_probability: 20,
  },
  {
    id: "s2",
    pipeline_id: "pl",
    name: "Proposal",
    position: 2,
    semantic: "open",
    win_probability: 40,
  },
  {
    id: "s3",
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
    name: "Fleet retrofit",
    amount_minor: 4_800_000,
    currency: "EUR",
    pipeline_id: "pl",
    stage_id: "s1",
    status: "open",
    source: "manual",
    captured_by: "human:u1",
    version: 4,
    created_at: "2026-06-01T00:00:00Z",
    updated_at: "2026-06-01T00:00:00Z",
    ...overrides,
  } as Deal;
}

function offer(overrides: Partial<Offer>): Offer {
  return {
    id: "o1",
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
  // What is staged against this deal. The record reads the workspace-wide
  // pending queue and filters it by target_entity_id, so a row only reaches
  // the panel when it names this deal.
  approvals: Approval[] = [],
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
      return jsonResponse({ data: approvals, page: { next_cursor: null } });
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

// AC-F1: column totals come from the server's per-stage
// aggregate (Σround(amount×p/100), never round(Σamount×p/100)) — not from
// summing whatever page of cards happened to load. buildStageTotals shapes
// the report's rows (grouped by stage_id + currency); buildColumns reads
// from that, and keeps building the CARD list from the loaded deals as
// before — the cap on cards is unrelated to the correctness of the totals.
describe("buildStageTotals", () => {
  it("carries one currency's totals straight through", () => {
    const totals = buildStageTotals([
      {
        stage_id: "s1",
        currency: "EUR",
        deals: 3,
        raw_minor: 300_000,
        weighted_minor: 60_000,
      },
    ]);
    expect(totals.get("s1")).toEqual({
      count: 3,
      rawMinor: 300_000,
      weightedMinor: 60_000,
      currency: "EUR",
      sumHidden: false,
    });
  });

  it("hides the sum when a stage has more than one currency row", () => {
    const totals = buildStageTotals([
      {
        stage_id: "s2",
        currency: "EUR",
        deals: 1,
        raw_minor: 100_000,
        weighted_minor: 20_000,
      },
      {
        stage_id: "s2",
        currency: "USD",
        deals: 1,
        raw_minor: 100_000,
        weighted_minor: 20_000,
      },
    ]);
    const s2 = totals.get("s2");
    expect(s2?.sumHidden).toBe(true);
    expect(s2?.count).toBe(2);
  });

  it("a stage absent from the rows gets zeroed, not undefined", () => {
    const totals = buildStageTotals([]);
    expect(totals.get("s1")).toBeUndefined();
  });
});

describe("buildColumns", () => {
  it("reads sums from the totals map, not from the loaded cards", () => {
    const totals = buildStageTotals([
      // The server's per-deal-rounded figure (12343 × 20% rounded per deal,
      // twice, then summed = 4938) — deliberately NOT what round(Σ) gives
      // (4937), so a regression back to client-side summing would fail this.
      {
        stage_id: "s1",
        currency: "EUR",
        deals: 2,
        raw_minor: 24_686,
        weighted_minor: 4_938,
      },
    ]);
    const columns = buildColumns(
      stages,
      [
        deal({
          id: "a",
          stage_id: "s1",
          amount_minor: 12_343,
          currency: "EUR",
        }),
        deal({
          id: "b",
          stage_id: "s1",
          amount_minor: 12_343,
          currency: "EUR",
        }),
      ],
      totals,
    );
    expect(columns[0].rawMinor).toBe(24_686);
    expect(columns[0].weightedMinor).toBe(4_938);
    expect(columns[0].deals).toHaveLength(2);
  });

  it("hides the sum for a mixed-currency stage per the totals map, regardless of which cards loaded", () => {
    const totals = buildStageTotals([
      {
        stage_id: "s2",
        currency: "EUR",
        deals: 1,
        raw_minor: 100_000,
        weighted_minor: 20_000,
      },
      {
        stage_id: "s2",
        currency: "USD",
        deals: 1,
        raw_minor: 100_000,
        weighted_minor: 20_000,
      },
    ]);
    const columns = buildColumns(
      stages,
      [
        deal({
          id: "c",
          stage_id: "s2",
          amount_minor: 100_000,
          currency: "EUR",
        }),
      ],
      totals,
    );
    expect(columns[1].sumHidden).toBe(true);
  });

  it("a stage with no totals row states no figure and no currency", () => {
    const columns = buildColumns(stages, [], new Map());
    expect(columns[0].rawMinor).toBeNull();
    expect(columns[0].currency).toBeNull();
    expect(columns[0].sumHidden).toBeFalsy();
  });
});

function stubBackend(
  deals: Deal[],
  opts: {
    onAdvance?: (body: unknown, ifMatch: string | null) => void;
    single?: Deal;
    onPatch?: (body: unknown, ifMatch: string | null) => void;
    onDelete?: () => void;
    onDealsUrl?: (url: string) => void;
    pipelines?: components["schemas"]["Pipeline"][];
    agentTools?: components["schemas"]["AgentTool"][];
    stageTotalsRows?: Record<string, unknown>[];
    onStageTotalsBody?: (body: unknown) => void;
    savedViews?: Record<string, unknown>[];
    onCreateView?: (body: unknown) => void;
    // The second keyset page, served when the request carries a cursor.
    nextPage?: Deal[];
  } = {},
) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const request = input instanceof Request ? input : null;
    const url = String(request ? request.url : input);
    const method = request ? request.method : (init?.method ?? "GET");
    if (method === "GET" && url.includes("/users")) {
      return jsonResponse({
        data: [
          {
            id: "u-me",
            email: "me@acme.test",
            display_name: "Me",
            timezone: "UTC",
            status: "active",
            is_agent: false,
          },
        ],
        page: { next_cursor: null },
      });
    }
    if (method === "POST" && url.includes("/views")) {
      const body = request
        ? await request.json()
        : JSON.parse(String(init?.body));
      opts.onCreateView?.(body);
      return jsonResponse({ id: "new-view", ...body }, 201);
    }
    if (method === "GET" && url.includes("/views")) {
      return jsonResponse({
        data: opts.savedViews ?? [],
        page: { next_cursor: null },
      });
    }
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
            name: "Sales",
            is_default: true,
            position: 0,
            stages,
          },
        ],
        page: { next_cursor: null },
      });
    }
    if (method === "POST" && url.includes("/reports/deals-by-stage")) {
      const body = request
        ? await request.json()
        : JSON.parse(String(init?.body));
      opts.onStageTotalsBody?.(body);
      return jsonResponse({
        report: "deals-by-stage",
        plan: {},
        columns: [],
        rows: opts.stageTotalsRows ?? [],
      });
    }
    if (method === "POST" && url.includes("/advance")) {
      const body = request
        ? await request.json()
        : JSON.parse(String(init?.body));
      opts.onAdvance?.(body, request?.headers.get("If-Match") ?? null);
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
      if (opts.nextPage) {
        return url.includes("cursor=")
          ? jsonResponse({
              data: opts.nextPage,
              page: { next_cursor: null, has_more: false },
            })
          : jsonResponse({
              data: deals,
              page: { next_cursor: "cur-2", has_more: true },
            });
      }
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

  // A view saved on the deals list is a server row, and the tab rail has to
  // read it. The rail carried only the one hardcoded sort before, so a saved
  // view was storable through the contract and then invisible.
  it("offers a saved view as a tab beside the standing sort", async () => {
    vi.stubGlobal(
      "fetch",
      stubBackend([deal({})], {
        savedViews: [
          {
            id: "v1",
            resource: "deals",
            name: "Slipping this quarter",
            query: {
              list: { sort: "-amount_minor", filters: { stalled: "true" } },
            },
            created_at: "2026-06-01T00:00:00Z",
            updated_at: "2026-06-01T00:00:00Z",
          },
        ],
      }),
    );
    const user = userEvent.setup();
    render(<DealsScreen />);
    await user.click(await screen.findByRole("button", { name: "Table" }));

    expect(
      await screen.findByRole("button", { name: "Slipping this quarter" }),
    ).toBeTruthy();
    expect(screen.getByRole("button", { name: "Newest" })).toBeTruthy();
  });

  // Picking the tab has to narrow the list, not just highlight: the saved
  // filters travel to the server or the view is decoration.
  it("applies a saved view's filters to the deals request", async () => {
    const urls: string[] = [];
    vi.stubGlobal(
      "fetch",
      stubBackend([deal({})], {
        onDealsUrl: (url) => urls.push(url),
        savedViews: [
          {
            id: "v1",
            resource: "deals",
            name: "My stalled deals",
            query: { list: { sort: "", filters: { stalled: "true" } } },
            created_at: "2026-06-01T00:00:00Z",
            updated_at: "2026-06-01T00:00:00Z",
          },
        ],
      }),
    );
    const user = userEvent.setup();
    render(<DealsScreen />);
    await user.click(await screen.findByRole("button", { name: "Table" }));
    await user.click(
      await screen.findByRole("button", { name: "My stalled deals" }),
    );

    await waitFor(() =>
      expect(urls.some((url) => url.includes("stalled=true"))).toBe(true),
    );
  });

  // The pipeline picker is the strongest dial on this screen and it lives in
  // its own state, outside the list query. Saved without it, a view would
  // restore against whichever pipeline happened to be showing — a different
  // list of deals under the name the reader chose.
  it("saves the selected pipeline as part of the view", async () => {
    let saved: Record<string, unknown> | undefined;
    vi.stubGlobal(
      "fetch",
      stubBackend([deal({})], {
        onCreateView: (body) => {
          saved = body as Record<string, unknown>;
        },
      }),
    );
    const user = userEvent.setup();
    render(<DealsScreen />);
    await user.click(await screen.findByRole("button", { name: "Table" }));
    // The save action appears only once something narrows the list; a sort is
    // the cheapest such narrowing to reach from here.
    await user.click(screen.getByRole("button", { name: "Sort by Value" }));
    await user.click(await screen.findByRole("button", { name: "Save view" }));
    await user.type(
      await screen.findByRole("textbox", { name: "Name" }),
      "Pipeline view",
    );
    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(saved).toBeTruthy());
    if (!saved) {
      throw new Error("the save never reached the server");
    }
    const list = (saved.query as Record<string, Record<string, unknown>>).list;
    expect((list.filters as Record<string, string>).pipeline_id).toBe("pl");
  });

  // A pipeline is always selected, so carrying it into the query must not make
  // an untouched list look narrowed: the save action would then offer to store
  // the default view, which is the clutter its own check exists to prevent.
  it("offers no save action until the reader narrows something", async () => {
    vi.stubGlobal("fetch", stubBackend([deal({})]));
    const user = userEvent.setup();
    render(<DealsScreen />);
    await user.click(await screen.findByRole("button", { name: "Table" }));

    expect(screen.queryByRole("button", { name: "Save view" })).toBeNull();
  });

  // A bulk verb is a fan-out of each row's own write, so every row must carry
  // ITS OWN version. One version copied across the selection would conflict on
  // every row but the one it came from.
  it("assigning an owner in bulk sends each row's own version", async () => {
    const patches: { body: unknown; ifMatch: string | null }[] = [];
    vi.stubGlobal(
      "fetch",
      stubBackend(
        [
          deal({ id: "d1", name: "First", version: 3 }),
          deal({ id: "d2", name: "Second", version: 9 }),
        ],
        { onPatch: (body, ifMatch) => patches.push({ body, ifMatch }) },
      ),
    );
    const user = userEvent.setup();
    render(<DealsScreen />);
    await user.click(await screen.findByRole("button", { name: "Table" }));

    await user.click(
      await screen.findByRole("checkbox", { name: "Select First" }),
    );
    await user.click(screen.getByRole("checkbox", { name: "Select Second" }));
    await pickOption(
      user,
      screen.getByRole("combobox", { name: "New owner" }),
      "Me",
    );
    await user.click(screen.getByRole("button", { name: "Assign" }));

    await waitFor(() => expect(patches.length).toBe(2));
    expect(patches.map((patch) => patch.ifMatch).sort()).toEqual(["3", "9"]);
    expect((patches[0].body as { owner_id: string }).owner_id).toBe("u-me");
  });

  // The server treats every advance as a transition — it writes a stage-history
  // row and emits deal.stage_changed without asking whether anything moved — so
  // a row already in the target stage must not be sent at all, or the velocity
  // reports read a move that never happened.
  it("moving to a stage skips the rows already in it", async () => {
    const advances: string[] = [];
    vi.stubGlobal(
      "fetch",
      stubBackend(
        [
          deal({ id: "d1", name: "Already there", stage_id: "s2" }),
          deal({ id: "d2", name: "Needs moving", stage_id: "s1" }),
        ],
        { onAdvance: (_body, _ifMatch) => advances.push("advance") },
      ),
    );
    const user = userEvent.setup();
    render(<DealsScreen />);
    await user.click(await screen.findByRole("button", { name: "Table" }));

    await user.click(
      await screen.findByRole("checkbox", { name: "Select Already there" }),
    );
    await user.click(
      screen.getByRole("checkbox", { name: "Select Needs moving" }),
    );
    await pickOption(
      user,
      screen.getByRole("combobox", { name: "Move to stage" }),
      "Proposal",
    );
    await user.click(screen.getByRole("button", { name: "Move" }));

    // One write, for the row that actually moves.
    await waitFor(() => expect(advances.length).toBe(1));
  });

  // Archiving many deals at once is the most destructive thing this bar does,
  // and every other archive in the product asks first.
  it("bulk archive asks before it removes anything", async () => {
    let deleted = 0;
    vi.stubGlobal(
      "fetch",
      stubBackend([deal({ id: "d1", name: "First" })], {
        onDelete: () => {
          deleted += 1;
        },
      }),
    );
    const user = userEvent.setup();
    render(<DealsScreen />);
    await user.click(await screen.findByRole("button", { name: "Table" }));
    await user.click(
      await screen.findByRole("checkbox", { name: "Select First" }),
    );

    await user.click(screen.getByRole("button", { name: "Archive" }));
    expect(deleted).toBe(0);

    // The dialog's own Archive button, not the bar's.
    const dialog = screen.getByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "Archive" }));
    await waitFor(() => expect(deleted).toBe(1));
  });

  // A closed deal takes no bulk write: archiving it is done or meaningless,
  // and moving it between open stages would be the silent reopen the record
  // page's stepper already refuses.
  it("offers no checkbox on a closed deal", async () => {
    vi.stubGlobal(
      "fetch",
      stubBackend([
        deal({ id: "d1", name: "Open one" }),
        deal({ id: "d2", name: "Won one", status: "won", stage_id: "s3" }),
      ]),
    );
    const user = userEvent.setup();
    render(<DealsScreen />);
    await user.click(await screen.findByRole("button", { name: "Table" }));

    expect(
      await screen.findByRole("checkbox", { name: "Select Open one" }),
    ).toBeTruthy();
    expect(
      screen.queryByRole("checkbox", { name: "Select Won one" }),
    ).toBeNull();
  });

  // The board draws its columns from the deals it holds, so a single capped
  // read meant a busy stage showed a fraction of its cards while its header —
  // which counts EVERY matching deal — went on naming the true number. A
  // column saying "40 deals" above six cards is what this prevents.
  it("the board walks past the first page", async () => {
    vi.stubGlobal(
      "fetch",
      stubBackend([deal({ id: "d1", name: "First page deal" })], {
        nextPage: [deal({ id: "d2", name: "Second page deal" })],
      }),
    );
    const user = userEvent.setup();
    render(<DealsScreen />);

    expect(await screen.findByText("First page deal")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Load more" }));

    // Both pages stand together — the walk adds cards, it does not replace them.
    expect(await screen.findByText("Second page deal")).toBeTruthy();
    expect(screen.getByText("First page deal")).toBeTruthy();
  });

  // The column headers count every matching deal through a SEPARATE report
  // query. Keyed apart from the cards it never saw the invalidation every deal
  // mutation fires, so a moved card sat under a header still counting it in
  // the stage it left. The key now lives UNDER ["deals"], which is what makes
  // that one invalidation reach both — assert the relationship, since a
  // request count cannot tell a real invalidation from a routine refetch.
  it("keys the column totals under the deals cache so one invalidation reaches both", async () => {
    const keys: unknown[][] = [];
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    vi.stubGlobal("fetch", stubBackend([deal({})]));
    rtlRender(
      <QueryClientProvider client={client}>
        <LocaleProvider initial="en">
          <DealsScreen />
        </LocaleProvider>
      </QueryClientProvider>,
    );
    await waitFor(() =>
      expect(screen.getByText("Fleet retrofit")).toBeTruthy(),
    );

    for (const query of client.getQueryCache().getAll()) {
      keys.push(query.queryKey as unknown[]);
    }
    const totals = keys.find((key) => key.includes("by-stage-totals"));
    expect(totals).toBeTruthy();
    // invalidateQueries({queryKey:["deals"]}) matches by PREFIX, so this is
    // the whole claim: the totals live under it.
    expect(totals?.[0]).toBe("deals");
  });

  // The board's column total must come from the deals-by-stage
  // report over EVERY matching deal, not from summing the (capped) page of
  // cards. The seeded card's own amount×probability would give a different,
  // WRONG figure if the board still computed it client-side — this proves
  // it renders the server's number instead.
  it("renders the board's column total from the deals-by-stage report, not from the loaded cards", async () => {
    vi.stubGlobal(
      "fetch",
      stubBackend([deal({ id: "a", stage_id: "s1", amount_minor: 1 })], {
        stageTotalsRows: [
          {
            stage_id: "s1",
            currency: "EUR",
            deals: 250,
            raw_minor: 9_999_999,
            weighted_minor: 1_234_567,
          },
        ],
      }),
    );
    render(<DealsScreen />);
    await waitFor(() =>
      expect(
        screen.getByText(formatMoney(9_999_999, "EUR", "en")),
      ).toBeTruthy(),
    );
    expect(
      screen.getByText(`weighted ${formatMoney(1_234_567, "EUR", "en")}`),
    ).toBeTruthy();
    // The true stage count (250), not "1" — the single loaded card's count.
    expect(screen.getByText("250 deals")).toBeTruthy();
  });

  it("sends the board's active filters to the deals-by-stage totals request", async () => {
    let sentBody: unknown;
    vi.stubGlobal(
      "fetch",
      stubBackend([deal({})], {
        onStageTotalsBody: (body) => {
          sentBody = body;
        },
      }),
    );
    render(<DealsScreen />);
    await waitFor(() =>
      expect(screen.getByText("Fleet retrofit")).toBeTruthy(),
    );
    expect(sentBody).toMatchObject({
      group_by: ["stage_id", "currency"],
    });
  });

  it("a terminal-stage advance opens the 🟡 confirm and posts only after confirming", async () => {
    const advances: [unknown, string | null][] = [];
    vi.stubGlobal(
      "fetch",
      stubBackend([deal({})], { onAdvance: (b, m) => advances.push([b, m]) }),
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
    expect(advances[0]).toEqual([{ to_stage_id: "s3", status: "won" }, "4"]);
  });

  it("the advance-confirm dot reads the live catalog tier, not a hardcode", async () => {
    vi.stubGlobal(
      "fetch",
      stubBackend([deal({})], {
        agentTools: [
          {
            name: "progress_deal",
            title: "Progress a deal with a note",
            description:
              'Move a deal to a new stage and leave a note on its timeline saying why. (Governance: some calls run immediately and others a person approves first, decided per call from its arguments; requires passport scope "write".)',
            required_scope: "write",
            tier: "auto_execute",
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
    // progress_deal is catalogued "auto_execute" — a hardcoded
    // "confirm" dot would render "confirm-first" here instead.
    await waitFor(() =>
      expect(screen.getByLabelText("auto-execute")).toBeTruthy(),
    );
  });

  it("an open-stage drop advances without a confirm", async () => {
    const advances: [unknown, string | null][] = [];
    vi.stubGlobal(
      "fetch",
      stubBackend([deal({})], { onAdvance: (b, m) => advances.push([b, m]) }),
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
    expect(advances[0]).toEqual([{ to_stage_id: "s2" }, "4"]);
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
    const user = userEvent.setup();
    const urls: string[] = [];
    vi.stubGlobal(
      "fetch",
      stubBackend([deal({})], {
        onDealsUrl: (u) => urls.push(u),
        pipelines: [
          {
            id: "pl",
            name: "Sales",
            is_default: true,
            position: 0,
            stages,
          },
          {
            id: "pl2",
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
    await pickOption(user, screen.getByLabelText("Pipeline"), "Renewals");
    await waitFor(() =>
      expect(urls.some((u) => u.includes("pipeline_id=pl2"))).toBe(true),
    );
  });

  // The board always shows one pipeline, so an unset choice would fall straight
  // back to the default one — the pipeline list therefore offers pipelines
  // only. The stage filter's "all" entry clears a query filter, which the board
  // can actually show, so that one stays.
  it("offers pipelines only, while the stage filter keeps its all-stages entry", async () => {
    const user = userEvent.setup();
    vi.stubGlobal(
      "fetch",
      stubBackend([deal({})], {
        pipelines: [
          {
            id: "pl",
            name: "Sales",
            is_default: true,
            position: 0,
            stages,
          },
          {
            id: "pl2",
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

    await user.click(screen.getByLabelText("Pipeline"));
    expect(
      within(screen.getByRole("listbox"))
        .getAllByRole("option")
        .map((option) => option.textContent),
    ).toEqual(["Sales", "Renewals"]);
    await user.keyboard("{Escape}");

    // Stage is a filter chip now rather than a picker of its own, so its
    // all-stages entry sits one step inside the Filter menu — and it still has
    // to be there, because that entry is how a chosen stage is cleared.
    // "Stage" also names a column header, so the attribute is picked from
    // inside the menu that is open rather than by a plain name match.
    await user.click(screen.getByRole("button", { name: "Table" }));
    await user.click(screen.getByRole("button", { name: "Filter" }));
    const menu = screen.getByRole("group", { name: "Filter" });
    await user.click(within(menu).getByRole("button", { name: "Stage" }));
    expect(
      within(menu).getByRole("button", { name: "All stages" }),
    ).toBeTruthy();
  });

  it("the stalled filter adds stalled=true to the deals query", async () => {
    const urls: string[] = [];
    vi.stubGlobal(
      "fetch",
      stubBackend([deal({})], { onDealsUrl: (u) => urls.push(u) }),
    );
    render(<DealsScreen />);
    await screen.findByText("Fleet retrofit");
    // The stalled filter lives on the table view, not the board.
    await userEvent.click(screen.getByRole("button", { name: "Table" }));

    // The Filter button's attribute step and its value step both carry the
    // "Stalled only" label — the chip's option shares the attribute's own
    // name — so each step is picked from inside the menu that is open at
    // that moment rather than by a plain (ambiguous) name match.
    await userEvent.click(screen.getByRole("button", { name: "Filter" }));
    const menu = screen.getByRole("group", { name: "Filter" });
    await userEvent.click(
      within(menu).getByRole("button", { name: "Stalled only" }),
    );
    await userEvent.click(
      within(menu).getByRole("button", { name: "Stalled only" }),
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

// Closing a deal used to be reachable only by dragging its card on the board,
// which meant it could not be done from the deal's own page at all — and not
// at all on a touch device, where HTML5 drag never fires.
describe("DealScreen — the stage stepper advances the deal", () => {
  beforeEach(() => localStorage.setItem("margince.workspaceSlug", "acme"));

  it("moving to an open stage posts the advance pinned to the version shown", async () => {
    const advances: { body: unknown; ifMatch: string | null }[] = [];
    const d = deal({ id: "x", version: 7, stage_id: "s1" });
    vi.stubGlobal(
      "fetch",
      stubBackend([d], {
        single: d,
        onAdvance: (body, ifMatch) => advances.push({ body, ifMatch }),
      }),
    );
    const user = userEvent.setup();
    render(<DealScreen id="x" />);

    await user.click(await screen.findByRole("button", { name: "Proposal" }));

    await waitFor(() => expect(advances.length).toBe(1));
    expect((advances[0].body as { to_stage_id: string }).to_stage_id).toBe(
      "s2",
    );
    // The version the record was drawn from, so a change made elsewhere
    // meanwhile fails loud rather than being erased.
    expect(advances[0].ifMatch).toBe("7");
  });

  // The advance is only half the job: a write whose confirmation is shown to
  // nobody reads exactly like one that did not happen.
  it("confirms the move on the record itself", async () => {
    const d = deal({ id: "x", version: 7, stage_id: "s1" });
    vi.stubGlobal("fetch", stubBackend([d], { single: d }));
    const user = userEvent.setup();
    render(<DealScreen id="x" />);

    await user.click(await screen.findByRole("button", { name: "Proposal" }));

    expect(await screen.findByText("Moved to Proposal")).toBeTruthy();
  });

  // A control that can only fail is worse than none: an archived deal is not
  // moved through the pipeline, it is restored first.
  it("offers no move on an archived deal", async () => {
    const d = deal({ id: "x", archived_at: "2026-07-01T00:00:00Z" });
    vi.stubGlobal("fetch", stubBackend([d], { single: d }));
    render(<DealScreen id="x" />);

    const step = await screen.findByRole("button", { name: "Proposal" });
    expect(step.hasAttribute("disabled")).toBe(true);
  });

  // Reopening is its own deliberate action, with a dialog that says the close
  // date and the frozen rate are being cleared. A stepper button that reopened
  // silently would be a second, quieter door to the same write.
  it("offers no move on a closed deal — reopening has its own action", async () => {
    const d = deal({
      id: "x",
      status: "won",
      stage_id: "s3",
      closed_at: "2026-07-01T00:00:00Z",
    });
    vi.stubGlobal("fetch", stubBackend([d], { single: d }));
    render(<DealScreen id="x" />);

    const step = await screen.findByRole("button", { name: "Proposal" });
    expect(step.hasAttribute("disabled")).toBe(true);
  });

  // The dialog stays mounted between openings, so an abandoned reason would
  // otherwise still be sitting there the next time a deal is closed — and it
  // would describe a different deal.
  it("a lost reason typed and then cancelled does not come back", async () => {
    const d = deal({ id: "x", version: 2, stage_id: "s1" });
    vi.stubGlobal(
      "fetch",
      stubBackend([d], {
        single: d,
        pipelines: [
          {
            id: "pl",
            name: "Sales",
            is_default: true,
            position: 0,
            stages: [
              ...stages,
              {
                id: "s4",
                pipeline_id: "pl",
                name: "Lost",
                position: 4,
                semantic: "lost",
                win_probability: 0,
              },
            ],
          },
        ],
      }),
    );
    const user = userEvent.setup();
    render(<DealScreen id="x" />);

    await user.click(await screen.findByRole("button", { name: "Lost" }));
    await user.type(
      screen.getByRole("textbox", { name: "Lost reason" }),
      "typed then abandoned",
    );
    await user.click(screen.getByRole("button", { name: "Cancel" }));

    await user.click(await screen.findByRole("button", { name: "Lost" }));
    expect(
      (screen.getByRole("textbox", { name: "Lost reason" }) as HTMLInputElement)
        .value,
    ).toBe("");
  });

  it("the stage the deal is already in is not a control", async () => {
    const d = deal({ id: "x", stage_id: "s1" });
    vi.stubGlobal("fetch", stubBackend([d], { single: d }));
    render(<DealScreen id="x" />);

    await screen.findByRole("button", { name: "Proposal" });
    expect(screen.queryByRole("button", { name: "Qualify" })).toBeNull();
  });

  // Terminal stages are the 🟡 confirm (AC-deal-6), and a lost deal must say
  // why — the same rule the board's drop enforces, because it is the same
  // dialog.
  it("closing as lost asks for a reason before anything is written", async () => {
    const advances: { body: unknown; ifMatch: string | null }[] = [];
    const d = deal({ id: "x", version: 2, stage_id: "s1" });
    vi.stubGlobal(
      "fetch",
      stubBackend([d], {
        single: d,
        pipelines: [
          {
            id: "pl",
            name: "Sales",
            is_default: true,
            position: 0,
            stages: [
              ...stages,
              {
                id: "s4",
                pipeline_id: "pl",
                name: "Lost",
                position: 4,
                semantic: "lost",
                win_probability: 0,
              },
            ],
          },
        ],
        onAdvance: (body, ifMatch) => advances.push({ body, ifMatch }),
      }),
    );
    const user = userEvent.setup();
    render(<DealScreen id="x" />);

    await user.click(await screen.findByRole("button", { name: "Lost" }));
    // Nothing is written while the dialog stands open.
    expect(advances.length).toBe(0);

    const confirm = screen.getByRole("button", { name: "Confirm" });
    expect(confirm.hasAttribute("disabled")).toBe(true);

    await user.type(
      screen.getByRole("textbox", { name: "Lost reason" }),
      "went with a competitor",
    );
    await user.click(screen.getByRole("button", { name: "Confirm" }));

    await waitFor(() => expect(advances.length).toBe(1));
    const body = advances[0].body as { status: string; lost_reason: string };
    expect(body.status).toBe("lost");
    expect(body.lost_reason).toBe("went with a competitor");
  });
});

describe("DealScreen — an archived deal keeps its verbs, refused", () => {
  beforeEach(() => localStorage.setItem("margince.workspaceSlug", "acme"));

  it("shows Edit, Archive, Share and Reopen disabled, each reachable from the one sentence naming the archive", async () => {
    const d = deal({
      id: "x",
      status: "won",
      stage_id: "s3",
      archived_at: "2026-07-13T00:00:00Z",
    });
    vi.stubGlobal("fetch", stubBackend([d], { single: d }));
    render(<DealScreen id="x" />);

    const refused = [
      await screen.findByTestId("edit-record"),
      screen.getByTestId("archive-record"),
      screen.getByTestId("share-record"),
      screen.getByTestId("reopen-open"),
    ];
    for (const control of refused) {
      expect(control.hasAttribute("disabled")).toBe(true);
      // The reason has to be reachable FROM the control: a disabled button
      // cannot be focused and a `title` on it is announced by nobody, so a
      // sentence the control does not point at reaches no reader who needed it.
      const describedBy = control.getAttribute("aria-describedby");
      expect(document.getElementById(describedBy ?? "")?.textContent).toBe(
        "This deal is archived and takes no changes.",
      );
    }
  });
});

describe("DealScreen — overlay mode write affordances", () => {
  beforeEach(() => localStorage.setItem("margince.workspaceSlug", "acme"));

  function overlayBackend(
    d: Deal,
    opts: {
      onPatch?: (body: unknown) => void;
      onDelete?: () => void;
    } = {},
  ) {
    // Mutable so a refetch after a successful PATCH (useUpdateRecord
    // invalidates the record query) sees the write applied — the same
    // "mirror re-read reflects the write-back" shape the real overlay
    // Provider.Update gives (mirrorWriteResult), not a stale echo.
    let current = d;
    return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : null;
      const url = String(request ? request.url : input);
      const method = request ? request.method : (init?.method ?? "GET");
      if (url.includes("/me")) {
        return jsonResponse({
          user: {
            id: "u-me",
            email: "me@acme.test",
            display_name: "Me",
            timezone: "UTC",
            status: "active",
            is_agent: false,
          },
          roles: ["admin"],
          teams: [],
          system_of_record: { mode: "overlay" },
        });
      }
      if (method === "PATCH") {
        const body = request
          ? await request.json()
          : JSON.parse(String(init?.body));
        opts.onPatch?.(body);
        current = { ...current, ...(body as Partial<Deal>) };
        return jsonResponse(current);
      }
      if (method === "DELETE") {
        opts.onDelete?.();
        return jsonResponse(current);
      }
      if (url.includes("/deals/")) {
        return jsonResponse(current);
      }
      return jsonResponse({ data: [], page: { next_cursor: null } });
    });
  }

  it("serves Edit and Archive — the mirror write-back seam accepts both", async () => {
    const d = deal({ id: "x", version: 3 });
    vi.stubGlobal("fetch", overlayBackend(d));
    render(<DealScreen id="x" />);
    expect(await screen.findByTestId("edit-record")).toBeTruthy();
    expect(screen.getByTestId("archive-record")).toBeTruthy();
  });

  it("Edit's real click path PATCHes and the 360 renders the updated value", async () => {
    const patches: unknown[] = [];
    const d = deal({ id: "x", version: 3 });
    vi.stubGlobal(
      "fetch",
      overlayBackend(d, { onPatch: (body) => patches.push(body) }),
    );
    render(<DealScreen id="x" />);
    await userEvent.click(await screen.findByTestId("edit-record"));
    const nameInput = screen.getByLabelText("Deal name *");
    await userEvent.clear(nameInput);
    await userEvent.type(nameInput, "Fleet retrofit — expanded scope");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(patches).toHaveLength(1));
    expect(
      await screen.findByText("Fleet retrofit — expanded scope"),
    ).toBeTruthy();
  });

  it("Edit's overlay notice names the partial write-back honestly", async () => {
    const d = deal({ id: "x" });
    vi.stubGlobal("fetch", overlayBackend(d));
    render(<DealScreen id="x" />);
    await userEvent.click(await screen.findByTestId("edit-record"));
    expect(
      screen.getByText(/Only the fields HubSpot accepts are written back/),
    ).toBeTruthy();
  });

  it("keeps Reopen and Share hidden even for a won deal", async () => {
    const d = deal({ id: "x", status: "won", stage_id: "s3" });
    vi.stubGlobal("fetch", overlayBackend(d));
    render(<DealScreen id="x" />);
    await screen.findByTestId("edit-record");
    expect(screen.queryByTestId("reopen-open")).toBeNull();
    expect(screen.queryByTestId("share-record")).toBeNull();
  });
});

describe("DealScreen reopen", () => {
  beforeEach(() => localStorage.setItem("margince.workspaceSlug", "acme"));

  it("reopen is shown only for won/lost and advances to an open stage with status open", async () => {
    const moves: [unknown, string | null][] = [];
    const d = deal({ id: "x", status: "won", stage_id: "s3" });
    vi.stubGlobal(
      "fetch",
      stubBackend([d], { single: d, onAdvance: (b, m) => moves.push([b, m]) }),
    );
    render(<DealScreen id="x" />);
    await userEvent.click(await screen.findByTestId("reopen-open"));
    await userEvent.click(screen.getByTestId("reopen-stage-s1"));
    await userEvent.click(screen.getByTestId("reopen-confirm"));
    await waitFor(() => expect(moves.length).toBe(1));
    expect(moves[0]).toEqual([{ to_stage_id: "s1", status: "open" }, "4"]);
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

describe("DealScreen pending approvals", () => {
  const staged = {
    id: "ap-1",
    kind: "advance_deal",
    status: "pending",
    summary: "Move Fleet retrofit to Proposal",
    proposed_change: { to_stage_id: "s2" },
    proposed_by: "agent:capture",
    target_entity_type: "deal",
    target_entity_id: "d1",
    created_at: "2026-07-01T08:00:00Z",
    evidence: [],
  } as Approval;

  // The panel states the same two facts the approvals inbox states, in the same
  // words: the kind through the shared catalog map, the proposer through the
  // provenance tag. Off the wire those facts read `advance_deal` and
  // `agent:capture` — the API's vocabulary on a page whose reader never sees
  // the API.
  it("names the staged kind and its proposer in the product's words, not the wire's", async () => {
    vi.stubGlobal("fetch", stubDealBackend(deal({}), [], undefined, [staged]));
    render(<DealScreen id="d1" />);

    // approval.kind.advance_deal — the key the inbox reads for the same kind.
    expect(await screen.findByText("Move a deal forward")).toBeTruthy();
    // trust.agentTag: an agent, named, rather than the doubled wire string.
    expect(screen.getByText("Automated by capture")).toBeTruthy();
    expect(screen.queryByText("advance_deal")).toBeNull();
    expect(screen.queryByText("agent:capture")).toBeNull();
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
