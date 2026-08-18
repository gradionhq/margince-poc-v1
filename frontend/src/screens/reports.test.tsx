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
import { formatMoney, MONEY_ABSENT } from "../format/format";
import { LocaleProvider } from "../i18n";
import {
  buildStageAggregates,
  groupForecastAmounts,
  parseDerivationQuery,
  ReportsScreen,
} from "./reports";

// D2 acceptance: a report picker over deals-by-stage (unchanged), forecast
// (unweighted category tiles + a weighted-vs-unweighted banner), and
// open-deals-per-company (a DataTable) — all driven by the same typed
// `runReport` POST, keyed on the report.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
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

type ReportsStubOpts = {
  onRun?: (key: string, body: Record<string, unknown>) => void;
  stageRows?: Record<string, unknown>[];
  forecastRows?: Record<string, unknown>[];
  companyRows?: Record<string, unknown>[];
  derivation?: Record<string, unknown>;
  onDerivation?: (url: string) => void;
};

function reportsStub(opts: ReportsStubOpts = {}) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const request = input instanceof Request ? input : null;
    const url = String(request ? request.url : input);
    const method = request ? request.method : (init?.method ?? "GET");
    if (method === "GET" && url.includes("/derivation")) {
      opts.onDerivation?.(url);
      return jsonResponse(opts.derivation ?? {});
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
                id: "pl-s1",
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
    if (method === "POST" && url.includes("/reports/")) {
      const match = url.match(/\/reports\/([^/?]+)/);
      const key = match ? match[1] : "";
      const body = request
        ? await request.json()
        : JSON.parse(String(init?.body));
      opts.onRun?.(key, body);
      const rows =
        key === "forecast"
          ? (opts.forecastRows ?? [])
          : key === "open-deals-per-company"
            ? (opts.companyRows ?? [])
            : (opts.stageRows ?? [
                {
                  stage_id: "pl-s1",
                  raw_minor: 100000,
                  deal_count: 2,
                  currency: "EUR",
                },
              ]);
      return jsonResponse({
        report: key,
        plan: {},
        columns: [],
        rows,
        derivation_url: `/v1/reports/${key}/derivation?by=stage_id&agg=sum:amount_minor:raw_minor&stage_id=pl-s1`,
      });
    }
    return jsonResponse({ data: [], page: { next_cursor: null } });
  });
}

