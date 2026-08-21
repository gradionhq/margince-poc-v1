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
import {
  buildColumns,
  type CompanyNaming,
  DealScreen,
  DealsScreen,
} from "./deals";

// How a deal's company reads, on the three surfaces that show one.
//
// Two facts drive every case here. The wire sends `organization_id` /
// `partner_org_id` as NULL when the reader may not read that company, and
// names the field in `masked_fields` — so a null is a refusal, and a surface
// that draws nothing over it states the opposite of what the wire said. And
// the companies this screen can NAME are not a fixed first page: the picker
// reads one capped page, so a card whose company falls outside it has to
// resolve that company by id rather than lose the row.
//
// Kept out of deals.test.tsx, which is long past its size ceiling.

type Stage = components["schemas"]["Stage"];
type Deal = components["schemas"]["Deal"];
type OrgRow = { id: string; display_name: string; logo_url?: string | null };

const MASK = "Masked value";

const stages: Stage[] = [
  {
    id: "s1",
    pipeline_id: "pl",
    name: "Qualify",
    position: 1,
    semantic: "open",
    win_probability: 20,
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
  };
}

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

/**
 * The backend these screens read, with the company reads split the way the
 * product splits them: `page` is the ONE capped page the create form's picker
 * fetches, and `byId` is what a per-id read answers. A company in `byId` and
 * not in `page` is exactly the case that used to draw a card with no company.
 */
// Which companies the board asked for one at a time, in order. A request the
// screen could not have needed is as much a defect as a name it failed to show.
function byIdCalls(fetchMock: { mock: { calls: unknown[][] } }): string[] {
  return fetchMock.mock.calls.flatMap((call) => {
    const first = call[0];
    const url = String(first instanceof Request ? first.url : first);
    const match = /\/organizations\/([^/?]+)/.exec(url);
    return match?.[1] ? [match[1]] : [];
  });
}

function stubBackend(opts: {
  deals: Deal[];
  page?: OrgRow[];
  byId?: Record<string, OrgRow>;
  // Ids whose per-id read is REFUSED rather than answered or reported gone.
  refuse?: readonly string[];
  // Resolves before the organizations page answers, so a test can look at what
  // the board asked for while that read was still out.
  pageGate?: Promise<void>;
  single?: Deal;
  // Puts the screen on the overlay mirror, which forces the flat table.
  overlay?: boolean;
}) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const request = input instanceof Request ? input : null;
    const url = String(request ? request.url : input);
    const method = request ? request.method : (init?.method ?? "GET");
    if (url.includes("/pipelines")) {
      return jsonResponse({
        data: [
          { id: "pl", name: "Sales", is_default: true, position: 0, stages },
        ],
        page: { next_cursor: null },
      });
    }
    if (method === "POST" && url.includes("/reports/")) {
      return jsonResponse({
        report: "deals-by-stage",
        plan: {},
        columns: [],
        rows: [],
      });
    }
    const byId = /\/organizations\/([^/?]+)/.exec(url);
    if (byId) {
      if (opts.refuse?.includes(byId[1] ?? "")) {
        return jsonResponse(
          { code: "permission_denied", title: "permission denied" },
          403,
        );
      }
      const org = opts.byId?.[byId[1]];
      return org
        ? jsonResponse(org)
        : jsonResponse({ code: "not_found", title: "not found" }, 404);
    }
    if (url.includes("/organizations")) {
      if (opts.pageGate) {
        await opts.pageGate;
      }
      return jsonResponse({
        data: opts.page ?? [],
        page: { next_cursor: null },
      });
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
        roles: ["rep"],
        teams: [],
        ...(opts.overlay ? { system_of_record: { mode: "overlay" } } : {}),
      });
    }
    if (method === "GET" && /\/deals\/[^/?]+(\?.*)?$/.test(url)) {
      return jsonResponse(opts.single ?? opts.deals[0]);
    }
    if (url.includes("/deals")) {
      return jsonResponse({ data: opts.deals, page: { next_cursor: null } });
    }
    return jsonResponse({ data: [], page: { next_cursor: null } });
  });
}

/** The board card naming `name` — the whole card is one button. */
async function boardCard(name: string): Promise<HTMLElement> {
  const card = (await screen.findByText(name)).closest("button");
  if (!card) {
    throw new Error(`no board card around "${name}"`);
  }
  return card;
}

/** The cell of `rowText`'s row sitting under the column headed `header`. */
function cellUnder(header: string, rowText: string): HTMLElement {
  const headers = screen
    .getAllByRole("columnheader")
    .map((cell) => cell.textContent ?? "");
  const index = headers.findIndex((text) => text.includes(header));
  expect(index).toBeGreaterThanOrEqual(0);
  const row = screen.getByText(rowText).closest("tr");
  expect(row).toBeTruthy();
  const cells = within(row as HTMLElement).getAllByRole("cell");
  const cell = cells[index];
  expect(cell).toBeTruthy();
  return cell;
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.location.hash = "";
  localStorage.clear();
});

