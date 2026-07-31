import { describe, expect, it } from "vitest";
import type { components } from "../api/schema";
import { QUIET_DAYS, readAccount, STRENGTH_WINDOW_DAYS } from "./accountread";

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

// The organization is spelled out rather than cast into shape: a cast would
// hide a field the schema later requires, and these builders exist so a
// contract change breaks the tests instead of passing through them.
function organization(): Organization360["organization"] {
  return {
    id: "o1",
    workspace_id: "w1",
    display_name: "ScaleCommerce",
    version: 1,
    source: "manual",
    captured_by: "human:u1",
    created_at: daysAgo(400),
    updated_at: daysAgo(1),
  };
}

type Activities = NonNullable<Organization360["activities"]>;
type Activity = Activities["data"][number];

// The timeline rows the rules read: kind, direction and when. Everything else
// is filled to the real schema so the builder cannot drift from the contract —
// a cast here would hide exactly the mismatch these tests exist to catch.
function activity(over: Partial<Activity> = {}): Activity {
  return {
    id: "a1",
    workspace_id: "w1",
    kind: "email",
    occurred_at: daysAgo(1),
    is_done: false,
    source: "gmail",
    captured_by: "connector:gmail",
    created_at: daysAgo(1),
    updated_at: daysAgo(1),
    version: 1,
    ...over,
  };
}

function activities(rows: Activity[]): Activities {
  return { data: rows, page: { has_more: false } };
}

