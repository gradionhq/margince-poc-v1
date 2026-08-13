// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { type ReactNode, useId } from "react";
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
 * `disabled` and `reason` are separate on purpose. A control can be
 * unavailable because the caller may not change it, or because a write is in
 * flight, and those want different words — or, for the in-flight case, no
 * words at all. Passing `reason` renders it and points the control at it with
 * `aria-describedby`, so the explanation reaches a screen reader rather than
 * sitting beside the control as decoration.
 */
export function Switch({
  label,
  labelHidden,
  hint,
  checked,
  onChange,
  disabled,
  reason,
  testId,
}: Readonly<{
  label: ReactNode;
  labelHidden?: boolean;
  /** A help line under the label — what the setting does, in the user's terms. */
  hint?: ReactNode;
  checked: boolean;
  onChange: (next: boolean) => void;
  disabled?: boolean;
  /** Why it cannot be changed. Rendered, and announced with the control. */
  reason?: ReactNode;
  testId?: string;
}>) {
  const hintId = useId();
  const reasonId = useId();
  // Both ids are minted unconditionally — hooks may not depend on which
  // optional props a caller passed — and only the ones with content are named,
  // so a reader is never pointed at an element that does not exist.
  const describedBy =
    [hint !== undefined ? hintId : null, reason !== undefined ? reasonId : null]
      .filter(Boolean)
      .join(" ") || undefined;

  return (
    <div className="switchrow">
      <button
        type="button"
        role="switch"
        aria-checked={checked}
        aria-describedby={describedBy}
        disabled={disabled}
        className="switchcontrol"
        data-testid={testId}
        onClick={() => onChange(!checked)}
      >
        {/* The track's knob. Decorative: the state is already on aria-checked,
            and announcing it twice is how a reader hears "on on". */}
        <span className="switchknob" aria-hidden="true" />
        <span className={labelHidden ? "sr-only" : "switchlabel"}>{label}</span>
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
