/** @vitest-environment jsdom */
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { TodayOnThisAccount } from "./companytoday";

// The section earns its place by carrying what nothing else on the page says,
// and by never stating a claim it cannot support. Both are testable.

afterEach(cleanup);

type Organization360 = components["schemas"]["Organization360"];

// A COMPLETE Organization360, not a cast one. A fixture asserted into the
// contract type can drop a required field or carry an invalid value and still
// compile, so the test would go on passing after the wire shape moved under it.
const BASE: Organization360 = {
  as_of: "2026-08-07T09:00:00Z",
  organization: {
    id: "o-1",
    workspace_id: "w-1",
    display_name: "Acme",
    source: "manual",
    captured_by: "human:test",
    created_at: "2026-08-01T09:00:00Z",
    updated_at: "2026-08-01T09:00:00Z",
  },
  sections_omitted: [],
};

function show(
  view?: Organization360,
  opts: { loading?: boolean; failed?: boolean } = {},
) {
  render(
    <LocaleProvider initial="en">
      <TodayOnThisAccount
        view={view}
        loading={opts.loading ?? false}
        failed={opts.failed ?? false}
      />
    </LocaleProvider>,
  );
}

describe("what needs a person on this account today", () => {
  it("names the meeting, when it is, and who is in it", () => {
    show({
      ...BASE,
      next_meeting: {
        activity_id: "a-1",
        starts_at: "2026-08-12T09:00:00Z",
        subject: "Renewal review",
        participants: [{ person_id: "p-1", display_name: "Dana Buyer" }],
      },
    });

    expect(screen.getByText(/Renewal review/)).toBeTruthy();
    expect(screen.getByText(/Dana Buyer/)).toBeTruthy();
    // A meeting is checkable, so it is labelled a fact rather than advice.
    expect(screen.getByText("Fact")).toBeTruthy();
  });

  it("says nothing about a meeting when none is booked", () => {
    // Absent AND not named in sections_omitted means "none scheduled". Writing
    // a line about it would be missing data dressed as a recommendation — only
    // the suggestion engine can name WHOM to contact, so only it may advise
    // booking one.
    show(BASE);
    expect(screen.getByText("Nothing here needs you today.")).toBeTruthy();
    expect(screen.queryByText(/Hidden from you/)).toBeNull();
  });

  it("says the calendar is hidden when the reader has no activity grant", () => {
    // The same ABSENT field, opposite meaning. Without sections_omitted a
    // client would tell someone with no calendar access to book a meeting that
    // already exists.
    show({
      ...BASE,
      sections_omitted: ["next_meeting"],
    });
    expect(screen.getByText(/Hidden from you/).textContent).toContain(
      "the calendar",
    );
  });

  it("says a source is hidden from the reader rather than composing a shorter list silently", () => {
    show({
      ...BASE,
      sections_omitted: ["next_meeting", "next_steps"],
    });

    // "Hidden from you", never "None": a list assembled from three of five
    // sources is not the same list, and only the reader can judge whether the
    // missing one mattered.
    const withheld = screen.getByText(/Hidden from you/);
    expect(withheld.textContent).toContain("the calendar");
    expect(withheld.textContent).toContain("open tasks");
  });

  it("distinguishes a failed read from a quiet account", () => {
    show(undefined, { failed: true });
    // "We could not assemble this" and "nothing needs you" are different
    // sentences, and only one of them is about the account.
    expect(screen.getByText(/could not be assembled/)).toBeTruthy();
    expect(screen.queryByText("Nothing here needs you today.")).toBeNull();
  });

  it("counts what changed since the reader was last here", () => {
    show({
      ...BASE,
      since_last_visit: {
        new_activities: 3,
        baseline_at: "2026-08-01T09:00:00Z",
      },
    });
    expect(screen.getByText(/3 new on the timeline/)).toBeTruthy();
  });

  it("reports the failure even when a view is in hand", () => {
    // show(undefined, {failed:true}) passes on the missing view alone, so it
    // cannot tell `if (failed || !view)` from `if (!view)`. This one can: the
    // view is present and quiet, and the failure still has to win.
    show(BASE, { failed: true });

    expect(screen.getByText(/could not be assembled/)).toBeTruthy();
    expect(screen.queryByText("Nothing here needs you today.")).toBeNull();
  });
});

