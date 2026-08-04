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
    // A message that HAS a subject renders it as the title, so the fixture
    // carries both. `extra` can drop it back to null for the subjectless rows,
    // whose title is the body or the kind.
    subject: title,
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

  it("marks the group holding the oldest entry, not the last group on screen", () => {
    // The thread ranks FIRST because a group takes the position of its newest
    // member, and it also holds the page's oldest message. Marking the last
    // group would offer to continue the single row — which is complete — and
    // stay silent about the conversation that is actually cut.
    const groups = groupChronology(
      [
        mail("t-new", "Re: Pricing", "2026-07-05T10:00:00Z", {
          threadKey: "t-1",
        }),
        mail("lone", "Intro", "2026-07-03T10:00:00Z"),
        mail("t-old", "Pricing", "2026-07-01T10:00:00Z", { threadKey: "t-1" }),
      ],
      true,
    );
    expect(groups[0].kind).toBe("thread");
    expect(groups[0].partial).toBe(true);
    expect(groups[1].partial).toBe(false);
  });

  it("does not let one attested message fold an unrelated same-subject reply", () => {
    // List-Unsubscribe is carried by ONE message. A reply that merely shares
    // the subject and the day is not part of the send the sender attested to,
    // and folding it would hide it inside a summary.
    const groups = groupChronology([
      mail("blast", "Produktupdate", "2026-07-17T09:00:00Z", {
        bulkAttested: true,
      }),
      mail("reply", "Re: Produktupdate", "2026-07-17T14:00:00Z"),
    ]);
    expect(groups).toHaveLength(2);
    expect(groups[0].kind).toBe("bulk");
    expect(groups[1].kind).toBe("single");
    expect(groups[1].entries.map((e) => e.id)).toEqual(["reply"]);
  });

  it("never bulk-folds subjectless messages, whatever their rendered title", () => {
    // A subjectless row renders its BODY as the title, and a wordless one
    // renders its kind. Keyed on the title, three of those on one day would
    // fold into a bulk send that was never sent.
    const groups = groupChronology([
      mail("1", "email", "2026-07-17T09:00:00Z", { subject: null }),
      mail("2", "email", "2026-07-17T09:00:01Z", { subject: null }),
      mail("3", "email", "2026-07-17T09:00:02Z", { subject: null }),
    ]);
    expect(groups).toHaveLength(3);
    expect(groups.every((g) => g.kind === "single")).toBe(true);
  });
});
