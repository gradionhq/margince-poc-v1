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

const BASE = {
  as_of: "2026-08-07T09:00:00Z",
  organization: { id: "o-1", display_name: "Acme" },
  sections_omitted: [],
} as unknown as Organization360;

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
    } as Organization360);

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
    } as unknown as Organization360);
    expect(screen.getByText(/Hidden from you/).textContent).toContain(
      "the calendar",
    );
  });

  it("says a source is hidden from the reader rather than composing a shorter list silently", () => {
    show({
      ...BASE,
      sections_omitted: ["next_meeting", "next_steps"],
    } as unknown as Organization360);

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
    } as unknown as Organization360);
    expect(screen.getByText(/3 new on the timeline/)).toBeTruthy();
  });
});
