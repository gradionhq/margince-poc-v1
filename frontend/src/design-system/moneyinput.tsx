import { type InputHTMLAttributes, useEffect, useRef, useState } from "react";
import { TextInput } from "./atoms";

// A thin wrapper around TextInput type="number" that displays/edits MAJOR
// units while emitting MINOR units, matching the major→minor convention
// already established in deals.tsx (Math.round(Number(amount) * 100)) and
// products.tsx's toMinor helper. This assumes 2-decimal currencies only —
// there is no second caller needing a different decimal count today, so
// there is no currency prop to thread through for one.
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
  onChangeMinor,
  onBlur,
  ...rest
}: Readonly<
  Omit<InputHTMLAttributes<HTMLInputElement>, "value" | "onChange" | "type"> & {
    valueMinor: number;
    onChangeMinor: (minor: number) => void;
  }
>) {
  const [text, setText] = useState((valueMinor / 100).toFixed(2));
  const lastCommittedMinor = useRef(valueMinor);

  useEffect(() => {
    if (valueMinor !== lastCommittedMinor.current) {
      setText((valueMinor / 100).toFixed(2));
      lastCommittedMinor.current = valueMinor;
    }
  }, [valueMinor]);

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
          const minor = Math.round(parsed * 100);
          lastCommittedMinor.current = minor;
          onChangeMinor(minor);
        }
      }}
      onBlur={(event) => {
        setText((lastCommittedMinor.current / 100).toFixed(2));
        onBlur?.(event);
      }}
      // type="number" defaults to step="1" — without this, a genuine
      // 2-decimal amount like "12.34" fails the input's native constraint
      // validation (:invalid, blocked form submission).
      step="0.01"
      {...rest}
    />
  );
}
