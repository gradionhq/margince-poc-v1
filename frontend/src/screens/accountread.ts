import type { components } from "../api/schema";
import type { MessageKey } from "../i18n/en";

// What the account looks like right now, read off the records the 360 already
// returned. No model, no second request: every finding below is a rule over
// the composite payload, so a rep can disagree with the rule rather than with
// a verdict they cannot inspect.
//
// The one discipline here: a section the caller's grants withheld produces NO
// finding. Silence is the only honest output for data this page was not
// allowed to read, and "nobody has replied" derived from a people section the
// reader cannot see is exactly the sentence that would send them into a
// meeting wrong.

type Organization360 = components["schemas"]["Organization360"];
type Section = Organization360["sections_omitted"][number];

/**
 * QUIET_DAYS is when silence becomes worth saying out loud.
 *
 * Two weeks is the point at which "we spoke recently" stops being true for a
 * B2B account: shorter reads as normal cadence and would fire on every
 * account every week, which trains the reader to skip the line.
 */
export const QUIET_DAYS = 14;

/**
 * ONE_WAY_FLOOR is how long a run of unanswered messages has to be to mean
 * something. Two is a follow-up; three is a pattern.
 */
export const ONE_WAY_FLOOR = 3;

/** A finding is one sentence the page is prepared to defend, plus its subject. */
export type AccountFinding = {
  /** Stable across renders and re-reads, so React keys and tests can name one. */
  id: string;
  /** risk draws the eye; neutral is context that needs no action. */
  tone: "risk" | "neutral";
  key: MessageKey;
  params?: Record<string, string | number>;
  /**
   * The record this finding is about, for the findings whose SENTENCE does not
   * already name it. A line that reads "Dana Buyer is your only way in" needs
   * no chip repeating "Dana Buyer" after it.
   *
   * It carries its own label, because the 360 already told us the name. An id
   * alone would make the brief resolve one record read per referenced finding,
   * which is a fan-out of lookups on the page whose whole design is a single
   * composite read.
   */
  subject?: { kind: "person" | "deal"; id: string; label: string };
};

function withheld(view: Organization360, section: Section): boolean {
  return (view.sections_omitted ?? []).includes(section);
}

/** daysBetween counts whole days, floored, so "13 days ago" never reads as 14. */
function daysBetween(from: string, now: Date): number {
  const then = new Date(from).getTime();
  if (Number.isNaN(then)) {
    return 0;
  }
  return Math.floor((now.getTime() - then) / 86_400_000);
}

/**
 * readAccount turns one 360 payload into the findings a rep needs before a
 * conversation, most urgent first.
 *
 * `now` is a parameter rather than a call to Date.now() so the rules are
 * testable against a fixed clock — the quiet-account rule is entirely a
 * statement about elapsed time, and a test that reads the wall clock proves
 * nothing about it.
 */
export function readAccount(
  view: Organization360,
  now: Date,
): AccountFinding[] {
  const findings: AccountFinding[] = [];
  findings.push(...quietness(view, now));
  findings.push(...depth(view));
  findings.push(...sinceLastVisit(view));
  findings.push(...coverage(view));
  findings.push(...pipeline(view));
  findings.push(...commitments(view));
  findings.push(...reciprocity(view));
  // Risks first: the reader's attention is spent top-down, and the neutral
  // lines are context for the risks rather than the other way round.
  return [
    ...findings.filter((f) => f.tone === "risk"),
    ...findings.filter((f) => f.tone === "neutral"),
  ];
}

// How long this account has been quiet. Read off the strength section, which
// is what carries the last interaction.
function quietness(view: Organization360, now: Date): AccountFinding[] {
  const strength = view.strength;
  if (!strength || withheld(view, "strength")) {
    return [];
  }
  if (!strength.last_interaction) {
    // Contacts on file and not one recorded interaction: a real state, and a
    // different one from an account nobody has entered yet.
    return strength.contact_count > 0
      ? [{ id: "never-touched", tone: "risk", key: "co.read.neverTouched" }]
      : [];
  }
  // How long it has been is ALWAYS worth a line, because "we spoke on Tuesday"
  // and "we spoke in April" lead to different opening sentences. Only the tone
  // changes at the threshold: below it this is context, above it it is the
  // thing to deal with.
  const days = daysBetween(strength.last_interaction, now);
  return [
    {
      id: "quiet",
      tone: days >= QUIET_DAYS ? "risk" : "neutral",
      key: days === 1 ? "co.read.lastTouchOne" : "co.read.lastTouch",
      params: { days },
    },
  ];
}

