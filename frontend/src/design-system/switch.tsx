// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { type ReactNode, useId } from "react";
import { BusyMark } from "./atoms";
import "./switch.css";

// Switch: a setting that takes effect when you flip it.
//
// The distinction from `Checkbox` is not cosmetic and decides which one a
// surface reaches for. A checkbox is a form field — it states an intent that
// something later submits, so a page full of them has one Save. A switch IS
// the action: flipping it writes. That is why it announces itself as
// `role="switch"` and why it carries a pending state, which a checkbox has no
// use for.
//
// So: many-of-N inside a form is a `Checkbox` (passport scopes, a multi-select
// custom field). One setting that writes on change is a `Switch` (auto-enrich,
// a subscription preference). A filter over a list is neither — that is a
// pressed button, and `aria-pressed` already says so.

/**
 * The one setting toggle.
 *
 * `label` is always required, because a control with no accessible name is not
 * operable by anyone who cannot see it — but `labelHidden` covers the two
 * shapes the product already has. A row that renders its own rich heading
 * (a lock badge, a state line, prose about why it is fixed) passes
 * `labelHidden` and keeps its layout; a plain setting lets the switch draw the
 * label and gets the whole thing as one click target.
 *
 * `disabled`, `reason` and `pending` are three separate props on purpose,
 * because a control can be unavailable for three different reasons and only
 * one of them is worth words. The caller may not change it (`disabled`); the
 * caller may not change it *and* there is a sentence saying why (`reason`,
 * announced through `aria-describedby` so it reaches a screen reader rather
 * than sitting beside the control as decoration); or the flip they just made
 * is still being written (`pending`, which needs no words and must not look
 * like either of the other two).
 *
 * Two of those three refuse the flip, and `reason` does it on its own: a
 * sentence explaining why a reader may not change a setting they then can
 * change is worse than silence, because they read the denial, flip it anyway,
 * and only the server says no.
 */
export function Switch({
  describedBy: describedByProp,
  label,
  labelHidden,
  hint,
  checked,
  onChange,
  disabled,
  reason,
  pending,
  testId,
}: Readonly<{
  label: ReactNode;
  labelHidden?: boolean;
  /** A help line under the label — what the setting does, in the user's terms. */
  hint?: ReactNode;
  /**
   * Whether the setting is on. Required, so a caller still has to say — but
   * typed to admit `undefined`, because the value is routinely read off a
   * server payload and a body that lost a contract-required field hands one
   * over anyway. `role="switch"` has exactly two states and no third, so the
   * control renders OFF for an absent one: the same thing the knob draws, and
   * the only reading ARIA can carry. Whether a payload that thin should reach
   * a control at all is a question for the query boundary, not for a toggle.
   */
  checked: boolean | undefined;
  onChange: (next: boolean) => void;
  disabled?: boolean;
  /**
   * Why it cannot be changed. Rendered, announced with the control through
   * `aria-describedby`, and refusing the flip by itself — a caller does not
   * have to remember `disabled` beside it, and cannot defeat it by passing
   * `disabled={false}`. `Button.reason` carries the same contract.
   */
  reason?: ReactNode;
  /**
   * Whether the flip the reader just made is still being written.
   *
   * Same contract as `Button`'s, and for the same reason: it sets
   * `aria-disabled` rather than `disabled`, so the control the reader is
   * standing on keeps focus while it waits instead of dropping them to
   * `<body>`, and the press is refused in the handler because `aria-disabled`
   * binds assistive technology and not the browser. `disabled` outranks it — a
   * switch nobody may flip cannot be mid-flip.
   */
  pending?: boolean;
  /**
   * An element elsewhere that describes this setting, named ALONGSIDE the
   * switch's own hint and reason rather than instead of them.
   *
   * It exists for `SettingRow`. The row draws the naming for the whole
   * decision, so a converted card moves what used to be the switch's `hint`
   * into the row's `description` — and a `Switch` passed as a node cannot see
   * the id the row minted for it. Without this the sentence saying what the
   * setting DOES stopped reaching anybody who cannot see it, which is the one
   * guarantee the pre-row spelling did make. Callers hand it the row's
   * `aria-describedby` through the function form of `control`.
   */
  describedBy?: string;
  testId?: string;
}>) {
  const hintId = useId();
  const reasonId = useId();
  // Every id is minted unconditionally — hooks may not depend on which optional
  // props a caller passed — and only the ones with content are named, so a
  // reader is never pointed at an element that does not exist. The caller's own
  // id comes FIRST: it describes the setting, and the hint and the refusal
  // qualify it.
  const describedBy =
    [
      describedByProp,
      hint !== undefined ? hintId : null,
      reason !== undefined ? reasonId : null,
    ]
      .filter(Boolean)
      .join(" ") || undefined;

  // Resolved once, and used for BOTH the announced state and the value handed
  // back on click, so what a reader hears and what the next write carries can
  // never disagree.
  const on = checked === true;
  // A stated reason IS a refusal, not a note beside one, and it holds whatever
  // the caller passes for `disabled` — the same reading `Button` gives its own
  // `reason`. Left as two independent props, a caller who said why the setting
  // was locked and stopped there shipped a live switch that announced its own
  // denial and then wrote anyway.
  const refused = reason !== undefined;
  // Refusal beats busy in both its spellings, exactly as `Button` reads it. A
  // switch carrying `reason` is one this reader may not change, so drawing the
  // mark beside a sentence that says "your seat cannot change this" would tell
  // them their write is going through and that they were never allowed to make
  // it, in the same row.
  const busy = pending === true && disabled !== true && !refused;

  return (
    <div className="switchrow">
      <button
        type="button"
        role="switch"
        aria-disabled={busy || undefined}
        aria-busy={busy || undefined}
        // `on`, never the raw prop: React omits an attribute whose value is
        // undefined, and a `role="switch"` with no `aria-checked` is a control
        // that announces no state at all — WCAG 4.1.2, which axe reports as
        // `aria-required-attr`. The stylesheet keys the knob off this same
        // attribute, so dropping it also left the track drawn as off with
        // nothing saying so.
        aria-checked={on}
        aria-describedby={describedBy}
        disabled={disabled || refused}
        className="switchcontrol"
        data-testid={testId}
        // A second flip while the first is still being written would send a
        // value derived from a state the server has not confirmed, and the two
        // writes could land in either order. `aria-disabled` says so; this is
        // what makes it true. The press is stopped rather than merely ignored,
        // so a row this switch sits inside does not treat it as a click on
        // itself.
        onClick={
          busy
            ? (event) => event.stopPropagation()
            : () => {
                onChange(!on);
              }
        }
      >
        {/* The track's knob. Decorative: the state is already on aria-checked,
            and announcing it twice is how a reader hears "on on". */}
        <span className="switchknob" aria-hidden="true" />
        <span className={labelHidden ? "sr-only" : "switchlabel"}>{label}</span>
        {/* After the label rather than over the knob: the knob is the only
            thing showing which way the setting is currently set, and covering
            it during the write hides the state the reader is changing FROM. */}
        {busy && <BusyMark />}
      </button>
      {hint !== undefined && (
        <p className="switchhint" id={hintId}>
          {hint}
        </p>
      )}
      {reason !== undefined && (
        <p className="switchreason" id={reasonId}>
          {reason}
        </p>
      )}
    </div>
  );
}
