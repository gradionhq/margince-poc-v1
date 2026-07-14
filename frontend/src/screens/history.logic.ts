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
    changes: [...changes].sort((a, b) => b.changed_at.localeCompare(a.changed_at)),
  }));
}

// The actor facet control: "human" vs anything non-human (agent/connector/system).
export function matchesActor(entry: { actor_type: string }, facet: ActorFacet): boolean {
  if (facet === "all") return true;
  if (facet === "human") return entry.actor_type === "human";
  return entry.actor_type !== "human";
}

export function distinctFields(entries: FieldHistoryEntry[]): string[] {
  const seen: string[] = [];
  for (const entry of entries) {
    if (!seen.includes(entry.field)) seen.push(entry.field);
  }
  return seen;
}
