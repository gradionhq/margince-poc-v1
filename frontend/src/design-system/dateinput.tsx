// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { ComponentPropsWithRef } from "react";

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
export function DateInput(props: Omit<ComponentPropsWithRef<"input">, "type">) {
  return (
    <input
      {...props}
      type="date"
      className={`input ${props.className ?? ""}`.trim()}
    />
  );
}