describe("a board card's company", () => {
  const named: CompanyNaming = {
    marks: new Map([["o1", { name: "Acme Corp", logoUrl: "/acme.png" }]]),
    unreadable: new Set(),
  };

  // The withheld case, which the board could not tell from an unlinked deal.
  // The card carries the refusal as a flag rather than as a name, so the mask
  // is the design system's to draw and no words for it reach the card.
  it("marks a company as withheld rather than drawing no company", () => {
    const columns = buildColumns(
      stages,
      [deal({ organization_id: null, masked_fields: ["organization_id"] })],
      new Map(),
      named,
    );
    expect(columns[0].deals[0].orgWithheld).toBe(true);
    expect(columns[0].deals[0].org).toBe("");
  });

  it("names a company it has a mark for, and carries the mark", () => {
    const columns = buildColumns(
      stages,
      [deal({ organization_id: "o1" })],
      new Map(),
      named,
    );
    expect(columns[0].deals[0].org).toBe("Acme Corp");
    expect(columns[0].deals[0].orgLogoUrl).toBe("/acme.png");
    expect(columns[0].deals[0].orgWithheld).toBeFalsy();
  });

  // The one reading a blank company row is allowed to have.
  it("draws nothing for a deal that names no company at all", () => {
    const columns = buildColumns(
      stages,
      [deal({ organization_id: null })],
      new Map(),
      named,
    );
    expect(columns[0].deals[0].org).toBe("");
    expect(columns[0].deals[0].orgWithheld).toBeFalsy();
  });

  // A masked field with a resolvable id would be the wire contradicting
  // itself; the mask wins, because it is the fact about this reader.
  it("keeps the withheld reading even where a mark is resolvable", () => {
    const columns = buildColumns(
      stages,
      [deal({ organization_id: "o1", masked_fields: ["organization_id"] })],
      new Map(),
      named,
    );
    expect(columns[0].deals[0].orgWithheld).toBe(true);
    expect(columns[0].deals[0].org).toBe("");
  });

  // The reading the board used to lose. A read that FAILED is not a deal with
  // no company: it is a company the reader could not fetch, which the table has
  // always said out loud. Collapsing it into the blank slot told the reader the
  // deal was unlinked.
  it("says a company could not be read rather than drawing none", () => {
    const unreadable: CompanyNaming = {
      marks: new Map(),
      unreadable: new Set(["o9"]),
    };
    const columns = buildColumns(
      stages,
      [deal({ organization_id: "o9" })],
      new Map(),
      unreadable,
    );
    expect(columns[0].deals[0].orgUnreadable).toBe(true);
    expect(columns[0].deals[0].org).toBe("");
    expect(columns[0].deals[0].orgWithheld).toBeFalsy();
  });

  // Withheld and unreadable say opposite things about the reader — the answer
  // exists and is not theirs, against nobody got an answer — so a wire that
  // claims both must not read as the weaker one.
  it("keeps the withheld reading over an unreadable one", () => {
    const both: CompanyNaming = {
      marks: new Map(),
      unreadable: new Set(["o1"]),
    };
    const columns = buildColumns(
      stages,
      [deal({ organization_id: "o1", masked_fields: ["organization_id"] })],
      new Map(),
      both,
    );
    expect(columns[0].deals[0].orgWithheld).toBe(true);
    expect(columns[0].deals[0].orgUnreadable).toBeFalsy();
  });
});

