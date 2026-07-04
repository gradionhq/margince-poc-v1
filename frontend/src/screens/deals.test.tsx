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
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { buildColumns, DealsScreen } from "./deals";

// B-EP09.11 acceptance: board renders per-column sub-lines from the fetched
// set, mixed-currency columns refuse a sum, the board↔table control keeps
// the SAME deal set with no reload, terminal drop opens the 🟡 confirm and
// nothing posts until confirmed, and an open-stage drop posts the advance.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
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

type Stage = components["schemas"]["Stage"];
type Deal = components["schemas"]["Deal"];

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

function stubBackend(deals: Deal[], onAdvance?: (body: unknown) => void) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const request = input instanceof Request ? input : null;
    const url = String(request ? request.url : input);
    const method = request ? request.method : (init?.method ?? "GET");
    if (url.includes("/pipelines")) {
      return jsonResponse({
        data: [
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
      onAdvance?.(body);
      return jsonResponse(deal({ stage_id: body.to_stage_id }));
    }
    if (url.includes("/deals")) {
      return jsonResponse({ data: deals, page: { next_cursor: null } });
    }
    return jsonResponse({ data: [], page: { next_cursor: null } });
  });
}

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
      stubBackend([deal({})], (body) => advances.push(body)),
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

  it("an open-stage drop advances without a confirm", async () => {
    const advances: unknown[] = [];
    vi.stubGlobal(
      "fetch",
      stubBackend([deal({})], (body) => advances.push(body)),
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
});
