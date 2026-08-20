// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { type ReactNode, useId } from "react";
import "./settingrow.css";

// SettingList / SettingRow — one settings DECISION per row, spelled once.
//
// Every settings card in this product is a list of decisions, and before this
// pair each card laid its own out: a switch that drew its own label on the
// left, a `Field` whose label sat above a full-width select, a button in a
// `.card-actions` band under a paragraph. Three shapes for one question, so a
// reader scanning a page had to find the answer somewhere new in every card.
//
// The shape here is the one Discord's settings and every other legible settings
// surface converge on, for the reason that makes it legible rather than because
// it is theirs: the NAMING (what this is, and what it does) reads left as prose,
// and the ANSWER (what it is set to) sits at one x on the right, so the eye
// travels down a single column to audit a page instead of hunting per card.
//
// What decides `stack` over the default is COMPLEXITY, not size: a control that
// answers the row's question — a switch, a dropdown, a value with an Edit verb —
// belongs beside it, and a control that IS the subject rather than an answer to
// it (a table, a toggle matrix, a log) takes the full width below. A control
// that needs two inputs submitted together is neither: it belongs behind a verb
// in the right column, in a `Modal`, which keeps the row an answer.

/**
 * What a row hands its control so the two are wired without the caller
 * minting ids.
 *
 * `aria-labelledby` rather than a `<label for>`: the row draws the label for
 * the whole decision, including the `value` beside the control and the
 * description under it, and several of the controls that sit here — `Switch`'s
 * track, an Edit `Button` — are not labelable elements. One naming mechanism
 * that works for all of them beats a `<label>` that works for half.
 *
 * `aria-describedby` is `undefined` when the row has no description, so a
 * control never points at an element that is not on the page.
 */
export type SettingControlProps = Readonly<{
  id: string;
  "aria-labelledby": string;
  "aria-describedby": string | undefined;
}>;

/**
 * One setting: what it is on the left, what it is set to on the right.
 *
 * `control` takes either a node or a function. Reach for the function form
 * whenever the control can carry ARIA — `Select`, `TextInput`, `Textarea`,
 * anything spread onto a native element — because that is what keeps the
 * label the row draws and the name the control announces the same string. A
 * `Switch` is the node form with `labelHidden` beside its `label`: it owns its
 * own hidden label by design (see switch.tsx), and pointing it at the row's
 * span as well would name it twice.
 *
 * `value` is the row's current answer standing beside the verb that changes it
 * — "luitpold.me [Edit]". It is presentation of a fact the control does not
 * show, so a row whose control already shows the value (a switch, a dropdown)
 * passes nothing: a value printed twice on one row reads as two settings.
 */
export function SettingRow({
  label,
  description,
  value,
  control,
  layout = "inline",
  testId,
}: Readonly<{
  label: ReactNode;
  /** What the setting does, in the reader's terms. */
  description?: ReactNode;
  /** The answer the row currently holds, where the control does not show it. */
  value?: ReactNode;
  control: ReactNode | ((props: SettingControlProps) => ReactNode);
  /**
   * `inline` puts the control in the right column — the default, and correct
   * for anything that answers the row's question. `stack` gives it the full
   * width below the naming, for a control that IS the subject: a table, a
   * matrix, a log.
   */
  layout?: "inline" | "stack";
  testId?: string;
}>) {
  const base = useId();
  const labelId = `${base}-label`;
  const descriptionId = `${base}-description`;
  const controlProps: SettingControlProps = {
    id: `${base}-control`,
    "aria-labelledby": labelId,
    "aria-describedby": description === undefined ? undefined : descriptionId,
  };
  return (
    <div
      className={
        layout === "stack" ? "settingrow settingrow-stack" : "settingrow"
      }
      data-testid={testId}
    >
      <div className="settingrow-naming">
        <span className="t-label" id={labelId}>
          {label}
        </span>
        {description !== undefined && (
          <p className="settingrow-description" id={descriptionId}>
            {description}
          </p>
        )}
      </div>
      <div className="settingrow-control">
        {value !== undefined && (
          <span className="settingrow-value">{value}</span>
        )}
        {typeof control === "function" ? control(controlProps) : control}
      </div>
    </div>
  );
}

/**
 * The rows of one card, with a hairline between them.
 *
 * A list rather than a margin on each row, because the interval between two
 * decisions belongs to the card that holds both: a row that spaced itself
 * would space itself differently in the next card, which is the drift this
 * whole directory exists to stop. It takes anything as a child — a
 * `SettingRow`, a `Disclosure` holding the card's advanced half, a
 * `SurfaceState` standing in for a withheld row — and rules between them all
 * the same way.
 */
export function SettingList({
  children,
  testId,
}: Readonly<{ children: ReactNode; testId?: string }>) {
  return (
    <div className="settinglist" data-testid={testId}>
      {children}
    </div>
  );
}
