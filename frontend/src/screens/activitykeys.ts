import type { QueryKey } from "@tanstack/react-query";
import type { EntityKind } from "../app/entity";

// Which cached reads render a record's timeline. Writing an activity has to
// invalidate ALL of them, and they are not the same set for every record kind:
// the person and deal screens read the timeline through its own
// ["activities", kind, id] query, while the company screen renders it out of
// the composite 360 payload. A mutation that names only the first key writes
// successfully and shows nothing, which reads exactly like a broken endpoint.
//
// Derived here once rather than spelled at each mutation site, so a new screen
// that renders a timeline from a different read is fixed in one place.

export function entityTimelineKeys(
  entityType: EntityKind,
  entityId: string,
): QueryKey[] {
  const keys: QueryKey[] = [["activities", entityType, entityId]];
  if (entityType === "organization") {
    keys.push(["organization360", entityId]);
  }
  return keys;
}

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
