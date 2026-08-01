import { describe, expect, it } from "vitest";
import type { components } from "../api/schema";
import { distinctFields, groupByField, mergeChronology } from "./history.logic";

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

describe("mergeChronology", () => {
  const row = (at: string) => ({ at });
  const at = (r: { at: string }) => r.at;

  it("interleaves both feeds newest-first when both are fully loaded", () => {
    const merged = mergeChronology(
      [
        { rows: [row("2026-03-01"), row("2026-01-01")], hasMore: false },
        { rows: [row("2026-02-01")], hasMore: false },
      ],
      at,
    );
    expect(merged.rows.map(at)).toEqual([
      "2026-03-01",
      "2026-02-01",
      "2026-01-01",
    ]);
    expect(merged.truncated).toBe(false);
  });

  it("cuts at the oldest row of a feed that has more, so the merge has no invisible gaps", () => {
    // The activity feed stops at 2026-02-01 and has older rows unfetched.
    // A change from January must NOT render under it: between them sit
    // activities nobody has loaded.
    const merged = mergeChronology(
      [
        { rows: [row("2026-03-01"), row("2026-02-01")], hasMore: true },
        { rows: [row("2026-02-15"), row("2026-01-01")], hasMore: false },
      ],
      at,
    );
    expect(merged.rows.map(at)).toEqual([
      "2026-03-01",
      "2026-02-15",
      "2026-02-01",
    ]);
    expect(merged.truncated).toBe(true);
  });

  it("takes the newest boundary when both feeds have more, and keeps the boundary row", () => {
    // Both feeds are complete AT 2026-02-20 — the second loaded down to it,
    // the first past it — so that row belongs. Only what is strictly older
    // could be missing rows the second feed has not fetched.
    const merged = mergeChronology(
      [
        { rows: [row("2026-03-01"), row("2026-02-01")], hasMore: true },
        { rows: [row("2026-03-05"), row("2026-02-20")], hasMore: true },
      ],
      at,
    );
    expect(merged.rows.map(at)).toEqual([
      "2026-03-05",
      "2026-03-01",
      "2026-02-20",
    ]);
    expect(merged.truncated).toBe(true);
  });

  it("shows nothing when a feed has more but has loaded nothing yet", () => {
    // Its newest row is unknown, so no part of the merge is provably complete
    // — an empty in-flight state, never a list that looks whole.
    const merged = mergeChronology(
      [
        { rows: [row("2026-03-01")], hasMore: false },
        { rows: [], hasMore: true },
      ],
      at,
    );
    expect(merged.rows).toEqual([]);
    expect(merged.truncated).toBe(true);
  });

  it("returns an empty, untruncated merge when both feeds are empty and complete", () => {
    const merged = mergeChronology(
      [
        { rows: [], hasMore: false },
        { rows: [], hasMore: false },
      ],
      at,
    );
    expect(merged.rows).toEqual([]);
    expect(merged.truncated).toBe(false);
  });
});
