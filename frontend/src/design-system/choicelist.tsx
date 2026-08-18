// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useId } from "react";
import { Radio } from "./atoms";
import "./choicelist.css";

/**
 * One answer out of a few, with every answer READABLE AT REST.
 *
 * It exists because the product had the `Radio` atom and no radio GROUP, so
 * every caller wrapped it by hand — each with its own stack gap, its own idea of
 * whether the question was named at all, and at least one with no `fieldset`,
 * which leaves a screen reader announcing loose radios rather than one question
 * with two answers. The invariant this holds: a set of radios IS a question, so
 * it carries a name and a group, and neither is the caller's to forget.
 *
 * When to reach for this instead of the neighbours, because the difference is
 * about the reader and not about taste:
 *
 *  - **`Select`** hides every option but one behind a click. Right for a long or
 *    boring list — a stage, a currency, a date field. Wrong for a decision the
 *    reader has to weigh, and wrong for a BINARY: a dropdown covering two
 *    choices makes somebody open a menu to find out what the alternative even
 *    was.
 *  - **`SegmentedControl`** shows a closed set at once, in a tab strip, at 12.5px
 *    and bold. Right for short labels that name a view. Wrong the moment an
 *    option is a sentence.
 *  - **`Switch`** is one thing on or off. An either/or where BOTH sides need
 *    naming is not that: "everyone except the people I leave out" has no
 *    off-state a reader could infer.
 *
 * `description` is part of the LABEL, not a sibling — which is deliberate, and
 * the reason `Radio` takes a node: for a tick the words are the other half of
 * the click target, so a help line rendered outside the label is a line the
 * reader cannot click and a screen reader does not read with the option it
 * belongs to. Both go in, and the type carries the hierarchy.
 *
 * Copy never lives here: every word arrives as a prop, translated by the caller.
 */
export type Choice<Value extends string> = Readonly<{
  value: Value;
  /** The answer, written so it reads on its own. */
  label: string;
  /** What choosing it means, when the label cannot carry it alone. */
  description?: string;
}>;

export function ChoiceList<Value extends string>({
  legend,
  hideLegend,
  value,
  choices,
  disabled,
  layout = "stack",
  className,
  onChange,
}: Readonly<{
  /** The question. A group of answers with nothing naming the question is a set
   * of unrelated controls to anybody not looking at the layout. */
  legend: string;
  /**
   * Keeps the legend as the group's accessible name while taking it off screen,
   * for a caller whose surrounding layout already asks the question once — a
   * card whose heading IS the question. Visually hidden rather than dropped,
   * because the group still needs naming for a reader who cannot see that
   * heading's proximity.
   */
  hideLegend?: boolean;
  /** The chosen value, or "" for a question nobody has answered yet. */
  value: Value | "";
  choices: readonly Choice<Value>[];
  disabled?: boolean;
  /**
   * `stack` is the default because an answer that needs explaining needs a line
   * of its own; `row` is for a closed set of SHORT labels where the options read
   * as a range — a window of days, a page size — and a column would spend four
   * rows saying what one says.
   *
   * A `row` option with a `description` is the one combination to avoid: side by
   * side, the help lines set the options' widths against each other and the
   * shorter one reads as clipped.
   */
  layout?: "stack" | "row";
  /** Layout the surrounding form owns — the margin its own rhythm expects. It
   * lands on the fieldset, which is the only element a caller can position. */
  className?: string;
  onChange: (next: Value) => void;
}>) {
  // The name groups the radios, and the group is what makes arrow keys move
  // between them rather than tab. Minted here because it is a DOM detail no
  // caller should have to think about, and two ChoiceLists on one page with the
  // same hand-written name would silently become one question.
  const name = useId();
  return (
    // A `fieldset` and a `legend`, with NO `role` on top. `role="radiogroup"`
    // reads like the sharper claim and is the wrong one here: it is the role for
    // a group somebody built out of divs, and on native radio inputs the browser
    // already exposes the grouping — so the override adds nothing and takes the
    // element out of the mapping that gives `legend` its meaning. Every caller
    // that hand-rolled this reached for the ARIA pair because it had no
    // component; the component's answer is the native one.
    <fieldset
      className={["choicelist", `choicelist-${layout}`, className ?? ""]
        .filter(Boolean)
        .join(" ")}
    >
      <legend className={hideLegend ? "sr-only" : "choicelist-legend"}>
        {legend}
      </legend>
      {choices.map((choice) => (
        <Radio
          key={choice.value}
          className="choicelist-choice"
          name={name}
          value={choice.value}
          checked={choice.value === value}
          disabled={disabled}
          onChange={() => onChange(choice.value)}
          label={
            <span className="choicelist-text">
              <span className="choicelist-label">{choice.label}</span>
              {choice.description !== undefined && (
                <span className="choicelist-note">{choice.description}</span>
              )}
            </span>
          }
        />
      ))}
    </fieldset>
  );
}
