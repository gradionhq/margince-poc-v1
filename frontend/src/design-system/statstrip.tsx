// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { Children, type ReactNode } from "react";
import "./statstrip.css";

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
  return (
    <section
      className={["stat-strip", className ?? ""].filter(Boolean).join(" ")}
      style={{ "--stat-strip-slots": slots } as React.CSSProperties}
      data-testid={testId}
    >
      {children}
    </section>
  );
}
