// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { Children, type CSSProperties, type ReactNode } from "react";
import "./statstrip.css";

// The strip's style carries the slot-count custom properties alongside the
// standard properties, so the object literal needs those keys typed rather
// than cast away.
type StripVars = CSSProperties & Record<`--${string}`, string | number>;

// StatStrip is the record page's readings row: ONE plate of equal slots
// divided by rules, not N free-standing cards. The difference is what the row
// claims — cards sit beside each other and are read one at a time, a strip is
// read across as a single comparison.
//
// It takes StatCards as children and owns only the plate: the slot count, the
// rules between slots, the fold when the row stops being legible, and the one
// type scale every slot in the row shares. A slot that sized itself to its own
// content would stop the row reading as one comparison — some slots carry a
// figure and some carry a sentence.
export function StatStrip({
  children,
  className,
  testId,
}: Readonly<{ children: ReactNode; className?: string; testId?: string }>) {
  // The column count follows the slots the caller actually drew. A fixed
  // template reserves cells nobody fills, and an empty cell on a plate reads
  // as a reading that failed to load rather than as one this record does not
  // have. `toArray` drops the nulls a conditional slot leaves behind, so the
  // count is what is on screen.
  const slots = Children.toArray(children).length;
  // The fold breakpoints cap at the same count rather than at the sheet's own
  // 3-then-2 ladder: `repeat()` needs an integer, so the cap can only come
  // from here, where the slot count is already known. A two-slot strip folds
  // to two columns at every width instead of inventing a third, empty one.
  const vars: StripVars = {
    "--stat-strip-slots": slots,
    "--stat-strip-slots-3": Math.min(slots, 3),
    "--stat-strip-slots-2": Math.min(slots, 2),
  };
  return (
    <section
      className={["stat-strip", className ?? ""].filter(Boolean).join(" ")}
      style={vars}
      data-testid={testId}
    >
      {children}
    </section>
  );
}
