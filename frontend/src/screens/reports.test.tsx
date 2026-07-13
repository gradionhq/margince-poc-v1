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
import { LocaleProvider } from "../i18n";
import { parseDerivationQuery, ReportsScreen } from "./reports";

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
};

function reportsStub(opts: ReportsStubOpts = {}) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const request = input instanceof Request ? input : null;
    const url = String(request ? request.url : input);
    const method = request ? request.method : (init?.method ?? "GET");
    if (method === "GET" && url.includes("/derivation")) {
      return jsonResponse(opts.derivation ?? {});
    }
    if (url.includes("/pipelines")) {
      return jsonResponse({
        data: [
          {
            id: "pl",
            workspace_id: "w",
            name: "Sales",
            is_default: true,
            position: 0,
            stages: [
              {
                id: "pl-s1",
                workspace_id: "w",
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
        derivation_url: `/v1/reports/${key}/derivation?by=stage_id&agg=sum:amount_minor:raw_minor`,
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

  it("explain fetches the derivation and renders source rows, not raw JSON", async () => {
    vi.stubGlobal(
      "fetch",
      reportsStub({
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