// The six tiles State D draws, and the rules that pick what each one names.
// The rules are choices rather than derivations, so each is pinned here: a
// selection nobody wrote down is one the next reader has to reverse-engineer
// from the sort call.
describe("the tiles, and which record each one picks", () => {
  // The contract requires the full factor breakdown on every strength; the
  // tiles read only the score, but a fixture that omits them is not the shape
  // the wire sends.
  const FACTORS = { recency: 0, frequency: 0, reciprocity: 0, direction: 0 };
  const CONTACT = {
    person_id: "p-1",
    full_name: "Sarah Cole",
    strength: { score: 40, bucket: "warm" as const, factors: FACTORS },
    deal_roles: [],
    consent: {},
  };

  it("names the head of the next-steps list, which the server already ordered", () => {
    show({
      ...BASE,
      next_steps: {
        data: [
          {
            activity_id: "a-1",
            subject: "Send the revised proposal",
            due_at: "2026-08-05T09:00:00Z",
            overdue: true,
          },
          { activity_id: "a-2", subject: "Later thing", overdue: false },
        ],
        page: { has_more: false, next_cursor: null },
      },
    });
    // The COUNT and the deadline, never the subject: the next-steps card
    // below renders that with a due-date edit and a complete button, and a
    // second flat copy here is the weaker of the two.
    expect(screen.getByText("1 overdue")).toBeTruthy();
    expect(screen.getByText(/Overdue since/)).toBeTruthy();
    expect(screen.queryByText("Send the revised proposal")).toBeNull();
    expect(screen.queryByText("Later thing")).toBeNull();
  });

  it("says a commitment has no due date rather than implying one", () => {
    show({
      ...BASE,
      next_steps: {
        data: [{ activity_id: "a-1", subject: "Someday", overdue: false }],
        page: { has_more: false, next_cursor: null },
      },
    });
    expect(screen.getByText("No due date")).toBeTruthy();
    expect(screen.getByText("1 open")).toBeTruthy();
  });

  // The route rule: strongest CONTACT, then that contact's strongest ROUTE.
  it("routes through the strongest contact who has a route at all", () => {
    show({
      ...BASE,
      people: {
        data: [
          // Stronger, but nobody has ever written to them: no way in to name.
          {
            ...CONTACT,
            person_id: "p-2",
            full_name: "Mark Hughes",
            strength: {
              score: 90,
              bucket: "strong" as const,
              factors: FACTORS,
            },
            routes: { top: [], remainder: 0, untried: true },
          },
          {
            ...CONTACT,
            routes: {
              top: [
                {
                  user_id: "u-1",
                  display_name: "Lars",
                  strength_bucket: "strong" as const,
                },
              ],
              remainder: 2,
              untried: false,
            },
          },
        ],
        page: { has_more: false, next_cursor: null },
      },
    });
    expect(screen.getByText("Lars → Sarah Cole")).toBeTruthy();
    expect(screen.getByText(/2 other colleagues/)).toBeTruthy();
  });

  it("picks the largest open deal, and ranks an unpriced one last", () => {
    show({
      ...BASE,
      deals: {
        data: [
          {
            deal_id: "d-1",
            name: "Small",
            status: "open" as const,
            stalled: false,
            amount: { amount_minor: 100000, currency: "EUR" },
          },
          {
            deal_id: "d-2",
            name: "Unpriced",
            status: "open" as const,
            stalled: false,
          },
          {
            deal_id: "d-3",
            name: "Expansion Phase 2",
            status: "open" as const,
            stalled: false,
            amount: { amount_minor: 9500000, currency: "EUR" },
          },
        ],
        page: { has_more: false, next_cursor: null },
        won_lifetime: { amount_minor: 0, currency: "EUR" },
        lost_count: 0,
      },
    });
    expect(screen.getByText(/Expansion Phase 2/)).toBeTruthy();
  });

  // A deal's amount is in its OWN currency with no base conversion, so ranking
  // across currencies would compare 100 JPY against 100 EUR.
  it("refuses to rank deals in different currencies and says why", () => {
    show({
      ...BASE,
      deals: {
        data: [
          {
            deal_id: "d-1",
            name: "In yen",
            status: "open" as const,
            stalled: false,
            amount: { amount_minor: 9000000, currency: "JPY" },
          },
          {
            deal_id: "d-2",
            name: "In euro",
            status: "open" as const,
            stalled: false,
            amount: { amount_minor: 100000, currency: "EUR" },
          },
        ],
        page: { has_more: false, next_cursor: null },
        won_lifetime: { amount_minor: 0, currency: "EUR" },
        lost_count: 0,
      },
    });
    expect(screen.getByText("2 open deals")).toBeTruthy();
    expect(screen.getByText(/different currencies/)).toBeTruthy();
    expect(screen.queryByText(/In yen/)).toBeNull();
  });

  it("repeats the strip's signal rather than forming a second verdict", () => {
    show({
      ...BASE,
      state_strip: {
        account: { lifecycle: "customer", relationship_types: [] },
        signal: {
          kind: "contract_ending",
          severity: "urgent",
          summary: "They wrote that the contract ends on 31 July.",
        },
      },
    });
    expect(
      screen.getByText("They wrote that the contract ends on 31 July."),
    ).toBeTruthy();
    // A threshold someone chose is an assessment, not an observation.
    expect(screen.getByText("Assessment")).toBeTruthy();
  });

  // The composer cannot draft from an account yet (DRAFT-WIRE-N-1). A button
  // that opens a composer with nothing in it is worse than one that says why.
  it("offers the draft verb disabled, with the reason on the page", () => {
    show({
      ...BASE,
      people: {
        data: [
          {
            ...CONTACT,
            routes: {
              top: [
                {
                  user_id: "u-1",
                  display_name: "Lars",
                  strength_bucket: "strong" as const,
                },
              ],
              remainder: 0,
              untried: false,
            },
          },
        ],
        page: { has_more: false, next_cursor: null },
      },
    });
    const draft = screen.getByRole("button", { name: /Draft follow-up/ });
    expect(draft.hasAttribute("disabled")).toBe(true);
    expect(screen.getByText(/not built yet/)).toBeTruthy();
  });
});
