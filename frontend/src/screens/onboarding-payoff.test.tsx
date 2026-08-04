/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";

import { cleanup, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { LocaleProvider } from "../i18n";
import {
  PAYOFF_FRESH_WINDOW_MS,
  type PayoffCounts,
  PayoffGrid,
  PayoffMessage,
} from "./onboarding-payoff";

// The payoff grid answers "what did those two minutes buy" in counts, and the
// load-bearing distinction is `null` vs `0`: a zero is something the server
// said, an absent number is an operation that never ran. Printing 0 for the
// second reads as a failure the user caused.

const full: PayoffCounts = {
  factsRead: 1284,
  factsConfirmed: 42,
  peopleFound: 7,
  profileFields: 12,
  pagesRead: 18,
  voiceWords: 30512,
};

function withLocale(ui: ReactNode, locale: "en" | "de" = "en") {
  return render(<LocaleProvider initial={locale}>{ui}</LocaleProvider>);
}

// The instant the payoff is read at. Both ends of the elapsed-time comparison
// are the test's own numbers — the component takes `nowMs` as a prop, so no
// case here depends on the machine's clock.
const NOW_MS = Date.parse("2026-07-31T12:00:00Z");

function startedAgo(ms: number): string {
  return new Date(NOW_MS - ms).toISOString();
}

const MINUTE_MS = 60_000;

// The cell a label belongs to: the <dt> and its <dd> are the only children of
// the cell element, so the parent's text is "label + value".
function cellText(label: string): string {
  return screen.getByText(label).parentElement?.textContent ?? "";
}

afterEach(cleanup);

describe("PayoffGrid", () => {
  it("renders every count as a label and a locale-formatted number", () => {
    withLocale(<PayoffGrid counts={full} locale="en" />);

    expect(cellText("facts read")).toContain("1,284");
    expect(cellText("facts you confirmed")).toContain("42");
    expect(cellText("people found")).toContain("7");
    expect(cellText("profile fields")).toContain("12");
    expect(cellText("pages read")).toContain("18");
    expect(cellText("words in your voice")).toContain("30,512");
  });

  it("groups digits by the locale it was handed, not by the catalog language", () => {
    // Labels stay English (the catalog), numbers turn German (the formatter):
    // proves the grouping comes from the `locale` prop rather than from a
    // hard-coded separator.
    withLocale(<PayoffGrid counts={full} locale="de" />, "en");

    expect(cellText("facts read")).toContain("1.284");
    expect(cellText("words in your voice")).toContain("30.512");
  });

  it("says the voice is untrained when there is no corpus, instead of showing a zero", () => {
    withLocale(
      <PayoffGrid counts={{ ...full, voiceWords: null }} locale="en" />,
    );

    expect(screen.getByText("voice not trained yet")).toBeInTheDocument();
    // The label survives for assistive tech, but no count is invented.
    expect(cellText("words in your voice")).not.toContain("0");
  });

  it("shows a real zero as a zero — a corpus that exists and is empty is a fact", () => {
    withLocale(<PayoffGrid counts={{ ...full, voiceWords: 0 }} locale="en" />);

    expect(cellText("words in your voice")).toContain("0");
    expect(screen.queryByText("voice not trained yet")).not.toBeInTheDocument();
  });

  it("marks the two facts that carry the argument, and only those", () => {
    const { container } = withLocale(<PayoffGrid counts={full} locale="en" />);

    // What was read and what the reader confirmed lead; the four counts that
    // support them do not compete with them for the eye.
    const led = [...container.querySelectorAll(".ob-payoff-cell.is-argument")];
    expect(led.map((cell) => cell.textContent)).toEqual([
      "facts read1,284",
      "facts you confirmed42",
    ]);
  });

  it("keeps the emphasis on the leading fact when an earlier cell is omitted", () => {
    // Position cannot carry the emphasis: a grid missing its first count would
    // hand it to whichever cell slid up into the gap.
    const { container } = withLocale(
      <PayoffGrid counts={{ ...full, factsRead: null }} locale="en" />,
    );

    const led = [...container.querySelectorAll(".ob-payoff-cell.is-argument")];
    expect(led.map((cell) => cell.textContent)).toEqual([
      "facts you confirmed42",
    ]);
  });

  it("omits a cell whose number never existed rather than filling it in", () => {
    // `pages_read` is optional on the wire, and there is no honest copy for its
    // absence — so the grid is one cell shorter instead of one guess longer.
    withLocale(
      <PayoffGrid counts={{ ...full, pagesRead: null }} locale="en" />,
    );

    expect(screen.queryByText("pages read")).not.toBeInTheDocument();
    expect(screen.getByText("facts read")).toBeInTheDocument();
  });
});

describe("PayoffMessage", () => {
  it("frames the grid with the lead, the body and both deferrals, each naming its exit", () => {
    withLocale(
      <PayoffMessage
        counts={full}
        locale="en"
        startedAt={startedAgo(4 * MINUTE_MS)}
        nowMs={NOW_MS}
        resumedSession={false}
      />,
    );

    expect(
      screen.getByText("Minutes ago this was an empty install."),
    ).toBeInTheDocument();
    expect(cellText("facts read")).toContain("1,284");
    expect(
      screen.getByText(/still points at the page it came from/),
    ).toBeInTheDocument();
    expect(screen.getByText(/Settings → Autonomy/)).toBeInTheDocument();
    expect(screen.getByText(/Settings → People/)).toBeInTheDocument();
  });

  it("keeps the two deferrals a list for assistive tech despite the markerless styling", () => {
    withLocale(
      <PayoffMessage
        counts={full}
        locale="en"
        startedAt={startedAgo(4 * MINUTE_MS)}
        nowMs={NOW_MS}
        resumedSession={false}
      />,
    );

    const list = screen.getByRole("list");
    expect(list).toHaveTextContent(/Settings → Autonomy/);
    expect(list).toHaveTextContent(/Settings → People/);
  });

  // Setup is resumable, so "minutes ago" is a claim about elapsed time and the
  // payoff is the one screen that cannot afford to overstate. Each case fixes
  // both instants itself.
  it("says minutes ago when the setup really did start minutes ago", () => {
    withLocale(
      <PayoffMessage
        counts={full}
        locale="en"
        startedAt={startedAgo(PAYOFF_FRESH_WINDOW_MS - MINUTE_MS)}
        nowMs={NOW_MS}
        resumedSession={false}
      />,
    );

    expect(
      screen.getByText("Minutes ago this was an empty install."),
    ).toBeInTheDocument();
  });

  it("drops the time claim for a setup finished in a later session", () => {
    withLocale(
      <PayoffMessage
        counts={full}
        locale="en"
        startedAt={startedAgo(2 * 24 * 60 * MINUTE_MS)}
        nowMs={NOW_MS}
        resumedSession={false}
      />,
    );

    expect(
      screen.getByText("This started as an empty install."),
    ).toBeInTheDocument();
    expect(
      screen.queryByText("Minutes ago this was an empty install."),
    ).not.toBeInTheDocument();
  });

  it("drops the time claim the moment the fresh window has passed", () => {
    withLocale(
      <PayoffMessage
        counts={full}
        locale="en"
        startedAt={startedAgo(PAYOFF_FRESH_WINDOW_MS)}
        nowMs={NOW_MS}
        resumedSession={false}
      />,
    );

    expect(
      screen.getByText("This started as an empty install."),
    ).toBeInTheDocument();
  });

  it("drops the time claim when there is no start instant to check", () => {
    // An unknown elapsed time is not a short one, and the neutral sentence is
    // true whenever the setup ran.
    withLocale(
      <PayoffMessage
        counts={full}
        locale="en"
        startedAt={null}
        nowMs={NOW_MS}
        resumedSession={false}
      />,
    );

    expect(
      screen.getByText("This started as an empty install."),
    ).toBeInTheDocument();
  });

  it("drops the time claim when the start instant is ahead of the reader's clock", () => {
    // Server and browser disagreeing is not evidence of freshness.
    withLocale(
      <PayoffMessage
        counts={full}
        locale="en"
        startedAt={startedAgo(-MINUTE_MS)}
        nowMs={NOW_MS}
        resumedSession={false}
      />,
    );

    expect(
      screen.getByText("This started as an empty install."),
    ).toBeInTheDocument();
  });

  it("drops the time claim for a resumed session even when the reader's own clock reads it as fresh", () => {
    // A restored session compares a server instant (startedAt) against a
    // reader's device clock that was never in the room for `startedAt` being
    // written — a device clock that runs behind reality can make days of
    // real elapsed time look like minutes. `resumedSession` is the one
    // signal the wire can prove without trusting that clock at all, so it
    // overrides an elapsed check that would otherwise call this fresh.
    withLocale(
      <PayoffMessage
        counts={full}
        locale="en"
        startedAt={startedAgo(4 * MINUTE_MS)}
        nowMs={NOW_MS}
        resumedSession={true}
      />,
    );

    expect(
      screen.getByText("This started as an empty install."),
    ).toBeInTheDocument();
    expect(
      screen.queryByText("Minutes ago this was an empty install."),
    ).not.toBeInTheDocument();
  });
});
