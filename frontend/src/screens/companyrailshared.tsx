// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { Badge } from "../design-system/atoms";
import type { SectionState } from "./company360";

// Small pieces companyrail.tsx and companyrailtags.tsx both draw a section
// summary off — a leaf so neither file imports the other, the same no-cycle
// shape companylookups.ts already keeps for organizations.tsx/company360.tsx.

// A section's collapsible summary: the name, plus how many rows it carries.
// Common to every section but Health, which shows its verdict there instead
// (a count would say how many dimensions when the number a reader wants is
// how good they are).
//
// `count` is absent, not zero, while the section is withheld, unavailable or
// still loading: a "0" badge above the restricted notice below it reads as an
// empty account rather than a permission boundary, the exact conflation
// sectionState exists to keep apart.
export function SectionSummary({
  title,
  count,
}: Readonly<{ title: string; count?: number }>) {
  return (
    <span className="co-sect-summary">
      {title}
      {count != null && <Badge>{count}</Badge>}
    </span>
  );
}

// A section has answered, one way or the other, only once it is ready or
// empty; withheld, unavailable, loading and failed are all still "we don't
// know yet" from the reader's side of the count badge.
export function sectionAnswered(state: SectionState): boolean {
  return state === "ready" || state === "empty";
}