describe("ReportsScreen", () => {
  it("defaults to deals-by-stage and renders unweighted/weighted columns", async () => {
    vi.stubGlobal("fetch", reportsStub());
    render(<ReportsScreen />);
    await waitFor(() => expect(screen.getByText("Qualify")).toBeTruthy());
  });

  it("switching to Forecast groups by forecast_category and renders category tiles", async () => {
    const bodies: { key: string; body: Record<string, unknown> }[] = [];
    vi.stubGlobal(
      "fetch",
      reportsStub({
        onRun: (key, body) => bodies.push({ key, body }),
        forecastRows: [
          {
            forecast_category: "commit",
            raw_minor: 500000,
            weighted_minor: 300000,
            deal_count: 3,
            currency: "EUR",
          },
        ],
      }),
    );
    render(<ReportsScreen />);
    await userEvent.click(
      await screen.findByRole("button", { name: "Forecast" }),
    );
    await waitFor(() => expect(screen.getByText("Commit")).toBeTruthy());
    expect(
      bodies.some(
        (b) =>
          b.key === "forecast" &&
          Array.isArray(b.body.group_by) &&
          b.body.group_by.includes("forecast_category"),
      ),
    ).toBe(true);
    // AC-F1: the weighted forecast is the server's own figure —
    // requested and rendered, not left computed-and-unshown.
    expect(
      bodies.some(
        (b) =>
          b.key === "forecast" &&
          Array.isArray(b.body.aggregates) &&
          (b.body.aggregates as { field?: string }[]).some(
            (a) => a.field === "weighted_amount_minor",
          ),
      ),
    ).toBe(true);
  });

  it("renders the slipped bucket in the Forecast tiles", async () => {
    vi.stubGlobal(
      "fetch",
      reportsStub({
        forecastRows: [
          {
            forecast_category: "slipped",
            raw_minor: 90000,
            weighted_minor: 45000,
            deal_count: 1,
            currency: "EUR",
          },
        ],
      }),
    );
    render(<ReportsScreen />);
    await userEvent.click(
      await screen.findByRole("button", { name: "Forecast" }),
    );
    await waitFor(() => expect(screen.getByText("Slipped")).toBeTruthy());
  });

  it("deals-by-stage requests and renders the server's weighted_minor, not a client re-derivation", async () => {
    const bodies: { key: string; body: Record<string, unknown> }[] = [];
    vi.stubGlobal(
      "fetch",
      reportsStub({
        onRun: (key, body) => bodies.push({ key, body }),
        // 12343 × 20% per deal (2469 × 2 = 4938) — a figure a client can only
        // reproduce by rounding round(24686 × 20%) = 4937, the wrong way.
        stageRows: [
          {
            stage_id: "pl-s1",
            raw_minor: 24686,
            weighted_minor: 4938,
            deal_count: 2,
            currency: "EUR",
          },
        ],
      }),
    );
    render(<ReportsScreen />);
    await waitFor(() => expect(screen.getByText("Qualify")).toBeTruthy());
    expect(
      bodies.some(
        (b) =>
          b.key === "deals-by-stage" &&
          Array.isArray(b.body.aggregates) &&
          (b.body.aggregates as { field?: string }[]).some(
            (a) => a.field === "weighted_amount_minor",
          ),
      ),
    ).toBe(true);
    expect(await screen.findByText("€49.38")).toBeTruthy();
    expect(screen.queryByText("€49.37")).toBeNull();
  });

  it("switching to Open deals per company groups by organization_id and renders a table", async () => {
    vi.stubGlobal(
      "fetch",
      reportsStub({
        companyRows: [
          {
            organization_id: "o1",
            raw_minor: 250000,
            deal_count: 4,
            currency: "EUR",
          },
        ],
      }),
    );
    render(<ReportsScreen />);
    await userEvent.click(
      await screen.findByRole("button", { name: "Open deals per company" }),
    );
    await waitFor(() => expect(screen.getByText("o1")).toBeTruthy());
  });

  it("the Quotas segment renders the quota surface and skips the report run", async () => {
    const runKeys: string[] = [];
    vi.stubGlobal("fetch", reportsStub({ onRun: (key) => runKeys.push(key) }));
    render(<ReportsScreen />);
    await userEvent.click(
      await screen.findByRole("button", { name: "Quotas" }),
    );
    // QuotasView owns its own query lifecycle: GET /quotas falls through the
    // stub to an empty page, so the honest "no quota set" empty state renders.
    await waitFor(() => expect(screen.getByText("No quota set")).toBeTruthy());
    // The shared /reports/{report} run is never issued for the quotas segment.
    expect(runKeys).not.toContain("quotas");
  });

  it("explain fetches the derivation and renders source rows, not raw JSON", async () => {
    const derivationUrls: string[] = [];
    vi.stubGlobal(
      "fetch",
      reportsStub({
        onDerivation: (u) => derivationUrls.push(u),
        derivation: {
          report: "deals-by-stage",
          definition: "Sum over open deals",
          plan: {},
          columns: ["name"],
          rows: [{ name: "Fleet retrofit" }],
        },
      }),
    );
    render(<ReportsScreen />);
    await userEvent.click(
      await screen.findByRole("button", { name: /Explain/ }),
    );
    await waitFor(() =>
      expect(screen.getByText("Fleet retrofit")).toBeTruthy(),
    );
    expect(screen.queryByText(/"plan":/)).toBeNull();
    // The equality predicate from derivation_url must survive to the request —
    // by/agg alone would explain the wrong slice.
    expect(derivationUrls[0]).toContain("stage_id=pl-s1");
    expect(derivationUrls[0]).toContain("by=stage_id");
  });
});

describe("parseDerivationQuery", () => {
  it("pulls by/agg + predicate params from a derivation_url", () => {
    const q = parseDerivationQuery(
      "/v1/reports/deals-by-stage/derivation?by=stage_id&agg=sum:amount_minor:raw&stage_id=s1",
    );
    expect(q.by).toEqual(["stage_id"]);
    expect(q.agg).toEqual(["sum:amount_minor:raw"]);
    expect(q.stage_id).toBe("s1");
  });
});

