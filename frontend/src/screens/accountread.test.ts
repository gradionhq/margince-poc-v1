import { describe, expect, it } from "vitest";
import type { components } from "../api/schema";
import { QUIET_DAYS, readAccount } from "./accountread";

type Organization360 = components["schemas"]["Organization360"];

// A fixed clock. The quiet-account rule is a statement about elapsed time, so
// a test that read the wall clock would prove nothing about it.
const NOW = new Date("2026-07-31T09:00:00Z");
const daysAgo = (days: number) =>
  new Date(NOW.getTime() - days * 86_400_000).toISOString();

type Contact = NonNullable<Organization360["people"]>["data"][number];
type Deals = NonNullable<Organization360["deals"]>;
type Strength = NonNullable<Organization360["strength"]>;

// The builders fill only what the rules read, and are typed against the real
// schema so a contract change breaks these tests rather than silently making
// them assert about a shape the server no longer sends.
const FACTORS = { recency: 0, frequency: 0, reciprocity: 0, direction: 0 };

function contact(over: Partial<Contact> = {}): Contact {
  return {
    person_id: "p1",
    full_name: "Christian Hagemeyer",
    strength: { score: 12, bucket: "warm", factors: FACTORS },
    deal_roles: [],
    consent: {},
    ...over,
  };
}

function strength(over: Partial<Strength> = {}): Strength {
  return {
    score: 2,
    bucket: "warm",
    factors: FACTORS,
    contact_count: 3,
    ...over,
  };
}

function deals(over: Partial<Deals> = {}): Deals {
  return {
    data: [],
    page: { has_more: false },
    lost_count: 0,
    won_lifetime: { amount_minor: 0, currency: "EUR" },
    ...over,
  };
}

function view(over: Partial<Organization360> = {}): Organization360 {
  return {
    as_of: NOW.toISOString(),
    organization: {
      id: "o1",
      workspace_id: "w1",
      display_name: "ScaleCommerce",
      version: 1,
    } as Organization360["organization"],
    sections_omitted: [],
    ...over,
  };
}

const ids = (v: Organization360) => readAccount(v, NOW).map((f) => f.id);

