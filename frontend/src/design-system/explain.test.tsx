/** @vitest-environment jsdom */
import { cleanup, render as rtlRender, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { LocaleProvider } from "../i18n";
import { ExplainNumber } from "./explain";

// B-EP09.18: the breakdown renders FROM the IR plan — the headline is the
// provided base_value verbatim, even when a hand-multiplied native×rate
// would disagree. That disagreement is the test: a UI that multiplies
// would show a different number.

afterEach(cleanup);

const render = (ui: ReactNode) =>
  rtlRender(<LocaleProvider initial="en">{ui}</LocaleProvider>);

describe("ExplainNumber", () => {
  it("renders the IR base_value verbatim, not a UI multiply", async () => {
    render(
      <ExplainNumber
        workspaceZone="Europe/Berlin"
        money={{
          baseValueMinor: 100_000, // €1,000.00 from the IR
          baseCurrency: "EUR",
          rows: [
            {
              label: "Fleet retrofit",
              nativeAmountMinor: 90_000, // $900.00 …
              nativeCurrency: "USD",
              rate: 2, // … × a rate that would give €1,800 if the UI multiplied
              rateDate: "2026-06-01T00:00:00Z",
            },
          ],
        }}
      />,
    );
    expect(screen.getByTestId("explained-base").textContent).toBe("€1,000.00");
    await userEvent.click(
      screen.getByRole("button", { name: "Explain this number" }),
    );
    expect(screen.getByText("$900.00")).toBeTruthy();
    expect(screen.getByText(/rate 2 on/)).toBeTruthy();
    // nowhere does €1,800 appear — the would-be multiply result
    expect(screen.queryByText(/1,800/)).toBeNull();
  });
});
