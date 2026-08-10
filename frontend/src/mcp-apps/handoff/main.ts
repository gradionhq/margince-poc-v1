// The handoff view: prepare_handoff's briefing for one project, with each gap
// beside the facts it is about.
//
// WHY THE GAPS COME FIRST. This panel is read to answer one question — is this
// work ready to hand over — and the answer is the list of what is missing. A
// brief that led with what it HAS reads as complete, which is exactly the
// failure the tool's gaps exist to prevent: an absent owner looks identical to
// a present one when nothing points at the absence.
//
// EVERY GAP SHOWS THE FIELD IT WAS READ OFF. The tool refuses to raise a gap
// it cannot point at a field for, and this document keeps that promise
// visible: a warning with no source beside it is advice, and neither the tool
// nor the view gives advice.

import { day, el, money, onResult, warned } from "../bridge";
import { asList, asRecord, asText, type Warning } from "../types";
import "../view.css";

/** The envelope's code for "a list in this answer stopped at its bound". The
 *  tool withholds the gaps a bounded read cannot support and says so with
 *  this, so a view that dropped it would present a briefing with checks
 *  MISSING as a briefing with nothing missing. */
const SWEEP_TRUNCATED = "sweep_truncated";

type Gap = { message: string; source: string };
type Deal = { name: string; status: string; amount: string };
type Seat = { person: string; role: string };
type Promise_ = { subject: string; state: string; dueAt: string };

/** The gaps, and how many were unreadable.
 *
 *  THE DROP COUNT IS RETURNED, not swallowed. A gap with no message cannot be
 *  rendered, and if EVERY gap is unreadable the list goes empty — which on this
 *  panel is not "nothing to report" but the verdict "ready to hand over". A
 *  drop has to be able to withhold that verdict, so the caller is told there
 *  was one. */
function gapsOf(data: Record<string, unknown>): {
  gaps: Gap[];
  dropped: number;
} {
  const read = asList(data.gaps)
    .map((entry) => asRecord(entry))
    .map((gap) => ({
      message: asText(gap.message),
      source: asText(gap.source),
    }));
  const gaps = read.filter((gap) => gap.message !== "");
  return { gaps, dropped: read.length - gaps.length };
}

function dealsOf(data: Record<string, unknown>): Deal[] {
  return asList(data.deals)
    .map((entry) => asRecord(entry))
    .map((deal) => ({
      name: asText(deal.name) || asText(deal.deal_id),
      status: asText(deal.status) || "unknown status",
      // Absent, not zero: a deal can be won before it is priced, which is one
      // of the gaps the panel above may already be reporting.
      amount: money(deal.amount_minor, deal.currency),
    }));
}

function seatsOf(data: Record<string, unknown>): Seat[] {
  return asList(data.stakeholders)
    .map((entry) => asRecord(entry))
    .map((seat) => ({
      // The name where the answer has one, the id where it does not — a seat
      // whose person the caller may not read comes back unnamed, and an id is
      // a worse answer than a name but a much better one than a blank.
      person: asText(seat.name) || asText(seat.person_id),
      // "no recorded part", not an empty cell: an untitled seat is a gap the
      // panel above names, and the row has to agree with it.
      role: asText(seat.role) || "no recorded part",
    }))
    .filter((seat) => seat.person !== "");
}

function promisesOf(data: Record<string, unknown>): Promise_[] {
  return asList(data.open_commitments)
    .map((entry) => asRecord(entry))
    .map((promise) => ({
      subject: asText(promise.subject) || "Untitled task",
      state: asText(promise.state),
      dueAt: asText(promise.due_at),
    }));
}

/** section renders a titled group, or nothing at all when the group is empty —
 *  an empty section here would be a heading over a void, and the gap list
 *  above has already said which absences matter. */
function section(title: string, rows: HTMLElement[]): HTMLElement | null {
  if (rows.length === 0) return null;
  const block = el("div", "section");
  block.appendChild(el("h2", "section-title", title));
  const list = el("div", "rows");
  for (const row of rows) list.appendChild(row);
  block.appendChild(list);
  return block;
}

function gapRow(gap: Gap): HTMLElement {
  const row = el("div", "gap");
  row.appendChild(el("span", undefined, gap.message));
  if (gap.source !== "") row.appendChild(el("span", "source", gap.source));
  return row;
}

function twoLineRow(primary: string, secondary: string[]): HTMLElement {
  const row = el("div", "row");
  const head = el("div", "row-head");
  head.appendChild(el("span", "name", primary));
  row.appendChild(head);
  const facts = el("div", "factors");
  for (const fact of secondary) facts.appendChild(el("span", "factor", fact));
  row.appendChild(facts);
  return row;
}

function dealRow(deal: Deal): HTMLElement {
  const row = el("div", "row");
  const head = el("div", "row-head");
  head.appendChild(el("span", "name", deal.name));
  head.appendChild(el("span", "state", deal.status));
  head.appendChild(el("span", "score", deal.amount));
  row.appendChild(head);
  return row;
}

