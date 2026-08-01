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
 */
export function mergeChronology<Row>(
  feeds: readonly Feed<Row>[],
  at: (row: Row) => string,
): { rows: Row[]; truncated: boolean } {
  const boundaries = feeds
    .filter((feed) => feed.hasMore && feed.rows.length > 0)
    .map((feed) => feed.rows.reduce((a, b) => (at(a) < at(b) ? a : b)))
    .map(at);
  // A feed that has more but loaded NOTHING bounds the merge at the top: its
  // very newest row is unknown, so no part of the merge is provably complete.
  const blind = feeds.some((feed) => feed.hasMore && feed.rows.length === 0);
  const floor =
    boundaries.length > 0
      ? boundaries.reduce((a, b) => (a > b ? a : b))
      : undefined;

  const all = feeds
    .flatMap((feed) => feed.rows)
    .sort((a, b) => at(b).localeCompare(at(a)));
  if (blind) {
    return { rows: [], truncated: true };
  }
  if (floor === undefined) {
    return { rows: all, truncated: false };
  }
  const rows = all.filter((row) => at(row) >= floor);
  return {
    rows,
    truncated: rows.length < all.length || feeds.some((f) => f.hasMore),
  };
}
