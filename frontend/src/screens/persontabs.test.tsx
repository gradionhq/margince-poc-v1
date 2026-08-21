/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import {
  PersonDealsTab,
  PersonMeetingsTab,
  PersonTimelineTab,
} from "./persontabs";

type Person360 = components["schemas"]["Person360"];
// The 360 carries its OWN spelling of an activity row — the section's element
// type, not the standalone `Activity` schema, which differs in two fields.
// Deriving it from the section is what keeps this fixture honest about the
// payload the page actually reads.
type SectionActivity = NonNullable<Person360["activities"]>["data"][number];

// Provenance is stamped by the server on every captured row, so a fixture
// that leaves it out is a payload no reader ever receives. These two build a
// row the CONTRACT admits, which is what makes the fixtures below typed
// rather than asserted: an assertion would let a field the page reads drift
// out of the fixture without a word from the compiler.
const CAPTURED = {
  source: "manual",
  captured_by: "human:u-1",
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-08-01T08:00:00Z",
} as const;

function activity(
  row: Pick<SectionActivity, "id" | "kind" | "occurred_at"> &
    Partial<SectionActivity>,
): SectionActivity {
  return { is_done: false, ...CAPTURED, ...row };
}

// The six tabs beside Overview were placeholders for a release, and the thing
// that made them safe to ship as placeholders — a sentence saying so — is
// exactly what makes a regression invisible: a tab that silently renders
// nothing looks like a tab with nothing on it. These pin the two facts a
// reader acts on. That a section WITH rows draws them, and that a section the
// grant withheld says so rather than reading as empty.

// This suite mounts several trees into one document. Without cleanup the
// second assertion reads the first render's DOM.
afterEach(() => {
  cleanup();
});

function withProviders(node: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{node}</LocaleProvider>
    </QueryClientProvider>,
  );
}

const view: Person360 = {
  as_of: "2026-08-13T09:00:00Z",
  person: { id: "p-1", full_name: "Dana Buyer", ...CAPTURED },
  sections_omitted: [],
  activities: {
    data: [
      activity({
        id: "a-1",
        kind: "email",
        subject: "Fleet renewal",
        direction: "inbound",
        occurred_at: "2026-08-11T12:00:00Z",
      }),
      activity({
        id: "a-2",
        kind: "meeting",
        subject: "Depot walkthrough",
        occurred_at: "2026-08-09T08:00:00Z",
      }),
    ],
    page: { has_more: false },
  },
  deal_roles: {
    data: [
      {
        relationship_id: "r-1",
        deal_id: "d-1",
        deal_title: "Fleet renewal 2026",
        deal_stage: "Proposal",
        role: "economic_buyer",
      },
    ],
    page: { has_more: false },
  },
  next_meeting: {
    activity_id: "a-9",
    starts_at: "2026-08-20T13:00:00Z",
    subject: "Contract review",
    participants: [{ person_id: "p-1", full_name: "Dana Buyer" }],
  },
};

// The same record read by someone whose grant reaches none of it: the sections
// are absent AND named, which is what separates "you may not see this" from
// "there is none".
const withheld: Person360 = {
  ...view,
  activities: undefined,
  deal_roles: undefined,
  next_meeting: undefined,
  sections_omitted: ["activities", "deal_roles", "next_meeting"],
};

describe("the timeline tab", () => {
  it("draws the exchanges the 360 carried", () => {
    withProviders(<PersonTimelineTab personId="p-1" view={view} />);
    expect(screen.getByText("Fleet renewal")).toBeTruthy();
  });

  it("opens on the activities the record already holds, not on its changes", () => {
    // The changes feed is a second read, and a tab that fired it on open
    // would spend it on every reader who never asked for changes.
    withProviders(<PersonTimelineTab personId="p-1" view={view} />);
    const activities = screen.getByRole("button", { name: "Activities" });
    expect(activities.getAttribute("aria-pressed")).toBe("true");
  });

  it("says the section is withheld rather than drawing it empty", () => {
    withProviders(<PersonTimelineTab personId="p-1" view={withheld} />);
    expect(screen.queryByText(/Nothing has been logged/)).toBeNull();
    expect(
      screen.getByText("Hidden — your role cannot read this"),
    ).toBeTruthy();
  });
});

