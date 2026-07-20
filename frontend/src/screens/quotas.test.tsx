/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { AttainmentRing } from "../design-system/atoms";
import { LocaleProvider } from "../i18n";
import { AttainmentNumbers, PaceLine, QuotasView } from "./quotas";
import { isOwnerXorTeam, parseEuroMinor } from "./quotas.forms";

// Quotas & attainment (RD-T06) acceptance: the ring reflects the server band
// (never a client recompute), the pace compare is ahead/behind/met, honest 422
// refusals draw no ring, the owner-XOR-team 422 branches to a targeted message,
// unresolved deal names fall back to an id, and target entry is integer-euro.

const page = { next_cursor: null, has_more: false };

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

// A method+path router over the same global-fetch seam the app's api client
// resolves per call. Path patterns use :param wildcards; an unmatched route
// falls through to an empty page (the honest default for a GET a test ignores).
type Handler = (ctx: { body: unknown }) => Response;
function installRouter(routes: Record<string, Handler>) {
  const compiled = Object.entries(routes).map(([key, handler]) => {
    const [method, pattern] = key.split(" ");
    const regex = new RegExp(`^${pattern.replace(/:[^/]+/g, "[^/]+")}$`);
    return { method, regex, handler };
  });
  const fetchMock = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : null;
      const url = new URL(
        request ? request.url : String(input),
        "http://localhost",
      );
      const method = (request?.method ?? init?.method ?? "GET").toUpperCase();
      const path = url.pathname.replace(/^\/v1/, "");
      let body: unknown = null;
      if (method !== "GET") {
        try {
          body = request
            ? await request.json()
            : JSON.parse(String(init?.body));
        } catch {
          body = null;
        }
      }
      const match = compiled.find(
        (route) => route.method === method && route.regex.test(path),
      );
      return match ? match.handler({ body }) : jsonResponse({ data: [], page });
    },
  );
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function mount(ui: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}

function renderPure(ui: ReactNode) {
  return render(<LocaleProvider initial="en">{ui}</LocaleProvider>);
}

const ownerQuota = {
  id: "q1",
  workspace_id: "w1",
  owner_id: "u1",
  team_id: null,
  period_start: "2026-07-01",
  period_end: "2026-09-30",
  target_minor: 28000000,
  currency: "EUR",
  version: 3,
  created_at: "2026-06-28T16:40:00Z",
  updated_at: "2026-07-01T09:12:00Z",
};

const users = {
  data: [
    {
      id: "u1",
      workspace_id: "w1",
      email: "riya@example.co",
      display_name: "Riya Patel",
      timezone: "UTC",
      status: "active",
      is_agent: false,
    },
  ],
  page,
};

const attainmentMet: components["schemas"]["QuotaAttainment"] = {
  quota_id: "q1",
  closed_won_minor: 31387200,
  target_minor: 28000000,
  currency: "EUR",
  attainment_pct: 113,
  gap_minor: 3387200,
  pace_pct: 64,
  band: "met",
  as_of_date: "2026-08-15",
  contributing_deals: [{ deal_id: "d1", base_value_minor: 17707200 }],
};

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("AttainmentRing", () => {
  function arc(container: HTMLElement) {
    const circles = container.querySelectorAll("circle");
    // [0] is the background track; [1] is the value arc.
    return circles[1];
  }
  const circumference = 2 * Math.PI * 68;

  it("colours the arc by the server band, not the raw percentage", () => {
    const met = renderPure(
      <AttainmentRing pct={113} band="met" caption="attained" />,
    );
    expect(arc(met.container).getAttribute("stroke")).toBe("var(--online)");
    expect(met.getByText("113%")).toBeTruthy();
    cleanup();

    const accent = renderPure(
      <AttainmentRing pct={72} band="accent" caption="attained" />,
    );
    expect(arc(accent.container).getAttribute("stroke")).toBe("var(--accent)");
    cleanup();

    const behind = renderPure(
      <AttainmentRing pct={41} band="behind" caption="attained" />,
    );
    expect(arc(behind.container).getAttribute("stroke")).toBe("var(--away)");
  });

  it("offsets the dash by min(pct/100, 1) and caps a full circle over 100%", () => {
    const half = renderPure(
      <AttainmentRing pct={50} band="behind" caption="attained" />,
    );
    const halfOffset = Number(
      arc(half.container).getAttribute("stroke-dashoffset"),
    );
    expect(halfOffset).toBeCloseTo(circumference * 0.5, 3);
    cleanup();

    // 150% attainment caps the arc at a full circle (offset 0) — the centre
    // still prints the real, uncapped figure.
    const over = renderPure(
      <AttainmentRing pct={150} band="met" caption="attained" />,
    );
    expect(
      Number(arc(over.container).getAttribute("stroke-dashoffset")),
    ).toBeCloseTo(0, 6);
    expect(over.getByText("150%")).toBeTruthy();
  });
});

