import { describe, expect, it } from "vitest";
import type { TimelineEntry } from "../design-system/composed";
import { groupChronology } from "./timelinegroups";

function mail(
  id: string,
  title: string,
  atIso: string,
  extra: Partial<TimelineEntry> = {},
): TimelineEntry {
  return {
    id,
    kind: "email",
    title,
    atIso,
    provenance: { kind: "human", self: false },
    ...extra,
  };
}

describe("grouping the account's chronology", () => {
  it("folds one conversation into one event, newest first", () => {
    const groups = groupChronology([
      mail("c", "Re: Pricing", "2026-07-03T10:00:00Z", { threadKey: "t-1" }),
      mail("b", "Re: Pricing", "2026-07-02T10:00:00Z", { threadKey: "t-1" }),
      mail("a", "Pricing", "2026-07-01T10:00:00Z", { threadKey: "t-1" }),
    ]);
    expect(groups).toHaveLength(1);
    expect(groups[0].kind).toBe("thread");
    // The group takes the position and the id of its NEWEST member, so the
    // reader's sense of what happened last survives the grouping.
    expect(groups[0].id).toBe("c");
    expect(groups[0].entries.map((e) => e.id)).toEqual(["c", "b", "a"]);
  });

  it("keeps two conversations apart even when their subjects match", () => {
    const groups = groupChronology([
      mail("x", "Re: Update", "2026-07-03T10:00:00Z", { threadKey: "t-1" }),
      mail("y", "Re: Update", "2026-07-03T11:00:00Z", { threadKey: "t-2" }),
    ]);
    // Subject grouping would have merged these. The provider's id does not.
    expect(groups).toHaveLength(2);
  });

  it("keeps one conversation together when its subject was renamed", () => {
    const groups = groupChronology([
      mail("b", "Re: now about hosting", "2026-07-03T10:00:00Z", {
        threadKey: "t-1",
      }),
      mail("a", "Pricing", "2026-07-01T10:00:00Z", { threadKey: "t-1" }),
    ]);
    expect(groups).toHaveLength(1);
  });

  it("folds a bulk send addressed to several people into one event", () => {
    const groups = groupChronology([
      mail("1", "Update zu Margince", "2026-07-17T09:00:00Z"),
      mail("2", "Update zu Margince", "2026-07-17T09:00:01Z"),
      mail("3", "Update zu Margince", "2026-07-17T09:00:02Z"),
    ]);
    expect(groups).toHaveLength(1);
    expect(groups[0].kind).toBe("bulk");
    expect(groups[0].entries).toHaveLength(3);
  });

  it("trusts the sender's own bulk attestation over the copy count", () => {
    const groups = groupChronology([
      mail("1", "Newsletter", "2026-07-17T09:00:00Z", { bulkAttested: true }),
    ]);
    expect(groups[0].kind).toBe("bulk");
  });

  it("leaves two same-subject messages as two rows", () => {
    // Two is a coincidence, not a send. Folding them would hide a message
    // inside a summary nobody opened.
    const groups = groupChronology([
      mail("1", "Question", "2026-07-17T09:00:00Z"),
      mail("2", "Question", "2026-07-17T18:00:00Z"),
    ]);
    expect(groups).toHaveLength(2);
    expect(groups.every((g) => g.kind === "single")).toBe(true);
  });

  it("does not fold the same subject sent on different days", () => {
    const groups = groupChronology([
      mail("1", "Weekly", "2026-07-17T09:00:00Z"),
      mail("2", "Weekly", "2026-07-18T09:00:00Z"),
      mail("3", "Weekly", "2026-07-19T09:00:00Z"),
    ]);
    expect(groups).toHaveLength(3);
  });

  it("never groups record changes", () => {
    const change = (id: string, atIso: string): TimelineEntry => ({
      id,
      kind: "change",
      title: "stage",
      atIso,
      provenance: { kind: "human", self: false },
    });
    const groups = groupChronology([
      change("c1", "2026-07-17T09:00:00Z"),
      change("c2", "2026-07-17T09:00:01Z"),
    ]);
    // Two edits are two facts.
    expect(groups).toHaveLength(2);
  });

  it("marks only the oldest group as possibly continuing past the page", () => {
    const groups = groupChronology(
      [
        mail("b", "Re: Pricing", "2026-07-03T10:00:00Z", { threadKey: "t-1" }),
        mail("a", "Intro", "2026-07-01T10:00:00Z", { threadKey: "t-2" }),
      ],
      true,
    );
    expect(groups[0].partial).toBe(false);
    // Only the oldest can have members beyond the edge of what the page holds.
    expect(groups[1].partial).toBe(true);
  });
});
