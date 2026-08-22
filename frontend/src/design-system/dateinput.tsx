// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { ComponentPropsWithRef } from "react";

/**
 * A calendar date written the way this control and the contract's `format: date`
 * fields both spell it.
 *
 * A type cannot rule out `2026-02-30`, and the element itself sanitizes anything
 * it cannot read down to `""` — so this is not validation. What it does rule out
 * is the class of mistake a caller actually makes: handing over a `Date`, an
 * epoch number, or a locale-formatted string like `18/07/2026`, each of which
 * would blank the field at runtime with nothing to say why.
 */
export type ISODate = `${number}-${number}-${number}`;

/**
 * isISODate is how a string the element reported becomes a `value` this
 * control accepts again. The element only ever reports `YYYY-MM-DD` or `""`,
 * so this narrows rather than validates — the same caveat as the type.
 */
export function isISODate(raw: string): raw is ISODate {
  return /^\d{4}-\d{2}-\d{2}$/.test(raw);
}

/**
 * `value` and `defaultValue` are narrowed from React's
 * `string | number | readonly string[]`; the rest of an input's props pass
 * through. `""` is admitted because a cleared date field is the ordinary empty
 * state, and it is what the element reports for a value it rejected.
 */
export type DateInputProps = Omit<
  ComponentPropsWithRef<"input">,
  "type" | "value" | "defaultValue"
> &
  Readonly<{
    value?: ISODate | "";
    defaultValue?: ISODate | "";
  }>;

/**
 * The one date field.
 *
 * A native `type="date"` rather than a hand-rolled calendar, and that is a
 * deliberate exception to the rule that governs `Select`. The rule exists
 * because a browser draws `<select>`'s option list in the platform's own idiom
 * and no CSS reaches it — a visible hole in a surface built entirely from these
 * tokens. A date input is the opposite case: its CLOSED FACE is an ordinary text
 * box that takes our tokens completely, and what the platform draws is the
 * picker popover, which appears only on request and is the one part users
 * already know how to drive. Reimplementing it would mean owning keyboard
 * navigation, locale-aware parsing, and a month grid's accessibility — to
 * replace something that ships correct.
 *
 * What it does own is the FORMAT. `value` is always `YYYY-MM-DD`, which is what
 * the HTML spec requires of the element and, not coincidentally, what the
 * contract's `format: date` fields carry — so a value round-trips from wire to
 * control to wire without a parse step anyone can get wrong. A caller with a
 * `Date` converts before it gets here, on purpose: this control never guesses at
 * a timezone.
 *
 * No label of its own, exactly like `TextInput` — the label is composed outside
 * by `Field` or a screen's own shell.
 */
export function DateInput(props: DateInputProps) {
  return (
    <input
      {...props}
      type="date"
      className={`input ${props.className ?? ""}`.trim()}
    />
  );
}