// How much of a relationship there actually is.
//
// The bucket is the server's word for it and nothing here re-derives a band
// from the score: a low bucket on an account with contacts on file means the
// names exist but the relationship does not, which is the single most
// misleading thing a populated-looking account page can hide.
function depth(view: Organization360): AccountFinding[] {
  const strength = view.strength;
  if (!strength || withheld(view, "strength") || !strength.last_interaction) {
    return [];
  }
  if (strength.bucket !== "weak" && strength.bucket !== "dormant") {
    return [];
  }
  // The score itself stays in the header chip. Repeating it here would put the
  // same number on screen twice; this line's job is what it MEANS.
  //
  // The strongest contact is named only if this reader's own people section
  // carried them. Absent — withheld, or past the section's page — the line
  // stands without a name rather than sending the page off to look one up.
  const contributor = view.people?.data.find(
    (c) => c.person_id === strength.contributor_person_id,
  );
  return [
    {
      id: "shallow",
      tone: "risk",
      key: "co.read.shallow",
      subject: contributor && {
        kind: "person",
        id: contributor.person_id,
        label: contributor.full_name,
      },
    },
  ];
}

// What has landed since the reader last looked.
//
// Both counts distinguish null from zero: null means the caller has no grant
// for that dimension so nobody counted it, and zero means it was counted and
// nothing happened. Neither earns a line, for opposite reasons.
function sinceLastVisit(view: Organization360): AccountFinding[] {
  const delta = view.since_last_visit;
  if (!delta || withheld(view, "since_last_visit")) {
    return [];
  }
  const out: AccountFinding[] = [];
  if (delta.new_activities > 0) {
    // The catalog carries no plural machinery, so count-bearing sentences pick
    // their key rather than interpolating a number into one fixed phrasing.
    out.push({
      id: "new-activity",
      tone: "neutral",
      key:
        delta.new_activities === 1
          ? "co.read.newActivityOne"
          : "co.read.newActivityMany",
      params: { count: delta.new_activities },
    });
  }
  const moves = delta.deal_stage_moves;
  if (moves) {
    out.push({
      id: "deal-moved",
      tone: "neutral",
      key: moves === 1 ? "co.read.dealMovedOne" : "co.read.dealMovedMany",
      params: { count: moves },
    });
  }
  return out;
}

// Who actually carries this relationship. The single-thread rule is the most
// valuable line on the page, so it is also the one held to the strictest
// evidence: it counts contacts who have ever registered a score, not contacts
// on file, because three names and one relationship is precisely the risk.
function coverage(view: Organization360): AccountFinding[] {
  if (!view.people || withheld(view, "people")) {
    return [];
  }
  // Every finding here is a statement about the WHOLE set of contacts, and the
  // section carries only its first page. With more behind it, "only one person
  // has engaged" and "nobody is champion" are claims this read cannot make —
  // the twenty-sixth contact is exactly where the champion would be hiding. A
  // partial answer is worse than none, because the reader cannot tell.
  if (view.people.page.has_more) {
    return [];
  }
  const contacts = view.people.data;
  if (contacts.length === 0) {
    return [{ id: "no-contacts", tone: "risk", key: "co.read.noContacts" }];
  }
  const out: AccountFinding[] = [];
  const engaged = contacts.filter((c) => c.strength.score > 0);
  if (engaged.length === 1 && contacts.length > 1) {
    const only = engaged[0];
    out.push({
      id: "single-thread",
      tone: "risk",
      key: "co.read.singleThread",
      params: { name: only.full_name },
    });
  } else if (contacts.length === 1) {
    out.push({
      id: "one-contact",
      tone: "risk",
      key: "co.read.oneContact",
      params: { name: contacts[0].full_name },
    });
  }
  // A champion is only meaningful against an open deal, so this stays quiet
  // on an account with no pipeline rather than reporting a gap in nothing.
  //
  // The role must be held on one of THOSE deals: a champion on a deal that
  // closed last year says nothing about the one open now, and counting them
  // hid the gap on exactly the accounts that have it.
  const openDeals = view.deals?.data ?? [];
  if (openDeals.length > 0 && !withheld(view, "deals")) {
    const openIds = new Set(openDeals.map((deal) => deal.deal_id));
    const hasChampion = contacts.some((c) =>
      c.deal_roles.some(
        (role) => role.role === "champion" && openIds.has(role.deal_id),
      ),
    );
    if (!hasChampion) {
      out.push({ id: "no-champion", tone: "risk", key: "co.read.noChampion" });
    }
  }
  return out;
}

