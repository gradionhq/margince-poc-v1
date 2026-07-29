/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { CompanyScreen } from "./organizations";

// The company view's honesty rules, which are the whole point of the
// composite read:
//
//   - a section the caller's role withheld says so, and never draws the
//     empty state that would read as "there is none";
//   - consent is per purpose and default-deny, so silence never renders as
//     permission;
//   - a workspace reading from an incumbent mirror gets one refusal, not a
//     page that quietly omits most of itself.

const org = {
  id: "o-1",
  workspace_id: "w",
  display_name: "Brandt Automotive GmbH",
  industry: "Automotive",
  captured_by: "human:u1",
  source: "manual",
  version: 1,
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-06-01T08:00:00Z",
};

const emptyPage = { has_more: false, next_cursor: null };

function view(overrides: Record<string, unknown> = {}) {
  return {
    as_of: "2026-06-01T09:00:00Z",
    organization: org,
    sections_omitted: [],
    people: { data: [], page: emptyPage },
    deals: {
      data: [],
      page: emptyPage,
      won_lifetime: { amount_minor: 0, currency: "EUR" },
      lost_count: 0,
    },
    activities: { data: [], page: emptyPage },
    next_steps: { data: [], page: emptyPage },
    pending_approvals: { data: [], page: emptyPage },
    tags: [],
    list_memberships: [],
    since_last_visit: {
      baseline_at: "2026-05-30T09:00:00Z",
      new_activities: 0,
      deal_stage_moves: 0,
      pending_proposals: 0,
    },
    ...overrides,
  };
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

const emptyRollup = {
  root_id: "o-1",
  scope: "tree",
  weighted_pipeline: { amount_minor: 0, currency: "EUR" },
  closed_won: { amount_minor: 0, currency: "EUR" },
  activity_count_30d: 0,
  aggregated_account_count: 1,
  restricted_excluded: [],
  computed_at: "2026-06-01T09:00:00Z",
};

function stub(three60: unknown, status = 200) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      const pathname = new URL(request.url).pathname;
      if (pathname.endsWith("/360")) {
        return jsonResponse(three60, status);
      }
      if (pathname.endsWith("/hierarchy-rollup")) {
        return jsonResponse(emptyRollup);
      }
      if (pathname.endsWith("/organizations/o-1")) {
        return jsonResponse(org);
      }
      return jsonResponse({ data: [], page: emptyPage });
    }),
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function render(ui: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}

function renderCompany() {
  render(<CompanyScreen id="o-1" />);
}

describe("company view — withheld sections", () => {
  it("says a section is hidden rather than drawing it empty", async () => {
    stub(view({ deals: undefined, sections_omitted: ["deals"] }));
    renderCompany();

    const card = await screen.findByRole("complementary", { name: "Business" });
    const deals = within(card).getByText("Deals").closest("section");
    if (!deals) {
      throw new Error("the deals card has no section wrapper");
    }
    expect(
      within(deals).getByText("Hidden — your role cannot read this"),
    ).toBeTruthy();
    // The empty state and the withheld state must never both appear: one
    // says there is nothing, the other says you may not know.
    expect(
      within(deals).queryByText("No open deal on this account."),
    ).toBeNull();
  });

  it("draws the empty state when the section is present and empty", async () => {
    stub(view());
    renderCompany();

    const card = await screen.findByRole("complementary", { name: "Business" });
    expect(
      within(card).getByText("No open deal on this account."),
    ).toBeTruthy();
    expect(
      within(card).queryByText("Hidden — your role cannot read this"),
    ).toBeNull();
  });
});

describe("company view — consent is per purpose", () => {
  it("reads an all-unknown consent map as no permission, not as silence", async () => {
    stub(
      view({
        people: {
          data: [
            {
              person_id: "p-1",
              full_name: "Dana Buyer",
              deal_roles: [],
              consent: {
                marketing_email: "unknown",
                product_updates: "unknown",
              },
              strength: {
                score: 0,
                bucket: "dormant",
                factors: {
                  recency: 0,
                  frequency: 0,
                  reciprocity: 0,
                  direction: 0,
                },
              },
            },
          ],
          page: emptyPage,
        },
      }),
    );
    renderCompany();

    await waitFor(() => expect(screen.getByText("Dana Buyer")).toBeTruthy());
    expect(screen.getByText("No consent on file")).toBeTruthy();
    expect(screen.queryByText("May contact")).toBeNull();
  });

  it("reads one granted purpose as contactable", async () => {
    stub(
      view({
        people: {
          data: [
            {
              person_id: "p-1",
              full_name: "Dana Buyer",
              deal_roles: [],
              consent: {
                marketing_email: "granted",
                product_updates: "unknown",
              },
              strength: {
                score: 62,
                bucket: "strong",
                factors: {
                  recency: 0.9,
                  frequency: 0.6,
                  reciprocity: 0.8,
                  direction: 0.8,
                },
              },
            },
          ],
          page: emptyPage,
        },
      }),
    );
    renderCompany();

    await waitFor(() => expect(screen.getByText("May contact")).toBeTruthy());
  });
});

