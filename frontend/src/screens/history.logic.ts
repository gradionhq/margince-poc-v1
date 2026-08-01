import type { components } from "../api/schema";

type FieldHistoryEntry = components["schemas"]["FieldHistoryEntry"];

export type ActorFacet = "all" | "human" | "agent";
export type FieldGroup = { field: string; changes: FieldHistoryEntry[] };

// Group field-history rows by field for the mockup's per-field sections.
// First-seen field order is preserved; within a group, newest change first.
export function groupByField(entries: FieldHistoryEntry[]): FieldGroup[] {
  const byField = new Map<string, FieldHistoryEntry[]>();
  for (const entry of entries) {
    const bucket = byField.get(entry.field);
    if (bucket) {
      bucket.push(entry);
    } else {
      byField.set(entry.field, [entry]);
    }
  }
  return [...byField.entries()].map(([field, changes]) => ({
    field,
    changes: [...changes].sort((a, b) =>
      b.changed_at.localeCompare(a.changed_at),
    ),
  }));
}

export function distinctFields(entries: FieldHistoryEntry[]): string[] {
  const seen: string[] = [];
  for (const entry of entries) {
    if (!seen.includes(entry.field)) seen.push(entry.field);
  }
  return seen;
}

// One feed going into a merged chronology: the rows loaded so far, and
// whether older ones exist that have not been fetched.
export type Feed<Row> = { rows: Row[]; hasMore: boolean };

/**
 * Interleave two independently-paged feeds into one chronology, cut at the
 * point where it stops being complete.
 *
 * Two feeds paged separately cannot simply be concatenated and sorted: below
 * the oldest row of a feed that still has more, the merge is missing rows it
 * does not know about, and the result reads as a complete history with gaps in
 * it — the one failure a reader cannot see. So the merged list ends at the
 * newest such boundary, and the caller says what was cut.
 *
 * A feed that is fully loaded imposes no boundary: nothing older is missing
 * from it. When neither feed has more, the merge is the whole history.
 *
 * The cut is STRICT. Both feeds page on (timestamp, id), not on timestamp
 * alone, so a feed whose oldest loaded row sits at T may still have unfetched
 * rows at exactly T. Keeping the boundary row would put a same-second gap
 * inside a stretch the merge presents as whole — which is the one failure this
 * function exists to prevent, reappearing one row further down.
 */
export function mergeChronology<Row>(
  feeds: readonly Feed<Row>[],
  at: (row: Row) => string,
): { rows: Row[]; truncated: boolean } {
  // Instants, never the strings that spell them. Two feeds are written by two
  // stores, and "2026-07-19T09:00:00Z" sorts against "2026-07-19T09:00:00.5Z"
  // and "2026-07-19T11:00:00+02:00" by character, which orders neither the way
  // the clock does. The whole point of this function is an order a reader can
  // trust, so it compares numbers.
  const instant = (row: Row) => Date.parse(at(row));
  const boundaries = feeds
    .filter((feed) => feed.hasMore && feed.rows.length > 0)
    .map((feed) =>
      feed.rows.reduce((a, b) => (instant(a) < instant(b) ? a : b)),
    )
    .map(instant);
  // A feed that has more but loaded NOTHING bounds the merge at the top: its
  // very newest row is unknown, so no part of the merge is provably complete.
  const blind = feeds.some((feed) => feed.hasMore && feed.rows.length === 0);
  const floor =
    boundaries.length > 0
      ? boundaries.reduce((a, b) => (a > b ? a : b))
      : undefined;

  const all = feeds
    .flatMap((feed) => feed.rows)
    .sort((a, b) => instant(b) - instant(a));
  if (blind) {
    return { rows: [], truncated: true };
  }
  if (floor === undefined) {
    return { rows: all, truncated: false };
  }
  const rows = all.filter((row) => instant(row) > floor);
  // A floor exists only because some feed reported more, so the merged view is
  // short of the account's history by construction — whether or not this cut
  // dropped any loaded row.
  return { rows, truncated: true };
}
