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
// products.tsx's toMinor helper. This assumes 2-decimal currencies only.
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
        valueMinor={150000}
        onChangeMinor={vi.fn()}
        aria-label="Amount"
      />,
    );
    const input = screen.getByLabelText("Amount") as HTMLInputElement;
    expect(input.value).toBe("1500.00");
  });

  it("sets step=0.01 so a 2-decimal amount passes native number validation", () => {
    // type="number" defaults to step="1" — without an explicit step, the
    // browser's own constraint validation rejects a genuine cents amount
    // like "12.34" as invalid.
    rtlRender(
      <MoneyInput valueMinor={0} onChangeMinor={vi.fn()} aria-label="Amount" />,
    );
    const input = screen.getByLabelText("Amount") as HTMLInputElement;
    expect(input.step).toBe("0.01");
  });

  it("emits minor units for a whole-number major input", () => {
    const onChangeMinor = vi.fn();
    rtlRender(
      <MoneyInput
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
        valueMinor={150000}
        onChangeMinor={onChangeMinor}
        aria-label="Amount"
      />,
    );
    rerender(
      <MoneyInput
        valueMinor={500}
        onChangeMinor={onChangeMinor}
        aria-label="Amount"
      />,
    );
    const input = screen.getByLabelText("Amount") as HTMLInputElement;
    expect(input.value).toBe("5.00");
  });
});
