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
  ...rest
}: Readonly<
  Omit<InputHTMLAttributes<HTMLInputElement>, "value" | "onChange" | "type"> & {
    valueMinor: number;
    currency: string;
    onChangeMinor: (minor: number) => void;
  }
>) {
  const digits = minorUnitDigits(currency);
  const [text, setText] = useState(() =>
    toMajorUnits(valueMinor, currency).toFixed(digits),
  );
  const lastCommittedMinor = useRef(valueMinor);
  // The currency the text on screen is written in, tracked beside the amount
  // because the SCALE is part of what that text says: an offer switched from
  // EUR to VND holds the same minor integer and must not keep showing the euro
  // reading of it.
  const lastRenderedCurrency = useRef(currency);

  useEffect(() => {
    if (
      valueMinor === lastCommittedMinor.current &&
      currency === lastRenderedCurrency.current
    ) {
      return;
    }
    setText(
      toMajorUnits(valueMinor, currency).toFixed(minorUnitDigits(currency)),
    );
    lastCommittedMinor.current = valueMinor;
    lastRenderedCurrency.current = currency;
  }, [valueMinor, currency]);

  return (
    <TextInput
      type="number"
      value={text}
      onChange={(event) => {
        setText(event.target.value);
        // An empty or unparseable buffer (mid-edit, e.g. a lone "-" or a
        // cleared field) is never committed as 0 — the last valid minor
        // value stands until the user finishes typing a real number.
        if (event.target.value.trim() === "") {
          return;
        }
        const parsed = Number(event.target.value);
        if (!Number.isNaN(parsed)) {
          const minor = toMinorUnits(parsed, currency);
          lastCommittedMinor.current = minor;
          onChangeMinor(minor);
        }
      }}
      onBlur={(event) => {
        setText(
          toMajorUnits(lastCommittedMinor.current, currency).toFixed(digits),
        );
        onBlur?.(event);
      }}
      // type="number" defaults to step="1" — without this, a genuine
      // 2-decimal amount like "12.34" fails the input's native constraint
      // validation (:invalid, blocked form submission). The step is the
      // currency's smallest unit, so a zero-decimal currency refuses a
      // fractional dong instead of accepting one it cannot store.
      step={digits === 0 ? "1" : `0.${"0".repeat(digits - 1)}1`}
      {...rest}
    />
  );
}