describe("PaceLine", () => {
  it("reads met from the band, then compares attainment_pct against pace_pct", () => {
    const met = renderPure(<PaceLine attainment={attainmentMet} />);
    expect(met.getByText("Target met — 113% attained.")).toBeTruthy();
    cleanup();

    const ahead = renderPure(
      <PaceLine
        attainment={{
          ...attainmentMet,
          band: "accent",
          attainment_pct: 70,
          pace_pct: 64,
        }}
      />,
    );
    expect(
      ahead.getByText("Ahead of pace — 70% attained vs 64% of period elapsed."),
    ).toBeTruthy();
    cleanup();

    const behind = renderPure(
      <PaceLine
        attainment={{
          ...attainmentMet,
          band: "behind",
          attainment_pct: 41,
          pace_pct: 64,
        }}
      />,
    );
    expect(
      behind.getByText("Behind pace — 41% attained vs 64% of period elapsed."),
    ).toBeTruthy();
  });
});

describe("AttainmentNumbers", () => {
  it("prefixes a non-negative gap with '+' and labels base-currency money", () => {
    const over = renderPure(
      <AttainmentNumbers attainment={attainmentMet} locale="en" />,
    );
    // gap_minor 3387200 is positive once over target → shown with a leading +.
    expect(over.getByText(/^\+/)).toBeTruthy();
  });
});

describe("parseEuroMinor", () => {
  it("parses grouped integer euros to minor units and round-trips", () => {
    expect(parseEuroMinor("280.000")).toBe(28000000);
    expect(parseEuroMinor("1234")).toBe(123400);
    expect(parseEuroMinor("")).toBe(0);
    expect(parseEuroMinor("abc")).toBe(0);
  });
});

describe("isOwnerXorTeam", () => {
  it("matches the branchable code at the top level or in details.errors", () => {
    expect(isOwnerXorTeam({ code: "owner_xor_team_required" })).toBe(true);
    expect(
      isOwnerXorTeam({
        code: "validation_error",
        details: { errors: [{ code: "owner_xor_team_required" }] },
      }),
    ).toBe(true);
    expect(
      isOwnerXorTeam({
        code: "validation_error",
        details: { errors: [{ code: "period_start_required" }] },
      }),
    ).toBe(false);
    expect(isOwnerXorTeam(null)).toBe(false);
  });
});