describe("the deals tab", () => {
  it("names every deal the person is recorded on, with their seat", () => {
    withProviders(<PersonDealsTab view={view} />);
    expect(screen.getByText("Fleet renewal 2026")).toBeTruthy();
    expect(screen.getByText("Proposal")).toBeTruthy();
    expect(screen.getByText("Economic buyer")).toBeTruthy();
  });

  it("does not report an absent grant as an absence of deals", () => {
    withProviders(<PersonDealsTab view={withheld} />);
    expect(screen.queryByText(/not recorded on any deal/)).toBeNull();
  });
});

describe("the meetings tab", () => {
  it("puts the booked meeting above the ones already held", () => {
    withProviders(<PersonMeetingsTab view={view} />);
    expect(screen.getByText("Contract review")).toBeTruthy();
    expect(screen.getByText("Depot walkthrough")).toBeTruthy();
  });

  it("draws only the meetings, never the whole chronology", () => {
    withProviders(<PersonMeetingsTab view={view} />);
    // The email in the same activities page belongs to the Activity tab. A
    // filter that let it through here would make this tab a second, worse
    // spelling of that one.
    expect(screen.queryByText("Fleet renewal")).toBeNull();
  });

  it("offers a brief for the booked meeting and for one already held", async () => {
    // The backend assembles a brief for ANY meeting activity. Reaching it only
    // through the next meeting's prep moment left every other meeting on the
    // record with a brief nothing could ask for.
    const briefed: string[] = [];
    withProviders(
      <PersonMeetingsTab
        view={view}
        onBriefMeeting={(id) => briefed.push(id)}
      />,
    );
    const actions = screen.getAllByRole("button", { name: "Brief me" });
    expect(actions.length).toBe(2);
    await userEvent.setup().click(actions[0]);
    expect(briefed.length).toBe(1);
  });

  it("offers no brief for a meeting the reader may find but not read", () => {
    // The timeline carries discoverable-but-withheld rows on purpose, so the
    // reader knows a conversation happened. The brief endpoint applies the
    // stricter content gate, so a verb here would promise what their own grant
    // refuses — and answer 404 when they took it up.
    const withWithheld: Person360 = {
      ...view,
      activities: {
        data: [
          activity({
            id: "a-secret",
            kind: "meeting",
            subject: "Board session",
            occurred_at: "2026-08-10T08:00:00Z",
            content_state: "withheld",
          }),
        ],
        page: { has_more: false },
      },
      next_meeting: undefined,
    };
    withProviders(
      <PersonMeetingsTab view={withWithheld} onBriefMeeting={() => {}} />,
    );
    // The row is DRAWN — the reader learns a meeting happened — and carries no
    // verb. Asserting the subject would be wrong: a withheld row redacts it,
    // which is the whole point of the state.
    expect(screen.queryByText(/Nothing logged/)).toBeNull();
    expect(screen.queryByRole("button", { name: "Brief me" })).toBeNull();
  });

  it("offers no brief when the surface cannot open one", () => {
    // Without the callback the verb would be a button that does nothing, which
    // teaches a reader the feature is broken rather than absent.
    withProviders(<PersonMeetingsTab view={view} />);
    expect(screen.queryByRole("button", { name: "Brief me" })).toBeNull();
  });

  it("names the meeting the reader picked, not the soonest one", async () => {
    // The defect this replaces: the drawer always read next_meeting, so a
    // brief opened from a past meeting described a different room.
    const briefed: string[] = [];
    withProviders(
      <PersonMeetingsTab
        view={view}
        onBriefMeeting={(id) => briefed.push(id)}
      />,
    );
    const actions = screen.getAllByRole("button", { name: "Brief me" });
    // The booked meeting leads the tab; the held one follows it.
    await userEvent.setup().click(actions[1]);
    expect(briefed).toEqual(["a-2"]);
  });
});
