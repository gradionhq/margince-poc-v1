import { describe, expect, it } from "vitest";
import type { components } from "../api/schema";
import { distinctFields, groupByField } from "./history.logic";

type FieldHistoryEntry = components["schemas"]["FieldHistoryEntry"];

const e = (
  field: string,
  at: string,
  actor: FieldHistoryEntry["actor_type"] = "human",
) =>
  ({
    id: at,
    entity_type: "deal",
    entity_id: "d1",
    field,
    old_value: null,
    new_value: "x",
    changed_at: at,
    actor_type: actor,
    actor_id: "u1",
  }) as const;

describe("groupByField", () => {
  it("groups entries by field, newest-first within a group, first-seen field order", () => {
    const groups = groupByField([
      e("name", "2026-01-01"),
      e("amount", "2026-01-02"),
      e("name", "2026-03-01"),
    ]);
    expect(groups.map((g) => g.field)).toEqual(["name", "amount"]);
    expect(groups[0].changes.map((c) => c.changed_at)).toEqual([
      "2026-03-01",
      "2026-01-01",
    ]);
  });
  it("returns [] for no entries", () => {
    expect(groupByField([])).toEqual([]);
  });
});

describe("distinctFields", () => {
  it("lists fields in first-seen order without dupes", () => {
    expect(
      distinctFields([e("name", "1"), e("amount", "2"), e("name", "3")]),
    ).toEqual(["name", "amount"]);
  });
});