function view(over: Partial<Organization360> = {}): Organization360 {
  return {
    as_of: NOW.toISOString(),
    organization: organization(),
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

  it("separates contacts on file from contacts who have ever engaged", () => {
    const v = view({
      people: {
        data: [
          contact({
            person_id: "p1",
            strength: {
              score: 12,
              bucket: "warm",
              factors: FACTORS,
              outbound_90d: 4,
              inbound_90d: 2,
            },
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
    // The counts are windowed, so the sentence carries the window. Without it
    // the line reads as an all-time claim that an account whose committee
    // wrote to us four months ago disproves.
    expect(single?.params?.days).toBe(STRENGTH_WINDOW_DAYS);
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

  it("says nothing about a champion when open deals run past this page", () => {
    // The champion may be named on a deal this response did not carry, so
    // "nobody is champion" is a claim this read cannot make. Silence is the
    // only honest output — a false gap sends a rep into a call to fix
    // something that is not broken.
    const v = view({
      people: { data: [contact()], page: { has_more: false } },
      deals: deals({
        data: [
          { deal_id: "d1", name: "Pilot", status: "open", stalled: false },
        ],
        page: { has_more: true },
      }),
    });
    expect(ids(v)).not.toContain("no-champion");
  });

  it("speaks of one open deal or several, never 'the deal' for both", () => {
    const one = view({
      people: { data: [contact()], page: { has_more: false } },
      deals: deals({
        data: [
          { deal_id: "d1", name: "Pilot", status: "open", stalled: false },
        ],
      }),
    });
    expect(readAccount(one, NOW).find((f) => f.id === "no-champion")?.key).toBe(
      "co.read.noChampion.one",
    );
    const several = view({
      people: { data: [contact()], page: { has_more: false } },
      deals: deals({
        data: [
          { deal_id: "d1", name: "Pilot", status: "open", stalled: false },
          { deal_id: "d2", name: "Rollout", status: "open", stalled: false },
        ],
      }),
    });
    expect(
      readAccount(several, NOW).find((f) => f.id === "no-champion")?.key,
    ).toBe("co.read.noChampion.other");
  });

  it("counts engagement from the traffic, not from the rounded score", () => {
    // A reply near the edge of the 90-day window can score zero. Reading the
    // score as "never engaged" made the brief say only one person had ever
    // engaged while that contact's own row said "Answered".
    const v = view({
      people: {
        data: [
          contact({
            strength: {
              score: 12,
              bucket: "warm",
              factors: FACTORS,
              outbound_90d: 3,
            },
          }),
          // Scored zero at the edge of the window, but he replied. Reading the
          // score would call him unengaged and make "only one person here has
          // engaged" true, while his own row says Answered.
          contact({
            person_id: "p2",
            full_name: "Thomas Lohner",
            strength: {
              score: 0,
              bucket: "dormant",
              factors: FACTORS,
              inbound_90d: 1,
            },
          }),
        ],
        page: { has_more: false },
      },
    });
    expect(ids(v)).not.toContain("single-thread");
  });

  it("makes no overdue count from a first page that has more behind it", () => {
    // "25 commitments are overdue" is a claim about all of them, and 26 may
    // exist. Silence beats a number the reader would act on as the total.
    const truncated = view({
      next_steps: {
        data: [{ activity_id: "t1", subject: "Send the quote", overdue: true }],
        page: { has_more: true },
      },
    });
    expect(ids(truncated)).not.toContain("overdue");

    const complete = view({
      next_steps: {
        data: [{ activity_id: "t1", subject: "Send the quote", overdue: true }],
        page: { has_more: false },
      },
    });
    expect(ids(complete)).toContain("overdue");
  });

  it("names a stalled deal, and says nothing when none has stalled", () => {
    const stalled = view({
      deals: deals({
        data: [{ deal_id: "d1", name: "Pilot", status: "open", stalled: true }],
      }),
    });
    expect(ids(stalled)).toContain("stalled:d1");
    const moving = view({
      deals: deals({
        data: [
          { deal_id: "d1", name: "Pilot", status: "open", stalled: false },
        ],
      }),
    });
    expect(ids(moving)).not.toContain("stalled:d1");
  });

  it("names an account with no contacts, and one carried by a single name", () => {
    const empty = view({ people: { data: [], page: { has_more: false } } });
    expect(ids(empty)).toContain("no-contacts");
    const alone = view({
      people: { data: [contact()], page: { has_more: false } },
    });
    expect(ids(alone)).toContain("one-contact");
  });

  it("names a missing next step only when the list is complete and empty", () => {
    const none = view({
      next_steps: { data: [], page: { has_more: false } },
    });
    expect(ids(none)).toContain("no-next-step");
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
    // The sections are POPULATED and also named as omitted. A version that
    // supplies no data proves nothing: deleting every withheld() check would
    // still yield no findings, because there would be nothing to read. Each
    // section here would produce a finding if its check were removed.
    const v = view({
      sections_omitted: ["people", "deals", "strength", "next_steps"],
      people: { data: [], page: { has_more: false } },
      deals: deals(),
      strength: strength({ last_interaction: daysAgo(40) }),
      next_steps: { data: [], page: { has_more: false } },
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

  it("reads last contact off the timeline the reader can see", () => {
    // Strength counts only interactions linked to a contact; the timeline
    // shows everything. Sourcing this from strength put "13 days ago" above a
    // timeline whose newest row was yesterday.
    const v = view({
      strength: strength({ last_interaction: daysAgo(13) }),
      activities: activities([
        activity({ direction: "outbound", occurred_at: daysAgo(2) }),
        activity({ id: "a2", direction: "inbound", occurred_at: daysAgo(13) }),
      ]),
    });
    expect(
      readAccount(v, NOW).find((f) => f.id === "quiet")?.params?.days,
    ).toBe(2);
  });

  it("does not count a note to ourselves as an exchange with them", () => {
    const v = view({
      strength: strength({ last_interaction: daysAgo(13) }),
      activities: activities([
        activity({ kind: "note", occurred_at: daysAgo(1) }),
        activity({ id: "a2", direction: "outbound", occurred_at: daysAgo(13) }),
      ]),
    });
    expect(
      readAccount(v, NOW).find((f) => f.id === "quiet")?.params?.days,
    ).toBe(13);
  });

  it("never says nobody has been in touch while messages are on screen", () => {
    // strength carries no last_interaction, but the timeline lists mail we
    // sent. Saying both in one screen is the page contradicting itself.
    const v = view({
      strength: strength({ last_interaction: undefined, contact_count: 3 }),
      activities: activities([
        activity({ direction: "outbound", occurred_at: daysAgo(4) }),
      ]),
    });
    const found = ids(v);
    expect(found).not.toContain("never-touched");
    expect(found).toContain("quiet");
  });

  it("still says nobody has been in touch when neither source knows of one", () => {
    const v = view({
      strength: strength({ last_interaction: undefined, contact_count: 3 }),
      activities: activities([]),
    });
    expect(ids(v)).toContain("never-touched");
  });

  it("falls back to strength when the activities section is withheld", () => {
    const v = view({
      sections_omitted: ["activities"],
      strength: strength({ last_interaction: daysAgo(20) }),
    });
    expect(
      readAccount(v, NOW).find((f) => f.id === "quiet")?.params?.days,
    ).toBe(20);
  });

  it("counts the contacts who have not replied, not one contact's traffic", () => {
    // The organization's own strength object is the MAX contact's, not a
    // total: quoting its outbound_90d as the account's traffic described
    // three contacts with 2, 2 and 1 outbound as "2 messages out".
    const v = view({
      strength: strength({ outbound_90d: 2, inbound_90d: 0 }),
      people: {
        data: [
          contact({
            person_id: "p1",
            strength: {
              score: 2,
              bucket: "weak",
              factors: FACTORS,
              outbound_90d: 2,
              inbound_90d: 0,
            },
          }),
          contact({
            person_id: "p2",
            full_name: "Thomas Lohner",
            strength: {
              score: 2,
              bucket: "weak",
              factors: FACTORS,
              outbound_90d: 2,
              inbound_90d: 0,
            },
          }),
          contact({
            person_id: "p3",
            full_name: "Markus Bueckle",
            strength: {
              score: 1,
              bucket: "weak",
              factors: FACTORS,
              outbound_90d: 1,
              inbound_90d: 0,
            },
          }),
        ],
        page: { has_more: false },
      },
    });
    const found = readAccount(v, NOW).find((f) => f.id === "unanswered");
    expect(found?.key).toBe("co.read.unansweredMany");
    expect(found?.params).toEqual({ count: 3, days: STRENGTH_WINDOW_DAYS });
  });

  it("says nothing once any one of them has replied", () => {
    const v = view({
      people: {
        data: [
          contact({
            person_id: "p1",
            strength: {
              score: 2,
              bucket: "weak",
              factors: FACTORS,
              outbound_90d: 2,
              inbound_90d: 0,
            },
          }),
          contact({
            person_id: "p2",
            full_name: "Thomas Lohner",
            strength: {
              score: 30,
              bucket: "warm",
              factors: FACTORS,
              outbound_90d: 2,
              inbound_90d: 1,
            },
          }),
        ],
        page: { has_more: false },
      },
    });
    expect(ids(v)).not.toContain("unanswered");
  });

  it("makes no claim about everyone from a truncated contact list", () => {
    const v = view({
      people: {
        data: [
          contact({
            person_id: "p1",
            strength: {
              score: 2,
              bucket: "weak",
              factors: FACTORS,
              outbound_90d: 2,
              inbound_90d: 0,
            },
          }),
        ],
        page: { has_more: true },
      },
    });
    expect(ids(v)).not.toContain("unanswered");
  });

  it("says nothing when nothing was sent in the window", () => {
    const v = view({
      people: {
        data: [
          contact({
            person_id: "p1",
            strength: {
              score: 0,
              bucket: "dormant",
              factors: FACTORS,
              outbound_90d: 0,
              inbound_90d: 0,
            },
          }),
        ],
        page: { has_more: false },
      },
    });
    expect(ids(v)).not.toContain("unanswered");
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
