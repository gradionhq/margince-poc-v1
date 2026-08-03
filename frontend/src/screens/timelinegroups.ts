import type { TimelineEntry, TimelineGroup } from "../design-system/composed";

export type { TimelineGroup };

/**
 * Grouping the account's chronology into the events a reader recognises.
 *
 * A company timeline rendered one row per message shows the same exchange
 * several times: a product update sent to three contacts is three rows, and a
 * five-message thread is five. The reader is left reconstructing conversations
 * from fragments, which is the job the page exists to do for them.
 *
 * Two groupings, and no more:
 *
 *  - a THREAD is the provider's own conversation id. Not a subject match —
 *    grouping by subject merges two unrelated "Re: Update" exchanges and splits
 *    one that was renamed mid-conversation.
 *  - a BULK SEND is one message the sender addressed to several people at once.
 *    It has no thread of its own, so it is recognised by shape: same subject,
 *    same day, and either the sender's own List-Unsubscribe attestation or
 *    enough copies that no other reading is plausible.
 *
 * Everything else stays a single row. A grouping that guessed would hide a
 * message inside a summary nobody opened, which is worse than the repetition
 * it replaced.
 */

/** How many same-subject, same-day copies imply one send rather than a coincidence. */
const BULK_COPIES = 3;

/** normalizeSubject strips the reply and forward prefixes a thread accretes. */
function normalizeSubject(title: string): string {
  return title
    .replace(/^((re|aw|fwd|fw|wg)\s*:\s*)+/i, "")
    .trim()
    .toLowerCase();
}

function dayOf(atIso: string): string {
  return atIso.slice(0, 10);
}

/**
 * groupChronology folds a newest-first entry list into groups, preserving that
 * order: a group takes the position of its newest member, so the reader's sense
 * of "what happened last" survives the grouping.
 *
 * `hasMore` says the underlying page was cut. It marks only the OLDEST group,
 * because that is the only one whose members could continue past the edge.
 */
/**
 * bulkMembership decides, before anything is grouped, which same-subject runs
 * are one send. Deciding it afterwards would leave a demoted candidate holding
 * both its messages in one row — hiding one, which is the failure this grouping
 * exists to prevent.
 */
function bulkMembership(entries: readonly TimelineEntry[]): Set<string> {
  const copies = new Map<string, number>();
  for (const entry of entries) {
    const key = groupKeyOf(entry);
    if (key?.kind !== "bulk") {
      continue;
    }
    copies.set(key.value, (copies.get(key.value) ?? 0) + 1);
  }
  const bulk = new Set<string>();
  for (const [value, count] of copies) {
    // Two same-subject messages are a coincidence, not a send. The count is
    // the only rule that can speak for a whole subject+day run: attestation
    // is carried by ONE message and is applied per entry in groupChronology,
    // because a reply that merely shares the subject and the day is not part
    // of the send the sender attested to.
    if (count >= BULK_COPIES) {
      bulk.add(value);
    }
  }
  return bulk;
}

export function groupChronology(
  entries: readonly TimelineEntry[],
  hasMore = false,
): TimelineGroup[] {
  const bulk = bulkMembership(entries);
  const groups: TimelineGroup[] = [];
  const byKey = new Map<string, TimelineGroup>();

  for (const entry of entries) {
    const key = groupKeyOf(entry);
    // A bulk key folds this entry when the run is large enough to have no
    // other reading, or when THIS message carries the sender's own
    // List-Unsubscribe attestation. One attested message does not speak for
    // every same-subject message that day.
    const bulkable =
      key?.kind === "bulk" &&
      (bulk.has(key.value) || Boolean(entry.bulkAttested));
    const groupable =
      key && (key.kind === "thread" || bulkable) ? key : undefined;
    if (!groupable) {
      groups.push({
        id: entry.id,
        kind: "single",
        entries: [entry],
        partial: false,
      });
      continue;
    }
    const existing = byKey.get(groupable.value);
    if (existing) {
      existing.entries.push(entry);
      continue;
    }
    // A group takes the position and the id of its newest member, which is the
    // first one seen in a newest-first list.
    const group: TimelineGroup = {
      id: entry.id,
      kind: groupable.kind,
      entries: [entry],
      partial: false,
    };
    byKey.set(groupable.value, group);
    groups.push(group);
  }

  // The cut runs under the page's OLDEST entry, and that entry need not sit in
  // the last group: a group takes the position of its NEWEST member, so an
  // interleaved thread can hold the oldest message while ranking above a
  // single row that arrived after it. Marking the last group would offer to
  // continue a conversation that is already complete and stay silent about the
  // one that is actually cut.
  if (hasMore && entries.length > 0) {
    const oldest = entries[entries.length - 1];
    const cut = groups.find((group) => group.entries.includes(oldest));
    if (cut) {
      cut.partial = true;
    }
  }
  return groups;
}

/**
 * groupKeyOf decides what, if anything, this entry groups by. A change row
 * never groups: it is a field edit, and two edits are two facts.
 */
function groupKeyOf(
  entry: TimelineEntry,
): { kind: "thread" | "bulk"; value: string } | undefined {
  if (entry.kind === "change") {
    return undefined;
  }
  if (entry.threadKey) {
    return { kind: "thread", value: `thread:${entry.threadKey}` };
  }
  if (entry.kind !== "email") {
    return undefined;
  }
  // The message's OWN subject, never the rendered title: `title` falls back to
  // the body, and then to the kind, so a run of subjectless emails would share
  // a key and fold into a bulk send that was never sent.
  const subject = normalizeSubject(entry.subject ?? "");
  if (!subject) {
    return undefined;
  }
  return { kind: "bulk", value: `bulk:${subject}:${dayOf(entry.atIso)}` };
}
