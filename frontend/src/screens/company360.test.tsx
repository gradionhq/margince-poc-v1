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
import { meFixture } from "../app/mefixture";
import { LocaleProvider } from "../i18n";
import { PeopleCard } from "./company360";
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

const EMPTY_BRIEF = {
  organization_id: "o-1",
  generated_at: "2026-06-01T09:00:00Z",
  generated_by: "deterministic",
  sentences: [],
};

// Reset after every test (see afterEach): a brief one case set for itself is
// otherwise still being served to the next one.
let briefBody: unknown = EMPTY_BRIEF;

// partnerOrg is the account that HAS a partner programme, and so carries the
// second tab. Partnerhood is read off the relationship type rather than the
// extension row, because the Organization read never selects that row — the
// two are equivalent, since the store enforces the invariant both ways.
// The bare fixture deliberately carries neither: a Partner tab on an account
// with no programme is the thing the tab gate removed.
const partnerOrg = { ...org, relationship_types: ["partner"] };

function stub(
  three60: unknown,
  status = 200,
  account: unknown = org,
  finance: unknown = { organization_id: "o-1", state: "no_connection" },
) {
  // The paths actually requested. A test proves the page did NOT refetch by
  // counting these rather than by trusting that it did not.
  const fetched: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      const pathname = new URL(request.url).pathname;
      fetched.push(pathname);
      if (pathname.endsWith("/360")) {
        return jsonResponse(three60, status);
      }
      if (pathname.endsWith("/finance-summary")) {
        return jsonResponse(finance);
      }
      if (pathname.endsWith("/hierarchy-rollup")) {
        return jsonResponse(emptyRollup);
      }
      if (pathname.endsWith("/brief")) {
        return jsonResponse(briefBody);
      }
      if (pathname.endsWith("/graph")) {
        return jsonResponse({
          nodes: [
            { id: "u-2", kind: "user", label: "Mira", root: false },
            { id: "p-1", kind: "person", label: "Dana Buyer", root: false },
          ],
          edges: [
            {
              from: "u-2",
              to: "p-1",
              kind: "in_contact_with",
              strength: 90,
              strength_bucket: "strong",
            },
          ],
          groups_omitted: [],
          dropped_count: 0,
        });
      }
      // The viewer's grants. Without this useCan denies — it fails closed on a
      // missing snapshot — and every in-place editor on the page renders as
      // read-only text, so a test could not tell "correctly withheld" from
      // "never built".
      if (pathname.endsWith("/v1/me")) {
        return jsonResponse(
          meFixture({ allow: { organization: ["read", "update"] } }),
        );
      }
      if (pathname.endsWith("/organizations/o-1")) {
        return jsonResponse(account);
      }
      if (pathname.endsWith("/pipelines")) {
        // One default pipeline with one OPEN stage — enough for a deal
        // created from this page to have somewhere to land.
        return jsonResponse({
          data: [
            {
              id: "pl-1",
              workspace_id: "w-1",
              name: "Sales",
              is_default: true,
              stages: [
                {
                  id: "st-1",
                  pipeline_id: "pl-1",
                  name: "Qualify",
                  position: 1,
                  semantic: "open",
                  probability: 10,
                },
              ],
            },
          ],
          page: emptyPage,
        });
      }
      return jsonResponse({ data: [], page: emptyPage });
    }),
  );
  return fetched;
}

// useMe only asks /v1/me once a workspace slug is resolved, and useCan denies
// until it answers — so without the slug every in-place editor on this page
// renders read-only and a test cannot tell a withheld control from a missing
// one.
beforeEach(() => {
  globalThis.localStorage.setItem("margince.workspaceSlug", "acme");
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  globalThis.localStorage.clear();
  briefBody = EMPTY_BRIEF;
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

  it("reports no committee gap when the people section was withheld", async () => {
    // The gap is computed from the contact list, and a withheld section
    // arrives as the same empty array an account with no contacts does.
    // Reading "nobody here is your champion" off contacts the caller was
    // never allowed to see states a fact about data the page does not have.
    stub(
      view({
        people: undefined,
        sections_omitted: ["people"],
        deals: {
          data: [
            {
              deal_id: "d-1",
              name: "Pilot",
              status: "open",
              stalled: false,
            },
          ],
          page: emptyPage,
          won_lifetime: { amount_minor: 0, currency: "EUR" },
          lost_count: 0,
        },
      }),
    );
    renderCompany();

    await screen.findByRole("complementary", { name: "Business" });
    expect(screen.queryByText(/Nobody here is your/)).toBeNull();
  });
});

describe("company view — the verbs that change a section", () => {
  it("offers New deal on an account with no open deal", async () => {
    // The empty state is exactly where a create verb belongs: a rep who has
    // just read "no open deal on this account" is one click from opening one.
    stub(view());
    renderCompany();

    const card = await screen.findByRole("complementary", { name: "Business" });
    expect(
      within(card).getByText("No open deal on this account."),
    ).toBeTruthy();
    // Awaited: the verb appears once the pipeline read resolves, because a
    // deal needs somewhere to land before the page offers to open one.
    expect(
      await within(card).findByRole("button", { name: "New deal" }),
    ).toBeTruthy();
  });

  it("offers no New deal on a section the caller may not read", async () => {
    // A caller who cannot read the deals has no business being offered a
    // button to add one, and the refusal must not be the first they hear of it.
    const fetched = stub(
      view({ deals: undefined, sections_omitted: ["deals"] }),
    );
    renderCompany();

    const card = await screen.findByRole("complementary", { name: "Business" });
    expect(
      within(card).getByText("Hidden — your role cannot read this"),
    ).toBeTruthy();
    // The absent button alone would prove nothing: the verb also renders null
    // while its pipeline read is in flight, so the assertion could pass on
    // that transient state with the guard deleted. What pins the guard is
    // that the verb never MOUNTED — it is the only thing on this page that
    // reads /pipelines, so an unfetched /pipelines means the withheld section
    // never rendered it.
    await waitFor(() =>
      expect(fetched.some((path) => path.endsWith("/360"))).toBe(true),
    );
    expect(fetched.some((path) => path.endsWith("/pipelines"))).toBe(false);
    expect(within(card).queryByRole("button", { name: "New deal" })).toBeNull();
  });
});

