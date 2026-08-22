import type { QueryKey } from "@tanstack/react-query";
import type { EntityKind } from "../app/entity";

// Which cached reads render a record's timeline. Writing an activity has to
// invalidate ALL of them, and they are not the same set for every record kind:
// every record page reads older pages and narrowed reads through its own
// ["activities", kind, id] query, while the company, contact and project
// pages draw the FIRST page out of the composite 360 payload and fetch
// nothing under that key until the reader asks for more. A mutation that
// names only the first key writes successfully and shows nothing, which reads
// exactly like a broken endpoint.
//
// Derived here once rather than spelled at each mutation site, so a new screen
// that renders a timeline from a different read is fixed in one place.

export function entityTimelineKeys(
  entityType: EntityKind,
  entityId: string,
): QueryKey[] {
  const keys: QueryKey[] = [["activities", entityType, entityId]];
  const seed = TIMELINE_SEED_KEYS[entityType]?.(entityId);
  if (seed) {
    keys.push(seed);
  }
  return keys;
}

// The composite reads that carry a timeline's first page, by record kind —
// spelled the way each page's own query spells its key.
const TIMELINE_SEED_KEYS: Partial<
  Record<EntityKind, (entityId: string) => QueryKey>
> = {
  organization: (id) => ["organization360", id],
  person: (id) => ["person360", id],
  project: (id) => ["project", id, "360"],
};

// A task is also a row in the standing work queue, which is keyed per workspace
// rather than per record — so completing or logging one has to reach further
// than the record's own timeline.
export const TASK_QUEUE_KEY: QueryKey = ["tasks"];

export function taskWriteKeys(
  entityType: EntityKind,
  entityId: string,
): QueryKey[] {
  return [...entityTimelineKeys(entityType, entityId), TASK_QUEUE_KEY];
}
