/** @vitest-environment jsdom */
import {
  cleanup,
  fireEvent,
  render as rtlRender,
  screen,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MoneyInput } from "./moneyinput";

// MoneyInput wraps the existing TextInput (atoms.tsx) to display/edit MAJOR
// units while emitting MINOR units, matching the major→minor convention
// already established in deals.tsx (Math.round(Number(amount) * 100)) and
// products.tsx's toMinor helper. This assumes 2-decimal currencies only
// (the currency prop is accepted for API symmetry/future use).
//
// The conversion specs fire a single change event carrying the finished
// input string rather than simulating keystroke-by-keystroke typing: this
// is a controlled number input, so per-keystroke typing into a non-empty
// starting value ("0.00") produces intermediate strings ("0.001", …) that
// don't reflect what a real caller (who owns the value round-trip through
// its own state) would see. A single change event isolates the formula
// under test — Math.round(Number(raw) * 100) — from that unrelated
// controlled-input replay concern.

afterEach(cleanup);

describe("MoneyInput", () => {
  it("displays the initial value in major units to two decimals", () => {
    rtlRender(
      <MoneyInput
        valueMinor={150000}
        currency="EUR"
        onChangeMinor={vi.fn()}
        aria-label="Amount"
      />,
    );
    const input = screen.getByLabelText("Amount") as HTMLInputElement;
    expect(input.value).toBe("1500.00");
  });

  it("emits minor units for a whole-number major input", () => {
    const onChangeMinor = vi.fn();
    rtlRender(
      <MoneyInput
        valueMinor={0}
        currency="EUR"
        onChangeMinor={onChangeMinor}
        aria-label="Amount"
      />,
    );
    const input = screen.getByLabelText("Amount");
    fireEvent.change(input, { target: { value: "1500" } });
    expect(onChangeMinor).toHaveBeenLastCalledWith(150000);
  });

  it("emits minor units for a fractional major input", () => {
    const onChangeMinor = vi.fn();
    rtlRender(
      <MoneyInput
        valueMinor={0}
        currency="EUR"
        onChangeMinor={onChangeMinor}
        aria-label="Amount"
      />,
    );
    const input = screen.getByLabelText("Amount");
    fireEvent.change(input, { target: { value: "19.99" } });
    expect(onChangeMinor).toHaveBeenLastCalledWith(1999);
  });

  it("forwards standard input props such as disabled", () => {
    rtlRender(
      <MoneyInput
        valueMinor={0}
        currency="EUR"
        onChangeMinor={vi.fn()}
        aria-label="Amount"
        disabled
      />,
    );
    const input = screen.getByLabelText("Amount") as HTMLInputElement;
    expect(input.disabled).toBe(true);
  });
});