// Money never sums across currencies (data-semantics §1 r4, AC-DS-FX1): every
// plan this screen runs groups by currency, so a stage or a category holding
// two currencies has two totals and the screen shows both. What it must never
// show is one figure that added them, or a currency it invented for a figure
// the server sent without one.
describe("reports never sum money across currencies", () => {
  const STAGES = [
    { id: "pl-s2", name: "Propose", position: 2 },
    { id: "pl-s1", name: "Qualify", position: 1 },
  ];

  it("asks the server to group every money plan by currency", async () => {
    const bodies: { key: string; body: Record<string, unknown> }[] = [];
    vi.stubGlobal(
      "fetch",
      reportsStub({ onRun: (key, body) => bodies.push({ key, body }) }),
    );
    render(<ReportsScreen />);
    await waitFor(() => expect(screen.getByText("Qualify")).toBeTruthy());
    const stagePlan = bodies.find((sent) => sent.key === "deals-by-stage");
    expect(stagePlan?.body).toMatchObject({
      group_by: ["stage_id", "currency"],
    });
  });

  it("renders a stage's two currencies as two rows and no combined figure", async () => {
    vi.stubGlobal(
      "fetch",
      reportsStub({
        stageRows: [
          {
            stage_id: "pl-s1",
            raw_minor: 100_000,
            weighted_minor: 20_000,
            deal_count: 2,
            currency: "EUR",
          },
          {
            stage_id: "pl-s1",
            raw_minor: 4_500_000_000,
            weighted_minor: 900_000_000,
            deal_count: 3,
            currency: "VND",
          },
        ],
      }),
    );
    render(<ReportsScreen />);
    expect(
      await screen.findByText(formatMoney(100_000, "EUR", "en")),
    ).toBeTruthy();
    expect(
      screen.getByText(formatMoney(4_500_000_000, "VND", "en")),
    ).toBeTruthy();
    expect(screen.getAllByText("EUR").length).toBeGreaterThan(0);
    expect(screen.getAllByText("VND").length).toBeGreaterThan(0);
    // The figure the old ungrouped plan printed: both currencies added and
    // labelled with whichever one happened to arrive first.
    expect(
      screen.queryByText(formatMoney(4_500_100_000, "EUR", "en")),
    ).toBeNull();
  });

  it("renders an unpriced stage as absent rather than as zero euros", async () => {
    vi.stubGlobal(
      "fetch",
      reportsStub({
        stageRows: [
          {
            stage_id: "pl-s1",
            raw_minor: null,
            weighted_minor: null,
            deal_count: 4,
            currency: null,
          },
        ],
      }),
    );
    render(<ReportsScreen />);
    await waitFor(() => expect(screen.getByText("Qualify")).toBeTruthy());
    // The count is real and stays; only the money is unknown.
    expect(screen.getByText("4")).toBeTruthy();
    expect(screen.getAllByText(MONEY_ABSENT).length).toBeGreaterThan(0);
    expect(screen.queryByText(formatMoney(0, "EUR", "en"))).toBeNull();
  });

  it("renders a forecast category with no deals as absent rather than as zero euros", async () => {
    vi.stubGlobal("fetch", reportsStub({ forecastRows: [] }));
    render(<ReportsScreen />);
    await userEvent.setup().click(await screen.findByText("Forecast"));
    await waitFor(() =>
      expect(screen.getAllByText(MONEY_ABSENT).length).toBeGreaterThan(0),
    );
    expect(screen.queryByText(formatMoney(0, "EUR", "en"))).toBeNull();
  });

  it("orders stage rows down the pipeline, then by currency code", () => {
    const rows = [
      { stage_id: "pl-s2", currency: "VND", raw_minor: 1, deal_count: 1 },
      { stage_id: "pl-s1", currency: "VND", raw_minor: 1, deal_count: 1 },
      { stage_id: "pl-s2", currency: "EUR", raw_minor: 1, deal_count: 1 },
      { stage_id: "pl-s1", currency: "EUR", raw_minor: 1, deal_count: 1 },
    ];
    expect(
      buildStageAggregates(rows, STAGES).map(
        (row) => `${row.stageName}/${row.currency}`,
      ),
    ).toEqual(["Qualify/EUR", "Qualify/VND", "Propose/EUR", "Propose/VND"]);
  });

  it("sorts a row whose stage the pipeline no longer carries to the end", () => {
    const rows = [
      { stage_id: "gone", currency: "EUR", raw_minor: 1, deal_count: 1 },
      { stage_id: "pl-s1", currency: "EUR", raw_minor: 1, deal_count: 1 },
    ];
    expect(
      buildStageAggregates(rows, STAGES).map((row) => row.stageId),
    ).toEqual(["pl-s1", "gone"]);
  });

  it("keeps an absent measure absent rather than reading it as zero", () => {
    const [row] = buildStageAggregates(
      [
        {
          stage_id: "pl-s1",
          currency: null,
          raw_minor: null,
          weighted_minor: null,
          deal_count: 3,
        },
      ],
      STAGES,
    );
    expect(row.rawMinor).toBeNull();
    expect(row.weightedMinor).toBeNull();
    expect(row.currency).toBeNull();
    // A count of zero would be a claim; the server did send this one.
    expect(row.count).toBe(3);
  });

  it("gathers a forecast category's currencies in code order", () => {
    const grouped = groupForecastAmounts([
      { forecast_category: "commit", currency: "VND", raw_minor: 2 },
      { forecast_category: "commit", currency: "EUR", raw_minor: 1 },
      { forecast_category: "pipeline", currency: "EUR", raw_minor: 3 },
    ]);
    expect(grouped.get("commit")?.map((amount) => amount.currency)).toEqual([
      "EUR",
      "VND",
    ]);
    expect(grouped.get("pipeline")).toHaveLength(1);
    expect(grouped.get("best_case")).toBeUndefined();
  });
});