describe("QuotasView", () => {
  it("renders the attainment ring, resolved names, and contributing deal", async () => {
    installRouter({
      "GET /quotas": () => jsonResponse({ data: [ownerQuota], page }),
      "GET /quotas/:id/attainment": () => jsonResponse(attainmentMet),
      "GET /users": () => jsonResponse(users),
      "GET /teams": () => jsonResponse({ data: [], page }),
      "GET /deals/:id": () =>
        jsonResponse({ id: "d1", name: "BÄR Pharma — Packaging QA" }),
    });
    mount(<QuotasView />);
    expect(await screen.findByText("113%")).toBeTruthy();
    expect(screen.getByText("Target met — 113% attained.")).toBeTruthy();
    expect(await screen.findByText("Riya Patel")).toBeTruthy();
    expect(await screen.findByText("BÄR Pharma — Packaging QA")).toBeTruthy();
  });

  it("shows the empty state with a CTA when no quota is set", async () => {
    installRouter({ "GET /quotas": () => jsonResponse({ data: [], page }) });
    mount(<QuotasView />);
    expect(await screen.findByText("No quota set")).toBeTruthy();
    expect(screen.getByTestId("quota-create")).toBeTruthy();
  });

  it("refuses target-zero attainment with the server detail and no ring", async () => {
    installRouter({
      "GET /quotas": () => jsonResponse({ data: [ownerQuota], page }),
      "GET /users": () => jsonResponse(users),
      "GET /quotas/:id/attainment": () =>
        jsonResponse(
          {
            code: "attainment_target_zero",
            detail: "target is zero — set a target to compute attainment",
            status: 422,
          },
          422,
        ),
    });
    mount(<QuotasView />);
    expect(
      await screen.findByText("This quota has no target yet"),
    ).toBeTruthy();
    expect(
      screen.getByText("target is zero — set a target to compute attainment"),
    ).toBeTruthy();
    // No ring is drawn for an un-computable attainment.
    expect(screen.queryByText("attained")).toBeNull();
  });

  it("refuses a failed computation with a retry, never a stale ring", async () => {
    installRouter({
      "GET /quotas": () => jsonResponse({ data: [ownerQuota], page }),
      "GET /users": () => jsonResponse(users),
      "GET /quotas/:id/attainment": () =>
        jsonResponse(
          {
            code: "attainment_computation_failed",
            detail: "the clean-core query timed out",
            status: 422,
          },
          422,
        ),
    });
    mount(<QuotasView />);
    expect(
      await screen.findByText("Attainment couldn't be computed"),
    ).toBeTruthy();
    expect(screen.getByText("the clean-core query timed out")).toBeTruthy();
    expect(screen.getByText("Retry")).toBeTruthy();
    expect(screen.queryByText("attained")).toBeNull();
  });

  it("falls back to the deal id when the deal name can't be resolved", async () => {
    installRouter({
      "GET /quotas": () => jsonResponse({ data: [ownerQuota], page }),
      "GET /users": () => jsonResponse(users),
      "GET /quotas/:id/attainment": () =>
        jsonResponse({
          ...attainmentMet,
          contributing_deals: [{ deal_id: "d2", base_value_minor: 9450000 }],
        }),
      "GET /deals/:id": () =>
        jsonResponse({ code: "not_found", status: 404 }, 404),
    });
    mount(<QuotasView />);
    // The unresolved deal renders its id, never a fabricated name.
    expect(await screen.findByText("d2")).toBeTruthy();
  });

  it("branches the owner-XOR-team 422 to a targeted message on create", async () => {
    installRouter({
      "GET /quotas": () => jsonResponse({ data: [], page }),
      "GET /users": () => jsonResponse(users),
      "GET /teams": () => jsonResponse({ data: [], page }),
      "POST /quotas": () =>
        jsonResponse(
          {
            code: "validation_error",
            detail: "validation failed",
            status: 422,
            details: { errors: [{ code: "owner_xor_team_required" }] },
          },
          422,
        ),
    });
    mount(<QuotasView />);
    await userEvent.click(await screen.findByTestId("quota-create"));
    const subjectSelect = await screen.findByRole("combobox");
    // Wait for the roster-fetched option to render — setting .value before the
    // matching <option> exists silently no-ops (leaving canSubmit false).
    await screen.findByRole("option", { name: "Riya Patel" });
    fireEvent.change(subjectSelect, { target: { value: "u1" } });
    const dateInputs = document.querySelectorAll('input[type="date"]');
    fireEvent.change(dateInputs[0], { target: { value: "2026-07-01" } });
    fireEvent.change(dateInputs[1], { target: { value: "2026-09-30" } });
    fireEvent.change(screen.getByLabelText(/Target amount/), {
      target: { value: "280000" },
    });
    await userEvent.click(screen.getByTestId("quota-create-submit"));
    expect(
      await screen.findByText("Choose exactly one of owner or team."),
    ).toBeTruthy();
  });
});
