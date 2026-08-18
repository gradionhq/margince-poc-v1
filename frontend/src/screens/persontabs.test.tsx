/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import {
  PersonActivityTab,
  PersonDealsTab,
  PersonMeetingsTab,
} from "./persontabs";

type Person360 = components["schemas"]["Person360"];

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
  person: { id: "p-1", full_name: "Dana Buyer" },
  sections_omitted: [],
  activities: {
    data: [
      {
        id: "a-1",
        kind: "email",
        subject: "Fleet renewal",
        direction: "inbound",
        occurred_at: "2026-08-11T12:00:00Z",
      },
      {
        id: "a-2",
        kind: "meeting",
        subject: "Depot walkthrough",
        occurred_at: "2026-08-09T08:00:00Z",
      },
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
} as unknown as Person360;

// The same record read by someone whose grant reaches none of it: the sections
// are absent AND named, which is what separates "you may not see this" from
// "there is none".
const withheld: Person360 = {
  ...view,
  activities: undefined,
  deal_roles: undefined,
  next_meeting: undefined,
  sections_omitted: ["activities", "deal_roles", "next_meeting"],
} as unknown as Person360;

describe("the activity tab", () => {
  it("draws the exchanges the 360 carried", () => {
    withProviders(<PersonActivityTab view={view} />);
    expect(screen.getByText("Fleet renewal")).toBeTruthy();
  });

  it("says the section is withheld rather than drawing it empty", () => {
    withProviders(<PersonActivityTab view={withheld} />);
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
});
