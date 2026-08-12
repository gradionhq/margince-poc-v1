/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { CompanyScreen } from "./organizations";

afterEach(() => {
  vi.unstubAllGlobals();
});

function jsonResponse(body: unknown) {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

function withProviders(ui: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return (
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>
  );
}

const org = {
  id: "o-1",
  workspace_id: "w",
  display_name: "Sontana Property Group",
  legal_name: "Sontana Property Group LLC",
  industry: "Property Management & Mid-Term Rentals",
  lifecycle: "customer",
  owner_id: "u-1",
  domains: [{ domain: "sontana.co", is_primary: true }],
  website_url: "https://sontana.co",
  captured_by: "human:u1",
  source: "manual",
  version: 1,
  created_at: "2026-08-10T07:58:00Z",
  updated_at: "2026-08-10T07:58:00Z",
};

const org360 = {
  as_of: "2026-08-12T09:00:00Z",
  organization: org,
  sections_omitted: [],
  people: { data: [], page: { has_more: false, next_cursor: null } },
  deals: {
    data: [],
    page: { has_more: false, next_cursor: null },
    won_lifetime: { amount_minor: 0, currency: "EUR" },
    lost_count: 0,
  },
  activities: { data: [], page: { has_more: false, next_cursor: null } },
  next_steps: { data: [], page: { has_more: false, next_cursor: null } },
  pending_approvals: { data: [], page: { has_more: false, next_cursor: null } },
  tags: [],
  list_memberships: [],
  since_last_visit: {
    baseline_at: null,
    new_activities: 0,
    deal_stage_moves: 0,
    pending_proposals: 0,
  },
  suggestions: [],
  suggestions_dropped: 0,
  last_inbound_at: "2026-07-20T09:00:00Z",
  last_outbound_at: "2026-07-23T09:00:00Z",
  state_strip: null,
};

describe("scratch: header dump", () => {
  it("prints the customer header markup", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        const pathname = new URL(request.url).pathname;
        if (pathname.endsWith("/strength")) {
          return jsonResponse({
            score: 0,
            bucket: "dormant",
            factors: { recency: 0, frequency: 0, reciprocity: 0, direction: 0 },
            last_interaction: null,
          });
        }
        if (pathname.endsWith("/360")) return jsonResponse(org360);
        if (pathname.endsWith("/hierarchy-rollup")) {
          return jsonResponse({
            root_id: "o-1",
            scope: "tree",
            weighted_pipeline: { amount_minor: 0, currency: "EUR" },
            closed_won: { amount_minor: 0, currency: "EUR" },
            activity_count_30d: 0,
            aggregated_account_count: 1,
            restricted_excluded: [],
            computed_at: "2026-08-12T09:00:00Z",
          });
        }
        if (pathname.endsWith("/brief")) {
          return jsonResponse({
            organization_id: "o-1",
            generated_at: "2026-08-12T09:00:00Z",
            generated_by: "deterministic",
            sentences: [],
          });
        }
        if (pathname.endsWith("/context")) {
          return jsonResponse({
            anchor: { type: "organization", id: "o-1" },
            sections: [],
          });
        }
        return jsonResponse(org);
      }),
    );
    render(withProviders(<CompanyScreen id="o-1" />));
    await waitFor(() =>
      expect(screen.getByText("Sontana Property Group")).toBeTruthy(),
    );
    const header = document.querySelector(".record-head");
    console.log(header?.outerHTML);
    expect(header).toBeTruthy();
  });
});
