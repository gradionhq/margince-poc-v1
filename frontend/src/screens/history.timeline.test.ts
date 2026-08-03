import { describe, expect, it } from "vitest";
import type { components } from "../api/schema";
import { changeTimeline } from "./history";

type FieldHistoryEntry = components["schemas"]["FieldHistoryEntry"];

// One audit row, three fields. The projection emits one entry per field and
// they all carry the AUDIT row's id, so the id alone does not identify a row.
const oneWriteThreeFields: FieldHistoryEntry[] = [
  "industry",
  "size_band",
  "legal_name",
].map((field) => ({
  id: "a-1",
  entity_type: "organization",
  entity_id: "o-1",
  field,
  old_value: null,
  new_value: "x",
  changed_at: "2026-07-14T10:00:00Z",
  actor_type: "human",
  // The spine stores the principal id, not the bare user id.
  actor_id: "human:u-1",
}));

describe("changeTimeline", () => {
  it("gives each field its own row identity within one audit write", () => {
    const rows = changeTimeline(oneWriteThreeFields, (field) => field);
    expect(new Set(rows.map((row) => row.id)).size).toBe(3);
  });

  it("labels the field and keeps the change time", () => {
    const [row] = changeTimeline(oneWriteThreeFields, () => "Industry");
    expect(row.title).toBe("Industry");
    expect(row.atIso).toBe("2026-07-14T10:00:00Z");
    expect(row.kind).toBe("change");
  });

  it("matches the reader through the principal prefix the spine stores", () => {
    const mine = changeTimeline(oneWriteThreeFields, (f) => f, "u-1");
    expect(mine[0].provenance).toEqual({
      kind: "human",
      self: true,
      userId: "u-1",
    });
    const theirs = changeTimeline(oneWriteThreeFields, (f) => f, "u-2");
    expect(theirs[0].provenance).toEqual({
      kind: "human",
      self: false,
      userId: "u-1",
    });
  });
});