describe("the board past the picker's first page", () => {
  // The bug this closes: the marks came from the create form's capped page, so
  // a deal on any other company drew its card with the deal name alone.
  it("names a company the picker's page never held", async () => {
    vi.stubGlobal(
      "fetch",
      stubBackend({
        deals: [deal({ organization_id: "o-offpage" })],
        page: [{ id: "o1", display_name: "Acme Corp" }],
        byId: {
          "o-offpage": { id: "o-offpage", display_name: "Northgate Systems" },
        },
      }),
    );
    render(<DealsScreen />);

    expect(await screen.findByText("Northgate Systems")).toBeTruthy();
  });

  // The per-id read may never name a withheld company: there is no id on the
  // wire to read. The card draws the MASK for it — the same control the table's
  // company cell draws — and never words, which would be a second spelling of
  // one reading.
  it("draws the mask over a withheld company, with no id to resolve", async () => {
    vi.stubGlobal(
      "fetch",
      stubBackend({
        deals: [
          deal({ organization_id: null, masked_fields: ["organization_id"] }),
        ],
        page: [{ id: "o1", display_name: "Acme Corp" }],
      }),
    );
    render(<DealsScreen />);

    const card = await boardCard("Fleet retrofit");
    expect(within(card).getByLabelText(MASK)).toBeTruthy();
    expect(screen.queryByText("Company withheld")).toBeNull();
  });

  // A read that comes back GONE is settled, and it is not the same fact as
  // withheld: the company is archived or hidden, so the card has none to draw
  // — and masking it there would state a refusal nobody made.
  it("draws no company where the company read comes back gone", async () => {
    vi.stubGlobal(
      "fetch",
      stubBackend({
        deals: [deal({ organization_id: "o-gone" })],
        page: [{ id: "o1", display_name: "Acme Corp" }],
      }),
    );
    render(<DealsScreen />);

    const card = await boardCard("Fleet retrofit");
    expect(within(card).queryByLabelText(MASK)).toBeNull();
    expect(screen.queryByText("o-gone")).toBeNull();
  });

  // A read that was REFUSED is not a read that came back gone. The backend
  // masks the id only for a row-scope miss, so a reader holding row visibility
  // of the company without the object grant gets the id and a 403 for it —
  // and the card that dropped the error said "no company" about a deal that
  // has one.
  it("says a company could not be read when the read was refused", async () => {
    vi.stubGlobal(
      "fetch",
      stubBackend({
        deals: [deal({ organization_id: "o-refused" })],
        page: [{ id: "o1", display_name: "Acme Corp" }],
        refuse: ["o-refused"],
      }),
    );
    render(<DealsScreen />);

    const card = await boardCard("Fleet retrofit");
    expect(await within(card).findByText("Name didn't load")).toBeTruthy();
    expect(within(card).queryByLabelText(MASK)).toBeNull();
    expect(screen.queryByText("o-refused")).toBeNull();
  });

  // The two reads are issued together and settle in any order. While the page
  // read is still out every company a loaded deal names looks unresolved, and
  // asking per id then is a request each for names the page is about to carry
  // — a hundred of them on a cold board, none of which can be un-sent.
  it("asks for no company by id until the picker's page has answered", async () => {
    let openPage = () => {};
    const pageGate = new Promise<void>((resolve) => {
      openPage = resolve;
    });
    const fetchMock = stubBackend({
      deals: [deal({ organization_id: "o-offpage" })],
      page: [{ id: "o1", display_name: "Acme Corp" }],
      byId: {
        "o-offpage": { id: "o-offpage", display_name: "Northgate Systems" },
      },
      pageGate,
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<DealsScreen />);

    // The board has the deals and not the page: the card is on screen with no
    // company on it, and nothing has been asked for by id.
    await boardCard("Fleet retrofit");
    expect(byIdCalls(fetchMock)).toEqual([]);

    openPage();
    expect(await screen.findByText("Northgate Systems")).toBeTruthy();
    expect(byIdCalls(fetchMock)).toEqual(["o-offpage"]);
  });
});

describe("the deals table's company columns", () => {
  const toTable = async () => {
    const user = userEvent.setup();
    render(<DealsScreen />);
    await user.click(await screen.findByRole("button", { name: "Table" }));
    return user;
  };

  it("reads a withheld company as withheld, not as an empty cell", async () => {
    vi.stubGlobal(
      "fetch",
      stubBackend({
        deals: [
          deal({ organization_id: null, masked_fields: ["organization_id"] }),
        ],
      }),
    );
    await toTable();

    await waitFor(() =>
      expect(screen.getByText("Fleet retrofit")).toBeTruthy(),
    );
    expect(
      within(cellUnder("Company", "Fleet retrofit")).getByRole("img", {
        name: MASK,
      }),
    ).toBeTruthy();
  });

  it("names a company the reader may read", async () => {
    vi.stubGlobal(
      "fetch",
      stubBackend({
        deals: [deal({ organization_id: "o1" })],
        byId: { o1: { id: "o1", display_name: "Acme Corp" } },
      }),
    );
    await toTable();

    await waitFor(() =>
      expect(
        within(cellUnder("Company", "Fleet retrofit")).getByText("Acme Corp"),
      ).toBeTruthy(),
    );
  });

  it("leaves the cell empty only when the deal names no company", async () => {
    vi.stubGlobal("fetch", stubBackend({ deals: [deal({})] }));
    await toTable();

    await waitFor(() =>
      expect(screen.getByText("Fleet retrofit")).toBeTruthy(),
    );
    const cell = cellUnder("Company", "Fleet retrofit");
    expect(cell.textContent).toBe("");
    expect(within(cell).queryByRole("img", { name: MASK })).toBeNull();
  });

  // The partner column carries the same refusal: a commission is priced from
  // this field, so "no partner" and "not yours to see" cannot look alike.
  it("reads a withheld partner as withheld", async () => {
    vi.stubGlobal(
      "fetch",
      stubBackend({
        deals: [
          deal({ partner_org_id: null, masked_fields: ["partner_org_id"] }),
        ],
      }),
    );
    await toTable();

    await waitFor(() =>
      expect(screen.getByText("Fleet retrofit")).toBeTruthy(),
    );
    expect(
      within(cellUnder("via Partner", "Fleet retrofit")).getByRole("img", {
        name: MASK,
      }),
    ).toBeTruthy();
  });
});

describe("a deal's edit form over a withheld reference", () => {
  beforeEach(() => localStorage.setItem("margince.workspaceSlug", "acme"));

  const openEdit = async (
    single: Deal,
    page: OrgRow[],
    byId?: Record<string, OrgRow>,
  ) => {
    const user = userEvent.setup();
    vi.stubGlobal(
      "fetch",
      stubBackend({ deals: [single], single, page, byId }),
    );
    render(<DealScreen id={single.id} />);
    await user.click(await screen.findByTestId("edit-record"));
    return user;
  };

  it("offers the company field as withheld rather than as an empty picker", async () => {
    await openEdit(
      deal({ organization_id: null, masked_fields: ["organization_id"] }),
      [{ id: "o1", display_name: "Acme Corp" }],
    );

    expect(
      screen.getByRole("combobox", { name: "Company" }).textContent,
    ).toContain("Company withheld");
  });

  // The reason the field is not simply hidden, and not simply blank: a picker
  // full of companies over a company nobody was shown invites a reader to
  // re-point the deal away from the one a colleague linked.
  it("offers no company to pick while the company is withheld", async () => {
    const user = await openEdit(
      deal({ organization_id: null, masked_fields: ["organization_id"] }),
      [
        { id: "o1", display_name: "Acme Corp" },
        { id: "o2", display_name: "Northgate Systems" },
      ],
    );
    await user.click(screen.getByRole("combobox", { name: "Company" }));

    const options = within(screen.getByRole("listbox")).getAllByRole("option");
    expect(options).toHaveLength(1);
    expect(options[0].textContent).toContain("Company withheld");
    expect(screen.queryByRole("option", { name: "Acme Corp" })).toBeNull();
  });

  it("offers the partner field as withheld too", async () => {
    await openEdit(
      deal({ partner_org_id: null, masked_fields: ["partner_org_id"] }),
      [{ id: "o1", display_name: "Acme Corp" }],
    );

    expect(
      screen.getByRole("combobox", { name: "via Partner" }).textContent,
    ).toContain("Partner withheld");
  });

  // Present, and off the pickable page: the same rule the partner field
  // already held. A select whose stored value is not among its options shows
  // blank, which reads as "no company" on a deal that has one.
  it("names a company the pickable page cannot reach", async () => {
    await openEdit(
      deal({ organization_id: "o-offpage" }),
      [{ id: "o1", display_name: "Acme Corp" }],
      { "o-offpage": { id: "o-offpage", display_name: "Northgate Systems" } },
    );

    await waitFor(() =>
      expect(
        screen.getByRole("combobox", { name: "Company" }).textContent,
      ).toContain("Northgate Systems"),
    );
  });

  it("names a company the pickable page does hold, without a second read", async () => {
    await openEdit(deal({ organization_id: "o1" }), [
      { id: "o1", display_name: "Acme Corp" },
    ]);

    expect(
      screen.getByRole("combobox", { name: "Company" }).textContent,
    ).toContain("Acme Corp");
  });
});

// The mirror carries the same `masked_fields` the native list does, and its
// amount cell read a refused figure as a deal nobody had priced — the same
// defect one surface over.
describe("the overlay mirror table", () => {
  it("reads a withheld amount as withheld, not as an unpriced deal", async () => {
    vi.stubGlobal(
      "fetch",
      stubBackend({
        overlay: true,
        deals: [
          deal({
            amount_minor: null,
            currency: null,
            masked_fields: ["amount_minor"],
          }),
        ],
      }),
    );
    render(<DealsScreen />);

    expect(await screen.findByText("Fleet retrofit")).toBeTruthy();
    expect(screen.getByRole("img", { name: MASK })).toBeTruthy();
  });
});

describe("a deal's fact line", () => {
  beforeEach(() => localStorage.setItem("margince.workspaceSlug", "acme"));

  // A bare mask on a line of joined facts says only "something is hidden".
  // Each withheld fact names the field it withholds.
  it("names which of the facts is withheld", async () => {
    const single = deal({
      organization_id: null,
      masked_fields: ["organization_id"],
    });
    vi.stubGlobal("fetch", stubBackend({ deals: [single], single }));
    render(<DealScreen id="d1" />);

    const mask = await screen.findByRole("img", { name: MASK });
    expect(mask.parentElement?.textContent).toContain("Company");
  });
});
