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
  const seen = new Map<string, { copies: number; attested: boolean }>();
  for (const entry of entries) {
    const key = groupKeyOf(entry);
    if (key?.kind !== "bulk") {
      continue;
    }
    const found = seen.get(key.value) ?? { copies: 0, attested: false };
    found.copies += 1;
    found.attested = found.attested || Boolean(entry.bulkAttested);
    seen.set(key.value, found);
  }
  const bulk = new Set<string>();
  for (const [value, found] of seen) {
    // Two same-subject messages are a coincidence, not a send. Only the
    // sender's own attestation overrides the count.
    if (found.attested || found.copies >= BULK_COPIES) {
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
    const groupable =
      key && (key.kind === "thread" || bulk.has(key.value)) ? key : undefined;
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

  if (hasMore && groups.length > 0) {
    groups[groups.length - 1].partial = true;
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
  const subject = normalizeSubject(entry.title);
  if (!subject) {
    return undefined;
  }
  return { kind: "bulk", value: `bulk:${subject}:${dayOf(entry.atIso)}` };
}
