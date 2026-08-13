// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { ReactNode } from "react";
import "./factlist.css";

// FactList: label→value pairs a reader scans rather than edits.
//
// It exists because ten sheets already style the same `<dl> > div > dt + dd`
// shape, and two sites style none at all — `person360.tsx` applies a
// `.fact-list` class no stylesheet has ever declared, so those rows render with
// the browser's 40px `dd` indent and no alignment at all. A primitive that
// nobody can forget to style is the fix; a eleventh sheet would not be.

/**
 * One row. `term` is what the value is, `value` is the value, and `note` is the
 * qualifier that belongs UNDER the value rather than beside it — how a figure
 * was reached, or that it is partial.
 *
 * Both sides are `ReactNode` because the real rows already carry more than
 * text: an evidence mark, a record link, a status pill, a monospace amount.
 * Formatting stays at the call site, where the locale is.
 */
export type Fact = Readonly<{
  /** Distinguishes rows that repeat the same `term`, which the real data does. */
  key: string;
  term: ReactNode;
  value: ReactNode;
  note?: ReactNode;
}>;

/**
 * A read-only fact table.
 *
 * Rows arrive as an array rather than as children so a caller filters absent
 * facts out before rendering — a `<dt>` with an empty `<dd>` reads as "we know
 * this and it is blank", which is a different claim from not knowing it. Where
 * a row must be shown as unknown, the caller passes the honest words as
 * `value`.
 *
 * `numeric` sets tabular figures on every value, for the case the workbench
 * runtime rows already handle by hand: a column of counts that would otherwise
 * shift as digits change width.
 */
export function FactList({
  facts,
  numeric,
  className,
}: Readonly<{
  facts: readonly Fact[];
  numeric?: boolean;
  className?: string;
}>) {
  return (
    <dl
      className={`factlist${numeric ? " factlist-numeric" : ""}${
        className ? ` ${className}` : ""
      }`}
    >
      {facts.map((fact) => (
        <div className="factlist-row" key={fact.key}>
          <dt className="factlist-term">{fact.term}</dt>
          <dd className="factlist-value">
            {fact.value}
            {fact.note !== undefined && (
              <small className="factlist-note">{fact.note}</small>
            )}
          </dd>
        </div>
      ))}
    </dl>
  );
}