describe("company view — overlay mode", () => {
  it("refuses once instead of rendering a page missing most of itself", async () => {
    stub(
      {
        title: "Unprocessable",
        code: "validation_error",
        details: {
          errors: [
            { field: "id", code: "unsupported_in_overlay_mode", message: "x" },
          ],
        },
      },
      422,
    );
    renderCompany();

    await waitFor(() =>
      expect(screen.getByText(/not assembled here/)).toBeTruthy(),
    );
    // No half-page: the business rail is absent entirely rather than showing
    // cards that would each read as an empty account.
    expect(
      screen.queryByRole("complementary", { name: "Business" }),
    ).toBeNull();
  });
});

describe("company view — what changed since the last visit", () => {
  it("counts only the dimensions it was allowed to count", async () => {
    stub(
      view({
        since_last_visit: {
          baseline_at: "2026-05-30T09:00:00Z",
          new_activities: 3,
          // Null, not zero: the caller has no deal grant, so this dimension
          // was not counted at all and must not read as "nothing moved".
          deal_stage_moves: null,
          pending_proposals: 2,
        },
      }),
    );
    renderCompany();

    await waitFor(() =>
      expect(screen.getByText("3 new activities")).toBeTruthy(),
    );
    expect(screen.getByText("2 decisions waiting")).toBeTruthy();
    expect(screen.queryByText(/deal stage moves/)).toBeNull();
  });

  it("greets a first visit as a first visit, not as nothing having happened", async () => {
    stub(
      view({
        since_last_visit: {
          baseline_at: null,
          new_activities: 0,
          deal_stage_moves: 0,
          pending_proposals: 0,
        },
      }),
    );
    renderCompany();

    await waitFor(() =>
      expect(
        screen.getByText("You are opening this account for the first time."),
      ).toBeTruthy(),
    );
    expect(screen.queryByText("Nothing new since your last visit.")).toBeNull();
  });
});

describe("company view — next steps", () => {
  it("marks an overdue task and names what it is linked to", async () => {
    stub(
      view({
        next_steps: {
          data: [
            {
              activity_id: "a-1",
              subject: "Send the renewal paperwork",
              due_at: "2026-05-01T09:00:00Z",
              overdue: true,
              linked_deal_id: null,
              linked_person_id: null,
              assignee_id: null,
            },
          ],
          page: emptyPage,
        },
      }),
    );
    renderCompany();

    await waitFor(() =>
      expect(screen.getByText("Send the renewal paperwork")).toBeTruthy(),
    );
    expect(screen.getByText("Overdue")).toBeTruthy();
  });
});

describe("company view — a failed read is not an empty account", () => {
  it("says the page is partial instead of drawing a bare account", async () => {
    stub({ title: "Internal", detail: "boom" }, 500);
    renderCompany();

    await waitFor(() =>
      expect(screen.getByText(/may not show everything/)).toBeTruthy(),
    );
    // The business rail STAYS, with each card saying it could not be loaded.
    // Removing it would read as an account with no people and no deals,
    // which is the one thing this page does not know.
    const card = screen.getByRole("complementary", { name: "Business" });
    expect(
      within(card).getAllByText(/Could not be loaded/).length,
    ).toBeGreaterThan(0);
    expect(
      within(card).queryByText("No open deal on this account."),
    ).toBeNull();
    expect(
      within(card).queryByText("No contact linked to this account yet."),
    ).toBeNull();
  });

  it("distinguishes a section that is missing from one that is empty", async () => {
    // No `deals` key at all and nothing named in sections_omitted: the
    // server did not say the caller may not read it, and did not send it —
    // so the page knows nothing, and must not claim there are none.
    stub(view({ deals: undefined }));
    renderCompany();

    const card = await screen.findByRole("complementary", { name: "Business" });
    const deals = within(card).getByText("Deals").closest("section");
    if (!deals) {
      throw new Error("the deals card has no section wrapper");
    }
    expect(within(deals).getByText(/Could not be loaded/)).toBeTruthy();
    expect(
      within(deals).queryByText("No open deal on this account."),
    ).toBeNull();
    expect(
      within(deals).queryByText("Hidden — your role cannot read this"),
    ).toBeNull();
  });
});
