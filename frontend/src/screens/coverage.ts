import type { components } from "../api/schema";
import type { MessageKey } from "../i18n/en";

// Where each person at the account stands with us, and which of them is worth
// writing to next.
//
// The people list on its own answered "who works there". Before reaching out
// the rep is asking something else: who here has ever answered, who has gone
// quiet on me, and who have I never tried. Those are three different next
// moves and the list rendered them identically.

type Contact = NonNullable<
  components["schemas"]["Organization360"]["people"]
>["data"][number];

/**
 * Reach is one contact's state, in the order a rep triages them.
 *
 *   answered   — they have written back inside the window. The way in.
 *   silent     — we have written and had nothing back. Following up again is
 *                a decision, not a default.
 *   untried    — nobody has written to them at all. Free to approach, and the
 *                most commonly missed opportunity on a stalled account.
 *
 * `untried` is deliberately not merged into `silent`: "no reply" and "never
 * asked" look the same in a contact list and call for opposite actions.
 */
export type Reach = "answered" | "silent" | "untried";

export function reachOf(contact: Contact): Reach {
  const strength = contact.strength;
  if ((strength.inbound_90d ?? 0) > 0) {
    return "answered";
  }
  return (strength.outbound_90d ?? 0) > 0 ? "silent" : "untried";
}

const REACH_LABELS: Record<Reach, MessageKey> = {
  answered: "co.reach.answered",
  silent: "co.reach.silent",
  untried: "co.reach.untried",
};

export function reachLabelKey(reach: Reach): MessageKey {
  return REACH_LABELS[reach];
}

/**
 * REACH_ORDER puts the people worth acting on first.
 *
 * Whoever has answered leads, because they are the way in. Untried comes next:
 * on an account where everyone has gone quiet, the person nobody has written
 * to is the only move left that is not a fourth follow-up.
 */
const REACH_ORDER: Record<Reach, number> = {
  answered: 0,
  untried: 1,
  silent: 2,
};

/** byReach sorts contacts into triage order, then by strength within a state. */
export function byReach(a: Contact, b: Contact): number {
  const rank = REACH_ORDER[reachOf(a)] - REACH_ORDER[reachOf(b)];
  return rank !== 0 ? rank : b.strength.score - a.strength.score;
}

/**
 * ROLES_WORTH_NAMING is the part of a buying committee whose absence is worth
 * reporting, in the order a deal needs them.
 *
 * Not the whole stakeholder vocabulary: `user` and `influencer` are useful to
 * record and unremarkable to be missing, and a gap list that names everything
 * names nothing.
 */
const ROLES_WORTH_NAMING = ["champion", "economic_buyer"] as const;

export type CommitteeRole = (typeof ROLES_WORTH_NAMING)[number];

const ROLE_LABELS: Record<CommitteeRole, MessageKey> = {
  champion: "co.role.champion",
  economic_buyer: "co.role.economic_buyer",
};

export function roleLabelKey(role: CommitteeRole): MessageKey {
  return ROLE_LABELS[role];
}

/**
 * missingRoles reports which of the named committee roles nobody holds on the
 * account's OPEN deals.
 *
 * Scoped to open deals because a champion on a deal that closed last year says
 * nothing about the one running now. Returns nothing at all when the contact
 * list is truncated: the twenty-sixth contact is exactly where the missing
 * champion would be.
 */
export function missingRoles(
  contacts: readonly Contact[],
  openDealIds: ReadonlySet<string>,
  truncated: boolean,
): CommitteeRole[] {
  if (truncated || openDealIds.size === 0) {
    return [];
  }
  const held = new Set<string>();
  for (const contact of contacts) {
    for (const role of contact.deal_roles) {
      if (openDealIds.has(role.deal_id)) {
        held.add(role.role);
      }
    }
  }
  return ROLES_WORTH_NAMING.filter((role) => !held.has(role));
}
