/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
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
  opts: {
    loading?: boolean;
    failed?: boolean;
    onDraftTo?: (personId: string) => void;
    onPrepareMeeting?: (activityId: string) => void;
  } = {},
) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">
        <TodayOnThisAccount
          orgId="o-1"
          view={view}
          loading={opts.loading ?? false}
          failed={opts.failed ?? false}
          onDraftTo={opts.onDraftTo}
          onPrepareMeeting={opts.onPrepareMeeting}
        />
      </LocaleProvider>
    </QueryClientProvider>,
  );
}

describe("what needs a person on this account today", () => {
  it("names the booked meeting and when it is", () => {
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
    // Who is in it belongs to the move row that prepares for the meeting,
    // beside the button — this caller passed no prepare handler, so there is
    // no such row and the guest list has nowhere to be acted on.
    expect(screen.queryByText(/Dana Buyer/)).toBeNull();
    expect(screen.getByText("Next meeting")).toBeTruthy();
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

  // The interaction tile reads the activities section, so a caller with no
  // activity grant must be TOLD the tile is missing. Without the section in
  // the footer's list it would vanish in silence, which reads as an account
  // nobody has spoken to.
  it("names the activities section when the reader may not see what was said", () => {
    show({ ...BASE, sections_omitted: ["activities"] });

    expect(screen.getByText(/Hidden from you/).textContent).toContain(
      "what was said",
    );
    expect(screen.queryByText("Last exchange")).toBeNull();
  });

  // Whose move it is and the open-risk tile both read `state_strip`
  // (whoseMove and openRisk). The KPI strip's own withheld rendering is
  // tested elsewhere (company360.test.tsx), but that is the STRIP's read of
  // the same fields — this brief reads them independently, off its own copy
  // of the view, and had no coverage of its own withheld path until now.
  it("names both readings when the reader may not see whose move it is or what is at risk", () => {
    show({ ...BASE, sections_omitted: ["state_strip"] });

    expect(screen.getByText(/Hidden from you/).textContent).toContain(
      "whose move it is and the signals",
    );
  });

  // The best-route tile reads `people`. A caller scoped away from the
  // roster must be told the reading is missing, not shown a brief that
  // silently never names a way in.
  it("names the contacts when the reader may not see who is here", () => {
    show({ ...BASE, sections_omitted: ["people"] });

    expect(screen.getByText(/Hidden from you/).textContent).toContain(
      "the contacts",
    );
  });

  it("distinguishes a failed read from a quiet account", () => {
    show(undefined, { failed: true });
    // "We could not assemble this" and "nothing needs you" are different
    // sentences, and only one of them is about the account.
    expect(screen.getByText(/could not be assembled/)).toBeTruthy();
    expect(screen.queryByText("Nothing here needs you today.")).toBeNull();
  });

  // The account brief's own footer reports this with the baseline it counted
  // from. A second, shorter copy here is the duplication this section's rules
  // forbid, so what changed since the last visit earns no tile.
  it("leaves what changed since the last visit to the brief that reports it", () => {
    show({
      ...BASE,
      since_last_visit: {
        new_activities: 3,
        baseline_at: "2026-08-01T09:00:00Z",
      },
    });
    expect(screen.getByText("Nothing here needs you today.")).toBeTruthy();
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
describe("the context line, and which record each reading picks", () => {
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

  // The 360 serves activities newest-first (ORDER BY occurred_at DESC), so the
  // head of the list is the most recent and this tile makes no ordering
  // decision of its own.
  it("says what the last exchange was about, from the newest activity", () => {
    show({
      ...BASE,
      activities: {
        data: [
          {
            id: "act-1",
            kind: "email",
            is_done: false,
            subject: "Questions about implementation capacity",
            occurred_at: "2026-08-04T09:00:00Z",
            source: "manual",
            captured_by: "human:test",
            created_at: "2026-08-04T09:00:00Z",
            updated_at: "2026-08-04T09:00:00Z",
          },
          {
            id: "act-2",
            kind: "email",
            is_done: false,
            subject: "An older thread",
            occurred_at: "2026-07-01T09:00:00Z",
            source: "manual",
            captured_by: "human:test",
            created_at: "2026-07-01T09:00:00Z",
            updated_at: "2026-07-01T09:00:00Z",
          },
        ],
        page: { has_more: false, next_cursor: null },
      },
    });

    expect(screen.getByText("Last exchange")).toBeTruthy();
    expect(
      screen.getByText(/Questions about implementation capacity/),
    ).toBeTruthy();
    expect(screen.queryByText(/An older thread/)).toBeNull();
  });

  // The timeline is unfiltered: tasks live in the same table and sort by the
  // same column. A task is something we wrote to ourselves, and this file
  // already refuses to render a task subject twice.
  it("skips a task when picking what was last said", () => {
    show({
      ...BASE,
      activities: {
        data: [
          {
            id: "act-task",
            kind: "task",
            is_done: false,
            subject: "Chase the signature",
            occurred_at: "2026-08-06T09:00:00Z",
            source: "manual",
            captured_by: "human:test",
            created_at: "2026-08-06T09:00:00Z",
            updated_at: "2026-08-06T09:00:00Z",
          },
          {
            id: "act-mail",
            kind: "email",
            is_done: false,
            subject: "Questions about capacity",
            occurred_at: "2026-08-04T09:00:00Z",
            source: "manual",
            captured_by: "human:test",
            created_at: "2026-08-04T09:00:00Z",
            updated_at: "2026-08-04T09:00:00Z",
          },
        ],
        page: { has_more: false, next_cursor: null },
      },
    });

    expect(screen.getByText(/Questions about capacity/)).toBeTruthy();
    expect(screen.queryByText(/Chase the signature/)).toBeNull();
  });

  // `occurred_at DESC` sorts a meeting booked for next week to the head of the
  // list. It has not been said yet, and the next-meeting tile already has it.
  it("skips an activity that has not happened yet", () => {
    show({
      ...BASE,
      activities: {
        data: [
          {
            id: "act-future",
            kind: "meeting",
            is_done: false,
            subject: "Executive alignment",
            occurred_at: "2026-08-20T09:00:00Z",
            source: "manual",
            captured_by: "human:test",
            created_at: "2026-08-01T09:00:00Z",
            updated_at: "2026-08-01T09:00:00Z",
          },
          {
            id: "act-past",
            kind: "email",
            is_done: false,
            subject: "Where we landed on scope",
            occurred_at: "2026-08-04T09:00:00Z",
            source: "manual",
            captured_by: "human:test",
            created_at: "2026-08-04T09:00:00Z",
            updated_at: "2026-08-04T09:00:00Z",
          },
        ],
        page: { has_more: false, next_cursor: null },
      },
    });

    expect(screen.getByText(/Where we landed on scope/)).toBeTruthy();
    expect(screen.queryByText(/Executive alignment/)).toBeNull();
  });

  // A withheld activities section and a quiet account are different answers.
  // "Nothing was said" invented from a section the caller may not read is the
  // conclusion this page must never draw.
  it("draws no exchange reading when there is nothing logged", () => {
    show({
      ...BASE,
      activities: { data: [], page: { has_more: false, next_cursor: null } },
    });
    expect(screen.queryByText("Last exchange")).toBeNull();
  });

  // What is owed lives in the panel's FOOTER now (nextCommitmentLine,
  // company360.tsx), not as a context tile — the count and the subject stay
  // out of it for the same reason the retired tile did: the next-steps card
  // renders the subject with a due-date edit and a complete button, and a
  // second flat copy here would be the weaker of the two.
  it("says how many are overdue in the footer, never the subject", () => {
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
    expect(screen.getByText("1 overdue")).toBeTruthy();
    expect(screen.queryByText("Send the revised proposal")).toBeNull();
    expect(screen.queryByText("Later thing")).toBeNull();
  });

  it("says how many are open when none is overdue", () => {
    show({
      ...BASE,
      next_steps: {
        data: [{ activity_id: "a-1", subject: "Someday", overdue: false }],
        page: { has_more: false, next_cursor: null },
      },
    });
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
    // The reading is the way IN, and nothing else. How many other colleagues
    // have written to the same contact is a fact about the CONTACT, and the
    // People tab is where a reader acts on it.
    expect(screen.queryByText(/other colleagues/)).toBeNull();
  });

  // The largest-open-deal reading moved to the Commercial panel
  // (organizations.tsx) alongside the full open-deals list, so this file no
  // longer picks or ranks a deal of its own.

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
    // The server's own summary, which says on what evidence the rule fired.
    // The kind alone ("Contract ending") leaves the reader to guess since when.
    expect(
      screen.getByText("They wrote that the contract ends on 31 July."),
    ).toBeTruthy();
    // A threshold someone chose is not something anybody observed, so the
    // reading is named as a risk rather than left to read as a fact.
    expect(screen.getByText("Risk")).toBeTruthy();
  });

  // Whose move it is used to be the strip's own tile ("Whose move"); it moved
  // here because it is a DATED reading, and the strip now carries only the
  // account's standing state. Lifted rather than reimplemented — the words
  // and the tone are `co.strip.engagement.*`, unchanged.
  it("names whose move it is, from the same engagement field the strip used to draw", () => {
    show({
      ...BASE,
      state_strip: {
        account: { lifecycle: "customer", relationship_types: [] },
        engagement: {
          state: "waiting_on_them",
          last_inbound_at: null,
          last_outbound_at: "2026-07-20T09:00:00Z",
        },
      },
    });
    expect(screen.getByText("Waiting on them")).toBeTruthy();
    // How long it has stood that way, counted from the last thing WE sent and
    // measured against the same `as_of` the rest of the page is read at: a
    // state with no duration is a status, and a rep cannot act on a status.
    expect(screen.getByText("no answer in 18 days")).toBeTruthy();
  });

  it("counts no silence when the answer has already come", () => {
    show({
      ...BASE,
      state_strip: {
        account: { lifecycle: "customer", relationship_types: [] },
        engagement: {
          state: "waiting_on_us",
          last_inbound_at: "2026-08-05T09:00:00Z",
          last_outbound_at: "2026-07-20T09:00:00Z",
        },
      },
    });
    expect(screen.getByText("Waiting on us")).toBeTruthy();
    // Days since our own last message say nothing once they have replied, and
    // "no answer in 18 days" over a thread that ended with their answer is
    // what costs a reader trust in every other reading beside it.
    expect(screen.queryByText(/no answer in/)).toBeNull();
  });

  // The button names the recipient it will write to, and hands that person to
  // the composer: an account-started message has no thread to anchor on, so
  // the recipient is what grounds it.
  it("hands the named recipient to the composer", () => {
    const drafted = vi.fn();
    show(
      {
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
      },
      { onDraftTo: drafted },
    );
    fireEvent.click(screen.getByRole("button", { name: "Draft" }));
    expect(drafted).toHaveBeenCalledWith(CONTACT.person_id);
  });

  // The MOVES half of the merged brief: a booked meeting's own verb renders
  // as a full-bleed row alongside whatever advice the account has, rather
  // than as a sidebar button beside the context tiles.
  it("offers to prepare for a booked meeting as a move row", () => {
    const prepared = vi.fn();
    show(
      {
        ...BASE,
        next_meeting: {
          activity_id: "a-1",
          starts_at: "2026-08-12T09:00:00Z",
          subject: "Renewal review",
          participants: [{ person_id: "p-1", display_name: "Dana Buyer" }],
        },
      },
      { onPrepareMeeting: prepared },
    );
    // The verb names what the button does: it opens the meeting brief for
    // this room. It read "Write to the room" while the account page had no
    // brief drawer and the handler opened the composer instead — the label
    // and the handler moved together (aiscope.test.tsx pins the
    // drawer).
    fireEvent.click(screen.getByRole("button", { name: "Prepare meeting" }));
    expect(prepared).toHaveBeenCalledWith("a-1");
  });

  // "Worth doing next"'s own rows are the OTHER half of the moves section —
  // merged into this one panel rather than a second card repeating the
  // account's own advice.
  it("carries the account's suggestions as moves alongside the context band", () => {
    show({
      ...BASE,
      suggestions: [
        {
          kind: "no_reply",
          fingerprint: "f-1",
          reason: "You reached out 15 days ago and nobody has come back.",
          evidence: [],
        },
      ],
    });
    expect(screen.getByText(/nobody has come back/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Not now" })).toBeTruthy();
  });
});

// Every 360 collection is a page of 25 with `has_more` beside it. A tile that
// counts or ranks off that page states a fact about the PAGE, and the reader
// has no way to tell. These are the three places it would have.
describe("a page is not the account", () => {
  const FACTORS = { recency: 0, frequency: 0, reciprocity: 0, direction: 0 };

  it("says 25+ overdue rather than a count it cannot stand behind", () => {
    show({
      ...BASE,
      next_steps: {
        data: [
          {
            activity_id: "a-1",
            subject: "One of many",
            due_at: "2026-08-05T09:00:00Z",
            overdue: true,
          },
        ],
        page: { has_more: true, next_cursor: "c" },
      },
    });
    expect(screen.getByText("1+ overdue")).toBeTruthy();
    expect(screen.queryByText("1 overdue")).toBeNull();
  });

  it("says the best route is the best of the contacts it could see", () => {
    show({
      ...BASE,
      people: {
        data: [
          {
            person_id: "p-1",
            full_name: "Sarah Cole",
            strength: { score: 40, bucket: "warm" as const, factors: FACTORS },
            deal_roles: [],
            consent: {},
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
        page: { has_more: true, next_cursor: "c" },
      },
    });
    expect(screen.getByText(/of the contacts shown/)).toBeTruthy();
  });
});
