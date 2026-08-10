// The commitments view: review_commitments' open promises, oldest first, with
// who owes each one and how far past its date it is.
//
// WHY THE STATE AND THE DATE TOGETHER. "Overdue" is a claim about a moment,
// and the moment is in the answer — so the header names the instant every
// state below it was judged against. A queue of red labels with no date
// attached cannot be told from a stale panel left open since yesterday.
//
// WHAT IT REFUSES TO INVENT. A promise with no due date renders as undated,
// never as overdue and never as "due today": the answer carries no timezone
// and neither does this document, so the only honest day is the one the server
// judged in. A promise with no owner renders as unowned, which is the state a
// reviewer is looking for rather than a blank to be filled in silently.

import { count, day, el, onResult, warned } from "../bridge";
import { asList, asRecord, asText, type Warning } from "../types";
import "../view.css";

/** The states the seam reports, and the only ones this document colours. A
 *  state outside the set still renders — with no colour rather than the wrong
 *  one — because the vocabulary belongs to the seam. */
const STATES: Record<string, true> = {
  overdue: true,
  upcoming: true,
  undated: true,
};

/** The envelope's code for "this read stopped at its bound". It matters more
 *  here than on any other view: the question is whether anything is being
 *  dropped, and a silently truncated queue answers no. */
const SWEEP_TRUNCATED = "sweep_truncated";

type Commitment = {
  subject: string;
  state: string;
  dueAt: string;
  daysOverdue: unknown;
  owner: string;
  about: string[];
};

/**
 * known narrows the untrusted payload, filtering BEFORE anything is counted so
 * the header describes what is actually shown.
 */
function known(data: Record<string, unknown>): Commitment[] {
  return asList(data.commitments)
    .filter(
      (c): c is Record<string, unknown> => typeof c === "object" && c !== null,
    )
    .map((c) => ({
      subject: asText(c.subject) || "Untitled task",
      state: asText(c.state),
      dueAt: asText(c.due_at),
      daysOverdue: c.days_overdue,
      owner: asText(c.assignee_name) || asText(c.assignee_id),
      about: asList(c.about)
        .map(aboutLabel)
        .filter((label) => label !== ""),
    }));
}

/** aboutLabel names the record a promise was made about, falling back to the
 *  id where the record has no name of its own — a lead captured as an email
 *  address and nothing else. */
function aboutLabel(entry: unknown): string {
  const about = asRecord(entry);
  const name = asText(about.name) || asText(about.entity_id);
  const type = asText(about.entity_type);
  if (name === "") return "";
  return type === "" ? name : `${type}: ${name}`;
}

// Object.hasOwn, not a plain lookup: a state of "constructor" or "toString"
// finds a truthy value on the prototype chain and would be rendered as a class
// this stylesheet does not have.
function stateClass(state: string): string {
  return Object.hasOwn(STATES, state) ? `state-${state}` : "state";
}

/** stateText says how late, not merely that it is late. Zero whole days is a
 *  real answer — hours past its date — so it reads as "overdue today" rather
 *  than as "0 days". */
function stateText(commitment: Commitment): string {
  if (commitment.state !== "overdue") return commitment.state || "unknown";
  const days = count(commitment.daysOverdue);
  if (days === "—") return "overdue";
  return days === "0" ? "overdue today" : `${days}d overdue`;
}

function commitmentRow(commitment: Commitment): HTMLElement {
  const row = el("div", "row");
  const head = el("div", "row-head");
  head.appendChild(el("span", "name", commitment.subject));
  head.appendChild(
    el("span", stateClass(commitment.state), stateText(commitment)),
  );
  row.appendChild(head);
  const facts = el("div", "factors");
  // "unowned" rather than an empty space: a promise nobody holds is the single
  // most useful thing on this panel, so it is said out loud.
  facts.appendChild(
    el(
      "span",
      "factor",
      commitment.owner === "" ? "unowned" : commitment.owner,
    ),
  );
  facts.appendChild(
    el(
      "span",
      "factor",
      commitment.dueAt === "" ? "no due date" : `due ${day(commitment.dueAt)}`,
    ),
  );
  for (const label of commitment.about) {
    facts.appendChild(el("span", "factor", label));
  }
  row.appendChild(facts);
  return row;
}

/** The meta line. "Everything outstanding" is only true of a COMPLETE sweep;
 *  past the bound these are the oldest promises FOUND, which is the claim the
 *  tool itself refuses to overstate. */
function metaLine(found: number, asOf: string, bounded: boolean): string {
  const judged = asOf === "" ? "" : ` · judged as of ${day(asOf)}`;
  if (bounded) {
    return `${found} shown — more are outstanding than are listed here${judged}`;
  }
  return `${found} open commitment(s), oldest promise first${judged}`;
}

export function render(
  root: HTMLElement,
  data: unknown,
  warnings: Warning[],
): void {
  root.replaceChildren();
  if (data === null || data === undefined) {
    root.appendChild(
      el("div", "empty", "The host sent no structured result for this review."),
    );
    return;
  }
  const answer = asRecord(data);
  const commitments = known(answer);
  root.appendChild(el("h1", undefined, "Open commitments"));
  root.appendChild(
    el(
      "p",
      "meta",
      metaLine(
        commitments.length,
        asText(answer.as_of),
        warned(warnings, SWEEP_TRUNCATED),
      ),
    ),
  );
  if (commitments.length === 0) {
    root.appendChild(
      el(
        "div",
        "empty",
        "Nothing is outstanding. That is the answer, not a gap — " +
          "though only promises written down as tasks are counted.",
      ),
    );
    return;
  }
  const rows = el("div", "rows");
  for (const commitment of commitments) {
    rows.appendChild(commitmentRow(commitment));
  }
  root.appendChild(rows);
}

onResult((data, warnings) => {
  const root = document.getElementById("root");
  // Guarded rather than asserted; see the account brief for why.
  if (root !== null) render(root, data, warnings);
});
