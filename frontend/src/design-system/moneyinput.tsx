import { type InputHTMLAttributes, useEffect, useRef, useState } from "react";
import {
  minorUnitDigits,
  toMajorUnits,
  toMinorUnits,
} from "../format/minorunits";
import { TextInput } from "./atoms";

// A thin wrapper around TextInput type="number" that displays and edits MAJOR
// units while emitting the MINOR units the API stores.
//
// The scale is the CURRENCY's, taken from format/minorunits. This used to
// assume two decimals, on the stated grounds that no caller needed a different
// count — which was true of the callers and false of the currencies: an offer
// priced in dong went to the server multiplied by a hundred, and the server had
// no way to tell that figure from a real one. So `currency` is required rather
// than defaulted; a component that silently assumes EUR is the same bug with a
// friendlier signature.
//
// The displayed text is its OWN state, not `(valueMinor / 100).toFixed(2)`
// recomputed on every render: a fully-derived display reformats after every
// keystroke (typing "1" then "2" for "12.50" renders "1.00" after the first
// keystroke, so the next keystroke lands on the already-rounded string
// instead of the one the user meant to extend). `lastCommittedMinor` tracks
// which minor value this input itself last emitted, so the resync effect
// below only snaps the text to the external value when it changes for a
// reason OTHER than this input's own typing (a different row's value
// swapped in, a reset) — never mid-edit.
export function MoneyInput({
  valueMinor,
  currency,
  onChangeMinor,
  onBlur,
  blankWhenZero = false,
  ...rest
}: Readonly<
  Omit<InputHTMLAttributes<HTMLInputElement>, "value" | "onChange" | "type"> & {
    valueMinor: number;
    currency: string;
    onChangeMinor: (minor: number) => void;
    // Show an unpriced record as an EMPTY field rather than "0.00".
    //
    // Off by default, because an offer line always has a price and "0.00" is
    // the honest reading of a free one. On for a record that may simply not be
    // priced yet, where a pre-filled zero is a figure nobody entered — and one
    // a reader then types after, turning 5000 into 0.005000.
    blankWhenZero?: boolean;
  }
>) {
  const digits = minorUnitDigits(currency);
  const asText = (minor: number, forCurrency: string) =>
    blankWhenZero && minor === 0
      ? ""
      : toMajorUnits(minor, forCurrency).toFixed(minorUnitDigits(forCurrency));
  const [text, setText] = useState(() => asText(valueMinor, currency));
  const lastCommittedMinor = useRef(valueMinor);
  // The currency the text on screen is written in, tracked beside the amount
  // because the SCALE is part of what that text says: an offer switched from
  // EUR to VND holds the same minor integer and must not keep showing the euro
  // reading of it.
  const lastRenderedCurrency = useRef(currency);

  useEffect(() => {
    // Object.is, not ===: a refused amount travels as NaN, and NaN === NaN is
    // false, so the parent echoing our own NaN back read as an external change
    // and snapped the buffer mid-edit — the one thing this guard exists to
    // prevent.
    if (
      Object.is(valueMinor, lastCommittedMinor.current) &&
      currency === lastRenderedCurrency.current
    ) {
      return;
    }
    // Inlined rather than through asText: a function created each render is a
    // dependency this effect must not have — listing it re-runs the resync on
    // every render and snaps the text mid-edit, which is the whole thing
    // lastCommittedMinor exists to prevent.
    setText(
      blankWhenZero && valueMinor === 0
        ? ""
        : toMajorUnits(valueMinor, currency).toFixed(minorUnitDigits(currency)),
    );
    lastCommittedMinor.current = valueMinor;
    lastRenderedCurrency.current = currency;
  }, [valueMinor, currency, blankWhenZero]);

  return (
    <TextInput
      type="number"
      value={text}
      onChange={(event) => {
        setText(event.target.value);
        // An empty or unparseable buffer (mid-edit, e.g. a lone "-" or a
        // cleared field) is never committed as 0 — the last valid minor
        // value stands until the user finishes typing a real number, and the
        // blur below snaps the text back to it.
        //
        // This is deliberate and it has a cost worth naming: a reader cannot
        // clear a priced field back to "no amount" by deleting it. That is
        // right for an offer line, which must have a price, and wrong for an
        // agreement that may legitimately be unpriced — but the remedy is an
        // explicit control on the form that means "remove this value", not a
        // component that reads a half-deleted buffer as an intention. Changing
        // it here would also make every mid-edit keystroke commit a zero.
        if (event.target.value.trim() === "") {
          return;
        }
        // A figure with more decimals than the currency HAS is not committed.
        // `step` governs native validity and nothing else — onChange still
        // fires, so typing "1.5" into a dong field rounded to 2 minor units and
        // an offer line saved the altered amount on blur. Holding the text
        // instead lets the reader finish or correct it, exactly as an empty
        // buffer is held above, and nothing is stored that nobody typed.
        // isFinite, not !isNaN: a pasted overflowing exponent like 1e309
        // parses to Infinity, which is not NaN and would reach the request
        // body as a number no column can hold.
        const parsed = Number(event.target.value);
        if (!Number.isFinite(parsed) || exceedsPrecision(parsed, digits)) {
          return;
        }
        {
          const minor = toMinorUnits(parsed, currency);
          lastCommittedMinor.current = minor;
          onChangeMinor(minor);
        }
      }}
      onBlur={(event) => {
        setText(asText(lastCommittedMinor.current, currency));
        onBlur?.(event);
      }}
      {...rest}
      // type="number" defaults to step="1" — without this, a genuine
      // 2-decimal amount like "12.34" fails the input's native constraint
      // validation (:invalid, blocked form submission). The step is the
      // currency's smallest unit, so a zero-decimal currency refuses a
      // fractional dong instead of accepting one it cannot store.
      //
      // AFTER the spread, not before: a caller passing its own `step` would
      // otherwise silently replace the currency's, and native validation would
      // then admit a precision the storage scale cannot keep. This is the one
      // prop the component owns rather than accepts.
      step={digits === 0 ? "1" : `0.${"0".repeat(digits - 1)}1`}
    />
  );
}

// exceedsPrecision reports whether an amount needs finer division than its
// currency has — a half dong, a tenth of a cent.
//
// Measured on the VALUE, not on the typed text. Counting written decimals is
// the obvious approach and it is wrong in both directions once exponent
// notation is allowed: `15e-1` has no decimal point and is one and a half,
// while `150e-1` has none either and is exactly fifteen. Rounding to the
// currency's own digits and asking whether the number moved answers both, and
// every other notation, without a parser.
function exceedsPrecision(value: number, digits: number): boolean {
  // At 1e21 and beyond toFixed returns EXPONENTIAL notation, so the round-trip
  // compares equal and the guard passes a value toMinorUnits then refuses as
  // NaN — which the parent echoes back and the buffer snaps to. Such a value is
  // over the safe-integer bound many times over, so it is refused here instead,
  // where refusing means the text simply stands.
  // The SCALED value has to be exact, not the typed one. A KWD amount can be a
  // safe integer in dinars and past the bound once multiplied by a thousand,
  // and this guard passing there hands toMinorUnits a figure it answers NaN to
  // — which the parent echoes back and the buffer snaps to.
  //
  // It also covers the exponent case: at 1e21 and beyond toFixed returns
  // exponential notation and the round-trip below compares equal.
  if (!Number.isSafeInteger(Math.trunc(value) * 10 ** digits)) {
    return true;
  }
  return Number(value.toFixed(digits)) !== value;
}