describe("readAccount", () => {
  it("escalates the last-touch line from context to risk at the threshold", () => {
    // How long it has been is always said; only the tone changes, because
    // "we spoke Tuesday" and "we spoke in April" open differently.
    const recent = readAccount(
      view({
        strength: strength({ last_interaction: daysAgo(QUIET_DAYS - 1) }),
      }),
      NOW,
    ).find((f) => f.id === "quiet");
    expect(recent?.tone).toBe("neutral");
    expect(recent?.params?.days).toBe(QUIET_DAYS - 1);

    const stale = readAccount(
      view({ strength: strength({ last_interaction: daysAgo(QUIET_DAYS) }) }),
      NOW,
    ).find((f) => f.id === "quiet");
    expect(stale?.tone).toBe("risk");
  });

  it("names a shallow relationship even when every contact has some score", () => {
    // Three names each scoring a point or two is a populated-looking account
    // with no relationship in it. The server's bucket is the authority.
    const v = view({
      strength: strength({
        score: 2,
        bucket: "weak",
        last_interaction: daysAgo(3),
        contributor_person_id: "p1",
      }),
      people: { data: [contact()], page: { has_more: false } },
    });
    const shallow = readAccount(v, NOW).find((f) => f.id === "shallow");
    // The score stays in the header chip; this line says what it means and
    // points at whoever is closest, named from the payload we already have.
    expect(shallow?.params).toBeUndefined();
    expect(shallow?.subject).toEqual({
      kind: "person",
      id: "p1",
      label: "Christian Hagemeyer",
    });
    expect(shallow?.tone).toBe("risk");
  });

  it("says nothing about depth on a strong account", () => {
    const v = view({
      strength: strength({
        score: 70,
        bucket: "strong",
        last_interaction: daysAgo(3),
      }),
    });
    expect(ids(v)).not.toContain("shallow");
  });

  it("separates contacts on file from contacts who have ever engaged", () => {
    const v = view({
      people: {
        data: [
          contact({
            person_id: "p1",
            strength: { score: 12, bucket: "warm", factors: FACTORS },
          }),
          contact({
            person_id: "p2",
            full_name: "Thomas Lohner",
            strength: { score: 0, bucket: "dormant", factors: FACTORS },
          }),
          contact({
            person_id: "p3",
            full_name: "Markus Bueckle",
            strength: { score: 0, bucket: "dormant", factors: FACTORS },
          }),
        ],
        page: { has_more: false },
      },
    });
    const single = readAccount(v, NOW).find((f) => f.id === "single-thread");
    expect(single?.params?.name).toBe("Christian Hagemeyer");
    // No subject chip: the sentence already names them, and a chip repeating
    // the name after it reads as a stutter.
    expect(single?.subject).toBeUndefined();
  });

  it("names no champion gap on an account with no open deal", () => {
    const v = view({
      people: { data: [contact()], page: { has_more: false } },
      deals: deals(),
    });
    expect(ids(v)).not.toContain("no-champion");
  });

  it("names the champion gap once a deal is open", () => {
    const v = view({
      people: { data: [contact()], page: { has_more: false } },
      deals: deals({
        data: [
          { deal_id: "d1", name: "Pilot", status: "open", stalled: false },
        ],
      }),
    });
    expect(ids(v)).toContain("no-champion");
  });

  it("distinguishes a never-customer from a customer with nothing open", () => {
    const never = view({ deals: deals() });
    expect(
      readAccount(never, NOW).find((f) => f.id === "no-open-deal")?.key,
    ).toBe("co.read.noOpenDeal");
    const returning = view({
      deals: deals({
        won_lifetime: { amount_minor: 500_000, currency: "EUR" },
      }),
    });
    expect(
      readAccount(returning, NOW).find((f) => f.id === "no-open-deal")?.key,
    ).toBe("co.read.noOpenDealCustomer");
  });

  it("says nothing about a section the reader may not see", () => {
    // The withheld people section must not become "you know nobody here" —
    // that is the sentence that would send a rep into a meeting wrong.
    const v = view({
      sections_omitted: ["people", "deals", "strength", "next_steps"],
    });
    expect(readAccount(v, NOW)).toHaveLength(0);
  });

  it("does not infer coverage from a people section that never arrived", () => {
    const v = view({ sections_omitted: [] });
    expect(ids(v)).not.toContain("no-contacts");
  });

  it("puts risks above context", () => {
    const v = view({
      strength: strength({ last_interaction: daysAgo(30), bucket: "strong" }),
      since_last_visit: { new_activities: 13 },
    });
    const findings = readAccount(v, NOW);
    expect(findings[0].tone).toBe("risk");
    expect(findings.at(-1)?.id).toBe("new-activity");
  });

  it("picks a singular or plural key rather than interpolating a count", () => {
    const one = view({ since_last_visit: { new_activities: 1 } });
    expect(readAccount(one, NOW)[0].key).toBe("co.read.newActivityOne");
    const many = view({ since_last_visit: { new_activities: 13 } });
    expect(readAccount(many, NOW)[0].key).toBe("co.read.newActivityMany");
  });

  it("treats an uncounted dimension as unknown, not as nothing moved", () => {
    // null is "you have no deal grant, so nobody counted"; 0 is "counted, and
    // nothing moved". Neither earns a line, and for opposite reasons.
    const ungranted = view({
      since_last_visit: { new_activities: 0, deal_stage_moves: null },
    });
    expect(ids(ungranted)).not.toContain("deal-moved");
    const counted = view({
      since_last_visit: { new_activities: 0, deal_stage_moves: 2 },
    });
    const moved = readAccount(counted, NOW).find((f) => f.id === "deal-moved");
    expect(moved?.key).toBe("co.read.dealMovedMany");
    expect(moved?.params?.count).toBe(2);
  });

  it("does not name a contact this reader's own payload never carried", () => {
    // Withheld, or past the section's page: the line stands without a name
    // rather than sending the page off to resolve an id.
    const v = view({
      strength: strength({
        bucket: "weak",
        last_interaction: daysAgo(3),
        contributor_person_id: "p-unseen",
      }),
    });
    const shallow = readAccount(v, NOW).find((f) => f.id === "shallow");
    expect(shallow).toBeDefined();
    expect(shallow?.subject).toBeUndefined();
  });

  it("makes no claim about coverage from a truncated contact list", () => {
    // The section carries only its first page. "Only one person has engaged"
    // and "nobody is champion" are statements about the whole set, and the
    // twenty-sixth contact is exactly where the champion would be hiding.
    const v = view({
      people: {
        data: [
          contact({ person_id: "p1" }),
          contact({
            person_id: "p2",
            full_name: "Thomas Lohner",
            strength: { score: 0, bucket: "dormant", factors: FACTORS },
          }),
        ],
        page: { has_more: true },
      },
      deals: deals({
        data: [
          { deal_id: "d1", name: "Pilot", status: "open", stalled: false },
        ],
      }),
    });
    const found = ids(v);
    expect(found).not.toContain("single-thread");
    expect(found).not.toContain("no-champion");
  });

  it("ignores a champion held on a deal that is not open", () => {
    // A champion on a deal that closed last year says nothing about the one
    // open now, and counting them hid the gap on the accounts that have it.
    const v = view({
      people: {
        data: [
          contact({
            deal_roles: [{ deal_id: "d-closed", role: "champion" }],
          }),
        ],
        page: { has_more: false },
      },
      deals: deals({
        data: [
          { deal_id: "d-open", name: "Pilot", status: "open", stalled: false },
        ],
      }),
    });
    expect(ids(v)).toContain("no-champion");
  });

  it("names no champion gap when the role is held on the open deal", () => {
    const v = view({
      people: {
        data: [
          contact({ deal_roles: [{ deal_id: "d-open", role: "champion" }] }),
        ],
        page: { has_more: false },
      },
      deals: deals({
        data: [
          { deal_id: "d-open", name: "Pilot", status: "open", stalled: false },
        ],
      }),
    });
    expect(ids(v)).not.toContain("no-champion");
  });

  it("names a run of unanswered messages as one-way", () => {
    const v = view({
      activities: {
        data: [
          { id: "a1", kind: "email", direction: "outbound" },
          { id: "a2", kind: "email", direction: "outbound" },
          { id: "a3", kind: "email", direction: "outbound" },
        ],
        page: { has_more: false },
      },
    } as Partial<Organization360>);
    const oneWay = readAccount(v, NOW).find((f) => f.id === "one-way");
    expect(oneWay?.key).toBe("co.read.oneWay");
    expect(oneWay?.params?.count).toBe(3);
  });

  it("counts only the current run, not the whole history's balance", () => {
    // Newest first. A reply behind three unanswered messages does not make
    // the run answered, and an account that replied once a year ago is
    // exactly the one a lifetime ratio would average away.
    const v = view({
      activities: {
        data: [
          { id: "a1", kind: "email", direction: "outbound" },
          { id: "a2", kind: "email", direction: "outbound" },
          { id: "a3", kind: "email", direction: "outbound" },
          { id: "a4", kind: "email", direction: "inbound" },
        ],
        page: { has_more: false },
      },
    } as Partial<Organization360>);
    expect(
      readAccount(v, NOW).find((f) => f.id === "one-way")?.params?.count,
    ).toBe(3);
  });

  it("says nothing once they have answered the latest message", () => {
    const v = view({
      activities: {
        data: [
          { id: "a1", kind: "email", direction: "inbound" },
          { id: "a2", kind: "email", direction: "outbound" },
          { id: "a3", kind: "email", direction: "outbound" },
          { id: "a4", kind: "email", direction: "outbound" },
        ],
        page: { has_more: false },
      },
    } as Partial<Organization360>);
    expect(ids(v)).not.toContain("one-way");
  });

  it("treats a follow-up as normal rather than as a pattern", () => {
    const v = view({
      activities: {
        data: [
          { id: "a1", kind: "email", direction: "outbound" },
          { id: "a2", kind: "email", direction: "outbound" },
        ],
        page: { has_more: false },
      },
    } as Partial<Organization360>);
    expect(ids(v)).not.toContain("one-way");
  });

  it("ignores rows that carry no direction at all", () => {
    // Notes and tasks have no direction. Counting them as ours would call a
    // page of internal notes an unanswered outreach.
    const v = view({
      activities: {
        data: [
          { id: "a1", kind: "note" },
          { id: "a2", kind: "task" },
          { id: "a3", kind: "note" },
        ],
        page: { has_more: false },
      },
    } as Partial<Organization360>);
    expect(ids(v)).not.toContain("one-way");
  });

  it("leads with the overdue commitment and names it", () => {
    const v = view({
      next_steps: {
        data: [
          { activity_id: "a1", subject: "Send the audit scope", overdue: true },
        ],
        page: { has_more: false },
      },
    });
    const overdue = readAccount(v, NOW).find((f) => f.id === "overdue");
    expect(overdue?.key).toBe("co.read.overdueOne");
    expect(overdue?.params?.subject).toBe("Send the audit scope");
  });
});
