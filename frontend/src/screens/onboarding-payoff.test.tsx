/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";

import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import {
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
      <PayoffMessage counts={full} locale="en" onContinue={vi.fn()} />,
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

  it("hands control back through onContinue", async () => {
    const onContinue = vi.fn();
    withLocale(
      <PayoffMessage counts={full} locale="en" onContinue={onContinue} />,
    );

    await userEvent.click(screen.getByRole("button", { name: "Understood" }));

    expect(onContinue).toHaveBeenCalledTimes(1);
  });
});