it("offers the tag verb but not the list verb when only lists are withheld", async () => {
  // The two halves of the card are governed separately, so one withheld
  // grant must not take the other's verb with it — and must not offer a
  // write whose refusal would be the first the reader hears of the limit.
  stub(
    view({
      list_memberships: undefined,
      sections_omitted: ["list_memberships"],
    }),
  );
  renderCompany();

  const card = await screen.findByRole("complementary", { name: "Business" });
  expect(
    await within(card).findByRole("button", { name: "Add tag" }),
  ).toBeTruthy();
  expect(
    within(card).queryByRole("button", { name: "Add to list" }),
  ).toBeNull();
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

describe("company view — the context column belongs to the account, not to a tab", () => {
  it("keeps the context column mounted when the reader switches tab", async () => {
    stub(view(), 200, partnerOrg);
    renderCompany();

    await screen.findByRole("complementary", { name: "Business" });

    await userEvent.click(screen.getByRole("button", { name: "Partner" }));

    // Partner and History used to render in a header-only frame, so the side
    // column unmounted, the grid re-columned under the reader, and every query
    // behind it refetched on the way back.
    expect(
      screen.getByRole("complementary", { name: "Business" }),
    ).toBeTruthy();
    // There is no second landmark to check any more: the profile, documents,
    // facts and tools disclosures live INSIDE the Business column (plan §4 —
    // one context column, not two), so they ride on its mount.
    expect(screen.queryByRole("complementary", { name: "Profile" })).toBeNull();
  });

  it("does not refetch the account when the reader switches tab and back", async () => {
    const fetched = stub(view(), 200, partnerOrg);
    renderCompany();
    await screen.findByRole("complementary", { name: "Business" });
    const before = fetched.filter((path) => path.endsWith("/360")).length;

    await userEvent.click(screen.getByRole("button", { name: "Partner" }));
    await userEvent.click(screen.getByRole("button", { name: "Overview" }));

    expect(fetched.filter((path) => path.endsWith("/360")).length).toBe(before);
  });

  it("leaves the timeline to its own tab rather than repeating it under a form", async () => {
    stub(view(), 200, partnerOrg);
    renderCompany();
    await screen.findByRole("complementary", { name: "Business" });

    // The chronology moved off the overview when the page gained its own
    // History tab, so it is not under the partner form either.
    await userEvent.click(screen.getByRole("button", { name: "History" }));
    expect(screen.getByRole("region", { name: "Timeline" })).toBeTruthy();

    await userEvent.click(screen.getByRole("button", { name: "Partner" }));
    expect(screen.queryByRole("region", { name: "Timeline" })).toBeNull();
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
      expect(
        screen.getByText("3 new items since your last visit."),
      ).toBeTruthy(),
    );
    // The decision count has ONE display, the header chip, which counts the
    // approvals section. This block used to render its own count off
    // since_last_visit.pending_proposals, and the two disagreed on screen.
    // deal_stage_moves came back null: not counted, so the brief says nothing
    // about it rather than reporting that no deal moved.
    expect(screen.queryByText(/moved stage/)).toBeNull();
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

describe("company view — one section never answers for another", () => {
  it("does not let readable tags claim there are no lists", async () => {
    // Tags came back empty; lists were withheld. "Not on any list, and no
    // tags applied" would be a claim about a half nobody answered for.
    stub(
      view({
        tags: [],
        list_memberships: undefined,
        sections_omitted: ["list_memberships"],
      }),
    );
    renderCompany();

    const rail = await screen.findByRole("complementary", { name: "Business" });
    // The refusal has to name WHICH half it is about: under a heading
    // covering both, an unattached "hidden from you" leaves the reader
    // unable to tell whether the lists or the tags were withheld.
    const listsPart = within(rail).getByRole("region", { name: "Lists" });
    expect(
      within(listsPart).getByText("Hidden — your role cannot read this"),
    ).toBeTruthy();

    const tagsPart = within(rail).getByRole("region", { name: "Tags" });
    expect(within(tagsPart).getByText("No tags applied.")).toBeTruthy();
    expect(
      within(tagsPart).queryByText("Hidden — your role cannot read this"),
    ).toBeNull();
    expect(within(rail).queryByText("Not on any list.")).toBeNull();
  });

  it("still shows the tags a caller can read when lists are withheld", async () => {
    stub(
      view({
        tags: [{ id: "t-1", workspace_id: "w", name: "Key account" }],
        list_memberships: undefined,
        sections_omitted: ["list_memberships"],
      }),
    );
    renderCompany();

    // Losing one grant narrows the card; it does not blank it.
    await waitFor(() => expect(screen.getByText("Key account")).toBeTruthy());
  });
});

describe("company view — figures that outlive the list they sit under", () => {
  it("keeps the lifetime won total on an account with no open deal", async () => {
    stub(
      view({
        deals: {
          data: [],
          page: emptyPage,
          won_lifetime: { amount_minor: 12_000_000, currency: "EUR" },
          lost_count: 3,
        },
      }),
    );
    renderCompany();

    const rail = await screen.findByRole("complementary", { name: "Business" });
    // No OPEN deal is true and is said. The account still won €120,000 —
    // hiding that because today's pipeline is empty loses a real fact.
    expect(
      within(rail).getByText("No open deal on this account."),
    ).toBeTruthy();
    expect(within(rail).getByText(/120,000/)).toBeTruthy();
    expect(within(rail).getByText("3 lost")).toBeTruthy();
  });
});

// The company page's own affordances: what it says is waiting, and what a
// reader can do about it without leaving the account.

describe("company view — the citations under a finding", () => {
  // The chips are shared by every grounded surface on this page. They are
  // exercised through the advice the brief carries, which is where a reader
  // meets them now that the standing summary card is gone.
  const suggestion = (evidence: unknown[]) => ({
    kind: "no_reply" as const,
    fingerprint: "f-1",
    reason: "You reached out 13 days ago and nobody has come back.",
    evidence,
  });

  it("collapses several sources of one unopenable kind into one counted chip", async () => {
    stub(
      view({
        suggestions: [
          suggestion([
            { entity_type: "activity", entity_id: "a-1" },
            { entity_type: "activity", entity_id: "a-2" },
            { entity_type: "activity", entity_id: "a-3" },
          ]),
        ],
      }),
    );
    renderCompany();
    // Not "activityactivityactivity": one chip that says how many.
    await waitFor(() => expect(screen.getByText("3 activities")).toBeTruthy());
    expect(screen.queryAllByText("activity")).toHaveLength(0);
  });

  it("counts one record cited twice as one source", async () => {
    stub(
      view({
        suggestions: [
          suggestion([
            { entity_type: "activity", entity_id: "a-1" },
            { entity_type: "activity", entity_id: "a-1" },
          ]),
        ],
      }),
    );
    renderCompany();
    await waitFor(() => expect(screen.getByText("activity")).toBeTruthy());
    expect(screen.queryByText("2 activities")).toBeNull();
  });
});

describe("company view — what is waiting on a decision", () => {
  const staged = {
    id: "ap-1",
    workspace_id: "w",
    kind: "site_lead",
    status: "pending",
    summary: "Add Markus Bueckle as a contact",
    proposed_change: { full_name: "Markus Bueckle" },
    proposed_by: "agent:capture",
    target_entity_type: "organization",
    target_entity_id: "o-1",
    diff_hash: "h1",
    created_at: "2026-06-01T08:00:00Z",
    evidence: [],
  };

  it("offers a way into the queue it counts, grouped by what is proposed", async () => {
    stub(
      view({
        pending_approvals: {
          data: [staged, { ...staged, id: "ap-2" }],
          page: emptyPage,
        },
        since_last_visit: {
          baseline_at: "2026-05-30T09:00:00Z",
          new_activities: 0,
          deal_stage_moves: 0,
          pending_proposals: 2,
        },
      }),
    );
    renderCompany();
    const open = await screen.findByRole("button", {
      name: "Review 2 waiting",
    });
    open.click();
    await waitFor(() =>
      expect(
        screen.getByText("2 × Add a person found on the site"),
      ).toBeTruthy(),
    );
  });

  it("says nothing is waiting rather than offering an empty queue", async () => {
    stub(view());
    renderCompany();
    await screen.findByText("Brandt Automotive GmbH");
    expect(screen.queryByRole("button", { name: /Review/ })).toBeNull();
  });
});

describe("company view — an open task can be acted on", () => {
  const step = {
    activity_id: "t-1",
    subject: "Send the retrofit proposal",
    due_at: "2026-06-10T09:00:00Z",
    overdue: false,
    linked_deal_id: null,
    linked_person_id: null,
    assignee_id: null,
  };

  it("renders the subject as a way to open the task, with the two verbs beside it", async () => {
    stub(view({ next_steps: { data: [step], page: emptyPage } }));
    renderCompany();
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Send the retrofit proposal" }),
      ).toBeTruthy(),
    );
    expect(screen.getByRole("button", { name: "Done" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Snooze 1d" })).toBeTruthy();
  });

  it("offers no snooze for a task with no date to move", async () => {
    stub(
      view({
        next_steps: { data: [{ ...step, due_at: null }], page: emptyPage },
      }),
    );
    renderCompany();
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Done" })).toBeTruthy(),
    );
    expect(screen.queryByRole("button", { name: "Snooze 1d" })).toBeNull();
  });
});

// The page said "nobody here is your champion" and gave the reader nowhere to
// say who is: the roles live on relationship rows written from the deal
// screen. The warning was true, unactionable and permanent.
describe("company view — naming the buying committee", () => {
  it("offers to record a role on the contact, against an open deal", async () => {
    stub(
      view({
        deals: {
          data: [
            { deal_id: "d-1", name: "Pilot", status: "open", stalled: false },
          ],
          page: { has_more: false, next_cursor: null },
          won_lifetime: { amount_minor: 0, currency: "EUR" },
          lost_count: 0,
        },
        people: {
          data: [
            {
              person_id: "p-1",
              full_name: "Christian Hagemeyer",
              deal_roles: [],
              consent: {},
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

    const set = await screen.findByRole("button", { name: "Set role" });
    await userEvent.click(set);

    // The two words are defined where they are being chosen, once.
    expect(
      screen.getByText(/argues for you when you are not in the room/),
    ).toBeTruthy();
    expect(
      screen.getByRole("dialog", { name: /What is Christian Hagemeyer/ }),
    ).toBeTruthy();
  });

  // A role belongs to a DEAL. With nothing open there is nothing to be a
  // champion of, and offering the verb would invite a write that has no
  // subject.
  it("offers nothing when the account has no open deal", async () => {
    stub(
      view({
        deals: {
          data: [],
          page: { has_more: false, next_cursor: null },
          won_lifetime: { amount_minor: 0, currency: "EUR" },
          lost_count: 0,
        },
        people: {
          data: [
            {
              person_id: "p-1",
              full_name: "Christian Hagemeyer",
              deal_roles: [],
              consent: {},
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
          page: { has_more: false, next_cursor: null },
        },
      }),
    );
    renderCompany();

    await screen.findByRole("button", { name: "Christian Hagemeyer" });
    expect(screen.queryByRole("button", { name: "Set role" })).toBeNull();
  });
});

// A role belongs to a deal, so the same person can be champion on one and
// nobody on another. Rendering the role alone made two badges that read
// identically — and React saw one key twice.
describe("company view — a buying role names the deal it is on", () => {
  const contactOnTwoDeals = {
    person_id: "p-1",
    full_name: "Dana Buyer",
    deal_roles: [
      { deal_id: "d-1", role: "champion" },
      { deal_id: "d-2", role: "champion" },
    ],
    consent: { marketing_email: "granted" },
    strength: {
      score: 62,
      bucket: "strong",
      factors: { recency: 1, frequency: 1, reciprocity: 1, direction: 1 },
    },
  };
  const twoOpenDeals = {
    data: [
      { deal_id: "d-1", name: "Renewal", status: "open", stalled: false },
      { deal_id: "d-2", name: "New business", status: "open", stalled: false },
    ],
    page: emptyPage,
    won_lifetime: { amount_minor: 0, currency: "EUR" },
    lost_count: 0,
  };

  it("names each deal when the person holds the same role on two of them", async () => {
    stub(
      view({
        people: { data: [contactOnTwoDeals], page: emptyPage },
        deals: twoOpenDeals,
      }),
    );
    renderCompany();

    await waitFor(() => expect(screen.getByText("Dana Buyer")).toBeTruthy());
    expect(screen.getByText("champion · Renewal")).toBeTruthy();
    expect(screen.getByText("champion · New business")).toBeTruthy();
  });

  it("leaves the deal name off when there is only one deal to be on", async () => {
    stub(
      view({
        people: {
          data: [
            {
              ...contactOnTwoDeals,
              deal_roles: [{ deal_id: "d-1", role: "champion" }],
            },
          ],
          page: emptyPage,
        },
        deals: {
          ...twoOpenDeals,
          data: [twoOpenDeals.data[0]],
        },
      }),
    );
    renderCompany();

    await waitFor(() => expect(screen.getByText("Dana Buyer")).toBeTruthy());
    expect(screen.getByText("champion")).toBeTruthy();
  });
});

describe("company view — a recorded role reaches the screen", () => {
  const contact = {
    person_id: "p-1",
    full_name: "Christian Hagemeyer",
    deal_roles: [],
    consent: {},
    strength: {
      score: 0,
      bucket: "dormant",
      factors: { recency: 0, frequency: 0, reciprocity: 0, direction: 0 },
    },
  };
  const withOneOpenDeal = view({
    deals: {
      data: [{ deal_id: "d-1", name: "Pilot", status: "open", stalled: false }],
      page: emptyPage,
      won_lifetime: { amount_minor: 0, currency: "EUR" },
      lost_count: 0,
    },
    people: { data: [contact], page: emptyPage },
  });

  it("re-reads the account after the role is saved", async () => {
    // The committee reading, the missing-role warning and the row's own chips
    // all come off the 360, so a save that does not re-read it leaves the page
    // showing the state the rep just changed.
    const fetched = stub(withOneOpenDeal);
    renderCompany();

    await userEvent.click(
      await screen.findByRole("button", { name: "Set role" }),
    );
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() =>
      expect(
        fetched.filter((path) => path.endsWith("/360")).length,
      ).toBeGreaterThan(1),
    );
  });

  it("offers no role control on an account that takes no writes", async () => {
    // Archived is read-only: the company page hides edit, merge and archive on
    // one, so a write dressed as a chip has no business staying. The page
    // passes its own read-only state down; the card is asserted directly
    // because that state comes from the record, not from the 360.
    render(<PeopleCard view={withOneOpenDeal} writable={false} />);

    await screen.findByRole("button", { name: "Christian Hagemeyer" });
    expect(screen.queryByRole("button", { name: "Set role" })).toBeNull();
  });

  it("offers it on an account that does", async () => {
    render(<PeopleCard view={withOneOpenDeal} writable />);

    expect(
      await screen.findByRole("button", { name: "Set role" }),
    ).toBeTruthy();
  });
});

it("does not offer a role the contact already holds on that deal", async () => {
  // The write creates an edge, so picking a held role asks the server for a
  // second copy of a fact it already has.
  stub(
    view({
      deals: {
        data: [
          { deal_id: "d-1", name: "Pilot", status: "open", stalled: false },
        ],
        page: emptyPage,
        won_lifetime: { amount_minor: 0, currency: "EUR" },
        lost_count: 0,
      },
      people: {
        data: [
          {
            person_id: "p-1",
            full_name: "Christian Hagemeyer",
            deal_roles: [{ deal_id: "d-1", role: "champion" }],
            consent: {},
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

  await userEvent.click(
    await screen.findByRole("button", { name: "Set role" }),
  );
  // The offered roles only exist while the control is open, so the list is opened
  // to be read. Asserted as the WHOLE set rather than as the absence of one word:
  // an absence check passes for the wrong reason the moment a label is recased or
  // reworded, and what this case is about is that a role this contact already
  // holds is not offered a second time.
  await userEvent.click(screen.getByLabelText("Role"));
  expect(
    within(screen.getByRole("listbox"))
      .getAllByRole("option")
      .map((option) => option.textContent),
  ).toEqual(["economic buyer", "influencer", "blocker", "end user"]);
});

describe("company view — Partner is not a permanent tab", () => {
  it("offers the account's own tabs but not Partner on an account with no programme", async () => {
    stub(view());
    renderCompany();
    await screen.findByRole("complementary", { name: "Business" });

    // Overview, People and History belong to every account. Partner is a form
    // about a commercial arrangement almost none of them have.
    expect(screen.getByRole("button", { name: "Overview" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "People" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "History" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Partner" })).toBeNull();
  });

  it("shows both tabs once the account has a programme", async () => {
    stub(view(), 200, partnerOrg);
    renderCompany();
    await screen.findByRole("complementary", { name: "Business" });

    expect(screen.getByRole("button", { name: "Partner" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Overview" })).toBeTruthy();
  });

  it("keeps the setup form reachable, so a first partner row can still be made", async () => {
    stub(view());
    renderCompany();
    await screen.findByRole("complementary", { name: "Business" });

    await userEvent.click(screen.getByRole("button", { name: "More actions" }));
    await userEvent.click(
      screen.getByRole("button", { name: "Set up partner programme" }),
    );

    // Asking for the form is what puts the tab on screen — without this the
    // hidden tab would have made the first partner row unreachable.
    expect(screen.getByRole("button", { name: "Partner" })).toBeTruthy();
  });
});

describe("company view — the visit baseline", () => {
  it("acknowledges the visit after the reader has stayed", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const fetched = stub(view());
      renderCompany();
      await screen.findByRole("complementary", { name: "Business" });
      expect(fetched.some((path) => path.endsWith("/view-ack"))).toBe(false);

      await vi.advanceTimersByTimeAsync(5_000);

      await waitFor(() =>
        expect(fetched.some((path) => path.endsWith("/view-ack"))).toBe(true),
      );
    } finally {
      vi.useRealTimers();
    }
  });

  it("does not acknowledge a visit the reader bounced straight out of", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const fetched = stub(view());
      const { unmount } = render(<CompanyScreen id="o-1" />);
      await screen.findByRole("complementary", { name: "Business" });
      unmount();

      await vi.advanceTimersByTimeAsync(30_000);

      // Marking unread activity as seen because a record flashed past is the
      // one failure this baseline must never have.
      expect(fetched.some((path) => path.endsWith("/view-ack"))).toBe(false);
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("company view — where the account stands, and what it is to us", () => {
  it("shows where the account stands and every relationship type, as separate things", async () => {
    // Not "partner": that type also raises the Partner tab, and the badge and
    // the tab would then share a label, which tells the test nothing.
    stub(view(), 200, {
      ...org,
      lifecycle: "former_customer",
      relationship_types: ["customer", "supplier"],
    });
    renderCompany();
    await screen.findByRole("complementary", { name: "Business" });

    // The retired classification held ONE value, which is how an account whose
    // contract had ended still read as "Prospect" while it was also a partner.
    // Lifecycle is now the editable control; the types stay read-only badges.
    // findBy, not getBy: the control appears only once /me answers with the
    // viewer's grants, which resolves independently of the 360 awaited above.
    expect(
      await screen.findByRole("button", { name: "Change Account lifecycle" }),
    ).toBeTruthy();
    expect(screen.getByText("Former customer")).toBeTruthy();
    expect(screen.getByText("Customer")).toBeTruthy();
    expect(screen.getByText("Supplier")).toBeTruthy();
  });

  it("offers the lifecycle control on an account nobody has assessed yet", async () => {
    stub(view(), 200, { ...org, lifecycle: "unknown", relationship_types: [] });
    renderCompany();
    await screen.findByRole("complementary", { name: "Business" });

    // 'unknown' used to draw nothing, on the reasoning that a badge announcing
    // "nobody has assessed this" is noise. That holds for a badge and breaks
    // for a control: hiding it at 'unknown' takes the field away from exactly
    // the account that needs it set, and there is no other way in from here.
    // What it must NOT do is read as a verdict, which is why it carries the
    // field name and 'Not assessed' never stands on its own.
    const control = await screen.findByRole("button", {
      name: "Change Account lifecycle",
    });
    expect(control.textContent).toContain("Not assessed");
  });
});

// §4.2's "never render" list is the hard half of the KPI row, and each case
// below is one of its bullets. They are about what the page must NOT claim,
// which is exactly what a refactor loses silently.
describe("company view — the KPI row never invents a figure", () => {
  const commercial = (over: Record<string, unknown>) => ({
    account: { lifecycle: "prospect", relationship_types: [] },
    commercial: {
      open_count: 2,
      stalled_count: 0,
      priced_count: 0,
      converted_count: 0,
      ...over,
    },
  });

  it("shows no money at all when no open deal carries a convertible amount", async () => {
    stub(view({ state_strip: commercial({}) }));
    renderCompany();
    const strip = await screen.findByRole("region", {
      name: "Where this account stands",
    });

    // A zero here would claim a priced pipeline worth nothing. The truth is
    // that the page cannot price this one, so it reports the count instead.
    // No currency figure AT ALL — not merely no zero. A stray non-zero total
    // would be the worse failure, and the loose form would have passed it.
    expect(strip.textContent).not.toMatch(/[€$£]/);
    expect(within(strip).getByText("2 open")).toBeTruthy();
    expect(
      within(strip).getByText("No convertible amount on these deals"),
    ).toBeTruthy();
  });

  // Two cards saying "in conversation" in different words is one card's worth
  // of information taking two of the four slots. On a live account the health
  // card reports the BALANCE of the exchange, which the engagement card does
  // not answer: they write and we do not reply, and we write into silence,
  // are both recent and are opposite problems.
  it("reports who is carrying a live relationship, not that it is live", async () => {
    stub(
      view({
        health: { days_since_last_inbound: 0, reply_balance: 0.86 },
        state_strip: {
          account: { lifecycle: "customer", relationship_types: [] },
          engagement: {
            state: "active",
            last_inbound_at: "2026-08-08T10:00:00Z",
          },
          commercial: {
            open_count: 1,
            stalled_count: 0,
            priced_count: 1,
            converted_count: 0,
            open_pipeline_minor_base: 100000,
            base_currency: "EUR",
          },
        },
      }),
    );
    renderCompany();
    const strip = await screen.findByRole("region", {
      name: "Where this account stands",
    });

    // 86% of the exchange is theirs: they are asking more than we answer.
    expect(within(strip).getByText("One-sided")).toBeTruthy();
    expect(
      within(strip).getByText(/86% of the exchange is theirs/),
    ).toBeTruthy();
  });

  it("says an empty pipeline is empty, not unpriced", async () => {
    stub(view({ state_strip: commercial({ open_count: 0 }) }));
    renderCompany();
    const strip = await screen.findByRole("region", {
      name: "Where this account stands",
    });

    // "No convertible amount" on an account with nothing open reports a data
    // problem where the truth is that nothing is running.
    expect(within(strip).getByText("No open deals")).toBeTruthy();
    expect(strip.textContent).not.toContain("No convertible amount");
  });

  it("names the conversion behind a cross-currency total", async () => {
    stub(
      view({
        state_strip: commercial({
          open_pipeline_minor_base: 4500000,
          base_currency: "EUR",
          priced_count: 2,
          converted_count: 1,
          fx_as_of: "2026-02-14",
        }),
      }),
    );
    renderCompany();
    const strip = await screen.findByRole("region", {
      name: "Where this account stands",
    });

    // §4.2 bars a cross-currency sum with no conversion source and as-of date.
    // The date is the oldest rate behind the figure — how far back any part of
    // it reaches.
    // The DATE itself, not just the prefix: a dropped or wrong interpolation
    // is exactly the failure this qualification exists to prevent.
    expect(
      within(strip).getByText(/1 converted, rates from .*2026/),
    ).toBeTruthy();
  });

  it("keeps saying the pipeline is unpriced even when a deal has stalled", async () => {
    stub(view({ state_strip: commercial({ stalled_count: 1 }) }));
    renderCompany();
    const strip = await screen.findByRole("region", {
      name: "Where this account stands",
    });

    // A reader told only "1 stalled" has no way to know the pipeline carries
    // no figure at all. Both qualifications are true, so both are shown.
    expect(strip.textContent).toContain("No convertible amount on these deals");
    expect(strip.textContent).toContain("1 stalled");
  });

  it("says how much of the pipeline a partial total covers", async () => {
    stub(
      view({
        state_strip: commercial({
          open_pipeline_minor_base: 4500000,
          base_currency: "EUR",
          priced_count: 1,
        }),
      }),
    );
    renderCompany();
    const strip = await screen.findByRole("region", {
      name: "Where this account stands",
    });

    // A sum covering one of two deals, shown bare, reads as the whole
    // pipeline — the unlabelled cross-currency total §4.2 forbids.
    expect(within(strip).getByText("1 of 2 deals priced")).toBeTruthy();
  });

  it("labels the sum of open deals Open pipeline, never revenue or potential", async () => {
    stub(
      view({
        state_strip: commercial({
          open_pipeline_minor_base: 4500000,
          base_currency: "EUR",
          priced_count: 2,
        }),
      }),
    );
    renderCompany();
    const strip = await screen.findByRole("region", {
      name: "Where this account stands",
    });

    expect(within(strip).getByText("Open pipeline")).toBeTruthy();
    expect(strip.textContent).not.toMatch(/revenue|potential/i);
  });

  // §4.2 gives customers and prospects different questions. A customer's page
  // is asked how the relationship stands; a prospect's is asked when the deal
  // lands. Showing one set to both makes half the row noise.
  it("asks a prospect when the deal closes, and a customer how it is going", async () => {
    stub(
      view({
        state_strip: {
          account: { lifecycle: "prospect", relationship_types: [] },
          commercial: {
            open_count: 1,
            stalled_count: 0,
            priced_count: 1,
            converted_count: 0,
            open_pipeline_minor_base: 100000,
            base_currency: "EUR",
            next_close_on: "2026-09-30",
          },
        },
      }),
    );
    renderCompany();
    let strip = await screen.findByRole("region", {
      name: "Where this account stands",
    });
    expect(within(strip).getByText("Expected close")).toBeTruthy();
    expect(within(strip).queryByText("Relationship")).toBeNull();

    cleanup();
    stub(
      view({
        health: { days_since_last_inbound: 90 },
        state_strip: {
          account: { lifecycle: "customer", relationship_types: [] },
          commercial: {
            open_count: 1,
            stalled_count: 0,
            priced_count: 1,
            converted_count: 0,
            open_pipeline_minor_base: 100000,
            base_currency: "EUR",
            next_close_on: "2026-09-30",
          },
        },
      }),
    );
    renderCompany();
    strip = await screen.findByRole("region", {
      name: "Where this account stands",
    });
    expect(within(strip).getByText("Relationship")).toBeTruthy();
    expect(within(strip).getByText("Gone quiet")).toBeTruthy();
    expect(within(strip).queryByText("Expected close")).toBeNull();
  });
});

describe("company view — the state strip", () => {
  it("leads with where the account stands, whose move it is, and what is open", async () => {
    stub(
      view({
        state_strip: {
          account: {
            lifecycle: "former_customer",
            relationship_types: ["partner"],
          },
          engagement: {
            state: "waiting_on_them",
            last_inbound_at: "2026-04-30T09:00:00Z",
            last_outbound_at: "2026-07-17T09:00:00Z",
          },
          // The full wire shape, so this case fails if the contract moves
          // under it rather than being silently accepted by a loose stub.
          commercial: {
            open_count: 2,
            stalled_count: 1,
            priced_count: 0,
            converted_count: 0,
          },
        },
      }),
    );
    renderCompany();
    await screen.findByRole("region", { name: "Where this account stands" });
    const strip = screen.getByRole("region", {
      name: "Where this account stands",
    });
    expect(within(strip).getByText("Former customer")).toBeTruthy();
    expect(within(strip).getByText("Waiting on them")).toBeTruthy();
    expect(within(strip).getByText("2 open")).toBeTruthy();
    expect(strip.textContent).toContain("1 stalled");
  });

  it("states the worst thing standing open, in the words its producer wrote", async () => {
    stub(
      view({
        state_strip: {
          account: { lifecycle: "customer", relationship_types: [] },
          engagement: null,
          commercial: null,
          signal: {
            kind: "contract_ended",
            severity: "warn",
            summary: "They wrote that the contract ends on 31 July.",
          },
        },
      }),
    );
    renderCompany();
    const strip = await screen.findByRole("region", {
      name: "Where this account stands",
    });
    expect(within(strip).getByText("Contract ending")).toBeTruthy();
    // The producer's sentence, not a rephrasing of it: the strip states what
    // the conversation said, and a reader checks it against the mail itself.
    expect(
      within(strip).getByText("They wrote that the contract ends on 31 July."),
    ).toBeTruthy();
  });

  it("says nothing about signals when nothing is open", async () => {
    stub(
      view({
        state_strip: {
          account: { lifecycle: "customer", relationship_types: [] },
          engagement: null,
          commercial: null,
          signal: null,
        },
      }),
    );
    renderCompany();
    const strip = await screen.findByRole("region", {
      name: "Where this account stands",
    });
    // Null covers both "nothing is open" and "you may not read signals", so
    // the tile is ABSENT rather than reassuring. A strip that told someone who
    // cannot look that nothing is wrong would be answering a question it has
    // no standing to answer.
    expect(within(strip).queryByText("Worth knowing")).toBeNull();
  });

  it("draws no engagement reading when the caller may not read the mail", async () => {
    stub(
      view({
        state_strip: {
          account: { lifecycle: "customer", relationship_types: [] },
          engagement: null,
          commercial: null,
        },
      }),
    );
    renderCompany();
    const strip = await screen.findByRole("region", {
      name: "Where this account stands",
    });

    // Scoped to the strip: the header has its own last-touch line, and an
    // unscoped query would pass on that instead of on what the strip drew.
    //
    // Inventing "never contacted" from data the caller was not allowed to see
    // states a conclusion the page has no basis for — and it is the one a rep
    // would act on.
    expect(within(strip).queryByText("Whose move")).toBeNull();
    expect(within(strip).queryByText("Never contacted")).toBeNull();
    expect(within(strip).queryByText("Open work")).toBeNull();
    expect(within(strip).getByText("Customer")).toBeTruthy();
  });
});

describe("company view — advice you can act on", () => {
  it("offers the action the server named, and none where it named none", async () => {
    stub(
      view({
        suggestions: [
          {
            kind: "no_reply",
            fingerprint: "f1",
            reason: "You reached out 15 days ago and nobody has come back.",
            evidence: [],
            action: { kind: "draft_reply", activity_id: "a-1" },
          },
          {
            kind: "no_next_step",
            fingerprint: "f2",
            reason: "2 open deal(s) here and no task saying what happens next.",
            evidence: [],
            action: null,
          },
        ],
        suggestions_dropped: 0,
      }),
    );
    renderCompany();
    await screen.findByText(/nobody has come back/);

    expect(screen.getByRole("button", { name: "Draft a reply" })).toBeTruthy();
    // The second rule named no action, so it advises without a control. A
    // button that does nothing teaches the reader to stop pressing them.
    expect(
      screen.queryByRole("button", { name: "Add the next step" }),
    ).toBeNull();
  });
});

describe("company view — the account's own tabs", () => {
  it("gives People the whole middle column", async () => {
    stub(
      view({
        people: {
          data: [
            {
              person_id: "p-1",
              full_name: "Christian Hagemeyer",
              title: "Managing director",
              strength: { score: 0, bucket: "dormant" },
              deal_roles: [],
              consent: [],
            },
          ],
          page: emptyPage,
        },
      }),
    );
    renderCompany();
    await screen.findByRole("complementary", { name: "Business" });

    await userEvent.click(screen.getByRole("button", { name: "People" }));
    // The rail's card is a summary; the tab is the roster. Both read the same
    // section of the one composite read, so they cannot disagree.
    expect(screen.getAllByText("Christian Hagemeyer").length).toBeGreaterThan(
      1,
    );
  });

  it("keeps Ask on the overview rather than following the history", async () => {
    stub(view());
    renderCompany();
    await screen.findByRole("complementary", { name: "Business" });

    // Asking is a tool for when the page did not answer the question. It
    // belongs to the account, not to its chronology.
    expect(screen.getByText("Ask Margince")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "History" }));
    expect(screen.queryByText("Ask Margince")).toBeNull();
  });
});

// The connections card asked nobody and answered everybody: a staff directory
// in the rail of every account, costing a graph read on every page load. The
// route-in asks the question a rep actually has, about one person, and only
// when they ask it.
describe("company view — the way in to one contact", () => {
  const withContact = () =>
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
    });

  it("reads no graph until someone asks for a way in", async () => {
    const fetched = stub(withContact());
    renderCompany();
    await screen.findByRole("complementary", { name: "Business" });
    expect(fetched.filter((path) => path.endsWith("/graph"))).toEqual([]);
  });

  it("names who here already talks to that person", async () => {
    const fetched = stub(withContact());
    renderCompany();
    await screen.findByRole("complementary", { name: "Business" });
    await userEvent.click(
      screen.getAllByRole("button", { name: "Route in" })[0],
    );

    await screen.findByText("Mira");
    expect(screen.getByText("in regular contact")).toBeTruthy();
    expect(fetched.filter((path) => path.endsWith("/graph"))).toHaveLength(1);
  });
});

describe("company view — the account's primary actions", () => {
  it("offers logging what happened and setting what happens next, as separate verbs", async () => {
    stub(view());
    renderCompany();
    await screen.findByRole("complementary", { name: "Business" });

    // One button reading "Log activity", with the task hidden behind a type
    // picker inside it, is why accounts collect notes and no follow-ups. The
    // two verbs answer different questions and are asked separately.
    expect(
      await screen.findByRole("button", { name: "Log activity" }),
    ).toBeTruthy();
    expect(
      await screen.findByRole("button", { name: "Add task" }),
    ).toBeTruthy();
  });

  it("offers neither on an archived company", async () => {
    stub(view(), 200, { ...org, archived_at: "2026-07-01T09:00:00Z" });
    renderCompany();
    await screen.findByRole("complementary", { name: "Business" });

    // The server refuses a write against a retired record, so the button would
    // only open a form that fails on save.
    expect(screen.queryByRole("button", { name: "Log activity" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Add task" })).toBeNull();
  });
});

// The KPI row's money slot has six reasons it can hold no figure, and they do
// not share a fix. Telling a reader to connect an accounting system they have
// already connected sends them to a settings page to change nothing.
describe("the money slot says WHY it has no figure", () => {
  const customer = {
    account: { lifecycle: "customer" as const, relationship_types: [] },
  };
  const strip = async () =>
    await screen.findByRole("region", { name: "Where this account stands" });

  it("names the setup step only when there is no source", async () => {
    stub(view({ state_strip: customer }), 200, org, {
      organization_id: "o-1",
      state: "no_connection",
    });
    renderCompany();
    await strip();
    expect(await screen.findByText("Connect your accounting")).toBeTruthy();
  });

  it("says the source is not matched rather than not connected", async () => {
    stub(view({ state_strip: customer }), 200, org, {
      organization_id: "o-1",
      state: "unmapped",
      provider: "offline_demo",
    });
    renderCompany();
    const region = await strip();
    expect(
      await screen.findByText("Not matched to a customer yet"),
    ).toBeTruthy();
    // The wrong advice, specifically: this reader HAS connected a source.
    expect(region.textContent).not.toMatch(/Connect your accounting/);
  });

  it("says a first sync is running rather than that nothing is connected", async () => {
    stub(view({ state_strip: customer }), 200, org, {
      organization_id: "o-1",
      state: "syncing",
      provider: "offline_demo",
    });
    renderCompany();
    const region = await strip();
    expect(await screen.findByText("Syncing…")).toBeTruthy();
    expect(region.textContent).not.toMatch(/Connect your accounting/);
  });

  // A stale figure is SHOWN with its caveat. The last known number is usually
  // the right one, and withholding it tells the reader less than showing it.
  it("keeps a stale figure on screen and marks it stale", async () => {
    stub(view({ state_strip: customer }), 200, org, {
      organization_id: "o-1",
      state: "stale",
      provider: "offline_demo",
      net_invoiced: { amount_minor: 18642000, currency: "EUR" },
    });
    renderCompany();
    await strip();
    expect(await screen.findByText(/186,420/)).toBeTruthy();
    expect(await screen.findByText(/Last sync failed/)).toBeTruthy();
  });

  it("names the source beside a real figure", async () => {
    stub(view({ state_strip: customer }), 200, org, {
      organization_id: "o-1",
      state: "connected",
      provider: "datev",
      net_invoiced: { amount_minor: 18642000, currency: "EUR" },
    });
    renderCompany();
    await strip();
    expect(await screen.findByText(/186,420/)).toBeTruthy();
    expect(await screen.findByText("datev")).toBeTruthy();
  });
});
