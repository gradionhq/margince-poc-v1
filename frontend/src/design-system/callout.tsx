// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { LucideIcon } from "lucide-react";
import type { ReactNode } from "react";
import "./callout.css";

// Callout: something the surface says ABOUT itself, rather than content.
//
// The product had fourteen spellings of this — three app banners duplicating
// the same inline style object, nine screen-local classes, and a handful of
// bare paragraphs tinted by hand. One of them, `.co-callout`, declared only a
// top margin: the name promised a callout and the sheet delivered whitespace.
//
// The tones are a closed set because they are claims, not decoration. `warn`
// says something will go wrong if you do nothing; `danger` says something is
// wrong or is about to be irreversible; `success` confirms an action landed;
// `info` is the default and carries no urgency at all. A surface that wants a
// fifth is usually reaching for emphasis, which is what the words are for.

export type CalloutTone = "info" | "warn" | "danger" | "success";

/**
 * A bordered notice with an optional heading, icon and actions.
 *
 * `live` decides how a screen reader learns about it, and the honest answer
 * depends on why it appeared. A notice rendered with the page needs nothing.
 * One that appears in response to what the user just did is `status`. One that
 * reports a failure they must act on is `alert`, which interrupts. Passing
 * `alert` to something merely informative is how a reader learns to ignore
 * them, so the default is silent.
 *
 * Copy never lives here: every word arrives as a prop, translated by the
 * caller.
 */
export function Callout({
  tone = "info",
  icon: Icon,
  title,
  actions,
  live,
  className,
  children,
}: Readonly<{
  tone?: CalloutTone;
  icon?: LucideIcon;
  title?: ReactNode;
  /** Buttons or links, laid out after the body. */
  actions?: ReactNode;
  live?: "status" | "alert";
  className?: string;
  children: ReactNode;
}>) {
  return (
    <div
      className={`callout callout-${tone}${className ? ` ${className}` : ""}`}
      role={live}
    >
      {Icon && (
        <span className="callout-icon" aria-hidden="true">
          <Icon size={16} strokeWidth={2} />
        </span>
      )}
      <div className="callout-body">
        {title !== undefined && <p className="callout-title">{title}</p>}
        <div className="callout-text">{children}</div>
        {actions !== undefined && (
          <div className="callout-actions">{actions}</div>
        )}
      </div>
    </div>
  );
}
