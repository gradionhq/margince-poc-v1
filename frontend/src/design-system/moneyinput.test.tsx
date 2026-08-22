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
// units while emitting the MINOR units the API stores, at the scale the
// CURRENCY carries — format/minorunits, not a hard-coded hundred.
//
// The displayed text is the component's OWN state, resynced from the
// external valueMinor only when it changes for a reason other than this
// input's own typing (see moneyinput.tsx) — so, unlike a naive
// `value={(valueMinor / 100).toFixed(2)}` controlled input, real
// keystroke-by-keystroke typing never gets fought by a reformat mid-edit.
// The sequential-fireEvent tests below replay actual keystrokes rather than
// one finished string, to pin exactly that.

afterEach(cleanup);

describe("MoneyInput", () => {
  it("displays the initial value in major units to two decimals", () => {
    rtlRender(
      <MoneyInput
        currency="EUR"
        valueMinor={150000}
        onChangeMinor={vi.fn()}
        aria-label="Amount"
      />,
    );
    const input = screen.getByLabelText("Amount") as HTMLInputElement;
    expect(input.value).toBe("1500.00");
  });

  it.each([
    ["EUR", "0.01"],
    ["KWD", "0.001"],
    ["VND", "1"],
  ])(
    "steps by %s's own smallest unit, so native validation matches what we can store",
    (currency, step) => {
      // type="number" defaults to step="1" — without an explicit step, the
      // browser's own constraint validation rejects a genuine cents amount
      // like "12.34" as invalid. The other direction matters too: a dong has
      // no fractional part, and a step of 0.01 invited one the API cannot
      // hold.
      rtlRender(
        <MoneyInput
          currency={currency}
          valueMinor={0}
          onChangeMinor={vi.fn()}
          aria-label="Amount"
        />,
      );
      expect((screen.getByLabelText("Amount") as HTMLInputElement).step).toBe(
        step,
      );
    },
  );

  // The defect: this input multiplied by 100 whatever the currency, so an
  // offer line typed as 18,000,000 dong reached the API as 1,800,000,000 —
  // a hundred times the price, on a document a buyer signs.
  it.each([
    ["VND", "18000000", 18_000_000],
    ["JPY", "950000", 950_000],
    ["KWD", "95", 95_000],
    ["EUR", "95", 9_500],
  ])("%s: typing %s emits %i minor units", (currency, typed, wantMinor) => {
    const onChangeMinor = vi.fn();
    rtlRender(
      <MoneyInput
        currency={currency}
        valueMinor={0}
        onChangeMinor={onChangeMinor}
        aria-label="Amount"
      />,
    );
    fireEvent.change(screen.getByLabelText("Amount"), {
      target: { value: typed },
    });
    expect(onChangeMinor).toHaveBeenLastCalledWith(wantMinor);
  });

  // A figure with more decimals than the currency HAS is held, not rounded.
  // `step` only governs native validity — onChange still fires — so a
  // fractional dong reached toMinorUnits, rounded, and an offer line saved the
  // altered amount on blur.
  it.each([
    ["VND", "1.5"],
    ["VND", "18000000.25"],
    ["EUR", "12.345"],
    ["KWD", "12.3456"],
  ])(
    "%s: %s has more decimals than the currency, so nothing is committed",
    (currency, typed) => {
      const onChangeMinor = vi.fn();
      rtlRender(
        <MoneyInput
          currency={currency}
          valueMinor={0}
          onChangeMinor={onChangeMinor}
          aria-label="Amount"
        />,
      );
      fireEvent.change(screen.getByLabelText("Amount"), {
        target: { value: typed },
      });
      expect(onChangeMinor).not.toHaveBeenCalled();
    },
  );

  // Exponent notation has no decimal point, so counting the literal text let
  // `15e-1` — one and a half — past the precision guard and it committed as 2
  // minor units of a currency that cannot hold a half.
  it.each([
    ["VND", "15e-1"],
    ["VND", "1.5e0"],
    ["EUR", "1005e-3"],
  ])("%s: %s is over-precise however it is written", (currency, typed) => {
    const onChangeMinor = vi.fn();
    rtlRender(
      <MoneyInput
        currency={currency}
        valueMinor={0}
        onChangeMinor={onChangeMinor}
        aria-label="Amount"
      />,
    );
    fireEvent.change(screen.getByLabelText("Amount"), {
      target: { value: typed },
    });
    expect(onChangeMinor).not.toHaveBeenCalled();
  });

  // A positive exponent shifts the point the other way and REMOVES decimals,
  // so these are within precision and must still commit.
  it.each([
    ["VND", "150e-1", 15],
    ["VND", "1.5e1", 15],
    ["EUR", "1.2345e2", 12_345],
  ])("%s: %s is exact after the exponent", (currency, typed, want) => {
    const onChangeMinor = vi.fn();
    rtlRender(
      <MoneyInput
        currency={currency}
        valueMinor={0}
        onChangeMinor={onChangeMinor}
        aria-label="Amount"
      />,
    );
    fireEvent.change(screen.getByLabelText("Amount"), {
      target: { value: typed },
    });
    expect(onChangeMinor).toHaveBeenLastCalledWith(want);
  });

  // And the boundary on the other side: exactly as many decimals as the
  // currency carries is a perfectly good amount and must still commit.
  it.each([
    ["EUR", "12.34", 1234],
    ["KWD", "12.345", 12_345],
    ["VND", "18000000", 18_000_000],
  ])("%s: %s is within the currency's precision", (currency, typed, want) => {
    const onChangeMinor = vi.fn();
    rtlRender(
      <MoneyInput
        currency={currency}
        valueMinor={0}
        onChangeMinor={onChangeMinor}
        aria-label="Amount"
      />,
    );
    fireEvent.change(screen.getByLabelText("Amount"), {
      target: { value: typed },
    });
    expect(onChangeMinor).toHaveBeenLastCalledWith(want);
  });

  // The read direction, which is the half that made the write bug survive a
  // round trip looking correct.
  it("seeds a zero-decimal amount without inventing a fractional part", () => {
    rtlRender(
      <MoneyInput
        currency="VND"
        valueMinor={18_000_000}
        onChangeMinor={vi.fn()}
        aria-label="Amount"
      />,
    );
    expect((screen.getByLabelText("Amount") as HTMLInputElement).value).toBe(
      "18000000",
    );
  });

  it("emits minor units for a whole-number major input", () => {
    const onChangeMinor = vi.fn();
    rtlRender(
      <MoneyInput
        currency="EUR"
        valueMinor={0}
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
        currency="EUR"
        valueMinor={0}
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
        currency="EUR"
        valueMinor={0}
        onChangeMinor={vi.fn()}
        aria-label="Amount"
        disabled
      />,
    );
    const input = screen.getByLabelText("Amount") as HTMLInputElement;
    expect(input.disabled).toBe(true);
  });

  it("never reformats the buffer mid-edit, so typing digit-by-digit reaches the intended amount", () => {
    // A controlled input whose value is `(valueMinor / 100).toFixed(2)`
    // recomputed every render snaps "1" to "1.00" the instant it commits,
    // so the next keystroke lands on the reformatted string instead of the
    // one the user is building — typing "125" one digit at a time would
    // never reach 125.00. Replaying each keystroke's resulting string here
    // (as a real <input> hands the DOM) proves the buffer isn't fought.
    const onChangeMinor = vi.fn();
    rtlRender(
      <MoneyInput
        currency="EUR"
        valueMinor={0}
        onChangeMinor={onChangeMinor}
        aria-label="Amount"
      />,
    );
    const input = screen.getByLabelText("Amount") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "1" } });
    fireEvent.change(input, { target: { value: "12" } });
    fireEvent.change(input, { target: { value: "125" } });
    expect(input.value).toBe("125");
    expect(onChangeMinor).toHaveBeenLastCalledWith(12500);
  });

  it("does not commit 0 while the field is empty mid-edit", () => {
    const onChangeMinor = vi.fn();
    rtlRender(
      <MoneyInput
        currency="EUR"
        valueMinor={150000}
        onChangeMinor={onChangeMinor}
        aria-label="Amount"
      />,
    );
    const input = screen.getByLabelText("Amount") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "" } });
    expect(input.value).toBe("");
    expect(onChangeMinor).not.toHaveBeenCalled();
  });

  it("snaps the buffer back to the last committed value on blur", () => {
    const onChangeMinor = vi.fn();
    rtlRender(
      <MoneyInput
        currency="EUR"
        valueMinor={150000}
        onChangeMinor={onChangeMinor}
        aria-label="Amount"
      />,
    );
    const input = screen.getByLabelText("Amount") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "19.9" } });
    fireEvent.blur(input);
    expect(input.value).toBe("19.90");
  });

  it("resyncs the buffer when valueMinor changes from outside (a different row swapped in)", () => {
    const onChangeMinor = vi.fn();
    const { rerender } = rtlRender(
      <MoneyInput
        currency="EUR"
        valueMinor={150000}
        onChangeMinor={onChangeMinor}
        aria-label="Amount"
      />,
    );
    rerender(
      <MoneyInput
        currency="EUR"
        valueMinor={500}
        onChangeMinor={onChangeMinor}
        aria-label="Amount"
      />,
    );
    const input = screen.getByLabelText("Amount") as HTMLInputElement;
    expect(input.value).toBe("5.00");
  });
});
