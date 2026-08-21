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
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { DealsScreen } from "./deals";

// What narrowed the pipeline has to be readable ON the surface that is
// narrowed.
//
// The board and the table read ONE query — one sort, one set of filters, one
// archived toggle — so the saved-view rail and the applied filter rows describe
// both of them. A board rendered instead of the surface took those controls off
// screen with it, and left the reader looking at fewer deals than the pipeline
// holds with nothing saying what narrowed it or how to widen it again.
//
// Kept out of deals.test.tsx, which is long past its size ceiling.

type Deal = components["schemas"]["Deal"];
type Stage = components["schemas"]["Stage"];

/** The saved view every case here picks, and the filter it narrows by. */
const VIEW_NAME = "Slipping this quarter";
const STALLED_CHIP = "Stalled only";

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
 * The backend this screen reads. `dealUrls` collects every deals request, which
 * is how a case tells a filter the surface merely DREW from one it actually
 * sent — a chip that narrows nothing is the defect, not the picture of it.
 *
 * The per-stage report answers a count of its own, deliberately larger than the
 * cards on the board: the column's count is the server's aggregate over every
 * matching deal, and the surface's count is what has loaded.
 */
function stubBackend(opts: {
  deals: Deal[];
  savedViews?: Record<string, unknown>[];
  stageTotalsRows?: Record<string, unknown>[];
  dealUrls?: string[];
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
    if (method === "GET" && url.includes("/views")) {
      return jsonResponse({
        data: opts.savedViews ?? [],
        page: { next_cursor: null },
      });
    }
    if (method === "POST" && url.includes("/reports/deals-by-stage")) {
      return jsonResponse({
        report: "deals-by-stage",
        plan: {},
        columns: [],
        rows: opts.stageTotalsRows ?? [],
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
      });
    }
    if (url.includes("/organizations")) {
      return jsonResponse({ data: [], page: { next_cursor: null } });
    }
    if (url.includes("/deals")) {
      opts.dealUrls?.push(url);
      return jsonResponse({
        data: opts.deals,
        page: { next_cursor: null, has_more: false },
      });
    }
    return jsonResponse({ data: [], page: { next_cursor: null } });
  });
}

/** A view stored by the reader, narrowing the list to its stalled deals. */
const stalledView = {
  id: "v1",
  resource: "deals",
  name: VIEW_NAME,
  query: { list: { sort: "", filters: { stalled: "true" } } },
  created_at: "2026-06-01T00:00:00Z",
  updated_at: "2026-06-01T00:00:00Z",
};

/** The rail tab for `name`, whichever view is showing. */
function viewTab(name: string): HTMLElement {
  return screen.getByRole("button", { name });
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.location.hash = "";
  localStorage.clear();
});

describe("a saved view over the deals board", () => {
  // The board opens first, so every case here starts by going to the table,
  // picking the view there, and coming back — which is the path the reader took
  // to a narrowed board with no rail on it.
  const pickOnTable = async (dealUrls?: string[]) => {
    const user = userEvent.setup();
    vi.stubGlobal(
      "fetch",
      stubBackend({
        deals: [deal({ stalled: true })],
        savedViews: [stalledView],
        dealUrls,
      }),
    );
    render(<DealsScreen />);
    await user.click(await screen.findByRole("button", { name: "Table" }));
    await user.click(await screen.findByRole("button", { name: VIEW_NAME }));
    await waitFor(() =>
      expect(viewTab(VIEW_NAME).getAttribute("aria-pressed")).toBe("true"),
    );
    return user;
  };

  it("keeps the view's tab on the rail, and lit, on the board and back", async () => {
    const user = await pickOnTable();

    await user.click(screen.getByRole("button", { name: "Board" }));
    expect(viewTab(VIEW_NAME).getAttribute("aria-pressed")).toBe("true");
    // The rail is the whole rail: the screen's own preset stands beside the
    // reader's view, unlit, so switching between them is one press either way.
    expect(viewTab("Newest").getAttribute("aria-pressed")).toBe("false");

    await user.click(screen.getByRole("button", { name: "Table" }));
    expect(viewTab(VIEW_NAME).getAttribute("aria-pressed")).toBe("true");
  });

  it("shows the filter that narrowed the board, and clears it from there", async () => {
    const dealUrls: string[] = [];
    const user = await pickOnTable(dealUrls);
    await user.click(screen.getByRole("button", { name: "Board" }));

    // The filter reads as a row of its own — the attribute and the value the
    // reader is looking at — rather than as a set of deals with no explanation.
    const row = screen.getByRole("group", {
      name: `${STALLED_CHIP}: ${STALLED_CHIP}`,
    });
    await user.click(
      within(row).getByRole("button", {
        name: `More actions for the ${STALLED_CHIP} filter`,
      }),
    );
    await user.click(screen.getByRole("button", { name: "Delete filter" }));

    // Cleared means WIDENED, not un-drawn: the next read of the list asks the
    // server for the deals the filter was keeping off the board.
    await waitFor(() =>
      expect(dealUrls.at(-1)?.includes("stalled=true")).toBe(false),
    );
    expect(
      screen.queryByRole("group", {
        name: `${STALLED_CHIP}: ${STALLED_CHIP}`,
      }),
    ).toBeNull();
    // And the tab stops claiming a list it no longer describes.
    expect(viewTab(VIEW_NAME).getAttribute("aria-pressed")).toBe("false");
  });

  it("keeps the board's own count, its stage totals and its archived toggle", async () => {
    const dealUrls: string[] = [];
    const user = userEvent.setup();
    vi.stubGlobal(
      "fetch",
      stubBackend({
        deals: [deal({}), deal({ id: "d2", name: "Depot rollout" })],
        dealUrls,
        // Seven matching deals in the stage, two of them loaded as cards: the
        // column reports the server's aggregate and the surface reports what is
        // on screen, and neither may be drawn as the other.
        stageTotalsRows: [
          {
            stage_id: "s1",
            currency: "EUR",
            deals: 7,
            raw_minor: 700_000,
            weighted_minor: 140_000,
          },
        ],
      }),
    );
    render(<DealsScreen />);

    expect(await screen.findByText("2 deals")).toBeTruthy();
    expect(screen.getByText("7 deals")).toBeTruthy();
    expect(screen.getByText("€7,000.00")).toBeTruthy();

    // Archived deals are reachable from the board, not only from the table: a
    // deal archived by mistake could otherwise only be restored by leaving.
    const archived = screen.getByRole("checkbox", { name: "Show archived" });
    await user.click(archived);
    await waitFor(() =>
      expect(dealUrls.at(-1)?.includes("include_archived=true")).toBe(true),
    );
  });

  // Both of the grid's own dials describe COLUMNS and ROW HEIGHT, and the board
  // draws neither. Offered there they are controls a reader can press twice and
  // see nothing happen, which reads as broken rather than as absent — and the
  // surface the board replaced never mounted them.
  it("offers no column or density dial on the board, and both on the table", async () => {
    const user = userEvent.setup();
    vi.stubGlobal("fetch", stubBackend({ deals: [deal({})] }));
    render(<DealsScreen />);
    await screen.findByRole("button", { name: "Board" });

    expect(screen.queryByRole("button", { name: "Columns" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Compact" })).toBeNull();

    await user.click(screen.getByRole("button", { name: "Table" }));
    expect(await screen.findByRole("button", { name: "Columns" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Compact" })).toBeTruthy();
  });
});
