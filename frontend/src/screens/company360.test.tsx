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
// second tab. The bare fixture deliberately does not: a Partner tab on an
// account with no programme is the thing the tab gate removed.
const partnerOrg = {
  ...org,
  partner: {
    id: "pt-1",
    workspace_id: "w",
    organization_id: "o-1",
    relationship_stage: "active",
    source: "manual",
    captured_by: "human:u1",
    version: 1,
    created_at: "2026-06-01T08:00:00Z",
    updated_at: "2026-06-01T08:00:00Z",
  },
};

function stub(three60: unknown, status = 200, account: unknown = org) {
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
      if (pathname.endsWith("/hierarchy-rollup")) {
        return jsonResponse(emptyRollup);
      }
      if (pathname.endsWith("/brief")) {
        return jsonResponse(briefBody);
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

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
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

describe("company view — the rails belong to the account, not to a tab", () => {
  it("keeps both side columns mounted when the reader switches tab", async () => {
    stub(view(), 200, partnerOrg);
    renderCompany();

    await screen.findByRole("complementary", { name: "Business" });
    expect(screen.getByRole("complementary", { name: "Profile" })).toBeTruthy();

    await userEvent.click(screen.getByRole("button", { name: "Partner" }));

    // Partner and History used to render in a header-only frame, so both
    // rails unmounted, the grid re-columned under the reader, and every query
    // behind them refetched on the way back.
    expect(
      screen.getByRole("complementary", { name: "Business" }),
    ).toBeTruthy();
    expect(screen.getByRole("complementary", { name: "Profile" })).toBeTruthy();
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

  it("leaves the timeline to the overview rather than repeating it under a form", async () => {
    stub(view(), 200, partnerOrg);
    renderCompany();
    await screen.findByRole("region", { name: "Timeline" });

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
  const roles = screen.getByLabelText("Role") as HTMLSelectElement;
  expect([...roles.options].map((option) => option.value)).not.toContain(
    "champion",
  );
});

describe("company view — Partner is not a permanent tab", () => {
  it("shows no tab strip at all on an account with no partner programme", async () => {
    stub(view());
    renderCompany();
    await screen.findByRole("complementary", { name: "Business" });

    // One tab is not a choice: the strip goes entirely rather than offering
    // the reader the page they are already on.
    expect(screen.queryByRole("button", { name: "Partner" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Overview" })).toBeNull();
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
  it("shows the lifecycle and every relationship type as separate badges", async () => {
    stub(view(), 200, {
      ...org,
      lifecycle: "former_customer",
      relationship_types: ["partner", "supplier"],
    });
    renderCompany();
    await screen.findByRole("complementary", { name: "Business" });

    // The retired classification held ONE value, which is how an account whose
    // contract had ended still read as "Prospect" while it was also a partner.
    expect(screen.getByText("Former customer")).toBeTruthy();
    expect(screen.getByText("Partner")).toBeTruthy();
    expect(screen.getByText("Supplier")).toBeTruthy();
  });

  it("draws no badge for an account nobody has assessed", async () => {
    stub(view(), 200, { ...org, lifecycle: "unknown", relationship_types: [] });
    renderCompany();
    await screen.findByRole("complementary", { name: "Business" });

    // 'unknown' is the honest default, and a badge announcing it on every new
    // record is noise — the old column defaulted to 'prospect' and rendered
    // that default as though someone had judged it.
    expect(screen.queryByText("Not assessed")).toBeNull();
  });
});