// The commercial state: what is open, what has stalled, and whether this
// account has ever bought anything.
function pipeline(view: Organization360): AccountFinding[] {
  const deals = view.deals;
  if (!deals || withheld(view, "deals")) {
    return [];
  }
  const out: AccountFinding[] = [];
  const stalled = deals.data.filter((deal) => deal.stalled);
  for (const deal of stalled) {
    out.push({
      id: `stalled:${deal.deal_id}`,
      tone: "risk",
      key: "co.read.stalled",
      params: { name: deal.name },
    });
  }
  if (deals.data.length === 0) {
    // Won lifetime separates "never a customer" from "a customer with nothing
    // open right now" — two accounts that call for opposite conversations.
    const won = deals.won_lifetime?.amount_minor ?? 0;
    out.push({
      id: "no-open-deal",
      tone: "neutral",
      key: won > 0 ? "co.read.noOpenDealCustomer" : "co.read.noOpenDeal",
    });
  }
  return out;
}

// Whether the conversation is still going, or has become a broadcast.
//
// The measure is the CURRENT RUN of unanswered messages, not the whole
// history's balance. An account that replied once a year ago and has ignored
// the last five mails is the one worth flagging, and a ratio over everything
// ever sent would average that away. The run is also the only claim the
// payload can support on its own: the activities section is paged, so
// "nobody has ever replied" is not something this read can know, while "the
// last five were ours" is true of the window whatever sits behind it.
function reciprocity(view: Organization360): AccountFinding[] {
  const activities = view.activities?.data;
  if (!activities || withheld(view, "activities")) {
    return [];
  }
  // Rows with no direction — notes, tasks — are ours by definition and say
  // nothing about whether they answered, so they are skipped rather than
  // counted: a page of internal notes is not an unanswered outreach.
  const directed = activities.filter((a) => a.direction);
  let run = 0;
  for (const activity of directed) {
    if (activity.direction !== "outbound") {
      break;
    }
    run += 1;
  }
  // One unanswered message is normal; a run of them is the account telling
  // you something, and the point at which a fourth of the same kind is the
  // wrong move.
  if (run < ONE_WAY_FLOOR) {
    return [];
  }
  return [
    {
      id: "one-way",
      tone: "risk",
      key: "co.read.oneWay",
      params: { count: run },
    },
  ];
}

// What was promised: the open tasks, with overdue called out separately
// because an overdue commitment is the thing a rep most wants to know before
// speaking to the person they made it to.
function commitments(view: Organization360): AccountFinding[] {
  const steps = view.next_steps?.data;
  if (!steps || withheld(view, "next_steps")) {
    return [];
  }
  const overdue = steps.filter((step) => step.overdue);
  if (overdue.length > 0) {
    return [
      {
        id: "overdue",
        tone: "risk",
        key:
          overdue.length === 1 ? "co.read.overdueOne" : "co.read.overdueMany",
        params: { count: overdue.length, subject: overdue[0].subject },
      },
    ];
  }
  if (steps.length === 0) {
    return [{ id: "no-next-step", tone: "risk", key: "co.read.noNextStep" }];
  }
  return [];
}