/** stateClass colours only the state this panel has to act on. An overdue
 *  promise at handover is the one that follows the work across. */
function stateClass(state: string): string {
  return state === "overdue" ? "state-overdue" : "state";
}

function promiseRow(promise: Promise_): HTMLElement {
  const row = el("div", "row");
  const head = el("div", "row-head");
  head.appendChild(el("span", "name", promise.subject));
  head.appendChild(
    el(
      "span",
      stateClass(promise.state),
      promise.dueAt === ""
        ? promise.state || "undated"
        : `${promise.state || "due"} · ${day(promise.dueAt)}`,
    ),
  );
  row.appendChild(head);
  return row;
}

/** headline is the project's own line: what it is called, where it stands, and
 *  when it is meant to end. */
function headline(answer: Record<string, unknown>): string {
  const parts = [asText(answer.phase) || "no phase"];
  const target = asText(answer.target_end_date);
  parts.push(target === "" ? "no target end date" : `target ${day(target)}`);
  const key = asText(answer.key);
  if (key !== "") parts.unshift(key);
  return parts.join(" · ");
}

/** ownerLine says who is receiving the work. An unowned project is the gap the
 *  panel leads with, so the line agrees with it rather than going blank. */
function ownerLine(answer: Record<string, unknown>): string {
  // The name where the answer has one, the id where it does not — the same
  // fallback the seats take, for the same reason: an id is a worse answer than
  // a name and a far better one than a blank.
  const owner = asText(answer.owner_name) || asText(answer.owner_id);
  return owner === "" ? "no owner" : `owner ${owner}`;
}

/**
 * verdict is the panel's answer to the one question it is opened for: is this
 * work ready to hand over.
 *
 * "Ready" is the strongest claim here, and it is only true when every check
 * RAN and every result was readable. A bounded read had checks withheld by the
 * tool; an unreadable gap had one lost in transit. Either way the honest
 * answer is that the question was not fully answered — which is a different
 * thing from the answer being "yes".
 */
function verdict(
  answer: Record<string, unknown>,
  bounded: boolean,
): HTMLElement {
  const { gaps, dropped } = gapsOf(answer);
  if (gaps.length > 0) {
    const block = el("div", "section");
    block.appendChild(
      el("h2", "section-title", `${gaps.length} thing(s) still missing`),
    );
    for (const gap of gaps) block.appendChild(gapRow(gap));
    if (bounded) block.appendChild(el("div", "state", boundedNote));
    return block;
  }
  if (bounded || dropped > 0) {
    return el(
      "div",
      "empty",
      "Not every check could be made, so this briefing cannot say the work " +
        "is ready to hand over. " +
        (bounded ? boundedNote : "A reported gap arrived unreadable."),
    );
  }
  return el(
    "div",
    "empty",
    "Nothing the records were checked for is missing. " +
      "This work is ready to hand over.",
  );
}

/** What a bounded read costs this briefing, in the tool's own terms. */
const boundedNote =
  "The lists below stopped at their bound, so they are partial — and the " +
  "checks for an absent won deal or an absent contact were withheld rather " +
  "than guessed.";

export function render(
  root: HTMLElement,
  data: unknown,
  warnings: Warning[],
): void {
  root.replaceChildren();
  if (data === null || data === undefined) {
    root.appendChild(
      el(
        "div",
        "empty",
        "The host sent no structured result for this project.",
      ),
    );
    return;
  }
  const answer = asRecord(data);
  // A payload that is not a handoff is refused rather than narrowed into one.
  // Every other member degrades quietly to an empty list, and an answer with
  // no gaps renders as "ready to hand over" — so a number, a string or another
  // tool's result would be shown to a human as a project cleared for handover.
  //
  // Both members are checked. `project_id` is the one the tool always answers,
  // and `gaps` is the one whose emptiness this panel reads as its verdict; the
  // tool always serializes it, an empty array at worst, so an absent member is
  // proof of skew rather than of a clean project.
  if (asText(answer.project_id) === "" || !Array.isArray(answer.gaps)) {
    root.appendChild(
      el("div", "empty", "The host sent no readable handoff for this project."),
    );
    return;
  }
  root.appendChild(
    el("h1", undefined, asText(answer.name) || "Delivery handoff"),
  );
  root.appendChild(
    el("p", "meta", `${headline(answer)} · ${ownerLine(answer)}`),
  );
  root.appendChild(verdict(answer, warned(warnings, SWEEP_TRUNCATED)));

  for (const block of [
    section("What was sold", dealsOf(answer).map(dealRow)),
    section(
      "Who to call",
      seatsOf(answer).map((seat) => twoLineRow(seat.person, [seat.role])),
    ),
    section("Already promised", promisesOf(answer).map(promiseRow)),
  ]) {
    if (block !== null) root.appendChild(block);
  }
}

onResult((data, warnings) => {
  const root = document.getElementById("root");
  // Guarded rather than asserted; see the account brief for why.
  if (root !== null) render(root, data, warnings);
});
