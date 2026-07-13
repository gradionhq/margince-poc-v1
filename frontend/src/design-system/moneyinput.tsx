import type { InputHTMLAttributes } from "react";
import { TextInput } from "./atoms";

// A thin wrapper (Phase 3, task 3.2) around TextInput type="number" that
// displays/edits MAJOR units while emitting MINOR units, matching the
// major→minor convention already established in deals.tsx
// (Math.round(Number(amount) * 100)) and products.tsx's toMinor helper.
// This assumes 2-decimal currencies only; `currency` is accepted for API
// symmetry/future use (non-2-decimal currencies) but not yet interpreted.

export function MoneyInput({
  valueMinor,
  currency: _currency,
  onChangeMinor,
  ...rest
}: Readonly<
  Omit<InputHTMLAttributes<HTMLInputElement>, "value" | "onChange" | "type"> & {
    valueMinor: number;
    currency: string;
    onChangeMinor: (minor: number) => void;
  }
>) {
  return (
    <TextInput
      type="number"
      value={(valueMinor / 100).toFixed(2)}
      onChange={(event) =>
        onChangeMinor(Math.round(Number(event.target.value) * 100))
      }
      {...rest}
    />
  );
}
